package sqlite

import (
	"context"
	"database/sql"
	"fmt"
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
		for _, expected := range []string{"open_price", "high_price", "low_price", "close_price", "previous_close", "change_value", "volume", "turnover", "turnover_rate", "amplitude", "quote_available"} {
			if !columns[expected] {
				t.Fatalf("%s migration did not add %s: %v", table, expected, columns)
			}
		}
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
	if len(summaries) != 2 || summaries[0].CollectedMinutes != 240 || summaries[0].CollectedResearch != 47 || summaries[0].CollectedDailyClose != 1 {
		t.Fatalf("unexpected summaries: %+v", summaries)
	}
	from := time.Date(2026, 8, 12, 0, 0, 0, 0, location)
	to := from.Add(24*time.Hour - time.Nanosecond)
	series, err := store.ResearchSeries(ctx, graymarket.RankIndustry, "industry-code", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 47 || series[0].SnapshotAt.Format("15:04") != "09:35" || series[46].SnapshotAt.Format("15:04") != "14:55" {
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
	if err != nil || len(work) != 48 || work[len(work)-1].SnapshotAt.Format("15:04") != "15:00" {
		t.Fatalf("intraday query did not fall back to research data: count=%d err=%v", len(work), err)
	}
	series, err = store.ResearchSeries(ctx, graymarket.RankIndustry, "industry-code", from, to)
	if err != nil || len(series) != 47 {
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
	if err != nil || len(quality) != 2 || quality[0].ExpectedResearch != 47 || quality[0].CollectedResearch != 47 || quality[0].ExpectedDailyClose != 1 || quality[0].CollectedDailyClose != 1 || len(quality[0].MissingResearch) != 0 || len(quality[0].MissingDailyClose) != 0 {
		t.Fatalf("quality did not separate research and close points: quality=%+v err=%v", quality, err)
	}
	second, err := store.CompactResearch(ctx, tradeDate)
	if err != nil || len(second) != 2 || second[0].CollectedResearch != 47 || second[0].CollectedDailyClose != 1 {
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
	if err != nil || len(quality) != 1 || quality[0].ExpectedResearch != 47 || quality[0].CollectedDailyClose != 1 {
		t.Fatalf("legacy quality was not migrated: quality=%+v err=%v", quality, err)
	}
}

func TestOpenCleansIntradayForFullyCompactedDates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC().Format(timestampLayout)
	for _, rankType := range []string{"industry", "concept"} {
		_, err = store.db.ExecContext(ctx, `INSERT INTO research_quality
(trade_date,rank_type,expected_minutes,collected_minutes,expected_research,collected_research,missing_minutes_json,missing_research_json,compacted_at)
VALUES (?,?,?,?,?,?,?,?,?)`, "2026-08-12", rankType, 240, 240, 47, 47, "[]", "[]", now)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.db.ExecContext(ctx, `INSERT INTO rank_snapshot
(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "close-"+rankType, "2026-08-12T15:00:00+08:00", "2026-08-12", "2026-08-12", "daily_close", rankType, 1, 90, "close-"+rankType, rankType, "", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, "", "", 101, 6, 1, now)
		if err != nil {
			t.Fatal(err)
		}
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
	if count != 0 {
		t.Fatalf("expected stale intraday rows to be cleaned, got %d", count)
	}
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
		{TradeDate: "2026-08-12", SnapshotAt: snapshotAt, RankType: graymarket.RankStock, Rank: 2, Code: "600000", Name: "浦发银行", DarkMoney: 100, FetchedAt: snapshotAt},
		{TradeDate: "2026-08-12", SnapshotAt: snapshotAt, RankType: graymarket.RankStock, Rank: 3, Code: "000002", Name: "万科A", DarkMoney: 200, FetchedAt: snapshotAt},
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
