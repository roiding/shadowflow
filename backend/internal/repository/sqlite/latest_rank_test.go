package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func TestLatestRankSelectsNewestWholeSource(t *testing.T) {
	for _, tc := range []struct {
		name, work, close, want string
	}{
		{"empty", "", "", ""},
		{"work only", "2026-08-10T09:31:00+08:00", "", "work"},
		{"close only", "", "2026-08-11T15:00:00+08:00", "close"},
		{"newer archive", "2026-08-10T15:00:00+08:00", "2026-08-11T15:00:00+08:00", "close"},
		{"newer work", "2026-08-12T09:31:00+08:00", "2026-08-11T15:00:00+08:00", "work"},
		{"same day close", "2026-08-11T14:59:00+08:00", "2026-08-11T15:00:00+08:00", "close"},
		{"equal time prefers close", "2026-08-11T15:00:00+08:00", "2026-08-11T15:00:00+08:00", "close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			save := func(source, clock string, rankType graymarket.RankType) {
				if clock == "" {
					return
				}
				at, err := time.Parse(time.RFC3339, clock)
				if err != nil {
					t.Fatal(err)
				}
				snapshot := graymarket.RankSnapshot{TradeDate: clock[:10], RankType: rankType, SnapshotAt: at}
				for _, rank := range []int64{2, 1} {
					snapshot.Records = append(snapshot.Records, graymarket.RankRecord{
						TradeDate: snapshot.TradeDate, SnapshotAt: at, RankType: rankType,
						Rank: rank, Code: fmt.Sprintf("%s-%d", source, rank), Name: source, FetchedAt: at, QuoteAvailable: true,
					})
				}
				if source == "work" {
					err = store.SaveIntraday(ctx, source, snapshot, false)
				} else {
					err = store.SaveDailyClose(ctx, source, snapshot)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			save("work", tc.work, graymarket.RankIndustry)
			save("close", tc.close, graymarket.RankIndustry)
			// Other universes and research snapshots cannot change the selection.
			save("work", "2026-08-20T15:00:00+08:00", graymarket.RankConcept)
			save("close", "2026-08-20T15:00:00+08:00", graymarket.RankStock)
			save("research", "2026-08-20T15:00:00+08:00", graymarket.RankIndustry)
			if _, err := store.db.ExecContext(ctx, `UPDATE rank_snapshot SET snapshot_kind='research_5m' WHERE run_id='research'`); err != nil {
				t.Fatal(err)
			}
			records, err := store.LatestRank(ctx, graymarket.RankIndustry)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(records) != 0 {
					t.Fatalf("expected empty result, got %+v", records)
				}
				return
			}
			if len(records) != 2 {
				t.Fatalf("expected one whole two-row snapshot, got %+v", records)
			}
			for i, record := range records {
				if record.Name != tc.want || record.Rank != int64(i+1) || !record.SnapshotAt.Equal(records[0].SnapshotAt) {
					t.Fatalf("mixed or incorrectly ordered snapshot: %+v", records)
				}
			}
		})
	}
}
