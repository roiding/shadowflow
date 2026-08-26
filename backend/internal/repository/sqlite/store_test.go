package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

func TestMigrateDailyQuoteColumnsAddsFieldsToLegacyTables(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"rank_intraday_work", "rank_snapshot"} {
		if _, err := db.Exec(`CREATE TABLE ` + table + ` (legacy_id INTEGER NOT NULL)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateDailyQuoteColumns(db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"rank_intraday_work", "rank_snapshot"} {
		columns := map[string]bool{}
		rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				t.Fatal(err)
			}
			columns[name] = true
		}
		rows.Close()
		for _, expected := range []string{"open_price", "high_price", "low_price", "close_price", "previous_close", "change_value", "volume", "turnover", "turnover_rate", "amplitude", "quote_available", "money_available"} {
			if !columns[expected] {
				t.Fatalf("%s migration did not add %s: %v", table, expected, columns)
			}
		}
	}
}

func TestMigrateLegacyBoardMoneyUsesOneAuthoritativeTable(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	at := "2026-08-12T09:35:00+08:00"
	if _, err := store.db.ExecContext(ctx, `DELETE FROM database_maintenance WHERE name='legacy_board_money_v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO rank_snapshot
(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "legacy-run", at, "2026-08-12", "20260812",
		"research_5m", "industry", 1, 90, "BK001", "legacy industry", "1660268100", 0, 0, 0,
		42, 21, 63, 0, 0, 0, 0, 0, "", "", 101, 6, 1, at); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyBoardMoney(store); err != nil {
		t.Fatal(err)
	}
	var currentRows, legacyRows int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM board_money_5m
WHERE trade_date='2026-08-12' AND rank_type='industry' AND code='BK001'`).Scan(&currentRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM rank_snapshot
WHERE snapshot_kind='research_5m'`).Scan(&legacyRows); err != nil {
		t.Fatal(err)
	}
	if currentRows != 1 || legacyRows != 0 {
		t.Fatalf("legacy funding was not normalized: current=%d legacy=%d", currentRows, legacyRows)
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	from := time.Date(2026, 8, 12, 0, 0, 0, 0, location)
	series, err := store.ResearchSeries(ctx, graymarket.RankIndustry, "BK001", from, from.Add(24*time.Hour-time.Nanosecond))
	if err != nil || len(series) != 1 || series[0].DarkMoney != 42 || !series[0].MoneyAvailable {
		t.Fatalf("normalized funding is not queryable: series=%+v err=%v", series, err)
	}
}

func TestIntradayCompactionAndCleanup(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tradeDate := "2026-08-12"

	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		for index, minute := range expectedMinuteTimes() {
			snapshotAt, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+minute, location)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := graymarket.RankSnapshot{
				RequestedDate: "20260812",
				TradeDate:     tradeDate,
				RankType:      rankType,
				SnapshotAt:    snapshotAt,
				Records: []graymarket.RankRecord{{
					TradeDate: tradeDate, SnapshotAt: snapshotAt, RankType: rankType, Rank: 1, Market: 90,
					Code: fmt.Sprintf("%s-code", rankType), Name: string(rankType), DarkMoney: int64(index), FetchedAt: snapshotAt,
				}},
			}
			if err := store.SaveIntraday(ctx, fmt.Sprintf("%s-%d", rankType, index), snapshot, false); err != nil {
				t.Fatal(err)
			}
		}
	}

	summaries, err := store.CompactResearch(ctx, tradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 || summaries[0].CollectedMinutes != 240 || summaries[0].CollectedResearch != 48 || summaries[0].CollectedDailyClose != 1 {
		t.Fatalf("unexpected summaries: %+v", summaries)
	}
	from := time.Date(2026, 8, 12, 0, 0, 0, 0, location)
	to := from.Add(24*time.Hour - time.Nanosecond)
	series, err := store.ResearchSeries(ctx, graymarket.RankIndustry, "industry-code", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 48 || series[0].SnapshotAt.Format("15:04") != "09:35" || series[47].SnapshotAt.Format("15:04") != "15:00" {
		t.Fatalf("unexpected research series: count=%d first=%s last=%s", len(series), series[0].SnapshotAt, series[len(series)-1].SnapshotAt)
	}
	var workCount int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM rank_intraday_work WHERE trade_date=?", tradeDate).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if workCount != 0 {
		t.Fatalf("work table was not cleaned: count=%d", workCount)
	}
	work, err := store.IntradaySeries(ctx, graymarket.RankIndustry, "industry-code", tradeDate)
	if err != nil || len(work) != 49 || work[len(work)-1].SnapshotAt.Format("15:04") != "15:00" {
		t.Fatalf("intraday query did not fall back to research data: count=%d err=%v", len(work), err)
	}
	series, err = store.ResearchSeries(ctx, graymarket.RankIndustry, "industry-code", from, to)
	if err != nil || len(series) != 48 {
		t.Fatalf("research data was lost: count=%d err=%v", len(series), err)
	}
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		page, total, err := store.DailyClosePage(ctx, rankType, tradeDate, "", "rank", false, 10, 0)
		if err != nil || total != 1 || len(page) != 1 || page[0].SnapshotAt.Format("15:04") != "15:00" {
			t.Fatalf("%s daily close missing: total=%d records=%+v err=%v", rankType, total, page, err)
		}
	}
	closeRank, err := store.RankAt(ctx, graymarket.RankIndustry, tradeDate, time.Date(2026, 8, 12, 15, 0, 0, 0, location))
	if err != nil || len(closeRank) != 1 || closeRank[0].DarkMoney != 239 {
		t.Fatalf("RankAt did not return archived 15:00 board close: records=%+v err=%v", closeRank, err)
	}
	latest, err := store.LatestRank(ctx, graymarket.RankIndustry)
	if err != nil || len(latest) != 1 || latest[0].SnapshotAt.Format("15:04") != "15:00" {
		t.Fatalf("latest did not prefer the archived board close: records=%+v err=%v", latest, err)
	}
	quality, err := store.Quality(ctx, tradeDate)
	if err != nil || len(quality) != 2 || quality[0].ExpectedResearch != 48 || quality[0].CollectedResearch != 48 || quality[0].ExpectedDailyClose != 1 || quality[0].CollectedDailyClose != 1 || len(quality[0].MissingResearch) != 0 || len(quality[0].MissingDailyClose) != 0 {
		t.Fatalf("quality did not separate research and close points: quality=%+v err=%v", quality, err)
	}
	second, err := store.CompactResearch(ctx, tradeDate)
	if err != nil || len(second) != 2 || second[0].CollectedResearch != 48 || second[0].CollectedDailyClose != 1 {
		t.Fatalf("repeated compaction was not idempotent: summaries=%+v err=%v", second, err)
	}
}

func TestCompactionRetainsWorkWhenDailyCloseIsMissing(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tradeDate := "2026-08-12"
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		at := time.Date(2026, 8, 12, 14, 55, 0, 0, location)
		record := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: at, RankType: rankType, Rank: 1, Code: string(rankType), Name: string(rankType), FetchedAt: at}
		if err := store.SaveIntraday(ctx, "missing-"+string(rankType), graymarket.RankSnapshot{TradeDate: tradeDate, RankType: rankType, SnapshotAt: at, Records: []graymarket.RankRecord{record}}, false); err != nil {
			t.Fatal(err)
		}
	}
	summaries, err := store.CompactResearch(ctx, tradeDate)
	if err == nil || len(summaries) != 2 || summaries[0].CollectedDailyClose != 0 || len(summaries[0].MissingDailyClose) != 1 {
		t.Fatalf("missing close was not reported: summaries=%+v err=%v", summaries, err)
	}
	var workCount int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM rank_intraday_work WHERE trade_date=?", tradeDate).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if workCount != 2 {
		t.Fatalf("work rows were silently cleaned despite missing close: %d", workCount)
	}
}

func TestOpenRecoversInterruptedRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	_, err = store.db.ExecContext(ctx, `INSERT INTO collection_run
(run_id,snapshot_at,snapshot_kind,rank_type,status,requested_date,actual_trade_date,expected_total,fetched_total,page_count,attempt_count,started_at,finished_at,duration_ms,error_code,error_message)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "stale", now.Format(timestampLayout), "minute_work", "industry", "running", "2026-08-13", "", 0, 0, 0, 1, now.Format(timestampLayout), nil, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.RecentRuns(ctx, "2026-08-13", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != repository.RunFailed || runs[0].ErrorCode != "interrupted" || runs[0].FinishedAt == nil {
		t.Fatalf("interrupted run was not recovered: %+v", runs)
	}
}

func TestOpenMigratesLegacyResearchCloseModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := "2026-08-12T15:00:00+08:00"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO rank_snapshot
(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "legacy", at, "2026-08-12", "2026-08-12", "research_5m", "industry", 1, 90, "BK001", "legacy", "", 0, 0, 42, 0, 0, 0, 0, 0, 0, 0, "", "", 101, 6, 1, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE research_quality`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE research_quality (
trade_date TEXT NOT NULL, rank_type TEXT NOT NULL, expected_minutes INTEGER NOT NULL, collected_minutes INTEGER NOT NULL,
expected_research INTEGER NOT NULL, collected_research INTEGER NOT NULL, missing_minutes_json TEXT NOT NULL,
missing_research_json TEXT NOT NULL, compacted_at TEXT NOT NULL, PRIMARY KEY (trade_date, rank_type))`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO research_quality VALUES (?,?,?,?,?,?,?,?,?)`,
		"2026-08-12", "industry", 240, 240, 48, 48, "[]", "[]", at); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var researchCount, closeCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM rank_snapshot WHERE snapshot_kind='research_5m' AND substr(snapshot_at,12,5)='15:00'`).Scan(&researchCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM rank_snapshot WHERE snapshot_kind='daily_close' AND rank_type='industry' AND substr(snapshot_at,12,5)='15:00'`).Scan(&closeCount); err != nil {
		t.Fatal(err)
	}
	if researchCount != 0 || closeCount != 1 {
		t.Fatalf("legacy 15:00 snapshot was not moved: research=%d close=%d", researchCount, closeCount)
	}
	quality, err := store.Quality(ctx, "2026-08-12")
	if err != nil || len(quality) != 1 || quality[0].ExpectedResearch != 48 || quality[0].CollectedResearch != 0 || quality[0].CollectedDailyClose != 1 {
		t.Fatalf("legacy quality was not migrated: quality=%+v err=%v", quality, err)
	}
}

func TestArchivedIntradayIsOnlyCleanedByNextDayTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC().Format(timestampLayout)
	for _, rankType := range []string{"industry", "concept"} {
		_, err = store.db.ExecContext(ctx, `INSERT INTO research_quality
(trade_date,rank_type,expected_minutes,collected_minutes,expected_research,collected_research,
expected_daily_close,collected_daily_close,missing_minutes_json,missing_research_json,missing_daily_close_json,compacted_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, "2026-08-12", rankType, 240, 240, 48, 48, 1, 1, "[]", "[]", "[]", now)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.db.ExecContext(ctx, `INSERT INTO rank_snapshot
(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "close-"+rankType, "2026-08-12T15:00:00+08:00", "2026-08-12", "2026-08-12", "daily_close", rankType, 1, 90, "close-"+rankType, rankType, "", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, "", "", 101, 6, 1, now)
		if err != nil {
			t.Fatal(err)
		}
		for index, clock := range expectedResearchTimes() {
			at, _ := time.ParseInLocation("2006-01-02 15:04", "2026-08-12 "+clock, time.FixedZone("Asia/Shanghai", 8*60*60))
			_, err = store.db.ExecContext(ctx, `INSERT INTO board_money_5m
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,dark_money,regular_money,main_money_inflow,source_time,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, "archive-"+rankType, at.Format(timestampLayout), "2026-08-12", rankType, 1, 90,
				"code-"+rankType, rankType, index, index, index*2, 0, now)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO stock_archive_quality
(trade_date,expected_stocks,expected_points,expected_kline_stocks,money_rows,kline_rows,daily_close_rows,daily_kline_rows,money_archived_at,kline_archived_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`, "2026-08-12", 1, 48, 1, 48, 48, 1, 1, now, now, now); err != nil {
		t.Fatal(err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO rank_intraday_work
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "stale", now, "2026-08-12", "industry", 1, 90, "BK001", "stale", "", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, "", "", 101, 6, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM rank_intraday_work WHERE trade_date='2026-08-12'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("restart must preserve intraday rows until the cleanup job, got %d", count)
	}
	if err := store.CleanupArchivedIntraday(ctx, "2026-08-13"); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM rank_intraday_work WHERE trade_date='2026-08-12'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected next-day cleanup to remove archived intraday rows, got %d", count)
	}
}

func TestCleanupRetainsIntradayWhenAnyLongTermArchivePartIsMissing(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	tradeDate := "2026-08-12"
	now := time.Now().UTC().Format(timestampLayout)
	for _, rankType := range []string{"industry", "concept"} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO research_quality
(trade_date,rank_type,expected_minutes,collected_minutes,expected_research,collected_research,
expected_daily_close,collected_daily_close,missing_minutes_json,missing_research_json,missing_daily_close_json,compacted_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, tradeDate, rankType, 240, 240, 48, 48, 1, 1, "[]", "[]", "[]", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO stock_archive_quality
(trade_date,expected_stocks,expected_points,expected_kline_stocks,money_rows,kline_rows,daily_close_rows,daily_kline_rows,money_archived_at,kline_archived_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`, tradeDate, 2, 48, 1, 48, 48, 2, 1, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO rank_intraday_work
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "work", now, tradeDate, "industry", 1, 90, "BK001", "work", "", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, "", "", 101, 6, 1, now); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		breakSQL string
		fixSQL   string
	}{
		{"board money", `UPDATE research_quality SET collected_research=47 WHERE trade_date='2026-08-12' AND rank_type='industry'`, `UPDATE research_quality SET collected_research=48 WHERE trade_date='2026-08-12' AND rank_type='industry'`},
		{"board close", `UPDATE research_quality SET collected_daily_close=0 WHERE trade_date='2026-08-12' AND rank_type='concept'`, `UPDATE research_quality SET collected_daily_close=1 WHERE trade_date='2026-08-12' AND rank_type='concept'`},
		{"stock money", `UPDATE stock_archive_quality SET money_rows=47 WHERE trade_date='2026-08-12'`, `UPDATE stock_archive_quality SET money_rows=48 WHERE trade_date='2026-08-12'`},
		{"stock five-minute K", `UPDATE stock_archive_quality SET kline_rows=47 WHERE trade_date='2026-08-12'`, `UPDATE stock_archive_quality SET kline_rows=48 WHERE trade_date='2026-08-12'`},
		{"stock close", `UPDATE stock_archive_quality SET daily_close_rows=1 WHERE trade_date='2026-08-12'`, `UPDATE stock_archive_quality SET daily_close_rows=2 WHERE trade_date='2026-08-12'`},
		{"stock daily K", `UPDATE stock_archive_quality SET daily_kline_rows=0 WHERE trade_date='2026-08-12'`, `UPDATE stock_archive_quality SET daily_kline_rows=1 WHERE trade_date='2026-08-12'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.db.ExecContext(ctx, test.breakSQL); err != nil {
				t.Fatal(err)
			}
			if err := store.CleanupArchivedIntraday(ctx, "2026-08-13"); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM rank_intraday_work WHERE trade_date=?`, tradeDate).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("cleanup removed work data while %s was incomplete", test.name)
			}
			if _, err := store.db.ExecContext(ctx, test.fixSQL); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := store.CleanupArchivedIntraday(ctx, "2026-08-13"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM rank_intraday_work WHERE trade_date=?`, tradeDate).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cleanup retained fully archived work data: %d", count)
	}
}

func TestSaveBoardArchivePersists48MoneyPointsAndOneClose(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	record := graymarket.RankRecord{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankIndustry,
		Rank: 1, Market: 90, Code: "BK001", Name: "行业", DarkMoney: 500, FetchedAt: closeAt}
	snapshot := graymarket.RankSnapshot{TradeDate: record.TradeDate, RankType: record.RankType, SnapshotAt: closeAt, Records: []graymarket.RankRecord{record}}
	points := testMoneyPoints(snapshot)
	if err := store.SaveBoardArchive(ctx, "board-archive", snapshot, points); err != nil {
		t.Fatal(err)
	}
	var moneyRows, closeRows int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM board_money_5m WHERE trade_date=? AND rank_type='industry'`, record.TradeDate).Scan(&moneyRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM rank_snapshot WHERE trade_date=? AND rank_type='industry' AND snapshot_kind='daily_close'`, record.TradeDate).Scan(&closeRows); err != nil {
		t.Fatal(err)
	}
	quality, err := store.Quality(ctx, record.TradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if moneyRows != 48 || closeRows != 1 || len(quality) != 1 || quality[0].CollectedResearch != 48 || quality[0].CollectedDailyClose != 1 {
		t.Fatalf("unexpected board archive: money=%d close=%d quality=%+v", moneyRows, closeRows, quality)
	}
}

func TestSaveBoardArchiveDoesNotMaterializeUnavailableMoneyPoints(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	record := graymarket.RankRecord{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankConcept,
		Rank: 0, Market: 90, Code: "BK-MISSING", Name: "缺资金", FetchedAt: closeAt}
	snapshot := graymarket.RankSnapshot{TradeDate: record.TradeDate, RankType: record.RankType,
		SnapshotAt: closeAt, Records: []graymarket.RankRecord{record}}
	points := testMoneyPoints(snapshot)[:24]
	if err := store.SaveBoardArchive(ctx, "board-partial", snapshot, points); err != nil {
		t.Fatal(err)
	}
	var total, available int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(money_available),0)
FROM board_money_5m WHERE trade_date=? AND rank_type='concept'`, record.TradeDate).Scan(&total, &available); err != nil {
		t.Fatal(err)
	}
	if total != 24 || available != 24 {
		t.Fatalf("unavailable board money should not be materialized: total=%d available=%d", total, available)
	}
}

func TestSaveBoardArchiveBatchFinalizesRankPerSnapshot(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 25, 15, 0, 0, 0, location)
	records := []graymarket.RankRecord{
		{TradeDate: "2026-08-25", SnapshotAt: closeAt, RankType: graymarket.RankConcept, Rank: 1, Market: 90, Code: "BK001", Name: "概念A", FetchedAt: closeAt},
		{TradeDate: "2026-08-25", SnapshotAt: closeAt, RankType: graymarket.RankConcept, Rank: 2, Market: 90, Code: "BK002", Name: "概念B", FetchedAt: closeAt},
	}
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-25", RankType: graymarket.RankConcept, SnapshotAt: closeAt, Records: records}
	at := func(clock string) time.Time {
		value, _ := time.ParseInLocation("2006-01-02 15:04", snapshot.TradeDate+" "+clock, location)
		return value
	}
	point := func(clock, code string, darkMoney int64) graymarket.MoneyPoint {
		return graymarket.MoneyPoint{TradeDate: snapshot.TradeDate, SnapshotAt: at(clock), RankType: snapshot.RankType,
			Market: 90, Code: code, Name: code, DarkMoney: darkMoney, FetchedAt: closeAt}
	}
	// BK001 arrives in the first batch, BK002 in the final batch; a per-batch
	// rank pass would wrongly rank BK001 first everywhere.
	first := []graymarket.MoneyPoint{
		point("10:00", "BK001", 100),
		point("10:05", "BK001", 300),
		point("10:10", "BK001", 200),
	}
	final := []graymarket.MoneyPoint{
		point("10:00", "BK002", 200),
		point("10:05", "BK002", 100),
		point("10:10", "BK002", 200),
	}
	if err := store.SaveBoardArchiveBatch(ctx, "rank-batch", snapshot, first, true, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBoardArchiveBatch(ctx, "rank-batch", snapshot, final, false, true); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT snapshot_at, code, rank FROM board_money_5m
WHERE trade_date=? AND rank_type='concept' ORDER BY snapshot_at, rank`, snapshot.TradeDate)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		snapshotAt string
		code       string
		rank       int64
	}
	var got []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.snapshotAt, &item.code, &item.rank); err != nil {
			t.Fatal(err)
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []row{
		{"2026-08-25T10:00:00+08:00", "BK002", 1}, {"2026-08-25T10:00:00+08:00", "BK001", 2},
		{"2026-08-25T10:05:00+08:00", "BK001", 1}, {"2026-08-25T10:05:00+08:00", "BK002", 2},
		// Equal dark_money is broken by ascending code.
		{"2026-08-25T10:10:00+08:00", "BK001", 1}, {"2026-08-25T10:10:00+08:00", "BK002", 2},
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected board rank rows: %+v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("rank mismatch at %d: got %+v want %+v", index, got[index], want[index])
		}
	}
}

func TestSaveStockArchiveBatchFinalizesMoneyRankPerMinute(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 25, 15, 0, 0, 0, location)
	records := []graymarket.RankRecord{
		{TradeDate: "2026-08-25", SnapshotAt: closeAt, RankType: graymarket.RankStock, Rank: 1, Market: 0, Code: "000001", Name: "甲", QuoteAvailable: true, OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10.5, FetchedAt: closeAt},
		{TradeDate: "2026-08-25", SnapshotAt: closeAt, RankType: graymarket.RankStock, Rank: 2, Market: 1, Code: "600001", Name: "乙", QuoteAvailable: true, OpenPrice: 20, HighPrice: 21, LowPrice: 19, ClosePrice: 20.5, FetchedAt: closeAt},
	}
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-25", RankType: graymarket.RankStock, SnapshotAt: closeAt, Records: records}
	at := func(clock string) time.Time {
		value, _ := time.ParseInLocation("2006-01-02 15:04", snapshot.TradeDate+" "+clock, location)
		return value
	}
	point := func(clock string, record graymarket.RankRecord, darkMoney int64) graymarket.MoneyPoint {
		return graymarket.MoneyPoint{TradeDate: snapshot.TradeDate, SnapshotAt: at(clock), RankType: snapshot.RankType,
			Market: record.Market, Code: record.Code, Name: record.Name, DarkMoney: darkMoney, FetchedAt: closeAt}
	}
	first := []graymarket.MoneyPoint{
		point("10:00", records[0], 100),
		point("10:05", records[0], 300),
	}
	final := []graymarket.MoneyPoint{
		point("10:00", records[1], 200),
		point("10:05", records[1], 100),
	}
	if err := store.SaveStockArchiveBatch(ctx, "rank-stock-batch", snapshot, first, true, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStockArchiveBatch(ctx, "rank-stock-batch", snapshot, final, false, true); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT minute_index, code, money_rank FROM stock_research_5m
WHERE trade_date=? ORDER BY minute_index, money_rank`, snapshot.TradeDate)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		minuteIndex int
		code        string
		rank        int64
	}
	var got []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.minuteIndex, &item.code, &item.rank); err != nil {
			t.Fatal(err)
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []row{
		{5, "600001", 1}, {5, "000001", 2},
		{6, "000001", 1}, {6, "600001", 2},
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected stock rank rows: %+v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("rank mismatch at %d: got %+v want %+v", index, got[index], want[index])
		}
	}
}

func TestSaveStockArchivePersists48MoneyBarsAndDailyK(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	records := []graymarket.RankRecord{
		{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Rank: 1, Market: 0, Code: "000001", Name: "交易股", QuoteAvailable: true, OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10.5, FetchedAt: closeAt},
		{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Rank: 2, Market: 1, Code: "600001", Name: "停牌股", PreviousClose: 8, FetchedAt: closeAt},
	}
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-14", RankType: graymarket.RankStock, SnapshotAt: closeAt, Records: records}
	if err := store.SaveStockArchive(ctx, "stock-archive", snapshot, testMoneyPoints(snapshot)); err != nil {
		t.Fatal(err)
	}
	quality, err := store.StockArchiveQuality(ctx, snapshot.TradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if quality.ExpectedStocks != 1 || quality.ExpectedKlineStocks != 1 || quality.MoneyRows != 48 || quality.DailyCloseRows != 1 || quality.DailyKlineRows != 1 || quality.KlineRows != 0 {
		t.Fatalf("unexpected stock money/daily archive quality: %+v", quality)
	}
	var identityRows int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock'`, snapshot.TradeDate).Scan(&identityRows); err != nil {
		t.Fatal(err)
	}
	if identityRows != 1 {
		t.Fatalf("suspended stock should not have a daily identity row: %d", identityRows)
	}
	var researchRows int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM stock_research_5m WHERE trade_date=? AND code='600001'`, snapshot.TradeDate).Scan(&researchRows); err != nil {
		t.Fatal(err)
	}
	if researchRows != 0 {
		t.Fatalf("suspended stock should not have five-minute research rows: %d", researchRows)
	}

	klines := make([]graymarket.StockKlinePoint, 0, 48)
	for index, clock := range expectedResearchTimes() {
		at, parseErr := time.ParseInLocation("2006-01-02 15:04", snapshot.TradeDate+" "+clock, location)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		klines = append(klines, graymarket.StockKlinePoint{TradeDate: snapshot.TradeDate, SnapshotAt: at, Market: 0, Code: "000001",
			OpenPrice: 10.1234, HighPrice: 10.5678, LowPrice: 9.8765, ClosePrice: 10.4321,
			Volume: int64(1000 + index), Turnover: int64(2000 + index), Amplitude: 0.123456,
			ChangePct: -0.012345, ChangeValue: -0.1234, TurnoverRate: 0.023456})
	}
	if err := store.SaveStockKlines(ctx, "stock-kline", klines); err != nil {
		t.Fatal(err)
	}
	series, err := store.StockResearchSeries(ctx, "000001", snapshot.TradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 48 || series[47].SnapshotAt.Format("15:04") != "15:00" || !series[0].KlineAvailable ||
		series[0].OpenPrice != 10.1234 || series[0].Amplitude != 0.123456 || series[0].ChangePct != -0.012345 {
		t.Fatalf("unexpected joined stock research series: %+v", series)
	}
	quality, err = store.StockArchiveQuality(ctx, snapshot.TradeDate)
	if err != nil || quality.KlineRows != 48 || quality.KlineArchivedAt == nil {
		t.Fatalf("unexpected completed stock quality: quality=%+v err=%v", quality, err)
	}
	complete, err := store.HasStockKlineArchive(ctx, snapshot.TradeDate)
	if err != nil || !complete {
		t.Fatalf("stock kline archive should be complete: complete=%v err=%v", complete, err)
	}

	points := testMoneyPoints(snapshot)
	points[0].DarkMoney = 999
	if err := store.SaveStockArchive(ctx, "stock-archive-rerun", snapshot, points); err != nil {
		t.Fatal(err)
	}
	series, err = store.StockResearchSeries(ctx, "000001", snapshot.TradeDate)
	if err != nil || len(series) != 48 || series[0].KlineAvailable || series[0].OpenPrice != 0 || series[0].DarkMoney != 999 {
		t.Fatalf("stock archive rerun did not invalidate stale klines while updating money: series=%+v err=%v", series, err)
	}
	quality, err = store.StockArchiveQuality(ctx, snapshot.TradeDate)
	if err != nil || quality.KlineRows != 0 || quality.KlineArchivedAt != nil {
		t.Fatalf("stock archive rerun retained stale kline quality: quality=%+v err=%v", quality, err)
	}
	if err := store.SaveStockKlines(ctx, "stock-kline-rerun", klines); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateStockResearchUniverseRemovesUnavailablePlaceholders(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	tradeDate := "2026-08-20"
	at := "2026-08-20T07:00:00Z"
	if _, err := store.db.ExecContext(ctx, `DELETE FROM database_maintenance WHERE name='stock_research_universe_v1'`); err != nil {
		t.Fatal(err)
	}
	for _, stock := range []struct {
		market int
		code   string
		quote  int
	}{{0, "000001", 1}, {1, "600001", 0}} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO rank_snapshot
(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at,
quote_available)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"run", at, tradeDate, "20260820", "daily_close", "stock", stock.market+1, stock.market, stock.code,
			"测试", "", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, "", "", 0, 0, 0, at, stock.quote); err != nil {
			t.Fatal(err)
		}
		for minute := 0; minute < 48; minute++ {
			if _, err := store.db.ExecContext(ctx, `INSERT INTO stock_research_5m
(trade_date,minute_index,market,code,money_rank,dark_money,regular_money,main_money_inflow,money_available)
VALUES (?,?,?,?,?,?,?,?,?)`, tradeDate, minute, stock.market, stock.code, 0, 0, 0, 0, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO stock_archive_quality
(trade_date,expected_stocks,expected_points,expected_kline_stocks,money_rows,kline_rows,daily_close_rows,daily_kline_rows,updated_at)
VALUES (?,?,?,?,?,?,?,?,?)`, tradeDate, 2, 48, 1, 48, 0, 2, 1, at); err != nil {
		t.Fatal(err)
	}
	if err := migrateStockResearchUniverse(store); err != nil {
		t.Fatal(err)
	}
	var kept, removed int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM stock_research_5m WHERE code='000001'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM stock_research_5m WHERE code='600001'`).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if kept != 48 || removed != 0 {
		t.Fatalf("unexpected placeholder migration result: kept=%d removed=%d", kept, removed)
	}
	var moneyRows, klineRows int
	if err := store.db.QueryRowContext(ctx, `SELECT money_rows,kline_rows FROM stock_archive_quality WHERE trade_date=?`, tradeDate).Scan(&moneyRows, &klineRows); err != nil {
		t.Fatal(err)
	}
	if moneyRows != 0 || klineRows != 0 {
		t.Fatalf("quality was not recomputed after placeholder removal: money=%d kline=%d", moneyRows, klineRows)
	}
	var identityRows int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM rank_snapshot WHERE trade_date=? AND rank_type='stock'`, tradeDate).Scan(&identityRows); err != nil {
		t.Fatal(err)
	}
	if identityRows != 2 {
		t.Fatalf("daily identity snapshot was changed: rows=%d", identityRows)
	}
}

func TestSaveStockKlinesCommitsCompleteStocksIncrementally(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	records := []graymarket.RankRecord{
		{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Rank: 1, Market: 0, Code: "000001", QuoteAvailable: true, FetchedAt: closeAt},
		{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Rank: 2, Market: 1, Code: "600001", QuoteAvailable: true, FetchedAt: closeAt},
	}
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-14", RankType: graymarket.RankStock, SnapshotAt: closeAt, Records: records}
	if err := store.SaveStockArchive(ctx, "stock-archive", snapshot, testMoneyPoints(snapshot)); err != nil {
		t.Fatal(err)
	}
	missing, err := store.MissingStockKlineCodes(ctx, snapshot.TradeDate)
	if err != nil || len(missing) != 2 {
		t.Fatalf("unexpected initial missing stocks: missing=%v err=%v", missing, err)
	}

	first := testStockKlines(snapshot.TradeDate, location, records[0])
	if err := store.SaveStockKlines(ctx, "partial-kline", first); err != nil {
		t.Fatal(err)
	}
	quality, err := store.StockArchiveQuality(ctx, snapshot.TradeDate)
	if err != nil || quality.KlineRows != 48 || quality.KlineArchivedAt != nil {
		t.Fatalf("partial archive quality is incorrect: quality=%+v err=%v", quality, err)
	}
	complete, err := store.HasStockKlineArchive(ctx, snapshot.TradeDate)
	if err != nil || complete {
		t.Fatalf("partial archive must remain incomplete: complete=%v err=%v", complete, err)
	}
	missing, err = store.MissingStockKlineCodes(ctx, snapshot.TradeDate)
	if err != nil || len(missing) != 1 || missing[0] != "600001" {
		t.Fatalf("completed stock should not be fetched again: missing=%v err=%v", missing, err)
	}

	second := testStockKlines(snapshot.TradeDate, location, records[1])
	if err := store.SaveStockKlines(ctx, "invalid-partial-stock", second[:47]); err == nil {
		t.Fatal("an incomplete single-stock batch must be rejected")
	}
	quality, err = store.StockArchiveQuality(ctx, snapshot.TradeDate)
	if err != nil || quality.KlineRows != 48 {
		t.Fatalf("rejected batch changed persisted progress: quality=%+v err=%v", quality, err)
	}

	if err := store.SaveStockKlines(ctx, "complete-kline", second); err != nil {
		t.Fatal(err)
	}
	quality, err = store.StockArchiveQuality(ctx, snapshot.TradeDate)
	if err != nil || quality.KlineRows != 96 || quality.KlineArchivedAt == nil {
		t.Fatalf("completed archive quality is incorrect: quality=%+v err=%v", quality, err)
	}
	complete, err = store.HasStockKlineArchive(ctx, snapshot.TradeDate)
	if err != nil || !complete {
		t.Fatalf("stock kline archive should be complete: complete=%v err=%v", complete, err)
	}
}

func testStockKlines(tradeDate string, location *time.Location, record graymarket.RankRecord) []graymarket.StockKlinePoint {
	points := make([]graymarket.StockKlinePoint, 0, 48)
	for _, clock := range expectedResearchTimes() {
		at, _ := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+clock, location)
		points = append(points, graymarket.StockKlinePoint{TradeDate: tradeDate, SnapshotAt: at, Market: record.Market, Code: record.Code,
			Source: graymarket.KlineSourceFiveMinute, OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10.5, Volume: 1000, Turnover: 2000})
	}
	return points
}

func testMoneyPoints(snapshot graymarket.RankSnapshot) []graymarket.MoneyPoint {
	location := snapshot.SnapshotAt.Location()
	points := make([]graymarket.MoneyPoint, 0, len(snapshot.Records)*48)
	for _, record := range snapshot.Records {
		for index, clock := range expectedResearchTimes() {
			at, _ := time.ParseInLocation("2006-01-02 15:04", snapshot.TradeDate+" "+clock, location)
			points = append(points, graymarket.MoneyPoint{TradeDate: snapshot.TradeDate, SnapshotAt: at, RankType: snapshot.RankType,
				Rank: int64(index + 1), Market: record.Market, Code: record.Code, Name: record.Name,
				DarkMoney: int64(index), RegularMoney: int64(index * 2), MainMoneyInflow: int64(index * 3), FetchedAt: snapshot.SnapshotAt})
		}
	}
	return points
}

func TestOperationalMetrics(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	finished := now.Add(time.Second)
	run := repository.CollectionRun{RunID: "metric-run", SnapshotAt: now, SnapshotKind: graymarket.SnapshotMinuteWork, RankType: graymarket.RankIndustry, Status: repository.RunRunning, RequestedDate: "2026-08-13", StartedAt: now}
	if err := store.StartRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.Status, run.FinishedAt, run.FetchedTotal, run.DurationMS = repository.RunSuccess, &finished, 71, 250
	if err := store.FinishRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	metrics, err := store.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.RunCounts) != 1 || metrics.RunCounts[0].Value != 1 || len(metrics.RecordCounts) != 1 || metrics.RecordCounts[0].Value != 71 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestArchiveManifestTracksCompletenessAndKlineSource(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tradeDate := "2026-08-14"
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		record := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: rankType,
			Rank: 1, Market: 90, Code: "BK-" + string(rankType), Name: string(rankType), FetchedAt: closeAt}
		snapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: rankType, SnapshotAt: closeAt,
			Records: []graymarket.RankRecord{record}}
		if err := store.SaveBoardArchive(ctx, "board-"+string(rankType), snapshot, testMoneyPoints(snapshot)); err != nil {
			t.Fatal(err)
		}
	}
	stock := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: graymarket.RankStock,
		Rank: 1, Market: 0, Code: "000001", Name: "stock", QuoteAvailable: true, FetchedAt: closeAt}
	stockSnapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: graymarket.RankStock,
		SnapshotAt: closeAt, Records: []graymarket.RankRecord{stock}}
	if err := store.SaveStockArchive(ctx, "stock", stockSnapshot, testMoneyPoints(stockSnapshot)); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.ArchiveManifest(ctx, tradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != archiveManifestPending || len(manifest.ValidationErrors) == 0 {
		t.Fatalf("manifest completed before stock klines: %+v", manifest)
	}
	klines := testStockKlines(tradeDate, location, stock)
	for index := range klines {
		klines[index].Source = graymarket.KlineSourceTrend241
	}
	if err := store.SaveStockKlines(ctx, "kline", klines); err != nil {
		t.Fatal(err)
	}
	manifest, err = store.ArchiveManifest(ctx, tradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != archiveManifestCompleted || manifest.CompletedAt == nil || len(manifest.ValidationErrors) != 0 ||
		manifest.IndustryCloseRows != 1 || manifest.IndustryMoneyRows != 48 ||
		manifest.ConceptCloseRows != 1 || manifest.ConceptMoneyRows != 48 ||
		manifest.StockCloseRows != 1 || manifest.StockMoneyRows != 48 ||
		manifest.StockKlineRows != 48 || manifest.StockDailyKlineRows != 1 ||
		manifest.CodeCount != 3 || len(manifest.CodeSetSHA256) != 64 ||
		manifest.KlineSourceCounts[graymarket.KlineSourceTrend241] != 1 {
		t.Fatalf("unexpected complete manifest: %+v", manifest)
	}
}

func TestArchiveRevisionTracksPrimaryDailyDataWithoutCopyingIt(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tradeDate := "2026-08-14"
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		record := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: rankType,
			Rank: 1, Market: 90, Code: "BK-" + string(rankType), Name: string(rankType), FetchedAt: closeAt}
		snapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: rankType, SnapshotAt: closeAt,
			Records: []graymarket.RankRecord{record}}
		if err := store.SaveBoardArchive(ctx, "board-"+string(rankType), snapshot, testMoneyPoints(snapshot)); err != nil {
			t.Fatal(err)
		}
	}
	stock := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: graymarket.RankStock,
		Rank: 1, Market: 0, Code: "000001", Name: "stock", QuoteAvailable: true,
		ClosePrice: 10, DarkMoney: 100, FetchedAt: closeAt}
	snapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: graymarket.RankStock,
		SnapshotAt: closeAt, Records: []graymarket.RankRecord{stock}}
	if err := store.SaveStockArchive(ctx, "stock-v1", snapshot, testMoneyPoints(snapshot)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStockKlines(ctx, "kline-v1", testStockKlines(tradeDate, location, stock)); err != nil {
		t.Fatal(err)
	}
	first, err := store.SealArchiveRevision(ctx, tradeDate, "revision-one")
	if err != nil {
		t.Fatal(err)
	}

	snapshot.Records[0].DarkMoney = 999
	points := testMoneyPoints(snapshot)
	points[0].DarkMoney = 777
	if err := store.SaveStockArchive(ctx, "stock-v2", snapshot, points); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStockKlines(ctx, "kline-v2", testStockKlines(tradeDate, location, stock)); err != nil {
		t.Fatal(err)
	}
	second, err := store.SealArchiveRevision(ctx, tradeDate, "revision-two")
	if err != nil {
		t.Fatal(err)
	}
	if first.RevisionNo != 1 || second.RevisionNo != 1 || second.RevisionID != first.RevisionID ||
		first.ContentSHA256 == second.ContentSHA256 {
		t.Fatalf("unexpected lightweight archive metadata: first=%+v second=%+v", first, second)
	}
	closeRows, _, err := store.DailyCloseRevisionPage(ctx, first.RevisionID, graymarket.RankStock, "", "rank", false, 10, 0)
	if err != nil || len(closeRows) != 1 || closeRows[0].DarkMoney != 999 {
		t.Fatalf("revision query did not read primary close archive: rows=%+v err=%v", closeRows, err)
	}
	pointsAfterRerun, err := store.StockResearchRevisionSeries(ctx, first.RevisionID, "000001")
	if err != nil || len(pointsAfterRerun) != 48 || pointsAfterRerun[0].DarkMoney != 777 {
		t.Fatalf("revision query did not read primary research archive: points=%+v err=%v", pointsAfterRerun, err)
	}
	for _, table := range []string{"rank_snapshot_revision", "board_money_revision", "stock_research_revision", "stock_kline_source_revision", "raw_response_revision"} {
		var exists int
		if err := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil || exists != 0 {
			t.Fatalf("duplicate archive table %s still exists: exists=%d err=%v", table, exists, err)
		}
	}
	manifest, err := store.ArchiveManifest(ctx, tradeDate)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := store.ArchiveRevisions(ctx, tradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.CurrentRevisionID != second.RevisionID || manifest.RevisionNo != 1 ||
		len(revisions) != 1 || revisions[0].RevisionID != second.RevisionID {
		t.Fatalf("current revision pointer is incorrect: manifest=%+v revisions=%+v", manifest, revisions)
	}
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		record := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: rankType,
			Rank: 1, Market: 90, Code: "BK-" + string(rankType), Name: string(rankType), FetchedAt: closeAt}
		boardSnapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: rankType,
			SnapshotAt: closeAt, Records: []graymarket.RankRecord{record}}
		if err := store.SaveBoardArchive(ctx, "rerun-"+string(rankType), boardSnapshot, testMoneyPoints(boardSnapshot)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveStockArchive(ctx, "stock-v3", snapshot, points); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStockKlines(ctx, "kline-v3", testStockKlines(tradeDate, location, stock)); err != nil {
		t.Fatal(err)
	}
	third, err := store.SealArchiveRevision(ctx, tradeDate, "revision-three")
	if err != nil {
		t.Fatal(err)
	}
	if third.RevisionNo != 1 || third.RevisionID != second.RevisionID || third.ContentSHA256 != second.ContentSHA256 {
		t.Fatalf("identical rerun changed the daily archive identity: second=%+v third=%+v", second, third)
	}
}

func TestVersionedFeaturesAndFutureLabels(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	dates := []string{"2026-08-10", "2026-08-11", "2026-08-12", "2026-08-13", "2026-08-14", "2026-08-17"}
	detectedAt := time.Date(2026, 8, 10, 8, 0, 0, 0, location).UTC().Format(timestampLayout)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO stock_board_relation_baseline
(baseline_date,stock_code,stock_market,stock_name,board_code,board_name,board_type,
source_order,relation_source,relation_scope,detected_at,raw_data)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, dates[0], "000001", 0, "stock", "BK-IND", "industry",
		"industry", 1, graymarket.RelationSourceQuoteClist,
		graymarket.RelationScopeBoardConstituents, detectedAt, `{}`); err != nil {
		t.Fatal(err)
	}
	for index, tradeDate := range dates {
		closeAt, parseErr := time.ParseInLocation("2006-01-02 15:04", tradeDate+" 15:00", location)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		industry := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt,
			RankType: graymarket.RankIndustry, Rank: 1, Market: 90, Code: "BK-IND",
			Name: "industry", QuoteAvailable: true, ClosePrice: 100 + float64(index),
			ChangePct: 0.01, Turnover: int64(1_000_000 + index*100_000),
			DarkMoney: int64(100 + index), MainMoneyInflow: int64(200 + index*10),
			DarkActivity: 0.02, FetchedAt: closeAt}
		concept := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt,
			RankType: graymarket.RankConcept, Rank: 1, Market: 90, Code: "BK-CON",
			Name: "concept", QuoteAvailable: true, ClosePrice: 200 + float64(index),
			ChangePct: 0.01, Turnover: int64(2_000_000 + index*100_000),
			DarkMoney: int64(200 + index), MainMoneyInflow: int64(300 + index*10),
			DarkActivity: 0.03, FetchedAt: closeAt}
		for _, record := range []graymarket.RankRecord{industry, concept} {
			snapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: record.RankType,
				SnapshotAt: closeAt, Records: []graymarket.RankRecord{record}}
			points := testMoneyPoints(snapshot)
			if err := store.SaveBoardArchive(ctx, "board-"+string(record.RankType)+"-"+tradeDate, snapshot, points); err != nil {
				t.Fatal(err)
			}
		}
		stock := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt,
			RankType: graymarket.RankStock, Rank: 1, Market: 0, Code: "000001", Name: "stock",
			QuoteAvailable: true, ClosePrice: 10 + float64(index), ChangePct: 0.02,
			Turnover: int64(500_000 + index*100_000), DarkMoney: int64(50 + index*10),
			RegularMoney: int64(25 + index*5), MainMoneyInflow: int64(75 + index*15),
			DarkActivity: 0.1, FetchedAt: closeAt}
		stockSnapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: graymarket.RankStock,
			SnapshotAt: closeAt, Records: []graymarket.RankRecord{stock}}
		if err := store.SaveStockArchive(ctx, "stock-"+tradeDate, stockSnapshot, testMoneyPoints(stockSnapshot)); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveStockKlines(ctx, "kline-"+tradeDate, testStockKlines(tradeDate, location, stock)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SealArchiveRevision(ctx, tradeDate, fmt.Sprintf("revision-%d", index)); err != nil {
			t.Fatal(err)
		}
	}

	features, set, err := store.DailyFeatures(ctx, dates[4], "", graymarket.RankStock)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 || set.RevisionID != "revision-4" || len(set.SourceRevisions) != 5 {
		t.Fatalf("unexpected feature set: features=%+v set=%+v", features, set)
	}
	feature := features[0]
	if feature.PrimaryIndustryCode != "BK-IND" || !feature.CurveAvailable ||
		feature.SelfTurnoverPercentile5 == nil || math.Abs(*feature.SelfTurnoverPercentile5-0.9) > 1e-9 ||
		feature.SelfDarkMoneyPercentile5 == nil || math.Abs(*feature.SelfDarkMoneyPercentile5-0.9) > 1e-9 ||
		feature.ConsecutiveInflowDays != 5 || feature.MoneyAcceleration != 0 ||
		math.Abs(feature.MorningDarkShare-float64(23)/47) > 1e-9 ||
		math.Abs(feature.LateDarkShare-float64(6)/47) > 1e-9 ||
		feature.PriceMoneyDivergence {
		t.Fatalf("unexpected derived stock feature: %+v", feature)
	}

	labels, err := store.FutureReturnLabels(ctx, dates[0], "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	var stockLabel *repository.FutureReturnLabel
	for index := range labels {
		if labels[index].RankType == graymarket.RankStock && labels[index].Code == "000001" {
			stockLabel = &labels[index]
			break
		}
	}
	if stockLabel == nil || stockLabel.TargetRevisionID != "revision-5" ||
		math.Abs(stockLabel.ReturnRate-0.5) > 1e-9 ||
		stockLabel.RelativeIndustryReturn == nil ||
		math.Abs(*stockLabel.RelativeIndustryReturn-0.45) > 1e-9 ||
		math.Abs(stockLabel.MaxFavorableReturn-0.5) > 1e-9 ||
		math.Abs(stockLabel.MaxAdverseReturn-0.1) > 1e-9 {
		t.Fatalf("unexpected five-day stock label: %+v", stockLabel)
	}

	tradeDate := dates[5]
	closeAt, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" 15:00", location)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []graymarket.RankRecord{
		{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: graymarket.RankIndustry,
			Rank: 1, Market: 90, Code: "BK-IND", Name: "industry", QuoteAvailable: true,
			ClosePrice: 110, ChangePct: 0.05, Turnover: 2_000_000, DarkMoney: 200, FetchedAt: closeAt},
		{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: graymarket.RankConcept,
			Rank: 1, Market: 90, Code: "BK-CON", Name: "concept", QuoteAvailable: true,
			ClosePrice: 220, ChangePct: 0.05, Turnover: 3_000_000, DarkMoney: 300, FetchedAt: closeAt},
	} {
		snapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: record.RankType,
			SnapshotAt: closeAt, Records: []graymarket.RankRecord{record}}
		if err := store.SaveBoardArchive(ctx, "rerun-"+string(record.RankType), snapshot, testMoneyPoints(snapshot)); err != nil {
			t.Fatal(err)
		}
	}
	rerunStock := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt,
		RankType: graymarket.RankStock, Rank: 1, Market: 0, Code: "000001", Name: "stock",
		QuoteAvailable: true, ClosePrice: 20, ChangePct: 0.1, Turnover: 2_000_000,
		DarkMoney: 500, MainMoneyInflow: 600, DarkActivity: 0.1, FetchedAt: closeAt}
	rerunSnapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: graymarket.RankStock,
		SnapshotAt: closeAt, Records: []graymarket.RankRecord{rerunStock}}
	if err := store.SaveStockArchive(ctx, "rerun-stock", rerunSnapshot, testMoneyPoints(rerunSnapshot)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStockKlines(ctx, "rerun-kline", testStockKlines(tradeDate, location, rerunStock)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SealArchiveRevision(ctx, tradeDate, "revision-5b"); err != nil {
		t.Fatal(err)
	}
	var labelVersions int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM future_return_label
WHERE signal_revision_id='revision-0' AND horizon=5 AND rank_type='stock' AND code='000001'`).
		Scan(&labelVersions); err != nil {
		t.Fatal(err)
	}
	currentLabels, err := store.FutureReturnLabels(ctx, dates[0], "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if labelVersions != 1 || len(currentLabels) == 0 {
		t.Fatalf("daily target labels were not refreshed in place: versions=%d current=%+v", labelVersions, currentLabels)
	}
	for _, label := range currentLabels {
		if label.RankType == graymarket.RankStock && label.Code == "000001" {
			if label.TargetRevisionID != "revision-5" || math.Abs(label.ReturnRate-1) > 1e-9 {
				t.Fatalf("current target label is incorrect after rerun: %+v", label)
			}
		}
	}
}

func TestMaintenanceAppliesSeparateRetentionAndPreservesDailyRaw(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	at := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	insertRun := func(id, status string, started time.Time) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `INSERT INTO collection_run
(run_id,snapshot_at,snapshot_kind,rank_type,status,requested_date,actual_trade_date,
expected_total,fetched_total,page_count,attempt_count,started_at,finished_at,duration_ms,error_code,error_message)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, started.Format(timestampLayout), "minute_work", "industry",
			status, started.Format("2006-01-02"), "", 0, 0, 0, 1, started.Format(timestampLayout),
			started.Format(timestampLayout), 0, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	insertRun("old-success", "success", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	insertRun("old-failed", "failed", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	insertRun("recent-failed", "failed", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	old := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Format(timestampLayout)
	for _, kind := range []string{"research_5m", "daily_close"} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO raw_response
(run_id,snapshot_at,snapshot_kind,rank_type,page,content_encoding,compression,body,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?)`, "raw-"+kind, old, kind, "industry", 1, "utf-8", "gzip", []byte("x"), old); err != nil {
			t.Fatal(err)
		}
	}
	for index, status := range []string{"success", "failed"} {
		started := old
		if status == "failed" {
			started = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(timestampLayout)
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO relation_sync_run
(run_id,trade_date,status,board_count,relation_count,added_count,removed_count,baseline_built,
started_at,finished_at,duration_ms,error_code,error_message)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("relation-%d", index), "2026-01-01", status,
			0, 0, 0, 0, 0, started, started, 0, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Maintain(ctx, at, 30, 180)
	if err != nil {
		t.Fatal(err)
	}
	if result.SuccessfulRunsDeleted != 1 || result.FailedRunsDeleted != 1 ||
		result.TransientRawDeleted != 1 || result.RelationRunsDeleted != 2 || !result.Optimized {
		t.Fatalf("unexpected maintenance result: %+v", result)
	}
	var runCount, rawCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM collection_run`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM raw_response`).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || rawCount != 1 {
		t.Fatalf("maintenance removed the wrong records: runs=%d raw=%d", runCount, rawCount)
	}
	metrics, err := store.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[repository.RunStatus]int64{}
	for _, item := range metrics.RunCounts {
		counts[item.Status] += item.Value
	}
	if counts[repository.RunSuccess] != 1 || counts[repository.RunFailed] != 2 || len(metrics.LastSuccess) != 1 {
		t.Fatalf("maintenance lost rolled-up metrics: %+v", metrics)
	}
	second, err := store.Maintain(ctx, at.Add(24*time.Hour), 30, 180)
	if err != nil {
		t.Fatal(err)
	}
	if second.Optimized {
		t.Fatal("PRAGMA optimize should not run again within 30 days")
	}
}

func TestDailyClosePaginationSearchAndSort(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	snapshotAt := time.Date(2026, 8, 12, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{RequestedDate: "20260812", TradeDate: "2026-08-12", RankType: graymarket.RankStock, SnapshotAt: snapshotAt, Records: []graymarket.RankRecord{
		{TradeDate: "2026-08-12", SnapshotAt: snapshotAt, RankType: graymarket.RankStock, Rank: 1, Code: "000001", Name: "平安银行",
			OpenPrice: 10.1, HighPrice: 10.8, LowPrice: 9.9, ClosePrice: 10.5, PreviousClose: 10, ChangeValue: 0.5,
			ChangePct: 0.05, Volume: 1234, Turnover: 5678900, TurnoverRate: 0.0123, Amplitude: 0.09, QuoteAvailable: true,
			DarkMoney: 300, DarkActivity: 0.000052827, FetchedAt: snapshotAt},
		{TradeDate: "2026-08-12", SnapshotAt: snapshotAt, RankType: graymarket.RankStock, Rank: 2, Code: "600000", Name: "浦发银行", DarkMoney: 100, QuoteAvailable: true, FetchedAt: snapshotAt},
		{TradeDate: "2026-08-12", SnapshotAt: snapshotAt, RankType: graymarket.RankStock, Rank: 3, Code: "000002", Name: "万科A", DarkMoney: 200, QuoteAvailable: true, FetchedAt: snapshotAt},
	}}
	if err := store.SaveDailyClose(ctx, "daily", snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDailyClose(ctx, "daily-retry", snapshot); err != nil {
		t.Fatal(err)
	}
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		board := graymarket.RankSnapshot{RequestedDate: "20260812", TradeDate: "2026-08-12", RankType: rankType, SnapshotAt: snapshotAt,
			Records: []graymarket.RankRecord{{TradeDate: "2026-08-12", SnapshotAt: snapshotAt, RankType: rankType, Rank: 1, Code: "board-" + string(rankType), Name: string(rankType), FetchedAt: snapshotAt}}}
		if err := store.SaveDailyClose(ctx, "daily-"+string(rankType), board); err != nil {
			t.Fatal(err)
		}
	}
	page, total, err := store.DailyClosePage(ctx, graymarket.RankStock, "2026-08-12", "银行", "dark_money", true, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(page) != 1 || page[0].Code != "000001" {
		t.Fatalf("unexpected first page: total=%d records=%+v", total, page)
	}
	page, total, err = store.DailyClosePage(ctx, graymarket.RankStock, "2026-08-12", "%", "rank", false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(page) != 0 {
		t.Fatalf("LIKE wildcard was not escaped: total=%d records=%+v", total, page)
	}
	selected, err := store.DailyCloseStocks(ctx, "2026-08-12", []string{"000002", "000001", "000001", "999999"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].Code != "000001" || selected[1].Code != "000002" {
		t.Fatalf("unexpected selected daily close stocks: %+v", selected)
	}
	first := selected[0]
	if first.OpenPrice != 10.1 || first.HighPrice != 10.8 || first.LowPrice != 9.9 || first.ClosePrice != 10.5 || first.PreviousClose != 10 || first.Turnover != 5678900 || first.TurnoverRate != 0.0123 || !first.QuoteAvailable {
		t.Fatalf("daily OHLC fields were not persisted: %+v", first)
	}
	all, err := store.DailyCloseRecords(ctx, "2026-08-12")
	if err != nil || len(all) != 5 {
		t.Fatalf("joint daily close did not contain all three rank types exactly once: count=%d err=%v", len(all), err)
	}
	seen := map[graymarket.RankType]bool{}
	for _, record := range all {
		seen[record.RankType] = true
	}
	if !seen[graymarket.RankIndustry] || !seen[graymarket.RankConcept] || !seen[graymarket.RankStock] {
		t.Fatalf("joint daily close types are incomplete: %v", seen)
	}
}

func TestResearchRawPagesUseLongTermKind(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-12", RankType: graymarket.RankIndustry, SnapshotAt: now,
		Records:  []graymarket.RankRecord{{TradeDate: "2026-08-12", SnapshotAt: now, RankType: graymarket.RankIndustry, Rank: 1, Code: "BK001", Name: "test", FetchedAt: now}},
		RawPages: []graymarket.RawPage{{Page: 1, ContentEncoding: "utf-8", Body: []byte(`{"ok":true}`), FetchedAt: now}},
	}
	if err := store.SaveIntraday(ctx, "raw", snapshot, true); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := store.db.QueryRowContext(ctx, "SELECT snapshot_kind FROM raw_response").Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != string(graymarket.SnapshotResearch5m) {
		t.Fatalf("expected research_5m raw response, got %q", kind)
	}
}

func TestDailyCloseBoundaryRawPageUsesDailyCloseKind(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	at := time.Date(2026, 8, 12, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-12", RankType: graymarket.RankIndustry, SnapshotAt: at,
		Records:  []graymarket.RankRecord{{TradeDate: "2026-08-12", SnapshotAt: at, RankType: graymarket.RankIndustry, Rank: 1, Code: "BK001", Name: "close", FetchedAt: at}},
		RawPages: []graymarket.RawPage{{Page: 1, ContentEncoding: "utf-8", Body: []byte(`{"ok":true}`), FetchedAt: at}},
	}
	if err := store.SaveIntraday(ctx, "close-raw", snapshot, true); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := store.db.QueryRowContext(ctx, "SELECT snapshot_kind FROM raw_response").Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != string(graymarket.SnapshotDailyClose) {
		t.Fatalf("expected daily_close raw response, got %q", kind)
	}
}

func TestRankAtPrefersWorkTableAtResearchBoundary(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tradeDate := "2026-08-12"
	snapshotAt := time.Date(2026, 8, 12, 10, 30, 0, 0, location)
	record := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: snapshotAt, RankType: graymarket.RankIndustry, Rank: 1, Market: 90, Code: "BK001", Name: "work", DarkMoney: 42, FetchedAt: snapshotAt}
	if err := store.SaveIntraday(ctx, "work-run", graymarket.RankSnapshot{TradeDate: tradeDate, RankType: graymarket.RankIndustry, SnapshotAt: snapshotAt, Records: []graymarket.RankRecord{record}}, false); err != nil {
		t.Fatal(err)
	}

	result, err := store.RankAt(ctx, graymarket.RankIndustry, tradeDate, snapshotAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Name != "work" || result[0].DarkMoney != 42 {
		t.Fatalf("expected work-table record, got %+v", result)
	}
}

func TestLatestRankUsesOneCompleteWorkSnapshot(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, test := range []struct {
		date string
		at   string
		code string
	}{
		{"2026-08-12", "15:00", "old"},
		{"2026-08-13", "09:31", "new"},
	} {
		at, _ := time.ParseInLocation("2006-01-02 15:04", test.date+" "+test.at, location)
		record := graymarket.RankRecord{TradeDate: test.date, SnapshotAt: at, RankType: graymarket.RankIndustry, Rank: 1, Code: test.code, Name: test.code, FetchedAt: at}
		if err := store.SaveIntraday(ctx, test.code, graymarket.RankSnapshot{TradeDate: test.date, RankType: graymarket.RankIndustry, SnapshotAt: at, Records: []graymarket.RankRecord{record}}, false); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.LatestRank(ctx, graymarket.RankIndustry)
	if err != nil || len(result) != 1 || result[0].Code != "new" {
		t.Fatalf("latest rank mixed dates or selected stale work data: records=%+v err=%v", result, err)
	}
}

func TestFileReaderPoolUsesPerConnectionQueryOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "暗盘", "shadowflow.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.readDB().QueryRow("SELECT 1").Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	if err := store.readDB().QueryRow("CREATE TABLE should_fail(id)").Err; err == nil {
		t.Fatal("reader pool unexpectedly permits writes")
	}
}
