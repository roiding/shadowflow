package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

func stageMoneyArchive(ctx context.Context, tx *sql.Tx, runID string, snapshot graymarket.RankSnapshot, records []graymarket.RankRecord, points []graymarket.MoneyPoint, first, final bool) error {
	if runID == "" || len(records) == 0 {
		return fmt.Errorf("archive run and records are required")
	}
	type key struct {
		market int64
		code   string
	}
	expected := make(map[key]bool, len(records))
	codes := make(map[string]bool, len(records))
	for _, record := range records {
		if record.Code == "" || codes[record.Code] || record.TradeDate != snapshot.TradeDate || record.RankType != snapshot.RankType {
			return fmt.Errorf("invalid archive candidate %s", record.Code)
		}
		codes[record.Code] = true
		expected[key{record.Market, record.Code}] = true
	}
	if first {
		if err := deleteMoneyStage(ctx, tx, runID, snapshot); err != nil {
			return err
		}
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO archive_money_stage
(run_id,trade_date,rank_type,minute_index,snapshot_at,market,code,name,rank,dark_money,regular_money,main_money_inflow,source_time,fetched_at,staged_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(run_id,trade_date,rank_type,market,code,minute_index) DO UPDATE SET
snapshot_at=excluded.snapshot_at,name=excluded.name,rank=excluded.rank,dark_money=excluded.dark_money,
regular_money=excluded.regular_money,main_money_inflow=excluded.main_money_inflow,
source_time=excluded.source_time,fetched_at=excluded.fetched_at,staged_at=excluded.staged_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	stagedAt := formatTimestamp(time.Now())
	for _, point := range points {
		at := point.SnapshotAt.In(location)
		minuteIndex, ok := researchMinuteIndex(at)
		if !ok || at.Second() != 0 || at.Nanosecond() != 0 || at.Format("2006-01-02") != snapshot.TradeDate ||
			point.TradeDate != snapshot.TradeDate || point.RankType != snapshot.RankType || !expected[key{point.Market, point.Code}] {
			return fmt.Errorf("invalid staged money point %s %s", point.Code, point.SnapshotAt)
		}
		if _, err := stmt.ExecContext(ctx, runID, snapshot.TradeDate, string(snapshot.RankType), minuteIndex, formatTimestamp(at), point.Market,
			point.Code, point.Name, point.Rank, point.DarkMoney, point.RegularMoney, point.MainMoneyInflow, point.SourceTime, formatTimestamp(point.FetchedAt), stagedAt); err != nil {
			return err
		}
	}
	if !final {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT market,code,count(*) FROM archive_money_stage
WHERE run_id=? AND trade_date=? AND rank_type=? GROUP BY market,code`, runID, snapshot.TradeDate, string(snapshot.RankType))
	if err != nil {
		return err
	}
	defer rows.Close()
	matched := 0
	for rows.Next() {
		var market int64
		var code string
		var count int
		if err := rows.Scan(&market, &code, &count); err != nil {
			return err
		}
		if !expected[key{market, code}] || count != 48 {
			return fmt.Errorf("%w: staged %s has %d points", repository.ErrArchiveIncomplete, code, count)
		}
		matched++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if matched != len(expected) {
		return fmt.Errorf("%w: staged %d/%d curves", repository.ErrArchiveIncomplete, matched, len(expected))
	}
	return nil
}

func replaceArchiveClose(ctx context.Context, tx *sql.Tx, runID string, snapshot graymarket.RankSnapshot, records []graymarket.RankRecord) error {
	for _, query := range []string{
		`DELETE FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type=?`,
		`DELETE FROM raw_response WHERE date(snapshot_at,'+8 hours')=? AND snapshot_kind='daily_close' AND rank_type=?`,
	} {
		if _, err := tx.ExecContext(ctx, query, snapshot.TradeDate, string(snapshot.RankType)); err != nil {
			return err
		}
	}
	if err := insertRecords(ctx, tx, "rank_snapshot", runID, snapshot.TradeDate, string(graymarket.SnapshotDailyClose), records); err != nil {
		return err
	}
	for _, page := range snapshot.RawPages {
		if err := insertRawPage(ctx, tx, runID, snapshot.SnapshotAt, graymarket.SnapshotDailyClose, snapshot.RankType, page); err != nil {
			return err
		}
	}
	return nil
}

func deleteMoneyStage(ctx context.Context, tx *sql.Tx, runID string, snapshot graymarket.RankSnapshot) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM archive_money_stage WHERE run_id=? AND trade_date=? AND rank_type=?`, runID, snapshot.TradeDate, string(snapshot.RankType))
	return err
}

func cleanupMoneyStages(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	_, err := db.ExecContext(ctx, `DELETE FROM archive_money_stage AS stage
WHERE EXISTS(SELECT 1 FROM collection_run run WHERE run.run_id=stage.run_id AND run.status!='running')
OR (julianday(stage.staged_at)<julianday(?) AND NOT EXISTS(SELECT 1 FROM collection_run run WHERE run.run_id=stage.run_id))`,
		formatTimestamp(time.Now().Add(-24*time.Hour)))
	return err
}
