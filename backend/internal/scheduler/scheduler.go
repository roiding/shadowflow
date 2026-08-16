package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/collector"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

type Scheduler struct {
	collector *collector.Service
	calendar  *tradingcalendar.Calendar
	logger    *slog.Logger
	location  *time.Location
	mu        sync.Mutex
	running   bool
	lastKeys  map[string]struct{}
}

func New(service *collector.Service, calendar *tradingcalendar.Calendar, logger *slog.Logger) (*Scheduler, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	return &Scheduler{collector: service, calendar: calendar, logger: logger, location: location, lastKeys: make(map[string]struct{})}, nil
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case current := <-ticker.C:
			s.check(ctx, current.In(s.location))
		}
	}
}

func (s *Scheduler) check(ctx context.Context, current time.Time) {
	if current.Second() < 5 {
		return
	}
	kind := jobKind(current)
	if kind == "" || kind != "cleanup" && !s.calendar.IsTradingDay(current) {
		return
	}
	key := current.Format("2006-01-02T15:04") + ":" + kind
	s.mu.Lock()
	if _, ok := s.lastKeys[key]; ok || s.running {
		s.mu.Unlock()
		return
	}
	s.lastKeys[key], s.running = struct{}{}, true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.prune(current)
			s.mu.Unlock()
		}()
		timeout := 50 * time.Second
		if kind == "end-of-day" {
			timeout = 15 * time.Minute
		} else if kind == "stock-kline" {
			timeout = 90 * time.Minute
		} else if kind == "relations" {
			timeout = 45 * time.Minute
		} else if kind == "cleanup" {
			timeout = 2 * time.Minute
		}
		jobCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var err error
		switch kind {
		case "minute":
			err = s.collector.CollectBoards(jobCtx, current.Truncate(time.Minute))
		case "end-of-day":
			if s.collector.HasEndOfDayArchive(jobCtx, current.Format("2006-01-02")) {
				s.logger.Info("end-of-day archive already available; skipping retry", "trade_date", current.Format("2006-01-02"), "at", current)
				return
			}
			err = s.collector.CollectEndOfDay(jobCtx, current)
		case "cleanup":
			err = s.collector.CleanupArchivedIntraday(jobCtx, current.Format("2006-01-02"))
		case "stock-kline":
			if s.collector.HasStockKlineArchive(jobCtx, current.Format("2006-01-02")) {
				s.logger.Info("stock kline archive already available; skipping retry", "trade_date", current.Format("2006-01-02"), "at", current)
				return
			}
			if !s.collector.HasEndOfDayArchive(jobCtx, current.Format("2006-01-02")) {
				s.logger.Warn("stock kline archive is waiting for end-of-day money archive", "trade_date", current.Format("2006-01-02"), "at", current)
				return
			}
			err = s.collector.CollectStockKlines(jobCtx, current)
		case "relations":
			tradeDate := current.Format("2006-01-02")
			if s.collector.HasStockBoardRelations(jobCtx, tradeDate) {
				s.logger.Info("stock-board relations already synchronized; skipping retry", "trade_date", tradeDate, "at", current)
				return
			}
			err = s.collector.CollectStockBoardRelations(jobCtx, tradeDate)
		}
		if err != nil {
			s.logger.Error("scheduled job failed", "kind", kind, "at", current, "error", err)
		}
	}()
}

func jobKind(current time.Time) string {
	switch {
	case isTradingMinute(current):
		return "minute"
	case current.Hour() == 16 && (current.Minute() == 0 || current.Minute() == 5 || current.Minute() == 10):
		return "end-of-day"
	case current.Hour() == 16 && current.Minute() == 15,
		current.Hour() == 17 && current.Minute() == 30,
		current.Hour() == 20 && current.Minute() == 0:
		return "stock-kline"
	case current.Hour() == 9 && current.Minute() == 0:
		return "cleanup"
	case current.Hour() == 8 && (current.Minute() == 0 || current.Minute() == 50),
		current.Hour() == 9 && current.Minute() == 15:
		return "relations"
	default:
		return ""
	}
}

func isTradingMinute(value time.Time) bool {
	timeOfDay := value.Format("15:04")
	return timeOfDay >= "09:31" && timeOfDay <= "11:30" || timeOfDay >= "13:01" && timeOfDay <= "15:00"
}

func (s *Scheduler) prune(current time.Time) {
	cutoff := current.Add(-24 * time.Hour).Format("2006-01-02")
	for key := range s.lastKeys {
		if len(key) >= 10 && key[:10] < cutoff {
			delete(s.lastKeys, key)
		}
	}
}
