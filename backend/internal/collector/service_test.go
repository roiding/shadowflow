package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

type sourceResult struct {
	snapshot graymarket.RankSnapshot
	err      error
}

type fakeSource struct {
	mu      sync.Mutex
	results []sourceResult
	calls   int
}

func (s *fakeSource) FetchAll(context.Context, graymarket.RankType, string, time.Time) (graymarket.RankSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return result.snapshot, result.err
}

type fakeStore struct {
	mu              sync.Mutex
	started         []repository.CollectionRun
	finished        []repository.CollectionRun
	savedIntraday   int
	savedDailyClose int
	startErr        error
	finishErr       error
	saveErr         error
	hasDailyClose   bool
	hasCloseErr     error
	quality         []repository.QualitySummary
	compactErr      error
}

func (s *fakeStore) SaveIntraday(_ context.Context, _ string, _ graymarket.RankSnapshot, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedIntraday++
	return s.saveErr
}

func (s *fakeStore) SaveDailyClose(_ context.Context, _ string, _ graymarket.RankSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedDailyClose++
	return s.saveErr
}

func (s *fakeStore) CompactResearch(context.Context, string) ([]repository.QualitySummary, error) {
	return s.quality, s.compactErr
}

func (s *fakeStore) HasDailyClose(context.Context, string) (bool, error) {
	return s.hasDailyClose, s.hasCloseErr
}

func (s *fakeStore) StartRun(_ context.Context, run repository.CollectionRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, run)
	return s.startErr
}

func (s *fakeStore) FinishRun(_ context.Context, run repository.CollectionRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, run)
	return s.finishErr
}

func newTestService(source Source, store store) *Service {
	service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.retryGap = 0
	return service
}

func successfulSnapshot(at time.Time) graymarket.RankSnapshot {
	tradeDate := at.Format("2006-01-02")
	return graymarket.RankSnapshot{
		TradeDate:     tradeDate,
		RankType:      graymarket.RankIndustry,
		SnapshotAt:    at,
		ExpectedTotal: 1,
		Records: []graymarket.RankRecord{{
			TradeDate:  tradeDate,
			SnapshotAt: at,
			RankType:   graymarket.RankIndustry,
			Rank:       1,
			Code:       "BK001",
			Name:       "board",
		}},
		RawPages: []graymarket.RawPage{{Page: 1}},
	}
}

func TestCollectRetriesThenPersistsSuccessfulRun(t *testing.T) {
	at := time.Date(2026, 8, 14, 10, 5, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	upstreamErr := errors.New("temporary upstream failure")
	source := &fakeSource{results: []sourceResult{{err: upstreamErr}, {snapshot: successfulSnapshot(at)}}}
	store := &fakeStore{}
	service := newTestService(source, store)

	if err := service.collect(context.Background(), graymarket.RankIndustry, graymarket.SnapshotMinuteWork, "20260814", at); err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 {
		t.Fatalf("expected 2 fetch attempts, got %d", source.calls)
	}
	if store.savedIntraday != 1 || store.savedDailyClose != 0 {
		t.Fatalf("unexpected saves: intraday=%d daily_close=%d", store.savedIntraday, store.savedDailyClose)
	}
	if len(store.finished) != 1 {
		t.Fatalf("expected one finished run, got %d", len(store.finished))
	}
	run := store.finished[0]
	if run.Status != repository.RunSuccess || run.AttemptCount != 2 || run.ActualTradeDate != "2026-08-14" {
		t.Fatalf("unexpected finished run: %+v", run)
	}
	if run.ExpectedTotal != 1 || run.FetchedTotal != 1 || run.PageCount != 1 || run.FinishedAt == nil {
		t.Fatalf("missing run metrics: %+v", run)
	}
}

func TestCollectDoesNotRetryNoData(t *testing.T) {
	at := time.Date(2026, 8, 14, 10, 5, 0, 0, time.UTC)
	source := &fakeSource{results: []sourceResult{{err: graymarket.ErrNoData}}}
	store := &fakeStore{}
	service := newTestService(source, store)

	err := service.collect(context.Background(), graymarket.RankConcept, graymarket.SnapshotMinuteWork, "20260814", at)
	if !errors.Is(err, graymarket.ErrNoData) {
		t.Fatalf("expected ErrNoData, got %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("no-data response should not retry, got %d calls", source.calls)
	}
	if len(store.finished) != 1 || store.finished[0].Status != repository.RunFailed || store.finished[0].ErrorCode != "no_data" {
		t.Fatalf("unexpected failed run: %+v", store.finished)
	}
}

func TestCollectRejectsDateMismatchBeforeSaving(t *testing.T) {
	at := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	snapshot := successfulSnapshot(at)
	snapshot.TradeDate = "2026-08-13"
	source := &fakeSource{results: []sourceResult{{snapshot: snapshot}}}
	store := &fakeStore{}
	service := newTestService(source, store)

	err := service.collect(context.Background(), graymarket.RankStock, graymarket.SnapshotDailyClose, "20260814", at)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected date mismatch, got %v", err)
	}
	if store.savedIntraday != 0 || store.savedDailyClose != 0 {
		t.Fatalf("mismatched snapshot must not be saved")
	}
	if len(store.finished) != 1 || store.finished[0].ErrorCode != "date_mismatch" || store.finished[0].ActualTradeDate != "2026-08-13" {
		t.Fatalf("unexpected failed run: %+v", store.finished)
	}
}

func TestCollectPropagatesFinishRunFailure(t *testing.T) {
	at := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	finishErr := errors.New("database is locked")
	source := &fakeSource{results: []sourceResult{{snapshot: successfulSnapshot(at)}}}
	store := &fakeStore{finishErr: finishErr}
	service := newTestService(source, store)

	err := service.collect(context.Background(), graymarket.RankStock, graymarket.SnapshotDailyClose, "20260814", at)
	if !errors.Is(err, finishErr) || !strings.Contains(err.Error(), "finish collection run") {
		t.Fatalf("expected finish error to be returned, got %v", err)
	}
	if store.savedDailyClose != 1 {
		t.Fatalf("snapshot should be saved before finish failure, got %d saves", store.savedDailyClose)
	}
	if len(store.finished) != 1 || store.finished[0].Status != repository.RunSuccess {
		t.Fatalf("unexpected finished run: %+v", store.finished)
	}
}
