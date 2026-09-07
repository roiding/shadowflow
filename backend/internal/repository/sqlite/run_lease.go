package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func migrateRunLeases(db *sql.DB) error {
	for _, table := range []string{"collection_run", "relation_sync_run"} {
		rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, declaredType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return err
			}
			found = found || name == "lease_until"
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if !found {
			if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN lease_until TEXT`); err != nil {
				return err
			}
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_` + table + `_lease ON ` + table + `(lease_until) WHERE status='running'`); err != nil {
			return err
		}
	}
	return nil
}

// Collection tasks already carry scheduler/CLI deadlines. Leave unbounded or
// legacy runs alone: opening another connection is not evidence of a crash.
func runLeaseUntil(ctx context.Context) any {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	return formatTimestamp(deadline.Add(time.Minute))
}

func recoverExpiredRuns(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, now time.Time) error {
	for _, table := range []string{"collection_run", "relation_sync_run"} {
		if _, err := db.ExecContext(ctx, `UPDATE `+table+`
SET status='failed',finished_at=COALESCE(finished_at,?),error_code='interrupted',
error_message='task lease expired before completion',lease_until=NULL
WHERE status='running' AND lease_until IS NOT NULL AND julianday(lease_until)<=julianday(?)`,
			formatTimestamp(now), formatTimestamp(now)); err != nil {
			return fmt.Errorf("recover expired %s: %w", table, err)
		}
	}
	return nil
}
