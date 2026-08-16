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
	mu               sync.Mutex
	results          []sourceResult
	calls            int
	quoteErr         error
	unavailableQuote map[string]bool
}

func (s *fakeSource) FetchMoney5m(_ context.Context, snapshot graymarket.RankSnapshot, includeClose bool) ([]graymarket.MoneyPoint, error) {
	pointsPerCode := 47
	if includeClose {
		pointsPerCode = 48
	}
	points := make([]graymarket.MoneyPoint, 0, len(snapshot.Records)*pointsPerCode)
	for _, record := range snapshot.Records {
		for _, session := range []struct{ start, end int }{{9*60 + 35, 11*60 + 30}, {13*60 + 5, 14*60 + 55}} {
			for minute := session.start; minute <= session.end; minute += 5 {
				at := time.Date(snapshot.SnapshotAt.Year(), snapshot.SnapshotAt.Month(), snapshot.SnapshotAt.Day(), minute/60, minute%60, 0, 0, snapshot.SnapshotAt.Location())
				points = append(points, graymarket.MoneyPoint{TradeDate: snapshot.TradeDate, SnapshotAt: at, RankType: record.RankType,
					Rank: record.Rank, Market: record.Market, Code: record.Code, Name: record.Name, DarkMoney: record.DarkMoney,
					RegularMoney: record.RegularMoney, MainMoneyInflow: record.MainMoneyInflow, FetchedAt: at})
			}
		}
		if includeClose {
			at := time.Date(snapshot.SnapshotAt.Year(), snapshot.SnapshotAt.Month(), snapshot.SnapshotAt.Day(), 15, 0, 0, 0, snapshot.SnapshotAt.Location())
			points = append(points, graymarket.MoneyPoint{TradeDate: snapshot.TradeDate, SnapshotAt: at, RankType: record.RankType,
				Rank: record.Rank, Market: record.Market, Code: record.Code, Name: record.Name, DarkMoney: record.DarkMoney,
				RegularMoney: record.RegularMoney, MainMoneyInflow: record.MainMoneyInflow, FetchedAt: at})
		}
	}
	return points, nil
}

func (s *fakeSource) FetchStockKlines5m(_ context.Context, snapshot graymarket.RankSnapshot) ([]graymarket.StockKlinePoint, error) {
	points := make([]graymarket.StockKlinePoint, 0, len(snapshot.Records)*48)
	for _, record := range snapshot.Records {
		for _, session := range []struct{ start, end int }{{9*60 + 35, 11*60 + 30}, {13*60 + 5, 15 * 60}} {
			for minute := session.start; minute <= session.end; minute += 5 {
				at := time.Date(snapshot.SnapshotAt.Year(), snapshot.SnapshotAt.Month(), snapshot.SnapshotAt.Day(), minute/60, minute%60, 0, 0, snapshot.SnapshotAt.Location())
				points = append(points, graymarket.StockKlinePoint{TradeDate: snapshot.TradeDate, SnapshotAt: at, Market: record.Market, Code: record.Code, ClosePrice: 10, FetchedAt: at})
			}
		}
	}
	return points, nil
}

func (s *fakeSource) FetchAll(context.Context, graymarket.RankType, string, time.Time) (graymarket.RankSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return result.snapshot, result.err
}

func (s *fakeSource) FetchStockQuotes(_ context.Context, relations []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	if s.quoteErr != nil {
		return nil, s.quoteErr
	}
	quotes := make([]graymarket.StockQuote, 0, len(relations))
	for _, relation := range relations {
		available := !s.unavailableQuote[relation.StockCode]
		quotes = append(quotes, graymarket.StockQuote{StockCode: relation.StockCode, StockMarket: relation.StockMarket,
			StockName: relation.StockName, LatestPrice: 10, OpenPrice: 9.5, HighPrice: 10.5, LowPrice: 9.25,
			PreviousClose: 9, ChangePct: 0.1111, ChangeValue: 1, Volume: 100, Turnover: 1000,
			TurnoverRate: 0.02, Amplitude: 0.1, QuoteTime: "2026-08-14T07:00:00Z", Available: available})
	}
	return quotes, nil
}

type fakeStore struct {
	mu              sync.Mutex
	started         []repository.CollectionRun
	finished        []repository.CollectionRun
	savedIntraday   int
	savedDailyClose int
	savedArchives   int
	savedKlineRows  int
	lastDailyClose  graymarket.RankSnapshot
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

func (s *fakeStore) SaveDailyClose(_ context.Context, _ string, snapshot graymarket.RankSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedDailyClose++
	s.lastDailyClose = snapshot
	return s.saveErr
}

func (s *fakeStore) SaveBoardArchive(_ context.Context, _ string, _ graymarket.RankSnapshot, _ []graymarket.MoneyPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedArchives++
	return s.saveErr
}

func (s *fakeStore) SaveStockArchive(_ context.Context, _ string, _ graymarket.RankSnapshot, _ []graymarket.MoneyPoint) error {
	s.savedArchives++
	return s.saveErr
}

func (s *fakeStore) SaveStockKlines(_ context.Context, _ string, points []graymarket.StockKlinePoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedKlineRows = len(points)
	return s.saveErr
}

func (s *fakeStore) CompactResearch(context.Context, string) ([]repository.QualitySummary, error) {
	return s.quality, s.compactErr
}

func (s *fakeStore) HasDailyClose(context.Context, string) (bool, error) {
	return s.hasDailyClose, s.hasCloseErr
}

func (s *fakeStore) HasEndOfDayArchive(context.Context, string) (bool, error) {
	return s.hasDailyClose, s.hasCloseErr
}

func (s *fakeStore) HasStockKlineArchive(context.Context, string) (bool, error) {
	return s.hasDailyClose, s.hasCloseErr
}

func (s *fakeStore) CleanupArchivedIntraday(context.Context, string) error { return nil }

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
			TradeDate:    tradeDate,
			SnapshotAt:   at,
			RankType:     graymarket.RankIndustry,
			Rank:         1,
			Code:         "BK001",
			Name:         "board",
			DarkMoney:    123,
			DarkActivity: 0.123,
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
	if len(store.lastDailyClose.Records) != 1 {
		t.Fatalf("daily close snapshot was not retained by test store: %+v", store.lastDailyClose)
	}
	record := store.lastDailyClose.Records[0]
	if record.OpenPrice != 9.5 || record.HighPrice != 10.5 || record.LowPrice != 9.25 || record.ClosePrice != 10 || record.PreviousClose != 9 || record.Turnover != 1000 || record.TurnoverRate != 0.02 || !record.QuoteAvailable {
		t.Fatalf("daily OHLC quote fields were not enriched: %+v", record)
	}
	if record.DarkActivity != 0.123 {
		t.Fatalf("original dark activity must be preserved, got %f", record.DarkActivity)
	}
	if len(store.finished) != 1 || store.finished[0].Status != repository.RunSuccess {
		t.Fatalf("unexpected finished run: %+v", store.finished)
	}
}

func TestCollectDailyCloseFailsWhenQuoteEnrichmentFails(t *testing.T) {
	at := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	quoteErr := errors.New("quote unavailable")
	source := &fakeSource{results: []sourceResult{{snapshot: successfulSnapshot(at)}}, quoteErr: quoteErr}
	store := &fakeStore{}

	err := newTestService(source, store).collect(context.Background(), graymarket.RankStock, graymarket.SnapshotDailyClose, "20260814", at)
	if !errors.Is(err, quoteErr) || store.savedDailyClose != 0 {
		t.Fatalf("incomplete OHLC snapshot must not be saved: err=%v saves=%d", err, store.savedDailyClose)
	}
	if len(store.finished) != 1 || store.finished[0].ErrorCode != "quote_enrichment" {
		t.Fatalf("unexpected failed run: %+v", store.finished)
	}
}

func TestCollectStockKlinesExcludesStocksWithoutDailyBar(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	runAt := time.Date(2026, 8, 14, 16, 15, 0, 0, location)
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-14", RankType: graymarket.RankStock, SnapshotAt: closeAt,
		Records: []graymarket.RankRecord{
			{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 0, Code: "000001", Name: "交易股"},
			{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: "600001", Name: "停牌股"},
		}}
	source := &fakeSource{results: []sourceResult{{snapshot: snapshot}}, unavailableQuote: map[string]bool{"600001": true}}
	store := &fakeStore{}
	if err := newTestService(source, store).CollectStockKlines(context.Background(), runAt); err != nil {
		t.Fatal(err)
	}
	if store.savedKlineRows != 48 || len(store.finished) != 1 {
		t.Fatalf("unexpected kline save: rows=%d runs=%+v", store.savedKlineRows, store.finished)
	}
	run := store.finished[0]
	if run.Status != repository.RunSuccess || run.ExpectedTotal != 48 || run.FetchedTotal != 48 || run.PageCount != 1 {
		t.Fatalf("kline run counted suspended stock: %+v", run)
	}
}
