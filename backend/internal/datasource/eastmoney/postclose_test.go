package eastmoney

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func TestFetchMoney5mReturns48RevisedPoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("code") != "600001" || r.URL.Query().Get("market") != "1" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		data := make([]map[string]int64, 0, 48)
		for index, clock := range archiveClocks() {
			data = append(data, map[string]int64{"time": int64(2608140000 + clock), "1": int64(index + 1), "2": 100, "3": int64(index + 101)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errid": 0, "data": data})
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-14", RankType: graymarket.RankStock, SnapshotAt: closeAt,
		Records: []graymarket.RankRecord{{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: "600001", Name: "test"}}}
	points, err := NewClient("unused", server.Client(), 100).WithDarkTradeTickBaseURL(server.URL).FetchMoney5m(context.Background(), snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 48 || points[0].SnapshotAt.Format("15:04") != "09:35" || points[47].SnapshotAt.Format("15:04") != "15:00" {
		t.Fatalf("unexpected money points: count=%d first=%s last=%s", len(points), points[0].SnapshotAt, points[len(points)-1].SnapshotAt)
	}
	if points[47].DarkMoney+points[47].RegularMoney != points[47].MainMoneyInflow {
		t.Fatalf("money identity was not preserved: %+v", points[47])
	}
}

func TestFetchStockKlines5mMapsUnadjusted48Bars(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fqt") != "0" || r.URL.Query().Get("klt") != "5" || r.URL.Query().Get("secid") != "1.600001" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		rows := make([]string, 0, 48)
		for _, clock := range archiveClocks() {
			rows = append(rows, fmt.Sprintf("2026-08-14 %02d:%02d,10.10,10.20,10.30,10.00,1234,567890.00,3.00,1.50,0.15,2.50", clock/100, clock%100))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"klines": rows}})
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-14", RankType: graymarket.RankStock, SnapshotAt: closeAt,
		Records: []graymarket.RankRecord{{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: "600001"}}}
	points, err := NewClient("unused", server.Client(), 100).WithStockKlineBaseURL(server.URL).FetchStockKlines5m(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 48 || points[47].SnapshotAt.Format("15:04") != "15:00" {
		t.Fatalf("unexpected kline points: %+v", points)
	}
	first := points[0]
	if first.OpenPrice != 10.10 || first.ClosePrice != 10.20 || first.HighPrice != 10.30 || first.LowPrice != 10 || first.Volume != 1234 || first.Turnover != 567890 || first.Amplitude != 0.03 || first.ChangePct != 0.015 || first.ChangeValue != 0.15 || first.TurnoverRate != 0.025 {
		t.Fatalf("unexpected mapped kline: %+v", first)
	}
}

func TestFetchStockKlines5mReturnsCompletedStocksWithBatchError(t *testing.T) {
	var failedRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("secid") == "0.000001" {
			failedRequests.Add(1)
			_, _ = w.Write([]byte("{"))
			return
		}
		rows := make([]string, 0, 48)
		for _, clock := range archiveClocks() {
			rows = append(rows, fmt.Sprintf("2026-08-14 %02d:%02d,10.10,10.20,10.30,10.00,1234,567890.00,3.00,1.50,0.15,2.50", clock/100, clock%100))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"klines": rows}})
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-14", RankType: graymarket.RankStock, SnapshotAt: closeAt,
		Records: []graymarket.RankRecord{
			{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 0, Code: "000001"},
			{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: "600001"},
		}}
	client := NewClient("unused", server.Client(), 100).WithStockKlineBaseURL(server.URL).
		WithStockTrendBaseURLs([]string{server.URL + "/api/qt/stock/trends2/get"})
	client.stockKlineRetryGap = 0
	points, err := client.FetchStockKlines5m(context.Background(), snapshot)
	if err == nil || !strings.Contains(err.Error(), "000001") || !strings.Contains(err.Error(), "completed 1/2 stocks") {
		t.Fatalf("unexpected partial batch error: %v", err)
	}
	if len(points) != 48 || points[0].Code != "600001" {
		t.Fatalf("completed stock was discarded: points=%d first=%+v", len(points), points[0])
	}
	if failedRequests.Load() < 4 {
		t.Fatalf("failed stock should retry the fallback endpoint, got %d requests", failedRequests.Load())
	}
}

func TestFetchStockKlines5mAggregatesOneMinuteFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/trends2/") {
			rows := make([]string, 0, 241)
			cumulativeVolume := 0
			cumulativeTurnover := 0
			for minute := 9*60 + 30; minute <= 11*60+30; minute++ {
				cumulativeVolume++
				cumulativeTurnover += 10
				rows = append(rows, fmt.Sprintf("2026-08-14 %02d:%02d,10.00,10.00,10.00,10.00,1,10.00,10.000,1,0.00,%d,%d.00", minute/60, minute%60, cumulativeVolume, cumulativeTurnover))
			}
			for minute := 13*60 + 1; minute <= 15*60; minute++ {
				cumulativeVolume++
				cumulativeTurnover += 10
				rows = append(rows, fmt.Sprintf("2026-08-14 %02d:%02d,10.00,10.00,10.00,10.00,1,10.00,10.000,1,0.00,%d,%d.00", minute/60, minute%60, cumulativeVolume, cumulativeTurnover))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"trends": rows}})
			return
		}
		_, _ = w.Write([]byte("{"))
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-14", RankType: graymarket.RankStock, SnapshotAt: closeAt,
		Records: []graymarket.RankRecord{{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock,
			Market: 1, Code: "600001", OpenPrice: 10, HighPrice: 10, LowPrice: 10, ClosePrice: 10, PreviousClose: 10,
			Volume: 241, Turnover: 2410, TurnoverRate: 0.0241, QuoteAvailable: true}}}
	client := NewClient("unused", server.Client(), 100).WithStockKlineBaseURL(server.URL).
		WithStockTrendBaseURLs([]string{server.URL + "/api/qt/stock/trends2/get"})
	client.stockKlineRetryGap = 0
	points, err := client.FetchStockKlines5m(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 48 || points[0].SnapshotAt.Format("15:04") != "09:35" || points[47].SnapshotAt.Format("15:04") != "15:00" {
		t.Fatalf("unexpected fallback bars: count=%d first=%s last=%s", len(points), points[0].SnapshotAt, points[len(points)-1].SnapshotAt)
	}
	if points[0].Volume != 6 || points[1].Volume != 5 || points[0].Turnover != 60 || math.Abs(points[0].TurnoverRate-0.0006) > 0.0000001 {
		t.Fatalf("unexpected first aggregated bars: first=%+v second=%+v", points[0], points[1])
	}
}

func archiveClocks() []int {
	result := make([]int, 0, 48)
	for minute := 9*60 + 35; minute <= 11*60+30; minute += 5 {
		result = append(result, minute/60*100+minute%60)
	}
	for minute := 13*60 + 5; minute <= 15*60; minute += 5 {
		result = append(result, minute/60*100+minute%60)
	}
	return result
}
