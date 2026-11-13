package thsquote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func TestFetchStockQuotesMapsDocumentedFieldsAndBatchesFifty(t *testing.T) {
	relations := make([]graymarket.StockBoardRelation, 0, 50)
	for index := 0; index < 50; index++ {
		relations = append(relations, graymarket.StockBoardRelation{
			StockCode: fmt.Sprintf("%06d", index+1), StockMarket: 0, StockName: fmt.Sprintf("stock-%d", index),
		})
	}
	var batches int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload snapshotRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		batches++
		if len(payload.CodeList) != 1 || payload.CodeList[0].Market != "33" || len(payload.CodeList[0].Codes) != 50 {
			t.Fatalf("unexpected batch: %+v", payload.CodeList)
		}
		quotes := make([]any, 0, 50)
		for _, code := range payload.CodeList[0].Codes {
			quotes = append(quotes, map[string]any{
				"market": "33", "code": code,
				"data_fields": []string{"55", "6", "7", "8", "9", "10", "13", "19"},
				"value":       [][]any{{"test-stock", 9.8, 10.0, 10.5, 9.7, 10.1, 678, 123400}},
			})
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"status_code": 0, "status_msg": "ok",
			"data": map[string]any{"quote_data": quotes, "fail_params": nil},
		})
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client()).WithSnapshotBaseURL(server.URL)
	quotes, err := client.FetchStockQuotes(context.Background(), relations)
	if err != nil {
		t.Fatal(err)
	}
	if batches != 1 || len(quotes) != 50 {
		t.Fatalf("unexpected request/result counts: batches=%d quotes=%d", batches, len(quotes))
	}
	quote := quotes[0]
	if quote.LatestPrice != 10.1 || quote.HighPrice != 10.5 || quote.LowPrice != 9.7 ||
		quote.OpenPrice != 10.0 || quote.PreviousClose != 9.8 || quote.Volume != 678 || quote.Turnover != 123400 {
		t.Fatalf("fields were mapped incorrectly: %+v", quote)
	}
	if quote.StockName != "test-stock" || !quote.Available || quote.QuoteTime != "" {
		t.Fatalf("unexpected availability/name: %+v", quote)
	}
}

func TestFetchStockKline5mBuildsOneStockArchiveCurve(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload klineRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.CodeList) != 1 || payload.CodeList[0].Market != "33" ||
			len(payload.CodeList[0].Codes) != 1 || payload.CodeList[0].Codes[0] != "000001" ||
			payload.TimePeriod != "min_5" || payload.AdjustType != "actual" ||
			payload.EndTime != time.Date(2026, 11, 13, 15, 0, 0, 0, location).UnixMilli() {
			t.Fatalf("unexpected request: %+v", payload)
		}
		values := make([][]any, 0, 48)
		for index := 0; index < 48; index++ {
			at := time.Date(2026, 11, 13, 9, 35, 0, 0, location).Add(time.Duration(index) * 5 * time.Minute)
			if index >= 24 {
				at = time.Date(2026, 11, 13, 13, 5, 0, 0, location).Add(time.Duration(index-24) * 5 * time.Minute)
			}
			values = append(values, []any{at.UnixMilli(), 10.0, 10.1, 9.9, 10.0, 2, 10})
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"status_code": 0, "status_msg": "ok",
			"data": map[string]any{"quote_data": []any{map[string]any{
				"market": "33", "code": "000001",
				"data_fields": []string{"1", "7", "8", "9", "11", "13", "19"},
				"value":       values,
			}}, "fail_params": nil},
		})
	}))
	defer server.Close()
	client := NewClient(server.URL, server.Client()).WithClock(func() time.Time {
		return time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC)
	})
	stock := graymarket.RankRecord{
		Code: "000001", Market: 0, TradeDate: "2026-11-13", SnapshotAt: time.Date(2026, 11, 13, 15, 0, 0, 0, location),
		OpenPrice: 10, HighPrice: 10.1, LowPrice: 9.9, ClosePrice: 10, PreviousClose: 9.8, Volume: 96, TurnoverRate: 0.01,
	}
	points, err := client.FetchStockKline5m(context.Background(), stock)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 48 || points[0].SnapshotAt.Format("15:04") != "09:35" || points[47].SnapshotAt.Format("15:04") != "15:00" {
		t.Fatalf("unexpected curve: count=%d first=%s last=%s", len(points), points[0].SnapshotAt, points[47].SnapshotAt)
	}
	if points[0].Volume != 2 || points[0].Turnover != 10 || points[0].Source != graymarket.KlineSourceTHSFiveMinute {
		t.Fatalf("unexpected point mapping: %+v", points[0])
	}
}

func TestFetchStockKline5mRejectsBatchAndMisalignedDailyBar(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*graymarket.RankRecord, *[]map[string]any)
	}{
		{
			name: "multi stock response",
			mutate: func(_ *graymarket.RankRecord, response *[]map[string]any) {
				(*response)[0]["quote_data"] = []any{map[string]any{"code": "000001"}, map[string]any{"code": "000002"}}
			},
		},
		{
			name:   "daily close conflict",
			mutate: func(stock *graymarket.RankRecord, _ *[]map[string]any) { stock.ClosePrice = 10.2 },
		},
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, testCase := range tests {
		t.Run(testCase.name, func(testingT *testing.T) {
			stockForTest := graymarket.RankRecord{Code: "000001", TradeDate: "2026-11-13", OpenPrice: 10, HighPrice: 10.1, LowPrice: 9.9, ClosePrice: 10, PreviousClose: 9.8, Volume: 96}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				values := make([][]any, 0, 48)
				for index := 0; index < 48; index++ {
					at := time.Date(2026, 11, 13, 9, 35, 0, 0, location).Add(time.Duration(index) * 5 * time.Minute)
					if index >= 24 {
						at = time.Date(2026, 11, 13, 13, 5, 0, 0, location).Add(time.Duration(index-24) * 5 * time.Minute)
					}
					values = append(values, []any{at.UnixMilli(), 10, 10.0, 10.1, 9.9, 10.0, 2, 8, 3})
				}
				payload := []map[string]any{{
					"market": "33", "code": "000001", "data_fields": []string{"1", "7", "8", "9", "11", "13", "19"}, "value": values,
				}}
				response := []map[string]any{{"status_code": 0, "status_msg": "ok", "data": map[string]any{"quote_data": payload}}}
				testCase.mutate(&stockForTest, &response)
				_ = json.NewEncoder(writer).Encode(response[0])
			}))
			defer server.Close()
			stock := stockForTest
			if _, err := NewClient(server.URL, server.Client()).FetchStockKline5m(context.Background(), stock); err == nil {
				t.Fatal("expected malformed response to fail")
			}
		})
	}
}

func TestFetchStockKline5mSkipsOtherDatesBeforeSelecting48Buckets(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		values := make([][]any, 0, 400)
		for day := 3; day <= 12; day++ {
			at := time.Date(2026, 11, day, 9, 35, 0, 0, location)
			values = append(values, []any{at.UnixMilli(), 10, 10.1, 9.9, 10, 2, 10})
		}
		for index := 0; index < 48; index++ {
			at := time.Date(2026, 11, 13, 9, 35, 0, 0, location).Add(time.Duration(index) * 5 * time.Minute)
			if index >= 24 {
				at = time.Date(2026, 11, 13, 13, 5, 0, 0, location).Add(time.Duration(index-24) * 5 * time.Minute)
			}
			values = append(values, []any{at.UnixMilli(), 10, 10.1, 9.9, 10, 2, 10})
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"status_code": 0, "status_msg": "ok",
			"data": map[string]any{"quote_data": []any{map[string]any{
				"market": "33", "code": "000001",
				"data_fields": []string{"1", "7", "8", "9", "11", "13", "19"},
				"value":       values,
			}}, "fail_params": nil},
		})
	}))
	defer server.Close()
	stock := graymarket.RankRecord{
		Code: "000001", TradeDate: "2026-11-13",
		SnapshotAt: time.Date(2026, 11, 13, 15, 0, 0, 0, location),
		OpenPrice:  10, HighPrice: 10.1, LowPrice: 9.9, ClosePrice: 10, PreviousClose: 9.8, Volume: 96,
	}
	points, err := NewClient(server.URL, server.Client()).FetchStockKline5m(context.Background(), stock)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 48 || points[0].SnapshotAt.Format("2006-01-02 15:04") != "2026-11-13 09:35" ||
		points[47].SnapshotAt.Format("2006-01-02 15:04") != "2026-11-13 15:00" {
		t.Fatalf("other dates leaked into the archive curve: count=%d", len(points))
	}
}

func TestMarketForRecord(t *testing.T) {
	for _, testCase := range []struct {
		market int64
		code   string
		want   string
	}{
		{market: 1, code: "600001", want: "17"},
		{market: 0, code: "000001", want: "33"},
		{market: 0, code: "920001", want: "151"},
	} {
		got, err := marketForRecord(graymarket.RankRecord{Market: testCase.market, Code: testCase.code})
		if err != nil || got != testCase.want {
			t.Fatalf("marketForRecord(%d,%s)=%s,%v", testCase.market, testCase.code, got, err)
		}
	}
}
