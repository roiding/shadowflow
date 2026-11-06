package collector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/proxypool"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
)

type gatedRelationSource struct {
	fakeRelationSource
	started chan struct{}
	ready   chan struct{}
	err     error
	calls   atomic.Int32
	purpose string
}

func (source *gatedRelationSource) PrepareUpstream(ctx context.Context, purpose string) error {
	source.purpose = purpose
	close(source.started)
	select {
	case <-source.ready:
		return source.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (source *gatedRelationSource) FetchBoardCatalog(context.Context, graymarket.BoardType) ([]graymarket.Board, error) {
	source.calls.Add(1)
	return nil, errors.New("catalog reached after preparation")
}

func TestRelationsWaitForPrewarmAndNeverPublishOnPrewarmFailure(testingT *testing.T) {
	for _, failPrewarm := range []bool{false, true} {
		name := "completed"
		if failPrewarm {
			name = "partial"
		}
		testingT.Run(name, func(test *testing.T) {
			store, err := sqlite.Open(":memory:")
			if err != nil {
				test.Fatal(err)
			}
			defer store.Close()
			source := &gatedRelationSource{started: make(chan struct{}), ready: make(chan struct{})}
			if failPrewarm {
				source.err = proxypool.ErrPrewarmIncomplete
			}
			service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
			service.retries = 0
			finished := make(chan error, 1)
			go func() { finished <- service.CollectStockBoardRelations(context.Background(), "2026-11-05") }()
			select {
			case <-source.started:
			case <-time.After(time.Second):
				test.Fatal("relation preparation was not started")
			}
			if source.calls.Load() != 0 || source.purpose != "relations" {
				test.Fatal("relation request escaped prewarm gate")
			}
			close(source.ready)
			err = <-finished
			if failPrewarm {
				if !errors.Is(err, proxypool.ErrPrewarmIncomplete) || source.calls.Load() != 0 {
					test.Fatalf("failed prewarm reached catalog: %v", err)
				}
			} else if source.calls.Load() != 1 {
				test.Fatal("successful prewarm did not release catalog request")
			}
			if success, err := store.HasSuccessfulRelationSync(context.Background(), "2026-11-05"); err != nil || success {
				test.Fatal("failed synchronization published a successful relation snapshot")
			}
		})
	}
}

type gatedMarketSource struct {
	fakeSource
	purposes []string
	err      error
}

func (source *gatedMarketSource) PrepareUpstream(_ context.Context, purpose string) error {
	source.purposes = append(source.purposes, purpose)
	return source.err
}

func TestArchivePreparationFailureCannotUseMissingQuoteDegradation(testingT *testing.T) {
	source := &gatedMarketSource{err: proxypool.ErrPrewarmIncomplete}
	service := New(source, &fakeStore{}, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAllowMissingQuotes(true)
	if err := service.CollectEndOfDay(context.Background(), time.Now()); !errors.Is(err, proxypool.ErrPrewarmIncomplete) {
		testingT.Fatalf("prewarm failure was degraded into an archive: %v", err)
	}
	if source.calls != 0 || len(source.purposes) != 1 || source.purposes[0] != "end-of-day" {
		testingT.Fatal("archive fetched before completing preparation")
	}
}

func TestKlinePreparationFailureDoesNotFetchOrSeal(testingT *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	runAt := time.Date(2026, 11, 5, 16, 15, 0, 0, location)
	records := []graymarket.RankRecord{{TradeDate: "2026-11-05", Market: 0, Code: "000001", QuoteAvailable: true}}
	store := &fakeStore{missingKlines: []string{"000001"}, dailyCloseRows: records}
	source := &gatedMarketSource{err: proxypool.ErrPrewarmIncomplete}
	service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.CollectStockKlines(context.Background(), runAt); !errors.Is(err, proxypool.ErrPrewarmIncomplete) {
		testingT.Fatalf("kline preparation failure was not propagated: %v", err)
	}
	if len(source.klineBatches) != 0 || len(source.purposes) != 1 || source.purposes[0] != "stock-kline" {
		testingT.Fatal("kline request escaped prewarm gate")
	}
}

type requestRecordSource struct {
	fakeSource
	purpose     string
	requests    int
	concurrency int
}

func (source *requestRecordSource) PrepareUpstreamRequests(_ context.Context, purpose string, requests int, concurrency int) error {
	source.purpose = purpose
	source.requests = requests
	source.concurrency = concurrency
	return nil
}

func TestKlinePreparationPassesMissingRecordCountAndConcurrency(testingT *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	runAt := time.Date(2026, 11, 5, 16, 15, 0, 0, location)
	for _, count := range []int{1, 3, 5} {
		codes := make([]string, 0, count)
		records := make([]graymarket.RankRecord, 0, count)
		for index := 0; index < count; index++ {
			code := fmt.Sprintf("60000%d", index+1)
			codes = append(codes, code)
			records = append(records, graymarket.RankRecord{
				TradeDate: "2026-11-05", Market: 1, Code: code, QuoteAvailable: true,
			})
		}
		store := &fakeStore{missingKlines: codes, dailyCloseRows: records}
		source := &requestRecordSource{}
		service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
		_ = service.CollectStockKlines(context.Background(), runAt)
		expectedConcurrency := min(4, count)
		if source.purpose != "stock-kline" || source.requests != count || source.concurrency != expectedConcurrency {
			testingT.Fatalf("count=%d: purpose=%s requests=%d concurrency=%d, expected purpose=stock-kline requests=%d concurrency=%d",
				count, source.purpose, source.requests, source.concurrency, count, expectedConcurrency)
		}
	}
}

func TestNoPendingRequestsSkipLegacyPreparation(testingT *testing.T) {
	source := &gatedMarketSource{err: errors.New("unexpected legacy preparation")}
	service := New(source, &fakeStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.PrepareUpstreamRequests(context.Background(), "stock-kline", 0, 4); err != nil {
		testingT.Fatalf("zero workload reached legacy prewarmer: %v", err)
	}
	if len(source.purposes) != 0 {
		testingT.Fatal("zero workload invoked legacy prewarmer")
	}
}

type requestRelationSource struct {
	fakeRelationSource
	purpose     string
	requests    int
	concurrency int
}

func (source *requestRelationSource) PrepareUpstreamRequests(_ context.Context, purpose string, requests int, concurrency int) error {
	source.purpose = purpose
	source.requests = requests
	source.concurrency = concurrency
	return errors.New("abort after prepare")
}

func TestRelationsPreparationPassesSequentialConcurrency(testingT *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	source := &requestRelationSource{}
	service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = service.CollectStockBoardRelations(context.Background(), "2026-11-05")
	if source.purpose != "relations" || source.requests != -1 || source.concurrency != 1 {
		testingT.Fatalf("unexpected relation prewarm: purpose=%s requests=%d concurrency=%d",
			source.purpose, source.requests, source.concurrency)
	}
}
