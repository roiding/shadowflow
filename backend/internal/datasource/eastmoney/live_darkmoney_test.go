package eastmoney

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func TestFetchLatestMoney5mUsesLatestTickInsideCurrentBucket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("code") != "001389" || request.URL.Query().Get("market") != "0" {
			t.Fatalf("unexpected query %s", request.URL.RawQuery)
		}
		data := []map[string]int64{
			{"time": 2611110935, "1": 100, "2": 200, "3": 300},
			{"time": 2611111035, "1": 150, "2": 250, "3": 400},
			{"time": 2611111040, "1": 200, "2": 300, "3": 500},
			{"time": 2611111045, "1": 999, "2": 1, "3": 1000},
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"errid": 0, "data": data})
	}))
	defer server.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	at := time.Date(2026, 11, 11, 10, 42, 0, 0, location)
	records := []graymarket.RankRecord{{
		TradeDate: "2026-11-11", RankType: graymarket.RankStock,
		Market: 0, Code: "001389", Name: "广合科技",
	}}
	points, err := NewClient("unused", server.Client(), 100).
		WithDarkTradeTickBaseURL(server.URL).
		FetchLatestMoney5m(context.Background(), "2026-11-11", records, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].SnapshotAt.Format("15:04") != "10:40" ||
		points[0].DarkMoney != 200 || points[0].MainMoneyInflow != 500 {
		t.Fatalf("unexpected latest dark-money point: %+v", points)
	}
}
