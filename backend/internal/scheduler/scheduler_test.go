package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

func TestTradingMinuteBoundaries(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tests := map[string]bool{
		"09:30": false, "09:31": true, "11:30": true, "11:31": false,
		"13:00": false, "13:01": true, "15:00": true, "15:01": false,
	}
	for value, expected := range tests {
		parsed, _ := time.ParseInLocation("15:04", value, location)
		if actual := isTradingMinute(parsed); actual != expected {
			t.Errorf("%s: expected %v, got %v", value, expected, actual)
		}
	}
}

func TestTradingDayContainsExactly240MinuteJobs(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, location)
	minutes := 0
	for current := start; current.Before(start.Add(24 * time.Hour)); current = current.Add(time.Minute) {
		if jobKind(current) == "minute" {
			minutes++
		}
	}
	if minutes != 240 {
		t.Fatalf("expected 240 minute jobs, got %d", minutes)
	}
}

func TestScheduledJobBoundaries(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tests := map[string]string{
		"06:59": "", "07:00": "relations", "07:01": "", "07:29": "", "07:30": "relations", "07:31": "",
		"07:59": "", "08:00": "relations", "08:01": "", "08:50": "", "08:51": "",
		"08:59": "", "09:00": "cleanup", "09:01": "", "09:15": "", "09:16": "", "09:30": "",
		"09:05": "maintenance",
		"15:05": "", "15:30": "", "16:30": "end-of-day", "16:35": "end-of-day", "16:40": "end-of-day", "16:41": "",
		"16:50": "ths-account-data", "17:00": "stock-kline", "17:10": "ths-account-data", "17:50": "ths-account-data",
		"17:30": "stock-kline", "20:00": "stock-kline", "20:01": "",
	}
	for value, expected := range tests {
		parsed, _ := time.ParseInLocation("15:04", value, location)
		if actual := jobKind(parsed); actual != expected {
			t.Errorf("%s: expected %q, got %q", value, expected, actual)
		}
	}
}

func TestRelationExecutionDeadlineUsesShanghaiMorningWindow(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{location: location}
	for _, tc := range []struct {
		at       string
		deadline string
	}{
		{"07:00", "07:45"}, {"08:00", "08:45"}, {"08:50", "09:15"},
		{"09:14", "09:15"}, {"09:15", ""}, {"10:40", ""}, {"16:00", ""},
	} {
		t.Run(tc.at, func(t *testing.T) {
			at, err := time.ParseInLocation("2006-01-02 15:04", "2026-09-07 "+tc.at, location)
			if err != nil {
				t.Fatal(err)
			}
			deadline, err := s.executionDeadline(ScheduledJob{Kind: "relations", TradeDate: "2026-09-07"}, at.UTC())
			if tc.deadline == "" {
				if !errors.Is(err, errRelationWindowClosed) {
					t.Fatalf("late relation sync was allowed: deadline=%v err=%v", deadline, err)
				}
			} else if err != nil || deadline.In(location).Format("15:04") != tc.deadline {
				t.Fatalf("unexpected deadline: got=%v want=%s err=%v", deadline, tc.deadline, err)
			}
			minuteDeadline, err := s.executionDeadline(ScheduledJob{Kind: "minute"}, at)
			if err != nil || !minuteDeadline.Equal(at.Add(policyFor("minute").timeout)) {
				t.Fatalf("relation window changed minute collection: deadline=%v err=%v", minuteDeadline, err)
			}
		})
	}
}

func TestDelayedRelationJobDoesNotRunAfterItsMorning(t *testing.T) {
	jobs := &recordingJobStore{finished: make(chan ScheduledJob, 1)}
	collector := &schedulerCollector{}
	s, err := New(collector, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Jobs: jobs})
	if err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().In(s.location).AddDate(0, 0, -1)
	staleTick := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 8, 0, 0, 0, s.location)
	job := newScheduledJob("relations", staleTick, staleTick.Format("2006-01-02"))
	job.RetryAt = &staleTick
	s.runJob(context.Background(), job, staleTick)
	finished := <-jobs.finished
	if finished.Status != JobSkipped || finished.LastErrorCode != "outside_relation_window" || finished.RetryAt != nil {
		t.Fatalf("late queued sync was not closed: %+v", finished)
	}
	if len(collector.calls) != 0 {
		t.Fatalf("late queued sync reached the collector: %v", collector.calls)
	}
}

type replayRecordingJobStore struct {
	noopJobStore
	jobs []ScheduledJob
}

func (s *replayRecordingJobStore) EnsureScheduledJob(_ context.Context, job ScheduledJob) error {
	s.jobs = append(s.jobs, job)
	return nil
}

func TestReplayableRelationJobsUseEarlyMorningSchedule(t *testing.T) {
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := &replayRecordingJobStore{}
	s, err := New(&schedulerCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Jobs: jobs})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 9, 30, 0, 0, s.location)
	s.enqueueReplayableJobs(context.Background(), now)
	var clocks []string
	for _, job := range jobs.jobs {
		if job.Kind == "relations" {
			clocks = append(clocks, job.PlannedAt.In(s.location).Format("15:04"))
		}
	}
	if fmt.Sprint(clocks) != "[07:00 07:30 08:00]" {
		t.Fatalf("restart replay used a different relation schedule: %v", clocks)
	}
}

type schedulerCollector struct {
	mu         sync.Mutex
	calls      []string
	minuteAt   time.Time
	endStarted chan struct{}
	endRelease chan struct{}
	hasEnd     bool
	hasKline   bool
}

func (c *schedulerCollector) CollectBoards(_ context.Context, at time.Time) error {
	c.mu.Lock()
	c.calls = append(c.calls, "minute")
	c.minuteAt = at
	c.mu.Unlock()
	return nil
}

func (c *schedulerCollector) CollectEndOfDay(context.Context, time.Time) error {
	c.mu.Lock()
	c.calls = append(c.calls, "end-of-day")
	c.mu.Unlock()
	if c.endStarted != nil {
		close(c.endStarted)
		<-c.endRelease
	}
	c.mu.Lock()
	c.hasEnd = true
	c.mu.Unlock()
	return nil
}

func (c *schedulerCollector) CollectStockKlines(context.Context, time.Time) error {
	c.mu.Lock()
	c.calls = append(c.calls, "stock-kline")
	c.hasKline = true
	c.mu.Unlock()
	return nil
}

func (c *schedulerCollector) CleanupArchivedIntraday(context.Context, string) error {
	return nil
}

func (c *schedulerCollector) Maintain(context.Context, time.Time, int, int) (repository.MaintenanceResult, error) {
	return repository.MaintenanceResult{}, nil
}

func (c *schedulerCollector) HasEndOfDayArchive(context.Context, string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hasEnd, nil
}

func (c *schedulerCollector) HasStockKlineArchive(context.Context, string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hasKline, nil
}

func (c *schedulerCollector) FinalizeArchive(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "seal")
	return nil
}

func TestCompletedKlinesStillRunFinalization(t *testing.T) {
	collector := &schedulerCollector{hasEnd: true, hasKline: true}
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(collector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 17, 30, 0, 0, s.location)
	if err := s.executeJob(context.Background(), newScheduledJob("stock-kline", now, "2026-09-04"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverArchive(context.Background(), "2026-09-04", now); err != nil {
		t.Fatal(err)
	}
	if len(collector.calls) != 2 || collector.calls[0] != "seal" || collector.calls[1] != "seal" {
		t.Fatalf("completion skipped finalization: %v", collector.calls)
	}
}

func (c *schedulerCollector) CollectStockBoardRelations(context.Context, string) error {
	c.mu.Lock()
	c.calls = append(c.calls, "relations")
	c.mu.Unlock()
	return nil
}

func (c *schedulerCollector) HasStockBoardRelations(context.Context, string) (bool, error) {
	return false, nil
}

func TestSchedulerUsesIndependentIntradayAndArchiveLanes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	calendar, err := tradingcalendar.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := &schedulerCollector{endStarted: make(chan struct{}), endRelease: make(chan struct{})}
	s, err := New(fake, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := time.Date(2026, 8, 17, 16, 0, 5, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	if !s.startJob(ctx, "end-of-day", current, "2026-08-17") {
		t.Fatal("end-of-day job did not start")
	}
	select {
	case <-fake.endStarted:
	case <-time.After(time.Second):
		t.Fatal("end-of-day job did not reach collector")
	}
	if !s.startJob(ctx, "minute", current, "2026-08-17") {
		t.Fatal("minute collection was blocked by archive lane")
	}
	if s.startJob(ctx, "stock-kline", current, "2026-08-17") {
		t.Fatal("stock-kline should wait for the active archive lane")
	}
	close(fake.endRelease)
	time.Sleep(20 * time.Millisecond)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 2 || fake.calls[0] != "end-of-day" || fake.calls[1] != "minute" {
		t.Fatalf("unexpected lane calls: %v", fake.calls)
	}
}

func TestDueMinuteJobRestoresShanghaiLocation(t *testing.T) {
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &schedulerCollector{}
	s, err := New(fake, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	localAt := time.Date(2026, 8, 17, 9, 31, 0, 0, location)
	job := ScheduledJob{Kind: "minute", PlannedAt: localAt.UTC()}
	if err := s.executeJob(context.Background(), job, localAt.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	got := fake.minuteAt
	fake.mu.Unlock()
	if got.Format("2006-01-02 15:04") != "2026-08-17 09:31" || got.Location().String() != "Asia/Shanghai" {
		t.Fatalf("minute job used %s (%s), want Shanghai 09:31", got.Format("2006-01-02 15:04"), got.Location())
	}
}

func TestRecoveryTradeDateOnlyRunsOutsideTradingSession(t *testing.T) {
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tests := []struct {
		at       string
		wantDate string
		wantRun  bool
	}{
		{"2026-08-17 08:30", "2026-08-14", true},
		{"2026-08-17 09:29", "2026-08-14", true},
		{"2026-08-17 09:30", "", false},
		{"2026-08-17 15:30", "", false},
		{"2026-08-17 16:01", "2026-08-17", true},
		{"2026-08-16 10:00", "2026-08-14", true},
	}
	for _, test := range tests {
		at, parseErr := time.ParseInLocation("2006-01-02 15:04", test.at, location)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		gotDate, gotRun := recoveryTradeDate(calendar, at)
		if gotDate != test.wantDate || gotRun != test.wantRun {
			t.Errorf("%s: got (%s,%v), want (%s,%v)", test.at, gotDate, gotRun, test.wantDate, test.wantRun)
		}
	}
}

type recordingJobStore struct {
	finished chan ScheduledJob
}

func (s *recordingJobStore) EnsureScheduledJob(context.Context, ScheduledJob) error { return nil }
func (s *recordingJobStore) ClaimScheduledJob(_ context.Context, job ScheduledJob, owner string, now, leaseUntil time.Time) (ScheduledJob, bool, error) {
	job.Status = JobRunning
	job.AttemptCount++
	job.LeaseOwner = owner
	job.StartedAt = &now
	job.LeaseUntil = &leaseUntil
	return job, true, nil
}
func (s *recordingJobStore) FinishScheduledJob(_ context.Context, job ScheduledJob) error {
	s.finished <- job
	return nil
}
func (*recordingJobStore) DueScheduledJobs(context.Context, time.Time, int) ([]ScheduledJob, error) {
	return nil, nil
}
func (*recordingJobStore) ExpireLeasedJobs(context.Context, time.Time) error { return nil }

func TestNoopJobStoreClaimsIncrementAttempts(t *testing.T) {
	now := time.Now()
	claimed, ok, err := (noopJobStore{}).ClaimScheduledJob(context.Background(), ScheduledJob{MaxAttempts: 2}, "owner", now, now.Add(time.Minute))
	if err != nil || !ok || claimed.Status != JobRunning || claimed.AttemptCount != 1 || claimed.LeaseOwner != "owner" {
		t.Fatalf("unexpected noop claim: job=%+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestStockKlineWaitsAndRetriesWhenEndOfDayArchiveIsMissing(t *testing.T) {
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := &recordingJobStore{finished: make(chan ScheduledJob, 1)}
	fake := &schedulerCollector{}
	s, err := New(fake, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Jobs: jobs, InstanceID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, 8, 17, 16, 15, 5, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	if !s.startJob(context.Background(), "stock-kline", current, "2026-08-17") {
		t.Fatal("stock-kline job did not start")
	}
	select {
	case job := <-jobs.finished:
		if job.Status != JobFailed || job.LastErrorCode != "dependency_unavailable" || job.RetryAt == nil {
			t.Fatalf("missing dependency must remain retryable: %+v", job)
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.calls) != 0 {
			t.Fatalf("stock kline collector ran without its dependency: %v", fake.calls)
		}
	case <-time.After(time.Second):
		t.Fatal("job was not finished")
	}
}

func TestSchedulerErrorCodesAreStable(t *testing.T) {
	if got := errorCode(errors.New("a volatile upstream message")); got != "job_failed" {
		t.Fatalf("generic error code = %q", got)
	}
	if got := errorCode(fmt.Errorf("wrapped: %w", errDependencyUnavailable)); got != "dependency_unavailable" {
		t.Fatalf("dependency error code = %q", got)
	}
}

func TestCleanupUsesCurrentDateAsExclusiveCutoff(t *testing.T) {
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := &recordingJobStore{finished: make(chan ScheduledJob, 1)}
	fake := &schedulerCollector{}
	s, err := New(fake, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Jobs: jobs, InstanceID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	monday := time.Date(2026, 8, 17, 9, 0, 5, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	s.check(context.Background(), monday)
	select {
	case job := <-jobs.finished:
		if job.TradeDate != "2026-08-17" {
			t.Fatalf("cleanup cutoff=%s, want current date", job.TradeDate)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup job did not finish")
	}
}

func TestDisabledKindsSuppressJobs(t *testing.T) {
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := &replayRecordingJobStore{}
	s, err := New(&schedulerCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Jobs: jobs,
		DisabledKinds: map[string]struct{}{"relations": {}, "minute": {}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 9, 30, 0, 0, s.location)

	s.enqueueReplayableJobs(context.Background(), now)
	for _, job := range jobs.jobs {
		if job.Kind == "relations" {
			t.Fatalf("disabled relations job was enqueued: %+v", job)
		}
	}

	s.check(context.Background(), now.Add(30*time.Minute))
	for _, job := range jobs.jobs {
		if job.Kind == "minute" {
			t.Fatalf("disabled minute job was started: %+v", job)
		}
	}

	if s.claimAndLaunch(context.Background(), ScheduledJob{JobKey: "queued-relations", Kind: "relations", TradeDate: "2026-09-07"}, now) {
		t.Fatal("queued disabled job was claimed")
	}
	if !s.kindEnabled("end-of-day-concept") || s.kindEnabled("minute") {
		t.Fatal("kind gate does not reflect the configured set")
	}
}

type prewarmRecordingCollector struct {
	schedulerCollector
	prewarmCalls   []string
	prewarmStarted chan struct{}
	prewarmRelease chan struct{}
	prewarmErr     error
	hasRelations   bool
	relationsErr   error
	klineErr       error
	endOfDayErr    error
}

func (collector *prewarmRecordingCollector) PrepareUpstream(_ context.Context, purpose string) error {
	collector.mu.Lock()
	collector.prewarmCalls = append(collector.prewarmCalls, purpose)
	startedCh := collector.prewarmStarted
	releaseCh := collector.prewarmRelease
	collector.mu.Unlock()
	if startedCh != nil {
		close(startedCh)
		<-releaseCh
	}
	return collector.prewarmErr
}

func (collector *prewarmRecordingCollector) HasStockBoardRelations(context.Context, string) (bool, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.relationsErr != nil {
		return false, collector.relationsErr
	}
	return collector.hasRelations, nil
}

func (collector *prewarmRecordingCollector) HasStockKlineArchive(context.Context, string) (bool, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.klineErr != nil {
		return false, collector.klineErr
	}
	return collector.hasKline, nil
}

func (collector *prewarmRecordingCollector) HasEndOfDayArchive(context.Context, string) (bool, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.endOfDayErr != nil {
		return false, collector.endOfDayErr
	}
	return collector.hasEnd, nil
}

type partArchivePrewarmCollector struct {
	prewarmRecordingCollector
	parts   map[graymarket.RankType]bool
	partErr error
}

func (collector *partArchivePrewarmCollector) CollectEndOfDayPart(context.Context, time.Time, graymarket.RankType) error {
	return nil
}

func (collector *partArchivePrewarmCollector) HasEndOfDayPart(_ context.Context, _ string, rankType graymarket.RankType) (bool, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.partErr != nil {
		return false, collector.partErr
	}
	return collector.parts[rankType], nil
}

func TestProxyPrewarmBoundaries(testingT *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tests := map[string]string{
		"06:54": "",
		"06:55": "proxy-prewarm-relations",
		"06:56": "",
		"07:24": "",
		"07:25": "proxy-prewarm-relations",
		"07:26": "",
		"07:54": "",
		"07:55": "proxy-prewarm-relations",
		"07:56": "",
		"09:25": "",
		"09:26": "proxy-prewarm-boards",
		"09:27": "",
		"12:55": "",
		"12:56": "proxy-prewarm-boards",
		"12:57": "",
		"15:54": "",
		"16:25": "proxy-prewarm-end-of-day",
		"16:26": "",
		"16:54": "",
		"16:55": "proxy-prewarm-stock-kline",
		"16:56": "",
		"17:24": "",
		"17:25": "proxy-prewarm-stock-kline",
		"17:26": "",
		"19:54": "",
		"19:55": "proxy-prewarm-stock-kline",
		"19:56": "",
		"20:00": "",
	}
	for clockValue, expectedKind := range tests {
		parsedTime, parseErr := time.ParseInLocation("15:04", clockValue, location)
		if parseErr != nil {
			testingT.Fatalf("parse time failed: %v", parseErr)
		}
		if actualKind := prewarmJobKind(parsedTime); actualKind != expectedKind {
			testingT.Errorf("%s: expected %q, got %q", clockValue, expectedKind, actualKind)
		}
	}
}

func TestProxyPrewarmTradingDayFiltering(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}
	collector := &prewarmRecordingCollector{}
	jobsStore := &replayRecordingJobStore{}
	scheduler, newErr := New(collector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Jobs:                jobsStore,
		ProxyPrewarmEnabled: true,
	})
	if newErr != nil {
		testingT.Fatal(newErr)
	}
	sundayTime := time.Date(2026, 9, 6, 6, 55, 5, 0, scheduler.location)
	scheduler.check(context.Background(), sundayTime)
	scheduler.enqueueReplayableJobs(context.Background(), sundayTime)
	if len(jobsStore.jobs) != 0 {
		testingT.Fatalf("non-trading day scheduled prewarm jobs: %+v", jobsStore.jobs)
	}
	mondayTime := time.Date(2026, 9, 7, 6, 55, 5, 0, scheduler.location)
	scheduler.check(context.Background(), mondayTime)
	foundMonday := false
	for _, jobItem := range jobsStore.jobs {
		if jobItem.Kind == "proxy-prewarm-relations" {
			foundMonday = true
			break
		}
	}
	if !foundMonday {
		testingT.Fatal("trading day prewarm relations was not started")
	}
}

func TestProxyPrewarmDefaultDisabled(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}
	collector := &prewarmRecordingCollector{}
	jobsStore := &replayRecordingJobStore{}
	scheduler, newErr := New(collector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Jobs: jobsStore})
	if newErr != nil {
		testingT.Fatal(newErr)
	}
	if scheduler.options.ProxyPrewarmEnabled {
		testingT.Fatal("ProxyPrewarmEnabled must default to false")
	}
	if scheduler.kindEnabled("proxy-prewarm-relations") {
		testingT.Fatal("proxy prewarm kinds must be disabled by default")
	}
	mondayTime := time.Date(2026, 9, 7, 6, 55, 5, 0, scheduler.location)
	scheduler.check(context.Background(), mondayTime)
	scheduler.enqueueReplayableJobs(context.Background(), mondayTime)
	for _, jobItem := range jobsStore.jobs {
		if strings.HasPrefix(jobItem.Kind, "proxy-prewarm-") {
			testingT.Fatalf("prewarm job was enqueued when disabled: %+v", jobItem)
		}
	}
}

func TestProxyPrewarmCallsCollectorWithCorrectPurpose(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}
	collector := &prewarmRecordingCollector{}
	scheduler, newErr := New(collector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
	})
	if newErr != nil {
		testingT.Fatal(newErr)
	}
	jobPlans := []struct {
		kind            string
		expectedPurpose string
	}{
		{"proxy-prewarm-relations", "relations"},
		{"proxy-prewarm-boards", "boards"},
		{"proxy-prewarm-end-of-day", "end-of-day"},
		{"proxy-prewarm-stock-kline", "stock-kline"},
	}
	for _, jobPlan := range jobPlans {
		targetDate := "2026-09-07"
		nowTime := time.Date(2026, 9, 7, 7, 0, 0, 0, scheduler.location)
		scheduledJob := newScheduledJob(jobPlan.kind, nowTime, targetDate)
		executeErr := scheduler.executeJob(context.Background(), scheduledJob, nowTime)
		if executeErr != nil {
			testingT.Fatalf("%s execute failed: %v", jobPlan.kind, executeErr)
		}
	}
	collector.mu.Lock()
	callsRecorded := append([]string(nil), collector.prewarmCalls...)
	collector.mu.Unlock()
	expectedCalls := []string{"relations", "boards", "end-of-day", "stock-kline"}
	if fmt.Sprint(callsRecorded) != fmt.Sprint(expectedCalls) {
		testingT.Fatalf("expected prewarm calls %v, got %v", expectedCalls, callsRecorded)
	}
}

func TestProxyPrewarmIndependentLane(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}
	collector := &prewarmRecordingCollector{
		prewarmStarted: make(chan struct{}),
		prewarmRelease: make(chan struct{}),
	}
	scheduler, newErr := New(collector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
	})
	if newErr != nil {
		testingT.Fatal(newErr)
	}
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()
	currentTime := time.Now().In(scheduler.location)
	currentTradeDate := currentTime.Format("2006-01-02")
	if !scheduler.startJob(ctx, "proxy-prewarm-stock-kline", currentTime, currentTradeDate) {
		testingT.Fatal("proxy prewarm job did not start")
	}
	select {
	case <-collector.prewarmStarted:
	case <-time.After(time.Second):
		testingT.Fatal("prewarm job did not reach collector")
	}
	if !scheduler.startJob(ctx, "minute", currentTime, currentTradeDate) {
		testingT.Fatal("minute collection was blocked by proxy prewarm lane")
	}
	if !scheduler.startJob(ctx, "end-of-day", currentTime, currentTradeDate) {
		testingT.Fatal("end-of-day collection was blocked by proxy prewarm lane")
	}
	if scheduler.startJob(ctx, "proxy-prewarm-boards", currentTime, currentTradeDate) {
		testingT.Fatal("second prewarm job should wait for active proxy-prewarm lane")
	}
	close(collector.prewarmRelease)
	time.Sleep(20 * time.Millisecond)
}

func TestProxyPrewarmLateStartDoesNotReplayAllOldJobs(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}
	jobsStore := &replayRecordingJobStore{}
	scheduler, newErr := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Jobs:                jobsStore,
		ProxyPrewarmEnabled: true,
	})
	if newErr != nil {
		testingT.Fatal(newErr)
	}
	afternoonTime := time.Date(2026, 9, 7, 14, 0, 0, 0, scheduler.location)
	scheduler.enqueueReplayableJobs(context.Background(), afternoonTime)
	for _, jobItem := range jobsStore.jobs {
		if strings.HasPrefix(jobItem.Kind, "proxy-prewarm-") {
			testingT.Fatalf("late start replayed old prewarm job: %+v", jobItem)
		}
	}
	shortWindowJobs := &replayRecordingJobStore{}
	shortWindowScheduler, shortErr := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Jobs:                shortWindowJobs,
		ProxyPrewarmEnabled: true,
	})
	if shortErr != nil {
		testingT.Fatal(shortErr)
	}
	shortWindowTime := time.Date(2026, 9, 7, 9, 28, 0, 0, shortWindowScheduler.location)
	shortWindowScheduler.enqueueReplayableJobs(context.Background(), shortWindowTime)
	var foundBoardsPrewarm bool
	for _, jobItem := range shortWindowJobs.jobs {
		if jobItem.Kind == "proxy-prewarm-boards" {
			foundBoardsPrewarm = true
		}
	}
	if !foundBoardsPrewarm {
		testingT.Fatal("recent 09:26 boards prewarm was not replayed at 09:28")
	}
	recordStore := &recordingJobStore{finished: make(chan ScheduledJob, 1)}
	runningScheduler, runSchedErr := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Jobs:                recordStore,
		ProxyPrewarmEnabled: true,
	})
	if runSchedErr != nil {
		testingT.Fatal(runSchedErr)
	}
	staleMorningTick := time.Date(2026, 9, 7, 6, 55, 0, 0, runningScheduler.location)
	staleJob := newScheduledJob("proxy-prewarm-relations", staleMorningTick, "2026-09-07")
	runningScheduler.runJob(context.Background(), staleJob, afternoonTime)
	finishedJob := <-recordStore.finished
	if finishedJob.Status != JobSkipped || finishedJob.LastErrorCode != "outside_prewarm_window" || finishedJob.RetryAt != nil {
		testingT.Fatalf("stale morning prewarm was not skipped in afternoon: %+v", finishedJob)
	}
}

func TestProxyPrewarmDisabledKinds(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}
	schedulerWithDisabledRelations, errRelations := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
		DisabledKinds:       map[string]struct{}{"relations": {}},
	})
	if errRelations != nil {
		testingT.Fatal(errRelations)
	}
	if schedulerWithDisabledRelations.kindEnabled("proxy-prewarm-relations") {
		testingT.Fatal("relations prewarm should be disabled when relations business is disabled")
	}

	schedulerWithDisabledMinute, errMinute := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
		DisabledKinds:       map[string]struct{}{"minute": {}},
	})
	if errMinute != nil {
		testingT.Fatal(errMinute)
	}
	if schedulerWithDisabledMinute.kindEnabled("proxy-prewarm-boards") {
		testingT.Fatal("boards prewarm should be disabled when minute business is disabled")
	}

	schedulerWithDisabledKline, errKline := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
		DisabledKinds:       map[string]struct{}{"stock-kline": {}},
	})
	if errKline != nil {
		testingT.Fatal(errKline)
	}
	if schedulerWithDisabledKline.kindEnabled("proxy-prewarm-stock-kline") {
		testingT.Fatal("stock-kline prewarm should be disabled when stock-kline business is disabled")
	}

	schedulerWithDisabledEndOfDay, errEndOfDay := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
		DisabledKinds:       map[string]struct{}{"end-of-day": {}},
	})
	if errEndOfDay != nil {
		testingT.Fatal(errEndOfDay)
	}
	if schedulerWithDisabledEndOfDay.kindEnabled("proxy-prewarm-end-of-day") {
		testingT.Fatal("end-of-day prewarm should be disabled when end-of-day business is disabled")
	}

	schedulerWithAllPartsDisabled, errParts := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
		DisabledKinds: map[string]struct{}{
			"end-of-day-industry": {},
			"end-of-day-concept":  {},
			"end-of-day-stock":    {},
		},
	})
	if errParts != nil {
		testingT.Fatal(errParts)
	}
	if schedulerWithAllPartsDisabled.kindEnabled("proxy-prewarm-end-of-day") {
		testingT.Fatal("end-of-day prewarm should be disabled when all parts are disabled")
	}

	schedulerWithPartialPartsDisabled, errPartial := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
		DisabledKinds: map[string]struct{}{
			"end-of-day-stock": {},
		},
	})
	if errPartial != nil {
		testingT.Fatal(errPartial)
	}
	if !schedulerWithPartialPartsDisabled.kindEnabled("proxy-prewarm-end-of-day") {
		testingT.Fatal("end-of-day prewarm should remain enabled when only some parts are disabled")
	}

	schedulerWithDirectDisabled, errDirect := New(&prewarmRecordingCollector{}, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
		DisabledKinds:       map[string]struct{}{"proxy-prewarm-relations": {}},
	})
	if errDirect != nil {
		testingT.Fatal(errDirect)
	}
	if schedulerWithDirectDisabled.kindEnabled("proxy-prewarm-relations") {
		testingT.Fatal("proxy-prewarm-relations should be disabled when directly specified in DisabledKinds")
	}
}

func TestProxyPrewarmFailureAndRelationsMorningCutoff(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}
	failingCollector := &prewarmRecordingCollector{
		prewarmErr: errors.New("upstream proxy preparation failed"),
	}
	recordingStore := &recordingJobStore{finished: make(chan ScheduledJob, 1)}
	scheduler, newErr := New(failingCollector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Jobs:                recordingStore,
		ProxyPrewarmEnabled: true,
		InstanceID:          "test-prewarm",
	})
	if newErr != nil {
		testingT.Fatal(newErr)
	}
	tickTime := time.Now().In(scheduler.location)
	currentDate := tickTime.Format("2006-01-02")
	jobFailed := newScheduledJob("proxy-prewarm-boards", tickTime, currentDate)
	scheduler.runJob(context.Background(), jobFailed, tickTime)
	finishedFailed := <-recordingStore.finished
	if finishedFailed.Status != JobFailed || finishedFailed.LastErrorCode != "job_failed" {
		testingT.Fatalf("failed prewarm must have JobFailed status: %+v", finishedFailed)
	}

	canceledContext, cancelFunc := context.WithCancel(context.Background())
	cancelFunc()
	scheduler.runJob(canceledContext, jobFailed, tickTime)
	finishedCanceled := <-recordingStore.finished
	if finishedCanceled.Status != JobFailed {
		testingT.Fatalf("canceled prewarm must have JobFailed status: %+v", finishedFailed)
	}

	lateMorningTime, parseLateErr := time.ParseInLocation("2006-01-02 15:04", "2026-09-07 09:16", scheduler.location)
	if parseLateErr != nil {
		testingT.Fatal(parseLateErr)
	}
	lateJob := ScheduledJob{Kind: "proxy-prewarm-relations", TradeDate: "2026-09-07", PlannedAt: lateMorningTime.Add(-2 * time.Minute)}
	deadlineCutoff, errCutoff := scheduler.executionDeadline(lateJob, lateMorningTime.UTC())
	if !errors.Is(errCutoff, errRelationWindowClosed) {
		testingT.Fatalf("late relations prewarm allowed past 09:15: deadline=%v err=%v", deadlineCutoff, errCutoff)
	}
	validMorningTime, parseValidErr := time.ParseInLocation("2006-01-02 15:04", "2026-09-07 09:10", scheduler.location)
	if parseValidErr != nil {
		testingT.Fatal(parseValidErr)
	}
	morningJob := ScheduledJob{Kind: "proxy-prewarm-relations", TradeDate: "2026-09-07", PlannedAt: validMorningTime.Add(-2 * time.Minute)}
	deadlineClamped, errClamped := scheduler.executionDeadline(morningJob, validMorningTime.UTC())
	if errClamped != nil || deadlineClamped.In(scheduler.location).Format("15:04") != "09:15" {
		testingT.Fatalf("relations prewarm deadline must be clamped to 09:15: got=%v err=%v", deadlineClamped, errClamped)
	}
}

func TestProxyPrewarmSkippedWhenTargetAlreadyCompleted(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}

	collector := &prewarmRecordingCollector{
		hasRelations: true,
	}
	collector.hasKline = true
	collector.hasEnd = true

	scheduler, newErr := New(collector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
	})
	if newErr != nil {
		testingT.Fatal(newErr)
	}

	targetDate := "2026-09-07"
	nowTime := time.Date(2026, 9, 7, 7, 0, 0, 0, scheduler.location)

	relationsJob := newScheduledJob("proxy-prewarm-relations", nowTime, targetDate)
	if executeErr := scheduler.executeJob(context.Background(), relationsJob, nowTime); executeErr != nil {
		testingT.Fatalf("completed relations prewarm failed: %v", executeErr)
	}

	klineJob := newScheduledJob("proxy-prewarm-stock-kline", nowTime, targetDate)
	if executeErr := scheduler.executeJob(context.Background(), klineJob, nowTime); executeErr != nil {
		testingT.Fatalf("completed stock kline prewarm failed: %v", executeErr)
	}

	endOfDayJob := newScheduledJob("proxy-prewarm-end-of-day", nowTime, targetDate)
	if executeErr := scheduler.executeJob(context.Background(), endOfDayJob, nowTime); executeErr != nil {
		testingT.Fatalf("completed end-of-day prewarm failed: %v", executeErr)
	}

	collector.mu.Lock()
	callsRecorded := append([]string(nil), collector.prewarmCalls...)
	collector.mu.Unlock()
	if len(callsRecorded) != 0 {
		testingT.Fatalf("expected 0 prewarm calls when targets already completed, got: %v", callsRecorded)
	}

	boardsJob := newScheduledJob("proxy-prewarm-boards", nowTime, targetDate)
	if executeErr := scheduler.executeJob(context.Background(), boardsJob, nowTime); executeErr != nil {
		testingT.Fatalf("boards prewarm failed: %v", executeErr)
	}
	collector.mu.Lock()
	callsRecorded = append([]string(nil), collector.prewarmCalls...)
	collector.mu.Unlock()
	expectedBoardsCall := []string{"boards"}
	if fmt.Sprint(callsRecorded) != fmt.Sprint(expectedBoardsCall) {
		testingT.Fatalf("expected boards prewarm not to be skipped, got: %v", callsRecorded)
	}
}

func TestProxyPrewarmFailsWhenCompletionQueryErrors(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}

	queryFailure := errors.New("database connection failed")
	collector := &prewarmRecordingCollector{
		relationsErr: queryFailure,
		klineErr:     queryFailure,
		endOfDayErr:  queryFailure,
	}

	scheduler, newErr := New(collector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
	})
	if newErr != nil {
		testingT.Fatal(newErr)
	}

	targetDate := "2026-09-07"
	nowTime := time.Date(2026, 9, 7, 7, 0, 0, 0, scheduler.location)

	relationsJob := newScheduledJob("proxy-prewarm-relations", nowTime, targetDate)
	if executeErr := scheduler.executeJob(context.Background(), relationsJob, nowTime); !errors.Is(executeErr, queryFailure) {
		testingT.Fatalf("expected query error for relations prewarm, got: %v", executeErr)
	}

	klineJob := newScheduledJob("proxy-prewarm-stock-kline", nowTime, targetDate)
	if executeErr := scheduler.executeJob(context.Background(), klineJob, nowTime); !errors.Is(executeErr, queryFailure) {
		testingT.Fatalf("expected query error for kline prewarm, got: %v", executeErr)
	}

	endOfDayJob := newScheduledJob("proxy-prewarm-end-of-day", nowTime, targetDate)
	if executeErr := scheduler.executeJob(context.Background(), endOfDayJob, nowTime); !errors.Is(executeErr, queryFailure) {
		testingT.Fatalf("expected query error for end-of-day prewarm, got: %v", executeErr)
	}

	collector.mu.Lock()
	callsRecorded := len(collector.prewarmCalls)
	collector.mu.Unlock()
	if callsRecorded != 0 {
		testingT.Fatalf("expected 0 prewarm calls when query errors, got: %d", callsRecorded)
	}
}

func TestProxyPrewarmArchivePartsCompletionCheck(testingT *testing.T) {
	calendar, loadErr := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if loadErr != nil {
		testingT.Fatal(loadErr)
	}

	allPartsCollector := &partArchivePrewarmCollector{
		parts: map[graymarket.RankType]bool{
			graymarket.RankIndustry: true,
			graymarket.RankConcept:  true,
			graymarket.RankStock:    true,
		},
	}
	schedulerAll, errAll := New(allPartsCollector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
	})
	if errAll != nil {
		testingT.Fatal(errAll)
	}

	targetDate := "2026-09-07"
	nowTime := time.Date(2026, 9, 7, 15, 55, 0, 0, schedulerAll.location)
	endOfDayJob := newScheduledJob("proxy-prewarm-end-of-day", nowTime, targetDate)
	if executeErr := schedulerAll.executeJob(context.Background(), endOfDayJob, nowTime); executeErr != nil {
		testingT.Fatalf("all parts complete prewarm failed: %v", executeErr)
	}
	if len(allPartsCollector.prewarmCalls) != 0 {
		testingT.Fatalf("expected 0 prewarm calls when all parts exist, got: %v", allPartsCollector.prewarmCalls)
	}

	partialCollector := &partArchivePrewarmCollector{
		parts: map[graymarket.RankType]bool{
			graymarket.RankIndustry: true,
			graymarket.RankConcept:  false,
			graymarket.RankStock:    true,
		},
	}
	schedulerPartial, errPartial := New(partialCollector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
	})
	if errPartial != nil {
		testingT.Fatal(errPartial)
	}
	if executeErr := schedulerPartial.executeJob(context.Background(), endOfDayJob, nowTime); executeErr != nil {
		testingT.Fatalf("partial parts prewarm failed: %v", executeErr)
	}
	expectedCalls := []string{"end-of-day"}
	if fmt.Sprint(partialCollector.prewarmCalls) != fmt.Sprint(expectedCalls) {
		testingT.Fatalf("expected prewarm end-of-day when part missing, got: %v", partialCollector.prewarmCalls)
	}

	disabledPartsCollector := &partArchivePrewarmCollector{
		parts: map[graymarket.RankType]bool{
			graymarket.RankIndustry: true,
			graymarket.RankConcept:  true,
			graymarket.RankStock:    false,
		},
	}
	schedulerDisabledStock, errDisabled := New(disabledPartsCollector, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ProxyPrewarmEnabled: true,
		DisabledKinds:       map[string]struct{}{"end-of-day-stock": {}},
	})
	if errDisabled != nil {
		testingT.Fatal(errDisabled)
	}
	if executeErr := schedulerDisabledStock.executeJob(context.Background(), endOfDayJob, nowTime); executeErr != nil {
		testingT.Fatalf("disabled stock part prewarm failed: %v", executeErr)
	}
	if len(disabledPartsCollector.prewarmCalls) != 0 {
		testingT.Fatalf("expected 0 prewarm calls when all enabled parts exist, got: %v", disabledPartsCollector.prewarmCalls)
	}
}
