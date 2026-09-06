package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

func TestLatestRankPreviousTradeDateUsesSnapshotCalendarNotArchive(t *testing.T) {
	for _, tc := range []struct {
		name, date, previous string
	}{
		{"weekday", "2026-09-04", "2026-09-03"},
		{"Monday skips weekend", "2026-09-07", "2026-09-04"},
		{"holiday and weekend", "2026-09-28", "2026-09-24"},
		{"stale snapshot uses its own date", "2026-08-17", "2026-08-14"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, store := testServer(t, "")
			defer store.Close()
			calendarPath := filepath.Join(t.TempDir(), "calendar.json")
			if err := os.WriteFile(calendarPath, []byte(`{"holidays":["2026-09-25"],"workdays":[],"valid_through":"2026-12-31"}`), 0600); err != nil {
				t.Fatal(err)
			}
			calendar, err := tradingcalendar.Load(calendarPath)
			if err != nil {
				t.Fatal(err)
			}
			server.calendar = calendar
			at, err := time.ParseInLocation("2006-01-02 15:04", tc.date+" 10:30", server.location)
			if err != nil {
				t.Fatal(err)
			}
			for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
				olderAt := time.Date(2026, 8, 3, 15, 0, 0, 0, server.location)
				older := graymarket.RankSnapshot{TradeDate: "2026-08-03", SnapshotAt: olderAt, RankType: rankType,
					Records: []graymarket.RankRecord{{TradeDate: "2026-08-03", SnapshotAt: olderAt, RankType: rankType, Code: "BK001", Rank: 1, FetchedAt: olderAt}}}
				if err := store.SaveDailyClose(context.Background(), "older-"+string(rankType), older); err != nil {
					t.Fatal(err)
				}
				current := graymarket.RankSnapshot{TradeDate: tc.date, SnapshotAt: at, RankType: rankType,
					Records: []graymarket.RankRecord{{TradeDate: tc.date, SnapshotAt: at, RankType: rankType, Code: "BK001", Rank: 1, FetchedAt: at}}}
				if err := store.SaveIntraday(context.Background(), "current-"+string(rankType), current, false); err != nil {
					t.Fatal(err)
				}
				response := httptest.NewRecorder()
				server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/ranks/latest?type="+string(rankType), nil))
				var payload struct {
					Data []graymarket.RankRecord `json:"data"`
					Meta struct {
						TradeDate         string `json:"trade_date"`
						PreviousTradeDate string `json:"previous_trade_date"`
					} `json:"meta"`
				}
				if response.Code != http.StatusOK {
					t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
				}
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Meta.TradeDate != tc.date || payload.Meta.PreviousTradeDate != tc.previous || len(payload.Data) != 1 {
					t.Fatalf("comparison date must not use current date or last archived day: %+v", payload)
				}
				response = httptest.NewRecorder()
				server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/ranks/daily-close?type="+string(rankType)+"&trade_date="+tc.previous+"&page_size=200", nil))
				var missing struct {
					Data []graymarket.RankRecord `json:"data"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &missing); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusOK || len(missing.Data) != 0 {
					t.Fatalf("missing previous day must not fall back to older archive: %s", response.Body.String())
				}
			}
		})
	}
}

func TestLatestRankWithoutSnapshotHasNoComparisonDate(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/ranks/latest?type=industry", nil))
	var payload struct {
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload.Meta["previous_trade_date"]; exists {
		t.Fatalf("must not fabricate comparison date without snapshot: %s", response.Body.String())
	}
}
