package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

type limitUpTestJobStore struct {
	noopJobStore
	mutex      sync.Mutex
	ensured    []ScheduledJob
	due        []ScheduledJob
	claimed    []ScheduledJob
	finished   []ScheduledJob
	finishedBy map[string][]ScheduledJob
	finishedCh chan ScheduledJob
}

func newLimitUpTestJobStore() *limitUpTestJobStore {
	return &limitUpTestJobStore{finishedBy: make(map[string][]ScheduledJob), finishedCh: make(chan ScheduledJob, 64)}
}

func (jobs *limitUpTestJobStore) EnsureScheduledJob(_ context.Context, job ScheduledJob) error {
	jobs.mutex.Lock()
	defer jobs.mutex.Unlock()
	jobs.ensured = append(jobs.ensured, job)
	return nil
}

func (jobs *limitUpTestJobStore) ClaimScheduledJob(_ context.Context, job ScheduledJob, owner string, now, leaseUntil time.Time) (ScheduledJob, bool, error) {
	jobs.mutex.Lock()
	defer jobs.mutex.Unlock()
	job.Status = JobRunning
	job.AttemptCount++
	job.LeaseOwner = owner
	job.LeaseUntil = &leaseUntil
	if job.StartedAt == nil {
		job.StartedAt = &now
	}
	jobs.claimed = append(jobs.claimed, job)
	return job, true, nil
}

func (jobs *limitUpTestJobStore) FinishScheduledJob(_ context.Context, job ScheduledJob) error {
	jobs.mutex.Lock()
	jobs.finished = append(jobs.finished, job)
	jobs.finishedBy[job.Kind] = append(jobs.finishedBy[job.Kind], job)
	jobs.mutex.Unlock()
	jobs.finishedCh <- job
	return nil
}

func (jobs *limitUpTestJobStore) DueScheduledJobs(context.Context, time.Time, int) ([]ScheduledJob, error) {
	jobs.mutex.Lock()
	defer jobs.mutex.Unlock()
	return append([]ScheduledJob(nil), jobs.due...), nil
}

func (jobs *limitUpTestJobStore) setDue(due ...ScheduledJob) {
	jobs.mutex.Lock()
	defer jobs.mutex.Unlock()
	jobs.due = append([]ScheduledJob(nil), due...)
}

func (jobs *limitUpTestJobStore) ensuredKind(kind string) []ScheduledJob {
	jobs.mutex.Lock()
	defer jobs.mutex.Unlock()
	result := make([]ScheduledJob, 0, len(jobs.ensured))
	for _, job := range jobs.ensured {
		if job.Kind == kind {
			result = append(result, job)
		}
	}
	return result
}

func (jobs *limitUpTestJobStore) claimedKind(kind string) []ScheduledJob {
	jobs.mutex.Lock()
	defer jobs.mutex.Unlock()
	result := make([]ScheduledJob, 0, len(jobs.claimed))
	for _, job := range jobs.claimed {
		if job.Kind == kind {
			result = append(result, job)
		}
	}
	return result
}

type limitUpTestCollector struct {
	schedulerCollector
	eventStarted chan struct{}
	eventRelease chan struct{}
	eventErr     error
	hasEvents    bool
	hasEventsErr error
	eventCalls   []time.Time
	partCalls    []graymarket.RankType
}

func (collector *limitUpTestCollector) CollectLimitUpEvents(_ context.Context, at time.Time) error {
	collector.mu.Lock()
	collector.eventCalls = append(collector.eventCalls, at)
	started := collector.eventStarted
	release := collector.eventRelease
	collector.mu.Unlock()
	if started != nil {
		close(started)
		<-release
	}
	return collector.eventErr
}

func (collector *limitUpTestCollector) HasLimitUpArchive(context.Context, string) (bool, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return collector.hasEvents, collector.hasEventsErr
}

func (collector *limitUpTestCollector) CollectEndOfDayPart(_ context.Context, _ time.Time, rankType graymarket.RankType) error {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.partCalls = append(collector.partCalls, rankType)
	return nil
}

func (collector *limitUpTestCollector) HasEndOfDayPart(context.Context, string, graymarket.RankType) (bool, error) {
	return false, nil
}

func (collector *limitUpTestCollector) eventCallsSnapshot() []time.Time {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return append([]time.Time(nil), collector.eventCalls...)
}

func (collector *limitUpTestCollector) partCallsSnapshot() []graymarket.RankType {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return append([]graymarket.RankType(nil), collector.partCalls...)
}

func loginDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func limitUpTestCalendar(testingT *testing.T) *tradingcalendar.Calendar {
	calendar, err := tradingcalendar.Load(filepath.Join(testingT.TempDir(), "missing.json"))
	if err != nil {
		testingT.Fatal(err)
	}
	return calendar
}

func waitForFinishedKind(testingT *testing.T, jobs *limitUpTestJobStore, kind string) ScheduledJob {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jobs.mutex.Lock()
		finished := jobs.finishedBy[kind]
		if len(finished) > 0 {
			job := finished[0]
			jobs.finishedBy[kind] = finished[1:]
			jobs.mutex.Unlock()
			return job
		}
		jobs.mutex.Unlock()
		time.Sleep(time.Millisecond)
	}
	testingT.Fatalf("timed out waiting for finished %s job", kind)
	return ScheduledJob{}
}

func TestLimitUpEventsDefaultDisabled(testingT *testing.T) {
	collector := &limitUpTestCollector{}
	jobs := newLimitUpTestJobStore()
	scheduler, err := New(collector, limitUpTestCalendar(testingT), loginDiscardLogger(), Options{Jobs: jobs})
	if err != nil {
		testingT.Fatal(err)
	}
	if scheduler.options.LimitUpArchiveEnabled {
		testingT.Fatal("LimitUpArchiveEnabled must default to false")
	}
	now := time.Date(2026, 9, 7, 16, 0, 5, 0, scheduler.location)
	dueJob := newScheduledJob("end-of-day-events", now, "2026-09-07")
	jobs.setDue(dueJob)
	scheduler.startEvents(context.Background(), now)
	scheduler.enqueueReplayableJobs(context.Background(), now.Add(10*time.Minute))
	scheduler.startDueJobs(context.Background(), now.Add(10*time.Minute))
	if len(jobs.ensuredKind("end-of-day-events")) != 0 {
		testingT.Fatal("disabled event job was enqueued")
	}
	if len(jobs.claimedKind("end-of-day-events")) != 0 {
		testingT.Fatal("disabled persisted event job was claimed")
	}
	if len(collector.eventCallsSnapshot()) != 0 {
		testingT.Fatal("disabled event collector was called")
	}
}

func TestLimitUpEventsReplayPostCloseSchedule(testingT *testing.T) {
	scheduler, err := New(&limitUpTestCollector{}, limitUpTestCalendar(testingT), loginDiscardLogger(), Options{
		Jobs:                  newLimitUpTestJobStore(),
		LimitUpArchiveEnabled: true,
	})
	if err != nil {
		testingT.Fatal(err)
	}
	jobs := scheduler.jobs.(*limitUpTestJobStore)
	scheduler.enqueueReplayableJobs(context.Background(), time.Date(2026, 9, 7, 15, 59, 0, 0, scheduler.location))
	if len(jobs.ensuredKind("end-of-day-events")) != 0 {
		testingT.Fatal("event jobs were replayed before 16:00")
	}
	scheduler.enqueueReplayableJobs(context.Background(), time.Date(2026, 9, 7, 16, 10, 0, 0, scheduler.location))
	events := jobs.ensuredKind("end-of-day-events")
	if len(events) != 3 {
		testingT.Fatalf("expected three post-close event invitations, got %d", len(events))
	}
	for index, clock := range []string{"16:00", "16:05", "16:10"} {
		if events[index].PlannedAt.In(scheduler.location).Format("15:04") != clock || events[index].TradeDate != "2026-09-07" {
			testingT.Fatalf("unexpected event invitation: %+v", events[index])
		}
	}
}

func TestLimitUpEventsRunWhenFundsPartsDisabled(testingT *testing.T) {
	collector := &limitUpTestCollector{}
	jobs := newLimitUpTestJobStore()
	scheduler, err := New(collector, limitUpTestCalendar(testingT), loginDiscardLogger(), Options{
		Jobs:                  jobs,
		LimitUpArchiveEnabled: true,
		DisabledKinds: map[string]struct{}{
			"end-of-day-industry": {},
			"end-of-day-concept":  {},
			"end-of-day-stock":    {},
		},
	})
	if err != nil {
		testingT.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 16, 0, 5, 0, scheduler.location)
	scheduler.check(context.Background(), now)
	finished := waitForFinishedKind(testingT, jobs, "end-of-day-events")
	if finished.Status != JobSucceeded {
		testingT.Fatalf("event job did not succeed: %+v", finished)
	}
	eventCalls := collector.eventCallsSnapshot()
	if len(eventCalls) != 1 || eventCalls[0].Format("2006-01-02 15:04") != "2026-09-07 16:00" {
		testingT.Fatalf("unexpected event collection calls: %v", eventCalls)
	}
	if len(collector.partCallsSnapshot()) != 0 {
		testingT.Fatal("disabled funds parts reached the collector")
	}
}

func TestLimitUpEventsDisabledByParentOrOwnKind(testingT *testing.T) {
	tests := []struct {
		name          string
		disabledKinds map[string]struct{}
	}{
		{name: "parent", disabledKinds: map[string]struct{}{"end-of-day": {}}},
		{name: "own", disabledKinds: map[string]struct{}{"end-of-day-events": {}}},
	}
	for _, testCase := range tests {
		testingT.Run(testCase.name, func(testingT *testing.T) {
			collector := &limitUpTestCollector{}
			jobs := newLimitUpTestJobStore()
			scheduler, err := New(collector, limitUpTestCalendar(testingT), loginDiscardLogger(), Options{
				Jobs:                  jobs,
				LimitUpArchiveEnabled: true,
				DisabledKinds:         testCase.disabledKinds,
			})
			if err != nil {
				testingT.Fatal(err)
			}
			if scheduler.kindEnabled("end-of-day-events") {
				testingT.Fatal("event kind remained enabled")
			}
			if !scheduler.kindEnabled("minute") || !scheduler.kindEnabled("end-of-day-industry") {
				testingT.Fatal("event disablement changed existing minute or funds jobs")
			}
			now := time.Date(2026, 9, 7, 16, 10, 5, 0, scheduler.location)
			jobs.setDue(newScheduledJob("end-of-day-events", now, "2026-09-07"))
			scheduler.enqueueReplayableJobs(context.Background(), now)
			scheduler.startDueJobs(context.Background(), now)
			if len(jobs.ensuredKind("end-of-day-events")) != 0 {
				testingT.Fatal("disabled event job was enqueued")
			}
			if len(jobs.claimedKind("end-of-day-events")) != 0 {
				testingT.Fatal("disabled event job was claimed")
			}
		})
	}
}

func TestLimitUpEventsRestartReplaySkipsCompletedArchive(testingT *testing.T) {
	collector := &limitUpTestCollector{hasEvents: true}
	jobs := newLimitUpTestJobStore()
	scheduler, err := New(collector, limitUpTestCalendar(testingT), loginDiscardLogger(), Options{
		Jobs:                  jobs,
		LimitUpArchiveEnabled: true,
	})
	if err != nil {
		testingT.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 16, 10, 5, 0, scheduler.location)
	jobs.setDue(newScheduledJob("end-of-day-events", now, "2026-09-07"))
	scheduler.enqueueReplayableJobs(context.Background(), now)
	scheduler.startDueJobs(context.Background(), now)
	finished := waitForFinishedKind(testingT, jobs, "end-of-day-events")
	if finished.Status != JobSucceeded {
		testingT.Fatalf("completed event archive was not skipped successfully: %+v", finished)
	}
	if len(jobs.claimedKind("end-of-day-events")) != 1 {
		testingT.Fatal("completed event archive was not claimed for its idempotent check")
	}
	if len(collector.eventCallsSnapshot()) != 0 {
		testingT.Fatal("completed event archive was collected again")
	}
	collector.mu.Lock()
	recordedCalls := append([]string(nil), collector.calls...)
	collector.mu.Unlock()
	for _, call := range recordedCalls {
		if call == "end-of-day" || call == "seal" {
			testingT.Fatalf("event archive depended on money archive finalization: %v", recordedCalls)
		}
	}
}

func TestLimitUpEventsFailureUsesFiniteRetries(testingT *testing.T) {
	policy := policyFor("end-of-day-events")
	if policy.timeout != 10*time.Minute || policy.maxAttempts != 3 || policy.retryAfter != time.Minute {
		testingT.Fatalf("unexpected event policy: %+v", policy)
	}
	collector := &limitUpTestCollector{eventErr: errors.New("upstream failed")}
	jobs := newLimitUpTestJobStore()
	scheduler, err := New(collector, limitUpTestCalendar(testingT), loginDiscardLogger(), Options{
		Jobs:                  jobs,
		LimitUpArchiveEnabled: true,
	})
	if err != nil {
		testingT.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 16, 0, 5, 0, scheduler.location)
	firstJob := newScheduledJob("end-of-day-events", now, "2026-09-07")
	firstJob.AttemptCount = 1
	scheduler.runJob(context.Background(), firstJob, now)
	firstFinished := <-jobs.finishedCh
	if firstFinished.Status != JobFailed || firstFinished.RetryAt == nil {
		testingT.Fatalf("first event failure must remain retryable: %+v", firstFinished)
	}
	finalJob := newScheduledJob("end-of-day-events", now, "2026-09-07")
	finalJob.AttemptCount = 3
	scheduler.runJob(context.Background(), finalJob, now)
	finalFinished := <-jobs.finishedCh
	if finalFinished.Status != JobFailed || finalFinished.RetryAt != nil {
		testingT.Fatalf("final event failure must stop retrying: %+v", finalFinished)
	}
}

func TestLimitUpEventsUseIndependentLane(testingT *testing.T) {
	collector := &limitUpTestCollector{
		eventStarted: make(chan struct{}),
		eventRelease: make(chan struct{}),
	}
	jobs := newLimitUpTestJobStore()
	scheduler, err := New(collector, limitUpTestCalendar(testingT), loginDiscardLogger(), Options{
		Jobs:                  jobs,
		LimitUpArchiveEnabled: true,
	})
	if err != nil {
		testingT.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 16, 0, 5, 0, scheduler.location)
	if !scheduler.startJob(context.Background(), "end-of-day-events", now, "2026-09-07") {
		testingT.Fatal("event job did not start")
	}
	select {
	case <-collector.eventStarted:
	case <-time.After(time.Second):
		testingT.Fatal("event job did not reach the collector")
	}
	if !scheduler.startJob(context.Background(), "minute", now, "2026-09-07") {
		testingT.Fatal("event lane blocked minute collection")
	}
	if !scheduler.startJob(context.Background(), "end-of-day-industry", now, "2026-09-07") {
		testingT.Fatal("event lane blocked funds archive")
	}
	if scheduler.startJob(context.Background(), "end-of-day-events", now.Add(5*time.Minute), "2026-09-07") {
		testingT.Fatal("second event job bypassed its lane")
	}
	close(collector.eventRelease)
	waitForFinishedKind(testingT, jobs, "end-of-day-events")
	waitForFinishedKind(testingT, jobs, "minute")
	waitForFinishedKind(testingT, jobs, "end-of-day-industry")
	collector.mu.Lock()
	recordedCalls := append([]string(nil), collector.calls...)
	collector.mu.Unlock()
	if len(recordedCalls) != 1 || recordedCalls[0] != "minute" {
		testingT.Fatalf("minute collection changed with event lane active: %v", recordedCalls)
	}
	parts := collector.partCallsSnapshot()
	if len(parts) != 1 || parts[0] != graymarket.RankIndustry {
		testingT.Fatalf("funds archive changed with event lane active: %v", parts)
	}
}

func TestLimitUpEventsStartupRecoveryUsesLatestTradeDate(testingT *testing.T) {
	tests := []struct {
		name       string
		at         string
		wantDate   string
		wantLaunch bool
	}{
		{name: "intraday", at: "2026-09-07 10:00", wantLaunch: false},
		{name: "preopen", at: "2026-09-07 08:00", wantDate: "2026-09-04", wantLaunch: true},
		{name: "postclose", at: "2026-09-07 16:30", wantDate: "2026-09-07", wantLaunch: true},
	}
	for _, testCase := range tests {
		testingT.Run(testCase.name, func(testingT *testing.T) {
			collector := &limitUpTestCollector{}
			jobs := newLimitUpTestJobStore()
			scheduler, err := New(collector, limitUpTestCalendar(testingT), loginDiscardLogger(), Options{
				Jobs: jobs,
				DisabledKinds: map[string]struct{}{
					"startup-recovery": {},
				},
				LimitUpArchiveEnabled: true,
			})
			if err != nil {
				testingT.Fatal(err)
			}
			if !scheduler.kindEnabled("cleanup") {
				testingT.Fatal("event recovery changed legacy cleanup enablement")
			}
			at, parseErr := time.ParseInLocation("2006-01-02 15:04", testCase.at, scheduler.location)
			if parseErr != nil {
				testingT.Fatal(parseErr)
			}
			scheduler.recoverLatestArchive(context.Background(), at)
			scheduler.recoverLatestEvents(context.Background(), at)
			if !testCase.wantLaunch {
				if len(jobs.ensuredKind("end-of-day-events")) != 0 || len(jobs.claimedKind("end-of-day-events")) != 0 {
					testingT.Fatal("event recovery ran during the intraday window")
				}
				return
			}
			waitForFinishedKind(testingT, jobs, "end-of-day-events")
			events := jobs.ensuredKind("end-of-day-events")
			if len(events) != 1 || events[0].TradeDate != testCase.wantDate {
				testingT.Fatalf("unexpected event recovery invitations: %+v", events)
			}
		})
	}
}
