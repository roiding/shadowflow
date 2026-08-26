package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func TestDailyCloseTradeDatesRequireCompleteConceptQuotesAndUseEligibleStocks(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, date := range []string{"2026-08-11", "2026-08-12", "2026-08-13", "2026-08-14"} {
		at, _ := time.Parse(time.RFC3339, date+"T15:00:00+08:00")
		available := date != "2026-08-14"
		for _, rankType := range []graymarket.RankType{graymarket.RankConcept, graymarket.RankStock} {
			record := graymarket.RankRecord{TradeDate: date, SnapshotAt: at, RankType: rankType, Rank: 1,
				Market: 1, Code: "600001", Name: "测试", QuoteAvailable: true, FetchedAt: at}
			if rankType == graymarket.RankConcept {
				record.Market, record.Code, record.QuoteAvailable = 90, "BK001", available
			}
			if err := store.SaveDailyClose(ctx, date+string(rankType), graymarket.RankSnapshot{
				RequestedDate: date, TradeDate: date, RankType: rankType, SnapshotAt: at, Records: []graymarket.RankRecord{record},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	dates, err := store.DailyCloseTradeDates(ctx, "2026-08-14", 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-08-11", "2026-08-12", "2026-08-13"}
	if len(dates) != len(want) {
		t.Fatalf("dates=%v want=%v", dates, want)
	}
	for index := range want {
		if dates[index] != want[index] {
			t.Fatalf("dates=%v want=%v", dates, want)
		}
	}
}

func TestHasBoardArchiveRejectsPartialIncrementalCurve(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	date := "2026-08-25"
	closeAt := "2026-08-25T15:00:00+08:00"
	// Two daily-close identities, but only one identity has all 48 money
	// points. Timestamp-only completion checks incorrectly accept this state.
	for code, rank := range map[string]int{"BK001": 1, "BK002": 2} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO rank_snapshot
(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"run", closeAt, date, date, "daily_close", "concept", rank, 90, code, code, "", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, "", "", 101, 6, 1, closeAt); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 48; index++ {
		clock := expectedResearchTimes()[index]
		at := date + "T" + clock + ":00+08:00"
		if _, err := store.db.ExecContext(ctx, `INSERT INTO board_money_5m
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,dark_money,regular_money,main_money_inflow,money_available,source_time,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "run", at, date, "concept", 1, 90, "BK001", "概念", int64(index), 0, int64(index), 1, 0, closeAt); err != nil {
			t.Fatal(err)
		}
	}
	ok, err := store.HasBoardArchive(ctx, date, graymarket.RankConcept)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("partial board archive was reported complete")
	}
}

func TestSnapshotMinutesNormalizesUTCToShanghai(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO board_money_5m
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,dark_money,regular_money,main_money_inflow,money_available,source_time,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "run", "2026-08-25T07:00:00Z", "2026-08-25", "concept", 1, 90, "BK001", "概念", 1, 0, 1, 1, 0, "2026-08-25T07:00:00Z"); err != nil {
		t.Fatal(err)
	}
	minutes, err := snapshotMinutes(ctx, store.db, "board_money_5m", "2026-08-25", graymarket.RankConcept, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(minutes) != 1 || minutes[0] != "15:00" {
		t.Fatalf("UTC timestamp was not normalized to Shanghai: %v", minutes)
	}
}
