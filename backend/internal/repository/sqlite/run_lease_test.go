package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

func TestOpeningAnotherStorePreservesLiveRunsAndStages(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(map[bool]string{false: "without deadline", true: "with deadline"}[bounded], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.db")
			first, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			ctx := context.Background()
			if bounded {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Hour)
				defer cancel()
			}
			now := time.Now()
			if err := first.StartRun(ctx, repository.CollectionRun{RunID: "live", SnapshotAt: now, SnapshotKind: graymarket.SnapshotMinuteWork,
				RankType: graymarket.RankIndustry, Status: repository.RunRunning, RequestedDate: "2026-09-07", StartedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := first.StartRelationSync(ctx, repository.RelationSyncRun{RunID: "live-relations", TradeDate: "2026-09-07", Status: repository.RunRunning, StartedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := first.StageRelations(ctx, "live-relations", []graymarket.StockBoardRelation{testRelation("000001", "stock", "BK001", "board", graymarket.BoardConcept, 1)}); err != nil {
				t.Fatal(err)
			}
			second, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			if _, err := second.Maintain(context.Background(), now, 30, 180); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"collection_run", "relation_sync_run"} {
				var count int
				if err := first.db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE status='running'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("live %s changed: count=%d err=%v", table, count, err)
				}
			}
			var staged int
			if err := first.db.QueryRow(`SELECT count(*) FROM stock_board_relation_stage`).Scan(&staged); err != nil || staged != 1 {
				t.Fatalf("live stage removed: count=%d err=%v", staged, err)
			}
			if _, err := first.db.Exec(`UPDATE relation_sync_run SET lease_until=?`, formatTimestamp(now.Add(-time.Minute))); err != nil {
				t.Fatal(err)
			}
			if _, err := second.Maintain(context.Background(), now, 30, 180); err != nil {
				t.Fatal(err)
			}
			if err := first.db.QueryRow(`SELECT count(*) FROM stock_board_relation_stage`).Scan(&staged); err != nil || staged != 0 {
				t.Fatalf("expired stage retained: count=%d err=%v", staged, err)
			}
		})
	}
}

func TestRunLeaseFollowsTaskDeadline(t *testing.T) {
	if got := runLeaseUntil(context.Background()); got != nil {
		t.Fatalf("unbounded task received an inferred lease: %v", got)
	}
	deadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if got := runLeaseUntil(ctx); got != formatTimestamp(deadline.Add(time.Minute)) {
		t.Fatalf("unexpected lease: %v", got)
	}
}

func TestRunLeaseMigrationPreservesExistingRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, table := range []string{"collection_run", "relation_sync_run"} {
		if _, err := db.Exec(`CREATE TABLE ` + table + ` (run_id TEXT, status TEXT); INSERT INTO ` + table + ` VALUES ('legacy','running')`); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateRunLeases(db); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"collection_run", "relation_sync_run"} {
		var status string
		var lease sql.NullString
		if err := db.QueryRow(`SELECT status,lease_until FROM `+table+` WHERE run_id='legacy'`).Scan(&status, &lease); err != nil || status != "running" || lease.Valid {
			t.Fatalf("migration changed legacy state: status=%s lease=%v err=%v", status, lease, err)
		}
	}
}
