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

func TestRefreshFailureIsVisibleToCallers(t *testing.T) {
	cache := NewCache(failingSource{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cache.Snapshot(graymarket.BoardIndustry, "BK001", nil)
	deadline := time.Now().Add(time.Second)
	for {
		cache.mu.Lock()
		refreshing := cache.entries["industry:BK001"].refreshing
		cache.mu.Unlock()
		if !refreshing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	snapshot, _ := cache.Snapshot(graymarket.BoardIndustry, "BK001", nil)
	if snapshot.Error == "" {
		t.Fatal("latest refresh error was hidden")
	}
}

func TestCacheEvictsLeastRecentlyUsedEntry(t *testing.T) {
	cache := NewCache(failingSource{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cache.maxEntries = 2
	cache.mu.Lock()
	cache.entries["industry:old"] = &entry{lastAccess: time.Now().Add(-time.Hour)}
	cache.entries["industry:new"] = &entry{lastAccess: time.Now()}
	cache.mu.Unlock()
	cache.Snapshot(graymarket.BoardIndustry, "third", nil)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, ok := cache.entries["industry:old"]; ok {
		t.Fatal("least recently used entry was not evicted")
	}
	if len(cache.entries) != 2 {
		t.Fatalf("entry count=%d, want 2", len(cache.entries))
	}
}

type blockingSource struct {
	release chan struct{}
}

func (s blockingSource) FetchStockQuotes(context.Context, []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	<-s.release
	return nil, nil
}

func TestCacheTrimsTemporaryOverflowAfterRefreshesFinish(t *testing.T) {
	release := make(chan struct{})
	cache := NewCache(blockingSource{release: release}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cache.maxEntries = 2
	for _, code := range []string{"one", "two", "three"} {
		cache.Snapshot(graymarket.BoardIndustry, code, nil)
	}
	cache.mu.Lock()
	if len(cache.entries) != 3 {
		cache.mu.Unlock()
		t.Fatalf("expected temporary overflow while all entries refresh, got %d", len(cache.entries))
	}
	cache.mu.Unlock()
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		cache.mu.Lock()
		count := len(cache.entries)
		refreshing := false
		for _, item := range cache.entries {
			refreshing = refreshing || item.refreshing
		}
		cache.mu.Unlock()
		if count <= 2 && !refreshing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache did not return to its entry limit: count=%d refreshing=%v", count, refreshing)
		}
		time.Sleep(time.Millisecond)
	}
}
