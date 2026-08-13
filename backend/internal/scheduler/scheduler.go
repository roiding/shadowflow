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
	if !s.calendar.IsTradingDay(current) || current.Second() < 5 {
		return
	}
	kind := jobKind(current)
	if kind == "" {
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
		if kind == "daily-close" || kind == "compact" {
			timeout = 2 * time.Minute
		}
		jobCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var err error
		switch kind {
		case "minute":
			err = s.collector.CollectBoards(jobCtx, current.Truncate(time.Minute))
		case "compact":
			_, err = s.collector.CompactAndCleanup(jobCtx, current.Format("2006-01-02"))
		case "daily-close":
			if s.collector.HasDailyClose(jobCtx, current.Format("2006-01-02")) {
				s.logger.Info("daily close already available; skipping retry", "trade_date", current.Format("2006-01-02"), "at", current)
				return
			}
			snapshotAt := time.Date(current.Year(), current.Month(), current.Day(), 15, 0, 0, 0, s.location)
			err = s.collector.CollectDailyClose(jobCtx, snapshotAt)
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
	case current.Hour() == 15 && (current.Minute() == 5 || current.Minute() == 7 || current.Minute() == 9):
		return "compact"
	case current.Hour() == 15 && (current.Minute() == 10 || current.Minute() == 20 || current.Minute() == 30):
		return "daily-close"
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
