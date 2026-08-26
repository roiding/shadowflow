package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/repository"
)

func (s *Store) Maintain(ctx context.Context, at time.Time, successRetentionDays, failureRetentionDays int) (repository.MaintenanceResult, error) {
	result := repository.MaintenanceResult{}
	if successRetentionDays < 1 || failureRetentionDays < successRetentionDays {
		return result, fmt.Errorf("invalid retention: success=%d failure=%d", successRetentionDays, failureRetentionDays)
	}
	successCutoff := at.UTC().AddDate(0, 0, -successRetentionDays).Format(timestampLayout)
	failureCutoff := at.UTC().AddDate(0, 0, -failureRetentionDays).Format(timestampLayout)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO collection_run_rollup
(rank_type,status,run_count,record_count,duration_ms,latest_success_at)
SELECT rank_type,status,count(*),coalesce(sum(fetched_total),0),coalesce(sum(duration_ms),0),
max(CASE WHEN status='success' THEN finished_at END)
FROM collection_run
WHERE (status IN ('success','skipped') AND coalesce(finished_at,started_at)<?)
OR (status IN ('failed','partial') AND coalesce(finished_at,started_at)<?)
GROUP BY rank_type,status
ON CONFLICT(rank_type,status) DO UPDATE SET
run_count=collection_run_rollup.run_count+excluded.run_count,
record_count=collection_run_rollup.record_count+excluded.record_count,
duration_ms=collection_run_rollup.duration_ms+excluded.duration_ms,
latest_success_at=CASE
WHEN excluded.latest_success_at IS NULL THEN collection_run_rollup.latest_success_at
WHEN collection_run_rollup.latest_success_at IS NULL THEN excluded.latest_success_at
ELSE max(collection_run_rollup.latest_success_at,excluded.latest_success_at) END`,
		successCutoff, failureCutoff); err != nil {
		return result, err
	}
	if result.SuccessfulRunsDeleted, err = execDeleted(ctx, tx, `DELETE FROM collection_run
WHERE status IN ('success','skipped') AND coalesce(finished_at,started_at)<?`, successCutoff); err != nil {
		return result, err
	}
	if result.FailedRunsDeleted, err = execDeleted(ctx, tx, `DELETE FROM collection_run
WHERE status IN ('failed','partial') AND coalesce(finished_at,started_at)<?`, failureCutoff); err != nil {
		return result, err
	}
	if result.TransientRawDeleted, err = execDeleted(ctx, tx, `DELETE FROM raw_response
WHERE snapshot_kind!='daily_close' AND fetched_at<?`, successCutoff); err != nil {
		return result, err
	}
	if result.RelationRunsDeleted, err = execDeleted(ctx, tx, `DELETE FROM relation_sync_run
WHERE (status='success' AND coalesce(finished_at,started_at)<?)
OR (status!='success' AND coalesce(finished_at,started_at)<?)`, successCutoff, failureCutoff); err != nil {
		return result, err
	}
	// Staged relations are only removed by a successful apply or an orderly
	// failure; a crashed sync leaves its rows behind forever (Open cleans them
	// too, but a long-running server never reopens the store). The 09:15
	// relations job may be live while maintenance runs at 09:05 after a slow
	// start, so rows of a currently running sync are kept.
	if _, err = execDeleted(ctx, tx, `DELETE FROM stock_board_relation_stage
WHERE run_id NOT IN (SELECT run_id FROM relation_sync_run WHERE status='running')`); err != nil {
		return result, err
	}
	if result.ScheduledJobsDeleted, err = execDeleted(ctx, tx, `DELETE FROM scheduled_job
WHERE (status IN ('succeeded','skipped') AND coalesce(finished_at,planned_at)<?)
OR (status='failed' AND coalesce(finished_at,planned_at)<?)
OR (status IN ('queued','running') AND planned_at<?)`, successCutoff, failureCutoff, failureCutoff); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}

	// TRUNCATE instead of PASSIVE: maintenance runs at 09:05 before the
	// market opens, so blocking briefly to reset the WAL to zero bytes is
	// safe and keeps the file from sitting at its end-of-day high-water mark
	// for the whole trading day.
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).
		Scan(&result.WALBusy, &result.WALLogFrames, &result.WALCheckpointedFrames); err != nil {
		return result, fmt.Errorf("checkpoint WAL: %w", err)
	}
	var lastOptimized string
	err = s.db.QueryRowContext(ctx, `SELECT completed_at FROM database_maintenance WHERE name='optimize'`).Scan(&lastOptimized)
	shouldOptimize := errors.Is(err, sql.ErrNoRows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if lastOptimized != "" {
		parsed, parseErr := time.Parse(timestampLayout, lastOptimized)
		if parseErr != nil {
			return result, parseErr
		}
		shouldOptimize = at.UTC().Sub(parsed) >= 30*24*time.Hour
	}
	if shouldOptimize {
		if _, err := s.db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
			return result, fmt.Errorf("optimize database: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO database_maintenance(name,completed_at)
VALUES ('optimize',?) ON CONFLICT(name) DO UPDATE SET completed_at=excluded.completed_at`,
			formatTimestamp(at)); err != nil {
			return result, err
		}
		result.Optimized = true
	}
	result.CompletedAt = time.Now().UTC()
	return result, nil
}

func execDeleted(ctx context.Context, tx *sql.Tx, query string, args ...any) (int, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}
