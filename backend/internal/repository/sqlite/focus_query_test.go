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
