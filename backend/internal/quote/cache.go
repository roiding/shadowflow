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
}

type Cache struct {
	source         Source
	logger         *slog.Logger
	location       *time.Location
	tradingTTL     time.Duration
	idleTTL        time.Duration
	staleLimit     time.Duration
	requestTimeout time.Duration
	mu             sync.Mutex
	entries        map[string]*entry
}

func NewCache(source Source, logger *slog.Logger) *Cache {
	location, _ := time.LoadLocation("Asia/Shanghai")
	return &Cache{
		source: source, logger: logger, location: location,
		tradingTTL: 15 * time.Second, idleTTL: 5 * time.Minute,
		staleLimit: 10 * time.Minute, requestTimeout: 5 * time.Second,
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
		item = &entry{}
		c.entries[key] = item
	}
	snapshot, status := c.visible(item, now)
	shouldRefresh := !item.refreshing && (item.lastAttempt.IsZero() || now.Sub(item.lastAttempt) >= c.ttl(now))
	if shouldRefresh {
		item.refreshing = true
		item.lastAttempt = now
	}
	c.mu.Unlock()
	if shouldRefresh {
		go c.refresh(key, relations)
	}
	return snapshot, status
}

func (c *Cache) visible(item *entry, now time.Time) (Snapshot, Status) {
	if item.lastSuccess.IsZero() {
		if item.lastAttempt.IsZero() || now.Sub(item.lastAttempt) <= c.staleLimit {
			return Snapshot{}, StatusWarming
		}
		return Snapshot{}, StatusUnavailable
	}
	if now.Sub(item.lastSuccess) <= c.ttl(now) {
		return item.snapshot, StatusReady
	}
	if now.Sub(item.lastSuccess) <= c.staleLimit {
		return item.snapshot, StatusStale
	}
	return Snapshot{}, StatusUnavailable
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
	if err == nil {
		item.snapshot = next
		item.lastSuccess = fetchedAt
	}
}
