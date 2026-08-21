package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/scheduler"
)

func TestScheduledJobClaimIsAtomicAndRetries(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := scheduler.ScheduledJob{JobKey: "2026-08-21T16:00:end-of-day:2026-08-21", Kind: "end-of-day",
		TradeDate: "2026-08-21", PlannedAt: now, MaxAttempts: 2}
	if err := store.EnsureScheduledJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureScheduledJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	first, ok, err := store.ClaimScheduledJob(ctx, job, "owner-a", now, now.Add(time.Minute))
	if err != nil || !ok || first.AttemptCount != 1 {
		t.Fatalf("first claim: ok=%v attempt=%d err=%v", ok, first.AttemptCount, err)
	}
	if _, ok, err := store.ClaimScheduledJob(ctx, job, "owner-b", now.Add(time.Second), now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("active lease was stolen: ok=%v err=%v", ok, err)
	}
	first.Status = scheduler.JobFailed
	retry := now.Add(2 * time.Second)
	first.RetryAt = &retry
	if err := store.FinishScheduledJob(ctx, first); err != nil {
		t.Fatal(err)
	}
	due, err := store.DueScheduledJobs(ctx, now.Add(time.Second), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("retry should not be due: due=%d err=%v", len(due), err)
	}
	due, err = store.DueScheduledJobs(ctx, now.Add(3*time.Second), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("retry should become due: due=%d err=%v", len(due), err)
	}
	second, ok, err := store.ClaimScheduledJob(ctx, job, "owner-b", now.Add(3*time.Second), now.Add(time.Minute))
	if err != nil || !ok || second.AttemptCount != 2 {
		t.Fatalf("second claim: ok=%v attempt=%d err=%v", ok, second.AttemptCount, err)
	}
	second.Status = scheduler.JobSucceeded
	if err := store.FinishScheduledJob(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimScheduledJob(ctx, job, "owner-c", now.Add(4*time.Second), now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("succeeded job was reclaimed: ok=%v err=%v", ok, err)
	}
}

func TestExpiredLeaseBecomesQueued(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	job := scheduler.ScheduledJob{JobKey: "lease", Kind: "cleanup", TradeDate: "2026-08-21", PlannedAt: now, MaxAttempts: 2}
	if err := store.EnsureScheduledJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	_, ok, err := store.ClaimScheduledJob(ctx, job, "gone", now, now.Add(-time.Second))
	if err != nil || !ok {
		t.Fatalf("claim failed: ok=%v err=%v", ok, err)
	}
	if err := store.ExpireLeasedJobs(ctx, now); err != nil {
		t.Fatal(err)
	}
	due, err := store.DueScheduledJobs(ctx, now, 10)
	if err != nil || len(due) != 1 || due[0].Status != scheduler.JobQueued {
		t.Fatalf("expired lease not recovered: due=%+v err=%v", due, err)
	}
}

func TestMaintenanceDeletesOldScheduledJobs(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -40).Format(timestampLayout)
	_, err = store.db.ExecContext(ctx, `INSERT INTO scheduled_job
(job_key,kind,trade_date,planned_at,status,attempt_count,max_attempts,last_error_code,last_error_message,duration_ms,finished_at)
VALUES ('old','cleanup','2026-07-01',?,'succeeded',1,1,'','',0,?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Maintain(ctx, now, 30, 180)
	if err != nil {
		t.Fatal(err)
	}
	if result.ScheduledJobsDeleted != 1 {
		t.Fatalf("expected one scheduled job deletion, got %d", result.ScheduledJobsDeleted)
	}
}

func TestStaleLeaseOwnerCannotFinishReclaimedJob(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := scheduler.ScheduledJob{JobKey: "stale-owner", Kind: "cleanup", TradeDate: "2026-08-21", PlannedAt: now, MaxAttempts: 3}
	if err := store.EnsureScheduledJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	stale, ok, err := store.ClaimScheduledJob(ctx, job, "owner-a", now, now.Add(-time.Second))
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	current, ok, err := store.ClaimScheduledJob(ctx, job, "owner-b", now.Add(time.Second), now.Add(time.Minute))
	if err != nil || !ok || current.LeaseOwner != "owner-b" {
		t.Fatalf("reclaim: job=%+v ok=%v err=%v", current, ok, err)
	}
	stale.Status = scheduler.JobSucceeded
	if err := store.FinishScheduledJob(ctx, stale); !errors.Is(err, scheduler.ErrJobLeaseLost) {
		t.Fatalf("stale owner finish error = %v, want ErrJobLeaseLost", err)
	}
	current.Status = scheduler.JobSucceeded
	if err := store.FinishScheduledJob(ctx, current); err != nil {
		t.Fatalf("current owner could not finish: %v", err)
	}
}

func TestClaimHonorsRetryAtInAtomicUpdate(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := scheduler.ScheduledJob{JobKey: "retry-window", Kind: "cleanup", TradeDate: "2026-08-21", PlannedAt: now, MaxAttempts: 3}
	if err := store.EnsureScheduledJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	first, ok, err := store.ClaimScheduledJob(ctx, job, "owner-a", now, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	first.Status = scheduler.JobFailed
	retryAt := now.Add(time.Minute)
	first.RetryAt = &retryAt
	if err := store.FinishScheduledJob(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimScheduledJob(ctx, job, "owner-b", now.Add(30*time.Second), now.Add(2*time.Minute)); err != nil || ok {
		t.Fatalf("job was claimed before retry_at: ok=%v err=%v", ok, err)
	}
}

func TestSchedulerTimeComparisonsHandleFractionalSeconds(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	base := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)

	leased := scheduler.ScheduledJob{JobKey: "fractional-lease", Kind: "cleanup", TradeDate: "2026-08-21", PlannedAt: base, MaxAttempts: 2}
	if err := store.EnsureScheduledJob(ctx, leased); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimScheduledJob(ctx, leased, "owner-a", base, base.Add(500*time.Millisecond)); err != nil || !ok {
		t.Fatalf("initial lease claim: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.ClaimScheduledJob(ctx, leased, "owner-b", base, base.Add(time.Minute)); err != nil || ok {
		t.Fatalf("fractional active lease was stolen: ok=%v err=%v", ok, err)
	}

	retrying := scheduler.ScheduledJob{JobKey: "fractional-retry", Kind: "cleanup", TradeDate: "2026-08-21", PlannedAt: base, MaxAttempts: 2}
	if err := store.EnsureScheduledJob(ctx, retrying); err != nil {
		t.Fatal(err)
	}
	first, ok, err := store.ClaimScheduledJob(ctx, retrying, "owner-a", base, base.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("initial retry claim: ok=%v err=%v", ok, err)
	}
	first.Status = scheduler.JobFailed
	retryAt := base.Add(500 * time.Millisecond)
	first.RetryAt = &retryAt
	if err := store.FinishScheduledJob(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimScheduledJob(ctx, retrying, "owner-b", base, base.Add(time.Minute)); err != nil || ok {
		t.Fatalf("fractional retry was claimed early: ok=%v err=%v", ok, err)
	}
}
