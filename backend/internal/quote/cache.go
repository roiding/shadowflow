package quote

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type Source interface {
	FetchStockQuotes(context.Context, []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error)
}

type Status string

const (
	StatusReady       Status = "ready"
	StatusWarming     Status = "warming"
	StatusStale       Status = "stale"
	StatusUnavailable Status = "unavailable"
)

type Snapshot struct {
	Quotes    []graymarket.StockQuote
	FetchedAt time.Time
	Error     string
}

type entry struct {
	snapshot    Snapshot
	refreshing  bool
	lastAttempt time.Time
	lastSuccess time.Time
	lastAccess  time.Time
	lastError   string
}

type Cache struct {
	source         Source
	logger         *slog.Logger
	location       *time.Location
	tradingTTL     time.Duration
	idleTTL        time.Duration
	staleLimit     time.Duration
	requestTimeout time.Duration
	maxEntries     int
	mu             sync.Mutex
	entries        map[string]*entry
}

func NewCache(source Source, logger *slog.Logger) *Cache {
	location, _ := time.LoadLocation("Asia/Shanghai")
	return &Cache{
		source: source, logger: logger, location: location,
		tradingTTL: 15 * time.Second, idleTTL: 5 * time.Minute,
		staleLimit: 10 * time.Minute, requestTimeout: 5 * time.Second, maxEntries: 256,
		entries: make(map[string]*entry),
	}
}

// Snapshot never performs a blocking upstream request. A cache miss starts a
// background refresh and returns archived data so API latency remains bounded
// by SQLite rather than EastMoney availability.
func (c *Cache) Snapshot(boardType graymarket.BoardType, boardCode string, relations []graymarket.StockBoardRelation) (Snapshot, Status) {
	key := string(boardType) + ":" + boardCode
	now := time.Now()
	c.mu.Lock()
	item := c.entries[key]
	if item == nil {
		c.evictLocked()
		item = &entry{}
		c.entries[key] = item
	}
	item.lastAccess = now
	snapshot, status := c.visible(item, now)
	// A refresh goroutine holds the flag for at most requestTimeout plus
	// scheduling slack; beyond stuckAfter the marker is treated as leaked
	// (e.g. a panicked goroutine) so the board can refresh again instead of
	// being frozen forever.
	stuck := item.refreshing && !item.lastAttempt.IsZero() && now.Sub(item.lastAttempt) > c.stuckAfter()
	shouldRefresh := (!item.refreshing || stuck) && (item.lastAttempt.IsZero() || now.Sub(item.lastAttempt) >= c.ttl(now))
	if shouldRefresh {
		item.refreshing = true
		item.lastAttempt = now
	}
	c.mu.Unlock()
	if shouldRefresh {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					if c.logger != nil {
						c.logger.Error("board quote refresh panicked", "board", key, "panic", r)
					}
					c.mu.Lock()
					if item := c.entries[key]; item != nil {
						item.refreshing = false
					}
					c.mu.Unlock()
				}
			}()
			c.refresh(key, relations)
		}()
	}
	return snapshot, status
}

func (c *Cache) stuckAfter() time.Duration {
	return c.requestTimeout * 6
}

func (c *Cache) visible(item *entry, now time.Time) (Snapshot, Status) {
	snapshot := item.snapshot
	snapshot.Error = item.lastError
	if item.lastSuccess.IsZero() {
		if item.lastAttempt.IsZero() || now.Sub(item.lastAttempt) <= c.staleLimit {
			return snapshot, StatusWarming
		}
		return snapshot, StatusUnavailable
	}
	if now.Sub(item.lastSuccess) <= c.ttl(now) {
		return snapshot, StatusReady
	}
	if now.Sub(item.lastSuccess) <= c.staleLimit {
		return snapshot, StatusStale
	}
	return Snapshot{Error: item.lastError}, StatusUnavailable
}

func (c *Cache) ttl(now time.Time) time.Duration {
	local := now.In(c.location)
	minute := local.Hour()*60 + local.Minute()
	trading := local.Weekday() >= time.Monday && local.Weekday() <= time.Friday &&
		((minute >= 570 && minute <= 690) || (minute >= 781 && minute <= 900))
	if trading {
		return c.tradingTTL
	}
	return c.idleTTL
}

func (c *Cache) refresh(key string, relations []graymarket.StockBoardRelation) {
	ctx, cancel := context.WithTimeout(context.Background(), c.requestTimeout)
	defer cancel()
	quotes, err := c.source.FetchStockQuotes(ctx, relations)
	fetchedAt := time.Now().UTC()
	next := Snapshot{Quotes: quotes, FetchedAt: fetchedAt}
	if err != nil {
		next.Error = err.Error()
		if c.logger != nil {
			c.logger.Warn("refresh board quotes", "board", key, "error", err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	item := c.entries[key]
	if item == nil {
		item = &entry{}
		c.entries[key] = item
	}
	item.refreshing = false
	item.lastError = next.Error
	if err == nil {
		item.snapshot = next
		item.lastSuccess = fetchedAt
	}
	c.trimLocked()
}

func (c *Cache) evictLocked() {
	if c.maxEntries <= 0 {
		return
	}
	for len(c.entries) >= c.maxEntries && c.evictOneLocked() {
	}
}

func (c *Cache) trimLocked() {
	if c.maxEntries <= 0 {
		return
	}
	for len(c.entries) > c.maxEntries && c.evictOneLocked() {
	}
}

func (c *Cache) evictOneLocked() bool {
	now := time.Now()
	var oldestKey string
	var oldest time.Time
	for key, item := range c.entries {
		// Keep entries with a live in-flight refresh, but do not let a leaked
		// marker make an entry permanently unevictable.
		if item.refreshing && now.Sub(item.lastAttempt) <= c.stuckAfter() {
			continue
		}
		if oldestKey == "" || item.lastAccess.Before(oldest) {
			oldestKey, oldest = key, item.lastAccess
		}
	}
	if oldestKey == "" {
		return false
	}
	delete(c.entries, oldestKey)
	return true
}
