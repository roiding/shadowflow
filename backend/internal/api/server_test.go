package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

func testServer(t *testing.T, staticDir string) (*Server, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(store, calendar, slog.New(slog.NewTextHandler(os.Stderr, nil)), Options{StaticDir: staticDir})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return server, store
}

func TestDailyClosePaginationAndValidation(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	at := time.Date(2026, 8, 12, 15, 0, 0, 0, location)
	snapshot := graymarket.RankSnapshot{RequestedDate: "20260812", TradeDate: "2026-08-12", RankType: graymarket.RankStock, SnapshotAt: at,
		Records: []graymarket.RankRecord{
			{TradeDate: "2026-08-12", SnapshotAt: at, RankType: graymarket.RankStock, Rank: 1, Code: "000001", Name: "one", DarkMoney: 20, FetchedAt: at},
			{TradeDate: "2026-08-12", SnapshotAt: at, RankType: graymarket.RankStock, Rank: 2, Code: "000002", Name: "two", DarkMoney: 10, FetchedAt: at},
		}}
	if err := store.SaveDailyClose(context.Background(), "daily", snapshot); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/ranks/daily-close?type=stock&trade_date=2026-08-12&page=2&page_size=1", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []graymarket.RankRecord `json:"data"`
		Meta map[string]any          `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].Code != "000002" || payload.Meta["total"] != float64(2) || payload.Meta["pages"] != float64(2) {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	board := graymarket.RankSnapshot{RequestedDate: "20260812", TradeDate: "2026-08-12", RankType: graymarket.RankIndustry, SnapshotAt: at,
		Records: []graymarket.RankRecord{{TradeDate: "2026-08-12", SnapshotAt: at, RankType: graymarket.RankIndustry, Rank: 1, Code: "BK001", Name: "行业", FetchedAt: at}}}
	if err := store.SaveDailyClose(context.Background(), "industry-close", board); err != nil {
		t.Fatal(err)
	}
	concept := graymarket.RankSnapshot{RequestedDate: "20260812", TradeDate: "2026-08-12", RankType: graymarket.RankConcept, SnapshotAt: at,
		Records: []graymarket.RankRecord{{TradeDate: "2026-08-12", SnapshotAt: at, RankType: graymarket.RankConcept, Rank: 1, Code: "BK002", Name: "概念", FetchedAt: at}}}
	if err := store.SaveDailyClose(context.Background(), "concept-close", concept); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/ranks/daily-close?type=industry&trade_date=2026-08-12", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"BK001"`) || !strings.Contains(response.Body.String(), `"snapshot_kind":"daily_close"`) {
		t.Fatalf("industry daily close was not queryable: status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/research/daily-close/export?trade_date=2026-08-12", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "industry") || !strings.Contains(response.Body.String(), "concept") || !strings.Contains(response.Body.String(), "stock") || !strings.Contains(response.Body.String(), "daily_close") {
		t.Fatalf("joint close export is incomplete: status=%d body=%s", response.Code, response.Body.String())
	}

	for _, target := range []string{
		"/api/v1/ranks/daily-close?type=invalid&trade_date=2026-08-12",
		"/api/v1/ranks/daily-close?type=stock&trade_date=2026-08-12&page_size=1000",
		"/api/v1/ranks/daily-close?type=stock&trade_date=2026-08-12&sort=drop_table",
	} {
		response = httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", target, response.Code)
		}
	}
}

func TestMetricsAndStaticSPA(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<h1>app</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "asset.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, store := testServer(t, staticDir)
	defer store.Close()

	tests := map[string]string{"/": "<h1>app</h1>", "/history/board": "<h1>app</h1>", "/asset.js": "console.log('ok')"}
	for target, expected := range tests {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("%s: status=%d body=%q", target, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/not-found", nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "<h1>app</h1>") {
		t.Fatalf("unknown API route was served by SPA: status=%d body=%q", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/plain") || !strings.Contains(response.Body.String(), "shadowflow_collector_runs_total") {
		t.Fatalf("unexpected metrics response: status=%d content-type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestTradingDaysRejectsLargeRanges(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/trading-days?from=2025-01-01&to=2026-08-13", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestIntradayMetaIdentifiesMinuteAndArchivedSeries(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tradeDate := "2026-08-12"
	for index, clock := range []string{"09:35", "09:36", "15:00"} {
		at, _ := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+clock, location)
		record := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: at, RankType: graymarket.RankIndustry, Rank: 1, Code: "BK001", Name: "board", FetchedAt: at}
		if err := store.SaveIntraday(ctx, "run-"+clock, graymarket.RankSnapshot{TradeDate: tradeDate, RankType: graymarket.RankIndustry, SnapshotAt: at, Records: []graymarket.RankRecord{record}}, index != 1); err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/boards/industry/BK001/intraday?trade_date="+tradeDate, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"interval":"1m"`) || !strings.Contains(response.Body.String(), `"research_points":1`) || !strings.Contains(response.Body.String(), `"daily_close_points":1`) {
		t.Fatalf("unexpected minute meta: status=%d body=%s", response.Code, response.Body.String())
	}
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		for _, session := range []struct{ hour, minute, end int }{{9, 31, 11*60 + 30}, {13, 1, 15 * 60}} {
			start := session.hour*60 + session.minute
			for currentMinute := start; currentMinute <= session.end; currentMinute++ {
				clock := fmt.Sprintf("%02d:%02d", currentMinute/60, currentMinute%60)
				at, _ := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+clock, location)
				record := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: at, RankType: rankType, Rank: 1, Code: "BK001", Name: "board", FetchedAt: at}
				if err := store.SaveIntraday(ctx, "compact-"+string(rankType)+"-"+clock, graymarket.RankSnapshot{TradeDate: tradeDate, RankType: rankType, SnapshotAt: at, Records: []graymarket.RankRecord{record}}, false); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := store.CompactResearch(ctx, tradeDate); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/boards/industry/BK001/intraday?trade_date="+tradeDate, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"interval":"5m+close"`) || !strings.Contains(response.Body.String(), `"daily_close_points":1`) {
		t.Fatalf("unexpected archived meta: status=%d body=%s", response.Code, response.Body.String())
	}
}
