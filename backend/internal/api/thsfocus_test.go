package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/thsfocus"
)

func TestThsFocusAPIReadOnlyScopeAndDate(testingT *testing.T) {
	server, store := testServer(testingT, "")
	defer store.Close()
	path := "/api/v1/limit-up/focus?trade_date=2026-11-06&scope=HS,GEM2STAR"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"not_collected"`) || !strings.Contains(response.Body.String(), `"snapshot":null`) {
		testingT.Fatalf("empty focus response: %d %s", response.Code, response.Body.String())
	}
	snapshot := thsfocus.Snapshot{
		TradeDate: "2026-11-06", Source: thsfocus.Source, Scope: thsfocus.ScopeDefault, MarketScope: thsfocus.MarketScope,
		FetchedAt: time.Date(2026, 11, 6, 16, 0, 0, 0, thsfocus.Shanghai()),
		Blocks:    []thsfocus.Block{}, Ladder: []thsfocus.Level{}, Limitations: []string{"Top题材无日期回显"},
		Market:     json.RawMessage(`{"rise_fall":{"rise":100,"fall":200,"deuce":10,"limit_up":10,"limit_down":2}}`),
		Comparison: json.RawMessage(`{"today_date":"2026-11-06","yesterday_date":"2026-11-05","board_list":[],"one_board":[]}`),
		RawPages:   []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
	if err := store.SaveThsFocus(context.Background(), snapshot); err != nil {
		testingT.Fatal(err)
	}
	for target, expected := range map[string]int{
		path: http.StatusOK,
		"/api/v1/limit-up/focus?trade_date=2026-11-06&scope=HS":                            http.StatusBadRequest,
		"/api/v1/limit-up/focus?trade_date=2026-11-06&date=2026-11-06":                     http.StatusBadRequest,
		"/api/v1/limit-up/focus?trade_date=2026-11-06&scope=HS,GEM2STAR&scope=HS,GEM2STAR": http.StatusBadRequest,
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != expected {
			testingT.Fatalf("%s: %d %s", target, response.Code, response.Body.String())
		}
		if target == path && (!strings.Contains(response.Body.String(), `"status":"complete"`) || !strings.Contains(response.Body.String(), `"source_date_verified":false`)) {
			testingT.Fatal("source limits were not returned")
		}
	}
}
