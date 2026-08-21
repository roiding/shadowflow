package quote

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type slowSource struct {
	mu      sync.Mutex
	calls   int
	release chan struct{}
}

func (s *slowSource) FetchStockQuotes(context.Context, []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		<-s.release
	}
	return []graymarket.StockQuote{{StockCode: "000001", Available: true}}, nil
}

func TestSnapshotDoesNotBlockOnFirstRefresh(t *testing.T) {
	source := &slowSource{release: make(chan struct{})}
	cache := NewCache(source, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := time.Now()
	snapshot, status := cache.Snapshot(graymarket.BoardIndustry, "BK001", nil)
	if time.Since(started) > 20*time.Millisecond || status != StatusWarming {
		t.Fatalf("first snapshot blocked or wrong status=%s elapsed=%s", status, time.Since(started))
	}
	close(source.release)
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, status = cache.Snapshot(graymarket.BoardIndustry, "BK001", nil)
		if status == StatusReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache never warmed: %s", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(snapshot.Quotes) != 1 || !snapshot.Quotes[0].Available {
		t.Fatalf("unexpected snapshot %+v", snapshot)
	}
}

type failingSource struct{}

func (failingSource) FetchStockQuotes(context.Context, []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	return nil, context.DeadlineExceeded
}

func TestRepeatedWarmupFailuresBecomeUnavailable(t *testing.T) {
	cache := NewCache(failingSource{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, status := cache.Snapshot(graymarket.BoardIndustry, "BK001", nil); status != StatusWarming {
		t.Fatalf("initial status=%s", status)
	}
	cache.mu.Lock()
	cache.entries["industry:BK001"].lastAttempt = time.Now().Add(-11 * time.Minute)
	cache.mu.Unlock()
	if _, status := cache.Snapshot(graymarket.BoardIndustry, "BK001", nil); status != StatusUnavailable {
		t.Fatalf("failed warmup status=%s", status)
	}
}
