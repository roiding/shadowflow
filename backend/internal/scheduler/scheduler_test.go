package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
		"15:05": "", "15:30": "", "16:00": "end-of-day", "16:05": "end-of-day", "16:10": "end-of-day", "16:11": "",
		"16:15": "stock-kline", "17:30": "stock-kline", "20:00": "stock-kline", "20:01": "",
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
