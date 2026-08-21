package scheduler

import (
	"context"
	"errors"
	"time"
)

var ErrJobLeaseLost = errors.New("scheduled job lease is no longer owned")

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobSkipped   JobStatus = "skipped"
)

type ScheduledJob struct {
	JobKey        string     `json:"job_key"`
	Kind          string     `json:"kind"`
	TradeDate     string     `json:"trade_date"`
	PlannedAt     time.Time  `json:"planned_at"`
	Status        JobStatus  `json:"status"`
	AttemptCount  int        `json:"attempt_count"`
	MaxAttempts   int        `json:"max_attempts"`
	LeaseOwner    string     `json:"lease_owner,omitempty"`
	LeaseUntil    *time.Time `json:"lease_until,omitempty"`
	RetryAt       *time.Time `json:"retry_at,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LastErrorCode string     `json:"last_error_code,omitempty"`
	DurationMS    int64      `json:"duration_ms"`
}

// JobStore persists scheduler intent across process restarts. Implementations
// must make Ensure and Claim atomic; Claim is the only path that increments
// attempt_count or changes a job to running.
type JobStore interface {
	EnsureScheduledJob(context.Context, ScheduledJob) error
	ClaimScheduledJob(ctx context.Context, job ScheduledJob, owner string, now time.Time, leaseUntil time.Time) (ScheduledJob, bool, error)
	FinishScheduledJob(ctx context.Context, job ScheduledJob) error
	DueScheduledJobs(ctx context.Context, now time.Time, limit int) ([]ScheduledJob, error)
	ExpireLeasedJobs(ctx context.Context, now time.Time) error
}

type noopJobStore struct{}

func (noopJobStore) EnsureScheduledJob(context.Context, ScheduledJob) error { return nil }
func (noopJobStore) ClaimScheduledJob(_ context.Context, job ScheduledJob, owner string, now time.Time, leaseUntil time.Time) (ScheduledJob, bool, error) {
	job.Status = JobRunning
	job.AttemptCount++
	job.LeaseOwner = owner
	job.LeaseUntil = &leaseUntil
	if job.StartedAt == nil {
		startedAt := now
		job.StartedAt = &startedAt
	}
	return job, true, nil
}
func (noopJobStore) FinishScheduledJob(context.Context, ScheduledJob) error { return nil }
func (noopJobStore) DueScheduledJobs(context.Context, time.Time, int) ([]ScheduledJob, error) {
	return nil, nil
}
func (noopJobStore) ExpireLeasedJobs(context.Context, time.Time) error { return nil }
