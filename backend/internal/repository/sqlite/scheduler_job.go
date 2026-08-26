package sqlite

import (
	"context"
	"database/sql"
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
	return formatTimestamp(*value)
}

func (s *Store) EnsureScheduledJob(ctx context.Context, job scheduler.ScheduledJob) error {
	_, err := s.writeDB().ExecContext(ctx, `INSERT INTO scheduled_job
(job_key,kind,trade_date,planned_at,status,attempt_count,max_attempts,lease_owner,lease_until,retry_at,
started_at,finished_at,last_error_code,last_error_message,duration_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(job_key) DO NOTHING`,
		job.JobKey, job.Kind, job.TradeDate, formatTimestamp(job.PlannedAt), string(scheduler.JobQueued),
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

	claimedAt := formatTimestamp(now)
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_job SET status=?,attempt_count=attempt_count+1,
lease_owner=?,lease_until=?,started_at=COALESCE(started_at,?)
WHERE job_key=? AND attempt_count<max_attempts AND (
    (status='queued' AND (retry_at IS NULL OR julianday(retry_at)<=julianday(?))) OR
    (status='failed' AND retry_at IS NOT NULL AND julianday(retry_at)<=julianday(?)) OR
    (status='running' AND (lease_until IS NULL OR julianday(lease_until)<=julianday(?)))
)`, string(scheduler.JobRunning), owner, formatTimestamp(leaseUntil), claimedAt, job.JobKey,
		claimedAt, claimedAt, claimedAt)
	if err != nil {
		return scheduler.ScheduledJob{}, false, fmt.Errorf("claim scheduled job %s: %w", job.JobKey, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return scheduler.ScheduledJob{}, false, fmt.Errorf("count claimed scheduled job %s: %w", job.JobKey, err)
	}
	if count != 1 {
		return scheduler.ScheduledJob{}, false, nil
	}
	claimed, err := scanScheduledJob(tx.QueryRowContext(ctx, `SELECT `+scheduledJobColumns+`
FROM scheduled_job WHERE job_key=?`, job.JobKey))
	if err != nil {
		return scheduler.ScheduledJob{}, false, fmt.Errorf("read claimed scheduled job %s: %w", job.JobKey, err)
	}
	if err := tx.Commit(); err != nil {
		return scheduler.ScheduledJob{}, false, fmt.Errorf("commit scheduled job claim %s: %w", job.JobKey, err)
	}
	return claimed, true, nil
}

func (s *Store) FinishScheduledJob(ctx context.Context, job scheduler.ScheduledJob) error {
	retryAt := formatNullableTime(job.RetryAt)
	result, err := s.writeDB().ExecContext(ctx, `UPDATE scheduled_job SET status=?,retry_at=?,finished_at=?,
duration_ms=?,last_error_code=?,last_error_message=?,lease_owner=NULL,lease_until=NULL
WHERE job_key=? AND status='running' AND lease_owner=?`,
		string(job.Status), retryAt, formatTimestamp(time.Now()), job.DurationMS,
		job.LastErrorCode, job.LastError, job.JobKey, job.LeaseOwner)
	if err != nil {
		return fmt.Errorf("finish scheduled job %s: %w", job.JobKey, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count finished scheduled job %s: %w", job.JobKey, err)
	}
	if count != 1 {
		return fmt.Errorf("finish scheduled job %s: %w", job.JobKey, scheduler.ErrJobLeaseLost)
	}
	return nil
}

func (s *Store) DueScheduledJobs(ctx context.Context, now time.Time, limit int) ([]scheduler.ScheduledJob, error) {
	if limit <= 0 {
		limit = 8
	}
	timestamp := formatTimestamp(now)
	rows, err := s.readDB().QueryContext(ctx, `SELECT `+scheduledJobColumns+`
FROM scheduled_job
WHERE attempt_count<max_attempts AND (
       (status='queued' AND (retry_at IS NULL OR julianday(retry_at)<=julianday(?)))
    OR (status='failed' AND retry_at IS NOT NULL AND julianday(retry_at)<=julianday(?))
    OR (status='running' AND lease_until IS NOT NULL AND julianday(lease_until)<=julianday(?))
)
ORDER BY julianday(planned_at) LIMIT ?`, timestamp, timestamp, timestamp, limit)
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
	timestamp := formatTimestamp(now)
	// Jobs with attempts left go back to the queue; jobs that died on their
	// final attempt are terminal. Without the second branch an exhausted job
	// would be requeued forever: DueScheduledJobs keeps returning it, Claim
	// keeps rejecting it (attempt_count>=max_attempts), and Maintain never
	// deletes queued rows.
	if _, err := s.writeDB().ExecContext(ctx, `UPDATE scheduled_job SET status='queued',lease_owner=NULL,lease_until=NULL
WHERE status='running' AND lease_until IS NOT NULL AND julianday(lease_until)<=julianday(?)
AND attempt_count<max_attempts`, timestamp); err != nil {
		return fmt.Errorf("expire scheduled job leases: %w", err)
	}
	if _, err := s.writeDB().ExecContext(ctx, `UPDATE scheduled_job SET status='failed',lease_owner=NULL,lease_until=NULL,
retry_at=NULL,last_error_code='lease_expired',last_error_message='lease expired after final attempt',
finished_at=?
WHERE status='running' AND lease_until IS NOT NULL AND julianday(lease_until)<=julianday(?)
AND attempt_count>=max_attempts`, timestamp, timestamp); err != nil {
		return fmt.Errorf("fail exhausted scheduled job leases: %w", err)
	}
	return nil
}

func nowUTC() time.Time { return time.Now().UTC() }
