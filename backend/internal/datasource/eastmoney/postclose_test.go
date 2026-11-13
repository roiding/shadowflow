package eastmoney

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestFetchStockKlines5mReturnsCompletedStocksWithBatchError(testingT *testing.T) {
	var failedRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(httpWriter http.ResponseWriter, httpRequest *http.Request) {
		if strings.Contains(httpRequest.URL.Path, "trends") {
			if httpRequest.URL.Query().Get("secid") == "0.000001" {
				_, _ = httpWriter.Write([]byte("{"))
				return
			}
			testingT.Fatalf("healthy stock should not reach the one-minute backup: %s", httpRequest.URL.Path)
		}
		if !strings.Contains(httpRequest.URL.Path, "/kline/get") {
			testingT.Fatalf("unexpected endpoint was requested: %s", httpRequest.URL.Path)
		}
		if httpRequest.URL.Query().Get("secid") == "0.000001" {
			failedRequests.Add(1)
			_, _ = httpWriter.Write([]byte("{"))
			return
		}
		location := time.FixedZone("Asia/Shanghai", 8*60*60)
		rows := uniformHistoricalKlineRows("2026-08-14", location)
		_ = json.NewEncoder(httpWriter).Encode(map[string]any{"rc": 0, "data": map[string]any{"klines": rows}})
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-08-14", RankType: graymarket.RankStock, SnapshotAt: closeAt,
		Records: []graymarket.RankRecord{
			{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 0, Code: "000001"},
			{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: "600001", OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10, PreviousClose: 10, Volume: 241, Turnover: 24100, TurnoverRate: 0.01, QuoteAvailable: true},
		}}
	client := NewClient("unused", server.Client(), 100).
		WithStockTrendBaseURLs([]string{server.URL + "/api/qt/stock/trends2/get"}).
		WithStockKlineBaseURL(server.URL + "/api/qt/stock/kline/get")
	client.stockKlineRetryGap = 0
	points, err := client.FetchStockKlines5m(context.Background(), snapshot)
	if err == nil || !strings.Contains(err.Error(), "000001") || !strings.Contains(err.Error(), "completed 1/2 stocks") {
		testingT.Fatalf("unexpected partial batch error: %v", err)
	}
	if len(points) != 48 || points[0].Code != "600001" {
		testingT.Fatalf("completed stock was discarded: points=%d first=%+v", len(points), points[0])
	}
	if points[0].Source != graymarket.KlineSourceFiveMinute {
		testingT.Fatalf("expected five-minute source, got %s", points[0].Source)
	}
	if failedRequests.Load() < 4 {
		testingT.Fatalf("failed stock should retry the kline endpoint, got %d requests", failedRequests.Load())
	}
}

func TestStockKlineFromTrendURLAggregatesOneMinuteData(testingT *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(httpWriter http.ResponseWriter, httpRequest *http.Request) {
		if strings.Contains(httpRequest.URL.Path, "/trends2/") {
			rows := make([]string, 0, 241)
			cumulativeVolume := 0
			cumulativeTurnover := 0
			for minute := 9*60 + 30; minute <= 11*60+30; minute++ {
				if minute == 9*60+30 {
					rows = append(rows, "2026-08-14 09:30,9.00,9.00,9.00,9.00,0,0.00,9.000,0,0.00,0,0.00")
					continue
				}
				cumulativeVolume++
				cumulativeTurnover += 250000
				rows = append(rows, fmt.Sprintf("2026-08-14 %02d:%02d,10.00,10.00,10.00,10.00,1,250000.00,10.000,1,0.00,%d,%d.00", minute/60, minute%60, cumulativeVolume, cumulativeTurnover))
			}
			for minute := 13*60 + 1; minute <= 15*60; minute++ {
				cumulativeVolume++
				cumulativeTurnover += 250000
				rows = append(rows, fmt.Sprintf("2026-08-14 %02d:%02d,10.00,10.00,10.00,10.00,1,250000.00,10.000,1,0.00,%d,%d.00", minute/60, minute%60, cumulativeVolume, cumulativeTurnover))
			}
			_ = json.NewEncoder(httpWriter).Encode(map[string]any{"rc": 0, "data": map[string]any{"trends": rows}})
			return
		}
		_, _ = httpWriter.Write([]byte("{"))
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	record := graymarket.RankRecord{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock,
		Market: 1, Code: "600001", OpenPrice: 10, HighPrice: 10, LowPrice: 10, ClosePrice: 10, PreviousClose: 9,
		Volume: 241, Turnover: 60000120, TurnoverRate: 0.0241, QuoteAvailable: true}
	client := NewClient("unused", server.Client(), 100)
	client.stockKlineRetryGap = 0
	points, err := client.fetchStockKlineFromTrendURL(context.Background(), server.URL+"/api/qt/stock/trends2/get", "2026-08-14", record)
	if err != nil {
		testingT.Fatal(err)
	}
	if len(points) != 48 || points[0].SnapshotAt.Format("15:04") != "09:35" || points[47].SnapshotAt.Format("15:04") != "15:00" {
		testingT.Fatalf("unexpected aggregated bars: count=%d first=%s last=%s", len(points), points[0].SnapshotAt, points[len(points)-1].SnapshotAt)
	}
	if points[0].Source != graymarket.KlineSourceTrend241 {
		testingT.Fatalf("one-minute aggregation source was not recorded: %+v", points[0])
	}
	if points[0].OpenPrice != 10 || points[0].LowPrice != 10 || points[0].Volume != 5 || points[1].Volume != 5 || points[0].Turnover != 1250000 || math.Abs(points[0].TurnoverRate-0.0005) > 0.0000001 {
		testingT.Fatalf("unexpected first aggregated bars: first=%+v second=%+v", points[0], points[1])
	}
	var totalTurnover int64
	var totalVolume int64
	for _, point := range points {
		totalVolume += point.Volume
		totalTurnover += point.Turnover
	}
	if totalVolume != 240 || totalTurnover != 60000000 {
		testingT.Fatalf("minute totals were rewritten to the post-close daily totals: volume=%d turnover=%d", totalVolume, totalTurnover)
	}
}

func TestStockKlineFromTrendURLUsesZeroVolumeOpeningAuctionPrice(testingT *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(httpWriter http.ResponseWriter, httpRequest *http.Request) {
		if strings.Contains(httpRequest.URL.Path, "/trends2/") {
			rows := make([]string, 0, 241)
			cumulativeVolume := 0
			cumulativeTurnover := 0
			for minute := 9*60 + 30; minute <= 11*60+30; minute++ {
				if minute == 9*60+30 {
					rows = append(rows, "2026-08-21 09:30,12.63,12.63,12.63,12.63,0,63.00,12.630,0,0.00,0,63.00")
					continue
				}
				volume := 1
				openPrice, closePrice, highPrice, lowPrice := 12.70, 12.70, 12.70, 12.70
				if minute == 9*60+31 {
					volume = 7
					openPrice, closePrice, highPrice, lowPrice = 12.84, 12.64, 12.84, 12.64
				}
				cumulativeVolume += volume
				cumulativeTurnover += volume * 1270
				rows = append(rows, fmt.Sprintf("2026-08-21 %02d:%02d,%.2f,%.2f,%.2f,%.2f,%d,%d.00,12.700,0,0.00,%d,%d.00",
					minute/60, minute%60, openPrice, closePrice, highPrice, lowPrice, volume, volume*1270, cumulativeVolume, cumulativeTurnover))
			}
			for minute := 13*60 + 1; minute <= 15*60; minute++ {
				cumulativeVolume++
				cumulativeTurnover += 1270
				rows = append(rows, fmt.Sprintf("2026-08-21 %02d:%02d,12.70,12.70,12.70,12.70,1,1270.00,12.700,0,0.00,%d,%d.00",
					minute/60, minute%60, cumulativeVolume, cumulativeTurnover))
			}
			_ = json.NewEncoder(httpWriter).Encode(map[string]any{"rc": 0, "data": map[string]any{"trends": rows}})
			return
		}
		_, _ = httpWriter.Write([]byte("{"))
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 21, 15, 0, 0, 0, location)
	record := graymarket.RankRecord{TradeDate: "2026-08-21", SnapshotAt: closeAt, RankType: graymarket.RankStock,
		Market: 0, Code: "920221", OpenPrice: 12.63, HighPrice: 12.84, LowPrice: 12.63, ClosePrice: 12.70, PreviousClose: 12.74,
		Volume: 246, Turnover: 312420, TurnoverRate: 0.0085, QuoteAvailable: true}
	client := NewClient("unused", server.Client(), 100)
	client.stockKlineRetryGap = 0
	points, err := client.fetchStockKlineFromTrendURL(context.Background(), server.URL+"/api/qt/stock/trends2/get", "2026-08-21", record)
	if err != nil {
		testingT.Fatal(err)
	}
	if len(points) != 48 || points[0].OpenPrice != 12.63 || points[0].HighPrice != 12.84 || points[0].LowPrice != 12.63 || points[0].Volume != 11 {
		testingT.Fatalf("unexpected zero-volume opening-auction handling: count=%d first=%+v", len(points), points[0])
	}
}

func TestStockKlineFromTrendURLUsesDailyCloseForZeroVolumeClosingAuction(testingT *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(httpWriter http.ResponseWriter, httpRequest *http.Request) {
		if strings.Contains(httpRequest.URL.Path, "/trends2/") {
			rows := make([]string, 0, 241)
			cumulativeVolume := 0
			cumulativeTurnover := 0
			for minute := 9*60 + 30; minute <= 11*60+30; minute++ {
				if minute == 9*60+30 {
					cumulativeVolume++
					cumulativeTurnover += 393
					rows = append(rows, fmt.Sprintf("2026-08-14 09:30,3.93,3.93,3.93,3.93,1,393.00,3.930,1,0.00,%d,%d.00", cumulativeVolume, cumulativeTurnover))
					continue
				}
				cumulativeVolume++
				cumulativeTurnover += 100
				rows = append(rows, fmt.Sprintf("2026-08-14 %02d:%02d,4.00,4.00,4.00,4.00,1,100.00,4.000,1,0.00,%d,%d.00", minute/60, minute%60, cumulativeVolume, cumulativeTurnover))
			}
			for minute := 13*60 + 1; minute <= 15*60; minute++ {
				volume, turnover := 1, 100
				if minute >= 14*60+57 {
					volume, turnover = 0, 0
				}
				cumulativeVolume += volume
				cumulativeTurnover += turnover
				closePrice := 4.00
				highPrice := 4.00
				if minute == 13*60+55 {
					highPrice = 4.01
				}
				if minute == 15*60 {
					closePrice = 4.10
				}
				rows = append(rows, fmt.Sprintf("2026-08-14 %02d:%02d,4.00,%.2f,%.2f,4.00,%d,%d.00,4.000,1,0.00,%d,%d.00", minute/60, minute%60, closePrice, highPrice, volume, turnover, cumulativeVolume, cumulativeTurnover))
			}
			_ = json.NewEncoder(httpWriter).Encode(map[string]any{"rc": 0, "data": map[string]any{"trends": rows}})
			return
		}
		_, _ = httpWriter.Write([]byte("{"))
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	record := graymarket.RankRecord{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock,
		Market: 1, Code: "600543", OpenPrice: 3.93, HighPrice: 4.10, LowPrice: 3.93, ClosePrice: 4.10, PreviousClose: 3.94,
		Volume: 240, Turnover: 24000, TurnoverRate: 0.01, QuoteAvailable: true}
	client := NewClient("unused", server.Client(), 100)
	client.stockKlineRetryGap = 0
	points, err := client.fetchStockKlineFromTrendURL(context.Background(), server.URL+"/api/qt/stock/trends2/get", "2026-08-14", record)
	if err != nil {
		testingT.Fatal(err)
	}
	if len(points) != 48 || points[47].ClosePrice != 4.10 || math.Abs(points[47].ChangeValue-0.10) > 0.0001 {
		testingT.Fatalf("unexpected zero-volume closing-auction handling: count=%d last=%+v", len(points), points[len(points)-1])
	}
}

func TestFetchStockKlines5mRequestsDirectPush2HisParametersAndNeverRequestsTrends(testingT *testing.T) {
	var trendsRequestCount atomic.Int32
	var klineRequestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(httpWriter http.ResponseWriter, httpRequest *http.Request) {
		if strings.Contains(httpRequest.URL.Path, "trends") {
			trendsRequestCount.Add(1)
			testingT.Fatalf("trends endpoint should not be requested: %s", httpRequest.URL.Path)
		}
		if !strings.Contains(httpRequest.URL.Path, "/api/qt/stock/kline/get") {
			testingT.Fatalf("unexpected endpoint: %s", httpRequest.URL.Path)
		}
		query := httpRequest.URL.Query()
		if query.Get("secid") != "1.600001" {
			testingT.Fatalf("unexpected secid parameter: %s", query.Get("secid"))
		}
		if query.Get("klt") != "5" {
			testingT.Fatalf("unexpected klt parameter: %s", query.Get("klt"))
		}
		if query.Get("fqt") != "0" {
			testingT.Fatalf("unexpected fqt parameter: %s", query.Get("fqt"))
		}
		if query.Get("beg") != "20260814" || query.Get("end") != "20260814" {
			testingT.Fatalf("unexpected beg/end parameter: beg=%s end=%s", query.Get("beg"), query.Get("end"))
		}
		if query.Get("fields1") != "f1,f2,f3,f4,f5,f6" {
			testingT.Fatalf("unexpected fields1 parameter: %s", query.Get("fields1"))
		}
		if query.Get("fields2") != "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61" {
			testingT.Fatalf("unexpected fields2 parameter: %s", query.Get("fields2"))
		}
		klineRequestCount.Add(1)
		location := time.FixedZone("Asia/Shanghai", 8*60*60)
		rows := uniformHistoricalKlineRows("2026-08-14", location)
		_ = json.NewEncoder(httpWriter).Encode(map[string]any{"rc": 0, "data": map[string]any{"klines": rows}})
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{
		TradeDate:  "2026-08-14",
		RankType:   graymarket.RankStock,
		SnapshotAt: closeAt,
		Records: []graymarket.RankRecord{
			{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: "600001", OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10, PreviousClose: 10, Volume: 241, Turnover: 24100, TurnoverRate: 0.01, QuoteAvailable: true},
		},
	}
	client := NewClient("unused", server.Client(), 100).
		WithStockTrendBaseURLs([]string{server.URL + "/api/qt/stock/trends2/get"}).
		WithStockKlineBaseURL(server.URL + "/api/qt/stock/kline/get")
	client.stockKlineRetryGap = 0
	points, err := client.FetchStockKlines5m(context.Background(), snapshot)
	if err != nil {
		testingT.Fatal(err)
	}
	if trendsRequestCount.Load() != 0 {
		testingT.Fatalf("expected 0 trends requests, got %d", trendsRequestCount.Load())
	}
	if klineRequestCount.Load() != 1 {
		testingT.Fatalf("expected 1 kline request, got %d", klineRequestCount.Load())
	}
	if len(points) != 48 {
		testingT.Fatalf("expected 48 points, got %d", len(points))
	}
	if points[0].Source != graymarket.KlineSourceFiveMinute {
		testingT.Fatalf("expected KlineSourceFiveMinute source, got %s", points[0].Source)
	}
	if points[0].SnapshotAt.Format("15:04") != "09:35" || points[47].SnapshotAt.Format("15:04") != "15:00" {
		testingT.Fatalf("unexpected point times: first=%s last=%s", points[0].SnapshotAt, points[47].SnapshotAt)
	}
}

// The archive no longer aggregates 241 one-minute quote snapshots into
// five-minute bars: that path drifted on auction rows with no trades. A
// failing native five-minute series must surface the error instead, so the
// marketdata router can fail over to another provider.
func TestFetchStockKlines5mDoesNotFallBackToOneMinuteAggregation(testingT *testing.T) {
	var trendsRequestCount atomic.Int32
	var klineRequestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(httpWriter http.ResponseWriter, httpRequest *http.Request) {
		if strings.Contains(httpRequest.URL.Path, "trends") {
			trendsRequestCount.Add(1)
			_ = json.NewEncoder(httpWriter).Encode(map[string]any{"rc": 0, "data": map[string]any{"trends": uniformTrendRows("2026-08-14")}})
			return
		}
		if strings.Contains(httpRequest.URL.Path, "/api/qt/stock/kline/get") {
			klineRequestCount.Add(1)
			httpWriter.WriteHeader(http.StatusInternalServerError)
			_, _ = httpWriter.Write([]byte(`{"rc":500,"message":"internal error"}`))
			return
		}
		testingT.Fatalf("unexpected request: %s", httpRequest.URL.Path)
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{
		TradeDate:  "2026-08-14",
		RankType:   graymarket.RankStock,
		SnapshotAt: closeAt,
		Records: []graymarket.RankRecord{
			{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: "600001", OpenPrice: 10, HighPrice: 10, LowPrice: 10, ClosePrice: 10, PreviousClose: 10, Volume: 241, Turnover: 24100, TurnoverRate: 0.01, QuoteAvailable: true},
		},
	}
	client := NewClient("unused", server.Client(), 100).
		WithStockTrendBaseURLs([]string{server.URL + "/api/qt/stock/trends2/get"}).
		WithStockKlineBaseURL(server.URL + "/api/qt/stock/kline/get")
	client.stockKlineRetryGap = 0
	points, err := client.FetchStockKlines5m(context.Background(), snapshot)
	if err == nil {
		testingT.Fatal("a failing five-minute series must not be replaced by one-minute aggregation")
	}
	if len(points) != 0 {
		testingT.Fatalf("aggregated fallback points were returned: %d", len(points))
	}
	if klineRequestCount.Load() < 4 {
		testingT.Fatalf("expected at least 4 five-minute attempts, got %d", klineRequestCount.Load())
	}
	if trendsRequestCount.Load() != 0 {
		testingT.Fatalf("one-minute aggregation must not run, got %d requests", trendsRequestCount.Load())
	}
}

func TestFetchStockKlines5mIncrementalDirect(testingT *testing.T) {
	var activeRequests atomic.Int32
	var maxConcurrentRequests atomic.Int32
	var trendsRequestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(httpWriter http.ResponseWriter, httpRequest *http.Request) {
		if strings.Contains(httpRequest.URL.Path, "trends") {
			trendsRequestCount.Add(1)
			testingT.Fatalf("unexpected trends request: %s", httpRequest.URL.Path)
		}
		currentActive := activeRequests.Add(1)
		for {
			previousMax := maxConcurrentRequests.Load()
			if currentActive <= previousMax || maxConcurrentRequests.CompareAndSwap(previousMax, currentActive) {
				break
			}
		}
		defer activeRequests.Add(-1)
		location := time.FixedZone("Asia/Shanghai", 8*60*60)
		rows := uniformHistoricalKlineRows("2026-08-14", location)
		_ = json.NewEncoder(httpWriter).Encode(map[string]any{"rc": 0, "data": map[string]any{"klines": rows}})
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	records := make([]graymarket.RankRecord, 0, 6)
	for index := 0; index < 6; index++ {
		code := fmt.Sprintf("60000%d", index+1)
		records = append(records, graymarket.RankRecord{
			TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: code,
			OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10, PreviousClose: 10, Volume: 241, Turnover: 24100, TurnoverRate: 0.01, QuoteAvailable: true,
		})
	}
	snapshot := graymarket.RankSnapshot{
		TradeDate:  "2026-08-14",
		RankType:   graymarket.RankStock,
		SnapshotAt: closeAt,
		Records:    records,
	}
	client := NewClient("unused", server.Client(), 100).
		WithStockTrendBaseURLs([]string{server.URL + "/api/qt/stock/trends2/get"}).
		WithStockKlineBaseURL(server.URL + "/api/qt/stock/kline/get")
	client.stockKlineRetryGap = 0
	var persistedBatches [][]graymarket.StockKlinePoint
	var persistMu sync.Mutex
	completedCount, err := client.FetchStockKlines5mIncremental(context.Background(), snapshot, func(batch []graymarket.StockKlinePoint) error {
		persistMu.Lock()
		defer persistMu.Unlock()
		persistedBatches = append(persistedBatches, batch)
		return nil
	})
	if err != nil {
		testingT.Fatal(err)
	}
	if completedCount != 6 {
		testingT.Fatalf("expected 6 completed stocks, got %d", completedCount)
	}
	if len(persistedBatches) != 6 {
		testingT.Fatalf("expected 6 batches persisted, got %d", len(persistedBatches))
	}
	for _, batch := range persistedBatches {
		if len(batch) != 48 {
			testingT.Fatalf("expected 48 points per batch, got %d", len(batch))
		}
		if batch[0].Source != graymarket.KlineSourceFiveMinute {
			testingT.Fatalf("expected KlineSourceFiveMinute, got %s", batch[0].Source)
		}
	}
	if trendsRequestCount.Load() != 0 {
		testingT.Fatalf("expected 0 trends requests, got %d", trendsRequestCount.Load())
	}
	if maxConcurrentRequests.Load() > 4 {
		testingT.Fatalf("expected at most 4 concurrent workers, got %d", maxConcurrentRequests.Load())
	}
}

func uniformHistoricalKlineRows(tradeDate string, location *time.Location) []string {
	rows := make([]string, 0, 48)
	for index := 0; index < 48; index++ {
		pointAt := researchTimeForIndex(tradeDate, index, location)
		fields := []string{
			pointAt.Format("2006-01-02 15:04"),
			"10.00",
			"10.00",
			"11.00",
			"9.00",
			"100",
			"1000.00",
			"20.00",
			"0.00",
			"0.00",
			"1.00",
		}
		rows = append(rows, strings.Join(fields, ","))
	}
	return rows
}

func uniformTrendRows(tradeDate string) []string {
	rows := make([]string, 0, 241)
	cumulativeVolume := 0
	cumulativeTurnover := 0
	appendRow := func(minute int) {
		cumulativeVolume++
		cumulativeTurnover += 100
		rows = append(rows, fmt.Sprintf("%s %02d:%02d,10.00,10.00,10.00,10.00,1,100.00,10.000,0,0.00,%d,%d.00",
			tradeDate, minute/60, minute%60, cumulativeVolume, cumulativeTurnover))
	}
	for minute := 9*60 + 30; minute <= 11*60+30; minute++ {
		appendRow(minute)
	}
	for minute := 13*60 + 1; minute <= 15*60; minute++ {
		appendRow(minute)
	}
	return rows
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
