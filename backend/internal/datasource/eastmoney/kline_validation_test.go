package eastmoney

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func TestHistoricalKlinesValidateNumbersAndDailyOHLC(t *testing.T) {
	for _, tc := range []struct {
		name       string
		field      int
		value      string
		wrongDaily bool
		valid      bool
	}{
		{name: "valid including cents", valid: true},
		{name: "malformed volume", field: 5, value: "not-a-number"},
		{name: "fractional volume", field: 5, value: "1.2"},
		{name: "negative volume", field: 5, value: "-1"},
		{name: "overflow turnover", field: 6, value: "9223372036854775808"},
		{name: "nonfinite close", field: 2, value: "NaN"},
		{name: "nonfinite ratio", field: 10, value: "Inf"},
		{name: "invalid low", field: 4, value: "12"},
		{name: "wrong daily prices", wrongDaily: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc := time.FixedZone("Asia/Shanghai", 8*3600)
			date := "2026-09-04"
			rows := make([]string, 0, 48)
			for i := range 48 {
				at := researchTimeForIndex(date, i, loc)
				fields := []string{at.Format("2006-01-02 15:04"), "10", "10", "11", "9", "100", "1000.25", "20", "0", "0", "1"}
				if i == 10 && tc.field > 0 {
					fields[tc.field] = tc.value
				}
				rows = append(rows, strings.Join(fields, ","))
			}
			slices.Reverse(rows)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"klines": rows}})
			}))
			defer server.Close()
			stock := graymarket.RankRecord{Code: "000001", Market: 0, OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10, PreviousClose: 10, Volume: 999999, Turnover: 999999, SnapshotAt: time.Date(2026, 9, 4, 15, 0, 0, 0, loc)}
			if tc.wrongDaily {
				stock.ClosePrice = 20
			}
			points, err := NewClient("unused", server.Client(), 100).WithStockKlineBaseURL(server.URL).fetchStockKlineFromHistory(context.Background(), date, stock)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid historical curve was accepted")
				}
				return
			}
			if err != nil || len(points) != 48 {
				t.Fatalf("valid historical curve rejected: %d %v", len(points), err)
			}
			if points[0].SnapshotAt.Format("15:04") != "09:35" || points[47].SnapshotAt.Format("15:04") != "15:00" || points[0].Turnover != 1000 {
				t.Fatalf("unexpected ordered points: %v", points[0])
			}
		})
	}
}

func TestTrendRejectsInvalidCumulativeQuantity(t *testing.T) {
	rows := uniformTrendRows("2026-08-14")
	fields := strings.Split(rows[10], ",")
	fields[11] = "invalid"
	rows[10] = strings.Join(fields, ",")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"trends": rows}})
	}))
	defer server.Close()
	stock := graymarket.RankRecord{Code: "000001", OpenPrice: 10, ClosePrice: 10, HighPrice: 10, LowPrice: 10, PreviousClose: 10}
	_, err := NewClient("unused", server.Client(), 100).fetchStockKlineFromTrendURL(context.Background(), server.URL, "2026-08-14", stock)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(11)) {
		t.Fatalf("invalid cumulative quantity accepted: %v", err)
	}
}

func TestHistoricalKlineUsesDailyOpeningAuctionOpen(t *testing.T) {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	date := "2026-11-09"
	rows := make([]string, 0, 48)
	for i := range 48 {
		at := researchTimeForIndex(date, i, loc)
		open := "75.35"
		if i > 0 {
			open = "75.80"
		}
		fields := []string{at.Format("2006-01-02 15:04"), open, "75.16", "76.84", "75.16", "100", "1000", "2", "1", "0.5", "1"}
		rows = append(rows, strings.Join(fields, ","))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"klines": rows}})
	}))
	defer server.Close()
	stock := graymarket.RankRecord{Code: "301557", Market: 0, OpenPrice: 75.80, HighPrice: 76.84, LowPrice: 75.16, ClosePrice: 75.16, PreviousClose: 75.35, SnapshotAt: time.Date(2026, 11, 9, 15, 0, 0, 0, loc)}
	points, err := NewClient("unused", server.Client(), 100).WithStockKlineBaseURL(server.URL).fetchStockKlineFromHistory(context.Background(), date, stock)
	if err != nil || len(points) != 48 {
		t.Fatalf("opening-auction curve rejected: %d %v", len(points), err)
	}
	if !samePrice(points[0].OpenPrice, stock.OpenPrice) {
		t.Fatalf("daily opening auction open was not applied: %v", points[0].OpenPrice)
	}
}

func TestHistoricalKlineRebuildsZeroAuctionFirstBarFromOneMinute(t *testing.T) {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	date := "2026-11-09"
	fiveMinuteRows := make([]string, 0, 48)
	for i := range 48 {
		at := researchTimeForIndex(date, i, loc)
		open, closePrice, high, low := "75.50", "75.50", "75.60", "75.16"
		volume, turnover := "100", "1000"
		if i == 0 {
			open, closePrice, high, low = "75.35", "76.23", "76.84", "75.35"
			volume, turnover = "422", "3217824"
		}
		if i == 47 {
			open, closePrice, high, low = "75.50", "75.16", "75.50", "75.16"
		}
		fields := []string{at.Format("2006-01-02 15:04"), open, closePrice, high, low, volume, turnover, "2", "1", "0.5", "1"}
		fiveMinuteRows = append(fiveMinuteRows, strings.Join(fields, ","))
	}
	oneMinuteRows := []string{
		"2026-11-09 09:31,75.80,76.01,76.16,75.80,47,355418.00,0.48,0.88,0.66,0.02",
		"2026-11-09 09:32,76.16,76.45,76.45,76.15,19,150000.00,0.38,0.29,0.22,0.01",
		"2026-11-09 09:33,76.61,76.45,76.84,76.45,155,2000000.00,0.30,-0.16,-0.12,0.05",
		"2026-11-09 09:34,76.17,76.17,76.17,76.17,5,30000.00,0.00,-0.28,-0.21,0.00",
		"2026-11-09 09:35,76.22,76.23,76.23,76.22,196,682406.00,0.08,0.06,0.05,0.06",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := fiveMinuteRows
		if r.URL.Query().Get("klt") == "1" {
			rows = oneMinuteRows
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"klines": rows}})
	}))
	defer server.Close()
	stock := graymarket.RankRecord{Code: "301557", Market: 0, OpenPrice: 75.80, HighPrice: 76.84, LowPrice: 75.16, ClosePrice: 75.16, PreviousClose: 75.35, SnapshotAt: time.Date(2026, 11, 9, 15, 0, 0, 0, loc)}
	points, err := NewClient("unused", server.Client(), 100).WithStockKlineBaseURL(server.URL).fetchStockKlineFromHistory(context.Background(), date, stock)
	if err != nil || len(points) != 48 {
		t.Fatalf("zero-auction curve rejected: %d %v", len(points), err)
	}
	first := points[0]
	if !samePrice(first.OpenPrice, 75.80) || !samePrice(first.LowPrice, 75.80) || !samePrice(first.HighPrice, 76.84) || !samePrice(first.ClosePrice, 76.23) {
		t.Fatalf("first bar was not rebuilt from one-minute rows: %+v", first)
	}
	if first.Volume != 422 || first.Turnover != 3217824 {
		t.Fatalf("first bar volume or turnover was not rebuilt: %+v", first)
	}
	if math.Abs(first.Amplitude-(76.84-75.80)/75.35) > 1e-9 {
		t.Fatalf("first bar amplitude was not recomputed: %v", first.Amplitude)
	}
}

func TestZeroAuctionFirstBarAcceptsOneLotVolumeDrift(t *testing.T) {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	date := "2026-11-10"
	oneMinuteRows := []string{
		"2026-11-10 09:31,48.22,48.98,49.09,47.99,135,659974.00,2.31,2.83,1.35,0.02",
		"2026-11-10 09:32,48.97,48.92,49.25,48.90,354,1737464.00,0.71,-0.12,-0.06,0.04",
		"2026-11-10 09:33,49.19,49.02,49.32,48.94,147,725250.00,0.78,0.20,0.10,0.02",
		"2026-11-10 09:34,49.02,49.07,49.07,49.00,56,276350.00,0.14,0.10,0.05,0.01",
		"2026-11-10 09:35,49.14,48.81,49.14,48.80,40,194313.00,0.69,-0.53,-0.26,0.00",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"klines": oneMinuteRows}})
	}))
	defer server.Close()
	stock := graymarket.RankRecord{Code: "688768", Market: 1, OpenPrice: 48.22, HighPrice: 51.00, LowPrice: 47.99, ClosePrice: 50.59, PreviousClose: 47.63, SnapshotAt: time.Date(2026, 11, 10, 15, 0, 0, 0, loc)}
	bar := graymarket.StockKlinePoint{
		SnapshotAt: time.Date(2026, 11, 10, 9, 35, 0, 0, loc),
		OpenPrice:  47.63, HighPrice: 49.32, LowPrice: 47.63, ClosePrice: 48.81,
		Volume: 733, Turnover: 3593351,
	}
	client := NewClient("unused", server.Client(), 100).WithStockKlineBaseURL(server.URL)
	if err := client.correctZeroAuctionFirstBar(context.Background(), date, stock, &bar); err != nil {
		t.Fatalf("one-lot volume drift rejected: %v", err)
	}
	if !samePrice(bar.OpenPrice, 48.22) || !samePrice(bar.LowPrice, 47.99) || bar.Volume != 732 {
		t.Fatalf("first bar was not rebuilt despite one-lot drift: %+v", bar)
	}
}
