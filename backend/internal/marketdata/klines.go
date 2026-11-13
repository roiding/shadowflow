package marketdata

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type StockKlineProvider interface {
	FetchStockKline5m(context.Context, graymarket.RankRecord) ([]graymarket.StockKlinePoint, error)
}

// StockKlineService adapts the router to the collector's existing bulk
// interface, preserving per-stock incremental persistence.
type StockKlineService struct {
	router   *Router[StockKlineProvider]
	workers  int
	interval time.Duration
}

func NewStockKlineService(router *Router[StockKlineProvider]) *StockKlineService {
	return &StockKlineService{router: router, workers: 4, interval: 100 * time.Millisecond}
}

func (s *StockKlineService) primaryHealthy() bool {
	healthy := s.router.healthy()
	return len(healthy) > 0 && healthy[0].name == s.router.entries[0].name
}

type klineResult struct {
	points []graymarket.StockKlinePoint
	err    error
}

// FetchStockKlines5m implements the collector's bulk interface. Each worker
// goes through the shared router, so one failed stock changes the provider
// for the whole remaining archive rather than only for that stock.
func (s *StockKlineService) FetchStockKlines5m(ctx context.Context, snapshot graymarket.RankSnapshot) ([]graymarket.StockKlinePoint, error) {
	points := make([]graymarket.StockKlinePoint, 0, len(snapshot.Records)*48)
	_, err := s.FetchStockKlines5mIncremental(ctx, snapshot, func(batch []graymarket.StockKlinePoint) error {
		points = append(points, batch...)
		return nil
	})
	if err != nil {
		return points, err
	}
	if len(points) != len(snapshot.Records)*48 {
		return nil, fmt.Errorf("incomplete stock kline archive: expected %d points, got %d", len(snapshot.Records)*48, len(points))
	}
	return points, nil
}

func (s *StockKlineService) FetchStockKlines5mIncremental(ctx context.Context, snapshot graymarket.RankSnapshot, persist func([]graymarket.StockKlinePoint) error) (int, error) {
	if snapshot.RankType != graymarket.RankStock || snapshot.TradeDate == "" || len(snapshot.Records) == 0 {
		return 0, fmt.Errorf("invalid stock kline snapshot")
	}
	parentCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerCount := min(s.workers, len(snapshot.Records))
	jobs := make(chan graymarket.RankRecord, workerCount)
	results := make(chan klineResult, workerCount)
	limiter := time.NewTicker(s.interval)
	defer limiter.Stop()
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for stock := range jobs {
				select {
				case <-ctx.Done():
					return
				case <-limiter.C:
				}
				var points []graymarket.StockKlinePoint
				err := s.router.Do(ctx, func(ctx context.Context, provider StockKlineProvider) error {
					fetched, err := provider.FetchStockKline5m(ctx, stock)
					if err == nil {
						points = fetched
					}
					return err
				})
				select {
				case results <- klineResult{points: points, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, stock := range snapshot.Records {
			select {
			case jobs <- stock:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	var firstErr error
	completed := 0
	failed := 0
	consecutiveFailures := 0
	for result := range results {
		if result.err != nil {
			failed++
			consecutiveFailures++
			if firstErr == nil {
				firstErr = result.err
			}
			if consecutiveFailures >= 8 {
				cancel()
			}
			continue
		}
		consecutiveFailures = 0
		if err := persist(result.points); err != nil {
			cancel()
			return completed, fmt.Errorf("persist stock kline batch: %w", err)
		}
		completed++
	}
	if err := parentCtx.Err(); err != nil {
		return completed, err
	}
	if firstErr != nil {
		return completed, fmt.Errorf("stock kline batch incomplete: completed %d/%d stocks, failed %d; first error: %w",
			completed, len(snapshot.Records), failed, firstErr)
	}
	if completed != len(snapshot.Records) {
		return completed, fmt.Errorf("incomplete stock kline archive: expected %d stocks, got %d", len(snapshot.Records), completed)
	}
	return completed, nil
}
