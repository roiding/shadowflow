package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/repository"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

type collectorService interface {
	CollectBoards(context.Context, time.Time) error
	CollectEndOfDay(context.Context, time.Time) error
	CollectStockKlines(context.Context, time.Time) error
	CleanupArchivedIntraday(context.Context, string) error
	Maintain(context.Context, time.Time, int, int) (repository.MaintenanceResult, error)
	HasEndOfDayArchive(context.Context, string) bool
	HasStockKlineArchive(context.Context, string) bool
	CollectStockBoardRelations(context.Context, string) error
	HasStockBoardRelations(context.Context, string) bool
}

type Options struct {
	SuccessRunRetentionDays int
	FailureRunRetentionDays int
}

type Scheduler struct {
	collector collectorService
	calendar  *tradingcalendar.Calendar
	logger    *slog.Logger
	location  *time.Location
	mu        sync.Mutex
	running   map[string]bool
	lastKeys  map[string]struct{}
	options   Options
}

func New(service collectorService, calendar *tradingcalendar.Calendar, logger *slog.Logger, options ...Options) (*Scheduler, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	config := Options{SuccessRunRetentionDays: 30, FailureRunRetentionDays: 180}
	if len(options) > 0 {
		config = options[0]
	}
	return &Scheduler{
		collector: service,
		calendar:  calendar,
		logger:    logger,
		location:  location,
		running:   make(map[string]bool),
		lastKeys:  make(map[string]struct{}),
		options:   config,
	}, nil
}

func (s *Scheduler) Run(ctx context.Context) {
	s.recoverLatestArchive(ctx, time.Now().In(s.location))
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
	if kind == "" || kind != "cleanup" && kind != "maintenance" && !s.calendar.IsTradingDay(current) {
		return
	}
	s.startJob(ctx, kind, current, current.Format("2006-01-02"))
}

func (s *Scheduler) startJob(ctx context.Context, kind string, current time.Time, tradeDate string) bool {
	lane := jobLane(kind)
	key := current.Format("2006-01-02T15:04") + ":" + kind + ":" + tradeDate
	s.mu.Lock()
	if s.running[lane] {
		s.mu.Unlock()
		return false
	}
	if _, ok := s.lastKeys[key]; ok {
		s.mu.Unlock()
		return false
	}
	s.lastKeys[key] = struct{}{}
	s.running[lane] = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.running[lane] = false
			s.prune(current)
			s.mu.Unlock()
		}()
		s.runJob(ctx, kind, current, tradeDate)
	}()
	return true
}

func (s *Scheduler) runJob(ctx context.Context, kind string, current time.Time, tradeDate string) {
	timeout := 50 * time.Second
	switch kind {
	case "end-of-day":
		timeout = 15 * time.Minute
	case "stock-kline":
		timeout = 90 * time.Minute
	case "relations":
		timeout = 45 * time.Minute
	case "cleanup", "maintenance":
		timeout = 2 * time.Minute
	case "startup-recovery":
		timeout = 105 * time.Minute
	}
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var err error
	switch kind {
	case "minute":
		err = s.collector.CollectBoards(jobCtx, current.Truncate(time.Minute))
	case "end-of-day":
		if s.collector.HasEndOfDayArchive(jobCtx, tradeDate) {
			s.logger.Info("end-of-day archive already available; skipping retry", "trade_date", tradeDate, "at", current)
			return
		}
		err = s.collector.CollectEndOfDay(jobCtx, parseShanghaiTime(tradeDate, "16:00", current))
	case "cleanup":
		err = s.collector.CleanupArchivedIntraday(jobCtx, tradeDate)
	case "maintenance":
		_, err = s.collector.Maintain(jobCtx, current, s.options.SuccessRunRetentionDays, s.options.FailureRunRetentionDays)
	case "stock-kline":
		if s.collector.HasStockKlineArchive(jobCtx, tradeDate) {
			s.logger.Info("stock kline archive already available; skipping retry", "trade_date", tradeDate, "at", current)
			return
		}
		if !s.collector.HasEndOfDayArchive(jobCtx, tradeDate) {
			s.logger.Warn("stock kline archive is waiting for end-of-day money archive", "trade_date", tradeDate, "at", current)
			return
		}
		err = s.collector.CollectStockKlines(jobCtx, parseShanghaiTime(tradeDate, "16:15", current))
	case "relations":
		if s.collector.HasStockBoardRelations(jobCtx, tradeDate) {
			s.logger.Info("stock-board relations already synchronized; skipping retry", "trade_date", tradeDate, "at", current)
			return
		}
		err = s.collector.CollectStockBoardRelations(jobCtx, tradeDate)
	case "startup-recovery":
		err = s.recoverArchive(jobCtx, tradeDate, current)
	}
	if err != nil {
		s.logger.Error("scheduled job failed", "kind", kind, "trade_date", tradeDate, "at", current, "error", err)
	}
}

func (s *Scheduler) recoverLatestArchive(ctx context.Context, current time.Time) {
	tradeDate, ok := recoveryTradeDate(s.calendar, current)
	if !ok {
		return
	}
	s.startJob(ctx, "startup-recovery", current, tradeDate)
}

func (s *Scheduler) recoverArchive(ctx context.Context, tradeDate string, current time.Time) error {
	if !s.collector.HasEndOfDayArchive(ctx, tradeDate) {
		if err := s.collector.CollectEndOfDay(ctx, parseShanghaiTime(tradeDate, "16:00", current)); err != nil {
			return err
		}
	}
	if !s.collector.HasStockKlineArchive(ctx, tradeDate) {
		if err := s.collector.CollectStockKlines(ctx, parseShanghaiTime(tradeDate, "16:15", current)); err != nil {
			return err
		}
	}
	return s.collector.CleanupArchivedIntraday(ctx, current.Format("2006-01-02"))
}

func recoveryTradeDate(calendar *tradingcalendar.Calendar, current time.Time) (string, bool) {
	if calendar.IsTradingDay(current) {
		afterOpen := current.Hour() > 9 || current.Hour() == 9 && current.Minute() >= 30
		if afterOpen && current.Hour() < 16 {
			return "", false
		}
	}
	var day time.Time
	if calendar.IsTradingDay(current) && current.Hour() >= 16 {
		day = current
	} else {
		day = calendar.PreviousTradingDay(current)
	}
	return day.Format("2006-01-02"), true
}

func parseShanghaiTime(tradeDate, clock string, fallback time.Time) time.Time {
	location := fallback.Location()
	value, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+clock, location)
	if err != nil {
		return time.Date(fallback.Year(), fallback.Month(), fallback.Day(), 16, 0, 0, 0, location)
	}
	return value
}

func jobLane(kind string) string {
	switch kind {
	case "minute":
		return "intraday"
	case "relations":
		return "relations"
	case "maintenance":
		return "maintenance"
	default:
		return "archive"
	}
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
	case current.Hour() == 9 && current.Minute() == 5:
		return "maintenance"
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
