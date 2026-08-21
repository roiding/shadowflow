package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/scheduler"
)

const scheduledJobColumns = `job_key,kind,trade_date,planned_at,status,attempt_count,max_attempts,
lease_owner,lease_until,retry_at,started_at,finished_at,last_error_code,last_error_message,duration_ms`

func scanScheduledJob(row interface{ Scan(...any) error }) (scheduler.ScheduledJob, error) {
	var job scheduler.ScheduledJob
	var plannedAt, leaseUntil, retryAt, startedAt, finishedAt sql.NullString
	var status string
	var owner sql.NullString
	var errorCode, errorMessage sql.NullString
	if err := row.Scan(&job.JobKey, &job.Kind, &job.TradeDate, &plannedAt, &status, &job.AttemptCount,
		&job.MaxAttempts, &owner, &leaseUntil, &retryAt, &startedAt, &finishedAt,
		&errorCode, &errorMessage, &job.DurationMS); err != nil {
		return scheduler.ScheduledJob{}, err
	}
	job.Status = scheduler.JobStatus(status)
	if plannedAt.Valid {
		job.PlannedAt = parseSQLiteTime(plannedAt.String)
	}
	if leaseUntil.Valid && leaseUntil.String != "" {
		value := parseSQLiteTime(leaseUntil.String)
		job.LeaseUntil = &value
	}
	if retryAt.Valid && retryAt.String != "" {
		value := parseSQLiteTime(retryAt.String)
		job.RetryAt = &value
	}
	if startedAt.Valid && startedAt.String != "" {
		value := parseSQLiteTime(startedAt.String)
		job.StartedAt = &value
	}
	if finishedAt.Valid && finishedAt.String != "" {
		value := parseSQLiteTime(finishedAt.String)
		job.FinishedAt = &value
	}
	job.LeaseOwner, job.LastErrorCode, job.LastError = owner.String, errorCode.String, errorMessage.String
	return job, nil
}

func parseSQLiteTime(value string) time.Time {
	parsed, err := time.Parse(timestampLayout, value)
	if err == nil {
		return parsed
	}
	parsed, err = time.Parse(time.RFC3339, value)
	if err == nil {
		return parsed
	}
	return time.Time{}
}

func formatNullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(timestampLayout)
}

func (s *Store) EnsureScheduledJob(ctx context.Context, job scheduler.ScheduledJob) error {
	_, err := s.writeDB().ExecContext(ctx, `INSERT INTO scheduled_job
(job_key,kind,trade_date,planned_at,status,attempt_count,max_attempts,lease_owner,lease_until,retry_at,
started_at,finished_at,last_error_code,last_error_message,duration_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(job_key) DO NOTHING`,
		job.JobKey, job.Kind, job.TradeDate, job.PlannedAt.UTC().Format(timestampLayout), string(scheduler.JobQueued),
		0, job.MaxAttempts, nil, nil, nil, nil, nil, "", "", 0)
	if err != nil {
		return fmt.Errorf("ensure scheduled job %s: %w", job.JobKey, err)
	}
	return nil
}

func (s *Store) ClaimScheduledJob(ctx context.Context, job scheduler.ScheduledJob, owner string, now, leaseUntil time.Time) (scheduler.ScheduledJob, bool, error) {
	tx, err := s.writeDB().BeginTx(ctx, nil)
	if err != nil {
		return scheduler.ScheduledJob{}, false, err
	}
	defer tx.Rollback()
	current, err := scanScheduledJob(tx.QueryRowContext(ctx, `SELECT `+scheduledJobColumns+`
FROM scheduled_job WHERE job_key=?`, job.JobKey))
	if errors.Is(err, sql.ErrNoRows) {
		return scheduler.ScheduledJob{}, false, nil
	}
	if err != nil {
		return scheduler.ScheduledJob{}, false, err
	}
	claimed := now.UTC()
	if current.Status == scheduler.JobSucceeded || current.Status == scheduler.JobSkipped ||
		current.AttemptCount >= current.MaxAttempts {
		return current, false, nil
	}
	if current.Status == scheduler.JobRunning && current.LeaseUntil != nil && current.LeaseUntil.After(claimed) {
		return current, false, nil
	}
	if current.Status == scheduler.JobFailed && current.RetryAt != nil && current.RetryAt.After(claimed) {
		return current, false, nil
	}
	lease := leaseUntil.UTC().Format(timestampLayout)
	started := claimed.Format(timestampLayout)
	if current.StartedAt != nil && !current.StartedAt.IsZero() {
		started = current.StartedAt.UTC().Format(timestampLayout)
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_job SET status=?,attempt_count=attempt_count+1,
lease_owner=?,lease_until=?,started_at=? WHERE job_key=? AND status IN ('queued','failed','running')`,
		string(scheduler.JobRunning), owner, lease, started, job.JobKey)
	if err != nil {
		return scheduler.ScheduledJob{}, false, err
	}
	if count, err := result.RowsAffected(); err == nil && count == 0 {
		return current, false, nil
	}
	if err = tx.Commit(); err != nil {
		return scheduler.ScheduledJob{}, false, err
	}
	current.Status = scheduler.JobRunning
	current.AttemptCount++
	current.LeaseOwner = owner
	leaseTime := leaseUntil
	current.LeaseUntil = &leaseTime
	if current.StartedAt == nil || current.StartedAt.IsZero() {
		startTime := parseSQLiteTime(started)
		current.StartedAt = &startTime
	}
	return current, true, nil
}

func (s *Store) FinishScheduledJob(ctx context.Context, job scheduler.ScheduledJob) error {
	retryAt := formatNullableTime(job.RetryAt)
	var errorMessage string
	if job.LastError != "" {
		errorMessage = job.LastError
	}
	_, err := s.writeDB().ExecContext(ctx, `UPDATE scheduled_job SET status=?,retry_at=?,finished_at=?,
duration_ms=?,last_error_code=?,last_error_message=?,lease_owner=NULL,lease_until=NULL WHERE job_key=?`,
		string(job.Status), retryAt, time.Now().UTC().Format(timestampLayout), job.DurationMS,
		job.LastErrorCode, errorMessage, job.JobKey)
	if err != nil {
		return fmt.Errorf("finish scheduled job %s: %w", job.JobKey, err)
	}
	return nil
}

func (s *Store) DueScheduledJobs(ctx context.Context, now time.Time, limit int) ([]scheduler.ScheduledJob, error) {
	if limit <= 0 {
		limit = 8
	}
	timestamp := now.UTC().Format(timestampLayout)
	rows, err := s.readDB().QueryContext(ctx, `SELECT `+scheduledJobColumns+`
FROM scheduled_job
WHERE (status='queued' AND (retry_at IS NULL OR retry_at<=?))
   OR (status='failed' AND retry_at IS NOT NULL AND retry_at<=?)
   OR (status='running' AND lease_until IS NOT NULL AND lease_until<=?)
ORDER BY planned_at LIMIT ?`, timestamp, timestamp, timestamp, limit)
	if err != nil {
		return nil, fmt.Errorf("query due scheduled jobs: %w", err)
	}
	defer rows.Close()
	result := make([]scheduler.ScheduledJob, 0, limit)
	for rows.Next() {
		job, err := scanScheduledJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func (s *Store) ExpireLeasedJobs(ctx context.Context, now time.Time) error {
	_, err := s.writeDB().ExecContext(ctx, `UPDATE scheduled_job SET status='queued',lease_owner=NULL,lease_until=NULL
WHERE status='running' AND lease_until IS NOT NULL AND lease_until<=?`, now.UTC().Format(timestampLayout))
	if err != nil {
		return fmt.Errorf("expire scheduled job leases: %w", err)
	}
	return nil
}

func nowUTC() time.Time { return time.Now().UTC() }
