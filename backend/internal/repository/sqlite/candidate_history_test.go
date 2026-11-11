package sqlite

import (
	"context"
	"math"
	"testing"
)

func TestReadCandidateHistoryMetricsUsesSameMinuteAndExcludesFutureDates(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for index, date := range []string{"2026-11-03", "2026-11-04", "2026-11-05", "2026-11-06", "2026-11-09"} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO stock_research_5m
(trade_date,minute_index,market,code,money_rank,dark_money,regular_money,main_money_inflow,money_available,
turnover,turnover_rate_ppm,kline_available)
VALUES (?,13,0,'001389',0,?,0,?,1,1000000,?,1)`,
			date, int64(100+index*10), int64(300+index*10), int64(30000+index*1000)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO stock_research_5m
(trade_date,minute_index,market,code,money_rank,dark_money,regular_money,main_money_inflow,money_available,
turnover,turnover_rate_ppm,kline_available)
VALUES ('2026-11-11',13,0,'001389',0,999,0,999,1,1000000,999000,1)`); err != nil {
		t.Fatal(err)
	}
	result, err := store.ReadCandidateHistoryMetrics(
		ctx, "2026-11-10", []string{"001389", "000001"}, 20, 13,
	)
	if err != nil {
		t.Fatal(err)
	}
	history := result["001389"]
	if len(history.TurnoverRates) != 5 || len(history.MainFlowPcts) != 5 ||
		len(history.DarkFlowPcts) != 5 {
		t.Fatalf("unexpected history sample count: %+v", history)
	}
	if math.Abs(history.TurnoverRates[0]-3) > 1e-9 ||
		math.Abs(history.MainFlowPcts[0]-0.03) > 1e-9 ||
		math.Abs(history.DarkFlowPcts[0]-0.01) > 1e-9 {
		t.Fatalf("unexpected same-minute metrics: %+v", history)
	}
	if len(result["000001"].TurnoverRates) != 0 {
		t.Fatalf("missing code should keep an empty result: %+v", result["000001"])
	}
}
