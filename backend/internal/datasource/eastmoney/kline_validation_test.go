package eastmoney

import (
	"context"
	"encoding/json"
	"fmt"
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
