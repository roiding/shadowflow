package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
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
	HasEndOfDayArchive(context.Context, string) (bool, error)
	HasStockKlineArchive(context.Context, string) (bool, error)
	CollectStockBoardRelations(context.Context, string) error
	HasStockBoardRelations(context.Context, string) (bool, error)
}

type Options struct {
	SuccessRunRetentionDays int
	FailureRunRetentionDays int
	Jobs                    JobStore
	InstanceID              string
}

type Scheduler struct {
	collector collectorService
	calendar  *tradingcalendar.Calendar
	logger    *slog.Logger
	location  *time.Location
	jobs      JobStore
	owner     string
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
	config := Options{SuccessRunRetentionDays: 30, FailureRunRetentionDays: 180, Jobs: noopJobStore{}}
	if len(options) > 0 {
		config = options[0]
	}
	if config.Jobs == nil {
		config.Jobs = noopJobStore{}
	}
	owner := strings.TrimSpace(config.InstanceID)
	if owner == "" {
		var seed [8]byte
		if _, err := rand.Read(seed[:]); err != nil {
			return nil, fmt.Errorf("generate scheduler owner: %w", err)
		}
		owner = hex.EncodeToString(seed[:])
	}
	return &Scheduler{
		collector: service,
		calendar:  calendar,
		logger:    logger,
		location:  location,
		jobs:      config.Jobs,
		owner:     owner,
		running:   make(map[string]bool),
		lastKeys:  make(map[string]struct{}),
		options:   config,
	}, nil
}

func (s *Scheduler) Run(ctx context.Context) {
	current := time.Now().In(s.location)
	s.recoverLatestArchive(ctx, current)
	s.enqueueReplayableJobs(ctx, current)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			current := now.In(s.location)
			s.expireLeases(ctx, current)
			s.startDueJobs(ctx, current)
			s.check(ctx, current)
		}
	}
}

func (s *Scheduler) expireLeases(ctx context.Context, now time.Time) {
	if err := s.jobs.ExpireLeasedJobs(ctx, now); err != nil {
		s.logger.Error("expire scheduler leases", "error", err)
	}
}

func (s *Scheduler) startDueJobs(ctx context.Context, now time.Time) {
	due, err := s.jobs.DueScheduledJobs(ctx, now, 16)
	if err != nil {
		s.logger.Error("query due scheduled jobs", "error", err)
		return
	}
	for _, job := range due {
		s.claimAndLaunch(ctx, job, now)
	}
}

func (s *Scheduler) enqueueReplayableJobs(ctx context.Context, now time.Time) {
	tradeDate := now.Format("2006-01-02")
	if !s.calendar.IsTradingDay(now) {
		return
	}
	plans := []struct {
		kind string
		at   string
	}{
		{"cleanup", "09:00"}, {"maintenance", "09:05"},
		{"relations", "08:00"}, {"relations", "08:50"}, {"relations", "09:15"},
		{"end-of-day", "16:00"}, {"end-of-day", "16:05"}, {"end-of-day", "16:10"},
		{"stock-kline", "16:15"}, {"stock-kline", "17:30"}, {"stock-kline", "20:00"},
	}
	for _, plan := range plans {
		plannedAt, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+plan.at, s.location)
		if err != nil || plannedAt.After(now) {
			continue
		}
		// Later retry invitations are redundant when an earlier instance is still queued.
		job := newScheduledJob(plan.kind, plannedAt, tradeDate)
		if err := s.jobs.EnsureScheduledJob(ctx, job); err != nil {
			s.logger.Error("enqueue replayable scheduled job", "kind", plan.kind, "trade_date", tradeDate, "error", err)
		}
	}
}

func newScheduledJob(kind string, plannedAt time.Time, tradeDate string) ScheduledJob {
	policy := policyFor(kind)
	return ScheduledJob{
		JobKey: jobKey(plannedAt, kind, tradeDate), Kind: kind, TradeDate: tradeDate,
		PlannedAt: plannedAt, Status: JobQueued, MaxAttempts: policy.maxAttempts,
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
	key := jobKey(current, kind, tradeDate)
	s.mu.Lock()
	if s.running[lane] {
		if _, seen := s.lastKeys[key]; seen {
			s.mu.Unlock()
			return false
		}
		s.mu.Unlock()
		return false
	}
	if _, seen := s.lastKeys[key]; seen {
		s.mu.Unlock()
		return false
	}
	s.lastKeys[key] = struct{}{}
	s.running[lane] = true
	s.mu.Unlock()

	job := newScheduledJob(kind, current.Truncate(time.Minute), tradeDate)
	if err := s.jobs.EnsureScheduledJob(ctx, job); err != nil {
		s.release(lane, key, current)
		s.logger.Error("ensure scheduled job", "kind", kind, "job_key", key, "error", err)
		return false
	}
	leaseUntil := current.Add(policyFor(kind).timeout + 30*time.Second)
	claimed, ok, err := s.jobs.ClaimScheduledJob(ctx, job, s.owner, current, leaseUntil)
	if err != nil {
		s.release(lane, key, current)
		s.logger.Error("claim scheduled job", "kind", kind, "job_key", key, "error", err)
		return false
	}
	if !ok {
		s.release(lane, key, current)
		return false
	}
	go func() {
		defer s.release(lane, key, current)
		s.runJob(ctx, claimed, current)
	}()
	return true
}

func (s *Scheduler) claimAndLaunch(ctx context.Context, candidate ScheduledJob, now time.Time) bool {
	lane := jobLane(candidate.Kind)
	key := candidate.JobKey
	s.mu.Lock()
	if s.running[lane] {
		if _, seen := s.lastKeys[key]; seen {
			s.mu.Unlock()
			return false
		}
		s.mu.Unlock()
		return false
	}
	if _, seen := s.lastKeys[key]; seen {
		s.mu.Unlock()
		return false
	}
	s.lastKeys[key] = struct{}{}
	s.running[lane] = true
	s.mu.Unlock()
	leaseUntil := now.Add(policyFor(candidate.Kind).timeout + 30*time.Second)
	claimed, ok, err := s.jobs.ClaimScheduledJob(ctx, candidate, s.owner, now, leaseUntil)
	if err != nil {
		s.release(lane, key, now)
		s.logger.Error("claim due scheduled job", "kind", candidate.Kind, "job_key", key, "error", err)
		return false
	}
	if !ok {
		s.release(lane, key, now)
		return false
	}
	go func() {
		defer s.release(lane, key, now)
		s.runJob(ctx, claimed, now)
	}()
	return true
}

func (s *Scheduler) release(lane, key string, current time.Time) {
	s.mu.Lock()
	delete(s.lastKeys, key)
	s.running[lane] = false
	s.mu.Unlock()
	s.prune(current)
}

func (s *Scheduler) runJob(parent context.Context, job ScheduledJob, current time.Time) {
	ctx, cancel := context.WithTimeout(parent, policyFor(job.Kind).timeout)
	defer cancel()
	startedAt := time.Now().UTC()

	err := s.executeJob(ctx, job, current)
	finishedAt := time.Now().UTC()
	job.DurationMS = finishedAt.Sub(startedAt).Milliseconds()
	switch {
	case ctx.Err() != nil && parent.Err() == nil:
		job.Status = JobFailed
		job.LastErrorCode, job.LastError = "timeout", ctx.Err().Error()
	case err == nil:
		job.Status = JobSucceeded
	default:
		job.Status = JobFailed
		job.LastErrorCode, job.LastError = errorCode(err), err.Error()
	}
	if job.Status == JobFailed {
		policy := policyFor(job.Kind)
		if job.AttemptCount < policy.maxAttempts {
			retryAt := current.Add(policy.retryAfter)
			job.RetryAt = &retryAt
		} else {
			job.RetryAt = nil
		}
	}
	if err := s.jobs.FinishScheduledJob(context.WithoutCancel(ctx), job); err != nil {
		s.logger.Error("finish scheduled job", "kind", job.Kind, "job_key", job.JobKey, "error", err)
	}
	if err != nil {
		s.logger.Error("scheduled job failed", "kind", job.Kind, "trade_date", job.TradeDate,
			"at", current, "attempt", job.AttemptCount, "retry_at", job.RetryAt, "error", err)
	}
}

func (s *Scheduler) executeJob(ctx context.Context, job ScheduledJob, current time.Time) error {
	tradeDate := job.TradeDate
	switch job.Kind {
	case "minute":
		return s.collector.CollectBoards(ctx, job.PlannedAt)
	case "end-of-day":
		exists, err := s.collector.HasEndOfDayArchive(ctx, tradeDate)
		if err != nil {
			return err
		}
		if exists {
			s.logger.Info("end-of-day archive already available; skipping retry", "trade_date", tradeDate, "at", current)
			return nil
		}
		return s.collector.CollectEndOfDay(ctx, parseShanghaiTime(tradeDate, "16:00", current))
	case "cleanup":
		return s.collector.CleanupArchivedIntraday(ctx, tradeDate)
	case "maintenance":
		_, err := s.collector.Maintain(ctx, current, s.options.SuccessRunRetentionDays, s.options.FailureRunRetentionDays)
		return err
	case "stock-kline":
		exists, err := s.collector.HasStockKlineArchive(ctx, tradeDate)
		if err != nil {
			return err
		}
		if exists {
			s.logger.Info("stock kline archive already available; skipping retry", "trade_date", tradeDate, "at", current)
			return nil
		}
		endExists, err := s.collector.HasEndOfDayArchive(ctx, tradeDate)
		if err != nil {
			return err
		}
		if !endExists {
			s.logger.Warn("stock kline archive is waiting for end-of-day money archive", "trade_date", tradeDate, "at", current)
			return nil
		}
		return s.collector.CollectStockKlines(ctx, parseShanghaiTime(tradeDate, "16:15", current))
	case "relations":
		exists, err := s.collector.HasStockBoardRelations(ctx, tradeDate)
		if err != nil {
			return err
		}
		if exists {
			s.logger.Info("stock-board relations already synchronized; skipping retry", "trade_date", tradeDate, "at", current)
			return nil
		}
		return s.collector.CollectStockBoardRelations(ctx, tradeDate)
	case "startup-recovery":
		return s.recoverArchive(ctx, tradeDate, current)
	default:
		return fmt.Errorf("unknown scheduled job kind %q", job.Kind)
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
	exists, err := s.collector.HasEndOfDayArchive(ctx, tradeDate)
	if err != nil {
		return err
	}
	if !exists {
		if err := s.collector.CollectEndOfDay(ctx, parseShanghaiTime(tradeDate, "16:00", current)); err != nil {
			return err
		}
	}
	exists, err = s.collector.HasStockKlineArchive(ctx, tradeDate)
	if err != nil {
		return err
	}
	if !exists {
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

func jobKey(current time.Time, kind, tradeDate string) string {
	return current.Format("2006-01-02T15:04") + ":" + kind + ":" + tradeDate
}

func errorCode(err error) string {
	message := err.Error()
	if len(message) > 80 {
		return message[:80]
	}
	return message
}

func (s *Scheduler) prune(current time.Time) {
	cutoff := current.Add(-24 * time.Hour).Format("2006-01-02")
	for key := range s.lastKeys {
		if len(key) >= 10 && key[:10] < cutoff {
			delete(s.lastKeys, key)
		}
	}
}
