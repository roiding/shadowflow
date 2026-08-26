package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

var errDependencyUnavailable = errors.New("scheduled job dependency is unavailable")

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

type archivePartCollector interface {
	CollectEndOfDayPart(context.Context, time.Time, graymarket.RankType) error
	HasEndOfDayPart(context.Context, string, graymarket.RankType) (bool, error)
}

type Options struct {
	SuccessRunRetentionDays int
	FailureRunRetentionDays int
	Jobs                    JobStore
	InstanceID              string
}

type Scheduler struct {
	collector      collectorService
	calendar       *tradingcalendar.Calendar
	logger         *slog.Logger
	location       *time.Location
	jobs           JobStore
	owner          string
	mu             sync.Mutex
	running        map[string]bool
	lastKeys       map[string]struct{}
	lastLeaseSweep time.Time
	jobsWG         sync.WaitGroup
	options        Options
}

func New(service collectorService, calendar *tradingcalendar.Calendar, logger *slog.Logger, options ...Options) (*Scheduler, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	config := Options{SuccessRunRetentionDays: 30, FailureRunRetentionDays: 180, Jobs: noopJobStore{}}
	if len(options) > 0 {
		provided := options[0]
		if provided.SuccessRunRetentionDays > 0 {
			config.SuccessRunRetentionDays = provided.SuccessRunRetentionDays
		}
		if provided.FailureRunRetentionDays > 0 {
			config.FailureRunRetentionDays = provided.FailureRunRetentionDays
		}
		if provided.Jobs != nil {
			config.Jobs = provided.Jobs
		}
		config.InstanceID = provided.InstanceID
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
			// Wait for in-flight job goroutines so they can persist their final
			// job status before the caller closes the store. Their contexts are
			// already cancelled, so this returns promptly.
			s.jobsWG.Wait()
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
	// Lease expiry is a coarse recovery mechanism (leases run minutes long);
	// sweeping every tick issued two write statements per second on the
	// single writer connection, competing with in-flight collection
	// transactions all day. Only Run's goroutine touches lastLeaseSweep.
	if now.Sub(s.lastLeaseSweep) < 30*time.Second {
		return
	}
	s.lastLeaseSweep = now
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
		{"end-of-day-industry", "16:00"}, {"end-of-day-industry", "16:05"}, {"end-of-day-industry", "16:10"},
		{"end-of-day-concept", "16:00"}, {"end-of-day-concept", "16:05"}, {"end-of-day-concept", "16:10"},
		{"end-of-day-stock", "16:00"}, {"end-of-day-stock", "16:05"}, {"end-of-day-stock", "16:10"},
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
	if kind == "end-of-day" {
		if _, ok := s.collector.(archivePartCollector); ok {
			for _, partKind := range []string{"end-of-day-industry", "end-of-day-concept", "end-of-day-stock"} {
				s.startJob(ctx, partKind, current, current.Format("2006-01-02"))
			}
			return
		}
	}
	s.startJob(ctx, kind, current, current.Format("2006-01-02"))
}

func (s *Scheduler) startJob(ctx context.Context, kind string, current time.Time, tradeDate string) bool {
	lane := jobLane(kind)
	key := jobKey(current, kind, tradeDate)

	// The in-memory dedupe comes first: while a job runs, every tick would
	// otherwise re-issue the (idempotent, but still write-locking) insert.
	s.mu.Lock()
	if _, seen := s.lastKeys[key]; seen {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	// Persist the job before contending for the lane. Jobs that share a lane
	// (e.g. the industry/concept/stock end-of-day parts) would otherwise be
	// dropped without a trace when the lane is busy; by enqueuing first, a
	// rejected job stays queued in the store and startDueJobs picks it up once
	// the lane frees. EnsureScheduledJob is idempotent (INSERT ... DO NOTHING).
	job := newScheduledJob(kind, current.Truncate(time.Minute), tradeDate)
	if err := s.jobs.EnsureScheduledJob(ctx, job); err != nil {
		s.logger.Error("ensure scheduled job", "kind", kind, "job_key", key, "error", err)
		return false
	}

	s.mu.Lock()
	if s.running[lane] {
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
	s.jobsWG.Add(1)
	go func() {
		defer s.jobsWG.Done()
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
	s.jobsWG.Add(1)
	go func() {
		defer s.jobsWG.Done()
		defer s.release(lane, key, now)
		s.runJob(ctx, claimed, now)
	}()
	return true
}

func (s *Scheduler) release(lane, key string, current time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lastKeys, key)
	s.running[lane] = false
	s.pruneLocked(current)
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
			retryAt := finishedAt.Add(policy.retryAfter)
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
		// Scheduled timestamps are persisted in UTC. Collection timestamps are
		// part of the Shanghai trading-day contract, so restore that location
		// before deriving the requested date and minute.
		return s.collector.CollectBoards(ctx, job.PlannedAt.In(s.location))
	case "end-of-day":
		if part, ok := s.collector.(archivePartCollector); ok {
			for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept, graymarket.RankStock} {
				exists, partErr := part.HasEndOfDayPart(ctx, tradeDate, rankType)
				if partErr != nil {
					return partErr
				}
				if !exists {
					if partErr = part.CollectEndOfDayPart(ctx, parseShanghaiTime(tradeDate, "16:00", current), rankType); partErr != nil {
						return partErr
					}
				}
			}
			return nil
		}
		exists, err := s.collector.HasEndOfDayArchive(ctx, tradeDate)
		if err != nil {
			return err
		}
		if exists {
			s.logger.Info("end-of-day archive already available; skipping retry", "trade_date", tradeDate, "at", current)
			return nil
		}
		return s.collector.CollectEndOfDay(ctx, parseShanghaiTime(tradeDate, "16:00", current))
	case "end-of-day-industry", "end-of-day-concept", "end-of-day-stock":
		part, ok := s.collector.(archivePartCollector)
		if !ok {
			return fmt.Errorf("archive part collector is unavailable")
		}
		rankType := graymarket.RankStock
		if job.Kind == "end-of-day-industry" {
			rankType = graymarket.RankIndustry
		}
		if job.Kind == "end-of-day-concept" {
			rankType = graymarket.RankConcept
		}
		exists, err := part.HasEndOfDayPart(ctx, tradeDate, rankType)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		return part.CollectEndOfDayPart(ctx, parseShanghaiTime(tradeDate, "16:00", current), rankType)
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
			return fmt.Errorf("%w: end-of-day archive for %s", errDependencyUnavailable, tradeDate)
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
	var combined error
	if part, ok := s.collector.(archivePartCollector); ok {
		for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept, graymarket.RankStock} {
			exists, err := part.HasEndOfDayPart(ctx, tradeDate, rankType)
			if err != nil {
				combined = errors.Join(combined, fmt.Errorf("check %s archive: %w", rankType, err))
				continue
			}
			if exists {
				s.logger.Info("startup archive part already available; skipping", "trade_date", tradeDate, "rank_type", rankType)
				continue
			}
			if err := part.CollectEndOfDayPart(ctx, parseShanghaiTime(tradeDate, "16:00", current), rankType); err != nil {
				// The other archive parts are independently retryable.
				combined = errors.Join(combined, fmt.Errorf("recover %s archive: %w", rankType, err))
			}
		}
	} else {
		exists, err := s.collector.HasEndOfDayArchive(ctx, tradeDate)
		if err != nil {
			combined = errors.Join(combined, err)
		}
		if err == nil && !exists {
			if err := s.collector.CollectEndOfDay(ctx, parseShanghaiTime(tradeDate, "16:00", current)); err != nil {
				combined = errors.Join(combined, err)
			}
		}
	}
	klineExists, klineErr := s.collector.HasStockKlineArchive(ctx, tradeDate)
	if klineErr != nil {
		return errors.Join(combined, klineErr)
	}
	if !klineExists {
		// K-lines depend on the complete money archive. Do not launch them
		// after a failed part and create a misleading second failure.
		endExists, endErr := s.collector.HasEndOfDayArchive(ctx, tradeDate)
		if endErr != nil {
			return errors.Join(combined, endErr)
		}
		if !endExists {
			return errors.Join(combined, fmt.Errorf("%w: end-of-day archive for %s", errDependencyUnavailable, tradeDate))
		}
		if err := s.collector.CollectStockKlines(ctx, parseShanghaiTime(tradeDate, "16:15", current)); err != nil {
			return errors.Join(combined, err)
		}
	}
	if combined != nil {
		return combined
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
	case "end-of-day-industry", "end-of-day-concept", "end-of-day-stock":
		return "archive"
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
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, errDependencyUnavailable):
		return "dependency_unavailable"
	default:
		return "job_failed"
	}
}

func (s *Scheduler) pruneLocked(current time.Time) {
	cutoff := current.Add(-24 * time.Hour).Format("2006-01-02")
	for key := range s.lastKeys {
		if len(key) >= 10 && key[:10] < cutoff {
			delete(s.lastKeys, key)
		}
	}
}
