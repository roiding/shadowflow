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
	"github.com/roiding/shadowflow/internal/repository"
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

type staticQuoteSource struct {
	quotes []graymarket.StockQuote
}

func (source staticQuoteSource) FetchStockQuotes(context.Context, []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	return source.quotes, nil
}

func TestRelationAPIsReconstructAsOfDate(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	ctx := context.Background()
	startedAt := time.Now().UTC()
	run := repository.RelationSyncRun{RunID: "relations", TradeDate: "2026-08-13", Status: repository.RunRunning, StartedAt: startedAt}
	if err := store.StartRelationSync(ctx, run); err != nil {
		t.Fatal(err)
	}
	relations := []graymarket.StockBoardRelation{
		{StockCode: "000001", StockName: "平安银行", BoardCode: "BK001", BoardName: "银行", BoardType: graymarket.BoardIndustry,
			SourceOrder: 1, RelationSource: graymarket.RelationSourceQuoteClist, RelationScope: graymarket.RelationScopeBoardConstituents, DetectedAt: startedAt, RawData: `{}`},
		{StockCode: "000001", StockName: "平安银行", BoardCode: "BK101", BoardName: "融资融券", BoardType: graymarket.BoardConcept,
			SourceOrder: 2, RelationSource: graymarket.RelationSourceQuoteClist, RelationScope: graymarket.RelationScopeBoardConstituents, DetectedAt: startedAt, RawData: `{}`},
	}
	if err := store.StageRelations(ctx, run.RunID, relations); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRelationScan(ctx, run.RunID, run.TradeDate, startedAt); err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 13, 15, 0, 0, 0, location)
	if err := store.SaveDailyClose(ctx, "stock-close", graymarket.RankSnapshot{
		RequestedDate: "20260813", TradeDate: run.TradeDate, RankType: graymarket.RankStock, SnapshotAt: closeAt,
		Records: []graymarket.RankRecord{{TradeDate: run.TradeDate, SnapshotAt: closeAt, RankType: graymarket.RankStock,
			Rank: 7, Code: "000001", Name: "平安银行", OpenPrice: 10.1, HighPrice: 10.8, LowPrice: 9.9,
			ClosePrice: 10.5, PreviousClose: 10, Turnover: 1000, TurnoverRate: 0.02, QuoteAvailable: true,
			DarkMoney: -250, MainMoneyInflow: 125, DarkActivity: 0.25, FetchedAt: closeAt}},
	}); err != nil {
		t.Fatal(err)
	}
	server.quotes = staticQuoteSource{quotes: []graymarket.StockQuote{{
		StockCode: "000001", StockName: "平安银行", LatestPrice: 10.5, Turnover: 1000, Available: true,
	}}}

	for target, expected := range map[string]string{
		"/api/v1/stocks/000001/boards?as_of=2026-08-13":         `"board_code":"BK101"`,
		"/api/v1/boards/industry/BK001/stocks?as_of=2026-08-13": `"stock_code":"000001"`,
		"/api/v1/boards/industry/BK001/quotes?as_of=2026-08-13": `"dark_activity":0.25`,
		"/api/v1/relations/changes?trade_date=2026-08-13":       `"data":[]`,
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("%s: status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/boards/industry/BK001/quotes?as_of=2026-08-13", nil))
	for _, expected := range []string{`"dark_rank":7`, `"dark_money":-250`, `"main_money_inflow":125`, `"dark_data_available":true`, `"open_price":10.1`, `"previous_close":10`, `"turnover_rate":0.02`} {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("board quote enrichment missing %s: status=%d body=%s", expected, response.Code, response.Body.String())
		}
	}
	for _, target := range []string{
		"/api/v1/stocks/151000/boards?as_of=bad-date",
		"/api/v1/stocks/ABC001/boards?as_of=2026-08-13",
		"/api/v1/boards/region/BK001/stocks?as_of=2026-08-13",
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d body=%s", target, response.Code, response.Body.String())
		}
	}
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

func TestThreeDayFocusReportsAccumulationStateAndValidatesDate(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/focus/three-day?as_of=2026-08-14", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":false`) || !strings.Contains(response.Body.String(), `"required_days":3`) {
		t.Fatalf("unexpected focus response: status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/focus/three-day?as_of=bad-date", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid date to return 400, got %d", response.Code)
	}
}

func TestDynamicFocusScanValidatesAndReportsRequestedDays(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	body := `{"as_of":"2026-08-14","consecutive_days":5,"concept_match":"all","concept_conditions":[],"stock_match":"all","stock_conditions":[],"stock_scope":{"main_board_only":false,"exclude_st":false,"require_qualified_concepts":false}}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/focus/scan", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":false`) || !strings.Contains(response.Body.String(), `"required_days":5`) {
		t.Fatalf("unexpected dynamic focus response: status=%d body=%s", response.Code, response.Body.String())
	}
	for _, invalid := range []string{
		`{"as_of":"bad-date","consecutive_days":3,"concept_match":"all","stock_match":"all"}`,
		`{"as_of":"2026-08-14","consecutive_days":0,"concept_match":"all","stock_match":"all"}`,
		`{"as_of":"2026-08-14","consecutive_days":3,"concept_match":"all","stock_match":"all","unknown":true}`,
	} {
		response = httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/focus/scan", strings.NewReader(invalid)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected invalid scan to return 400, got %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestStockResearchAndQualityAPIsExpose48PlusDailyArchive(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tradeDate := "2026-08-14"
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	record := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: graymarket.RankStock,
		Rank: 1, Market: 0, Code: "000001", Name: "one", QuoteAvailable: true, OpenPrice: 10, ClosePrice: 11, FetchedAt: closeAt}
	snapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: graymarket.RankStock, SnapshotAt: closeAt, Records: []graymarket.RankRecord{record}}
	money := make([]graymarket.MoneyPoint, 0, 48)
	klines := make([]graymarket.StockKlinePoint, 0, 48)
	for _, session := range []struct{ start, end int }{{9*60 + 35, 11*60 + 30}, {13*60 + 5, 15 * 60}} {
		for minute := session.start; minute <= session.end; minute += 5 {
			at := time.Date(2026, 8, 14, minute/60, minute%60, 0, 0, location)
			money = append(money, graymarket.MoneyPoint{TradeDate: tradeDate, SnapshotAt: at, RankType: graymarket.RankStock,
				Rank: 1, Market: 0, Code: record.Code, Name: record.Name, DarkMoney: 10, RegularMoney: 20, MainMoneyInflow: 30, FetchedAt: closeAt})
			klines = append(klines, graymarket.StockKlinePoint{TradeDate: tradeDate, SnapshotAt: at, Market: 0, Code: record.Code,
				OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10.5, Volume: 100, Turnover: 1000})
		}
	}
	if err := store.SaveStockArchive(ctx, "stock", snapshot, money); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStockKlines(ctx, "kline", klines); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/stocks/000001/research-5m?trade_date="+tradeDate, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"money_points":48`) || !strings.Contains(response.Body.String(), `"kline_points":48`) || !strings.Contains(response.Body.String(), `"snapshot_at":"2026-08-14T15:00:00+08:00"`) {
		t.Fatalf("unexpected stock research response: status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/research/quality?trade_date="+tradeDate, nil))
	for _, expected := range []string{`"expected_points":48`, `"expected_kline_stocks":1`, `"money_rows":48`, `"kline_rows":48`, `"daily_close_rows":1`, `"daily_kline_rows":1`, `"archive_manifest":`, `"status":"incomplete"`} {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("quality response missing %s: status=%d body=%s", expected, response.Code, response.Body.String())
		}
	}
}

func TestRevisionFeatureAndVersionedExportAPIs(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tradeDate := "2026-08-14"
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	moneyPoints := func(snapshot graymarket.RankSnapshot) []graymarket.MoneyPoint {
		result := make([]graymarket.MoneyPoint, 0, len(snapshot.Records)*48)
		for _, record := range snapshot.Records {
			for _, session := range []struct{ start, end int }{{9*60 + 35, 11*60 + 30}, {13*60 + 5, 15 * 60}} {
				for minute := session.start; minute <= session.end; minute += 5 {
					at := time.Date(2026, 8, 14, minute/60, minute%60, 0, 0, location)
					result = append(result, graymarket.MoneyPoint{TradeDate: tradeDate, SnapshotAt: at,
						RankType: record.RankType, Rank: record.Rank, Market: record.Market, Code: record.Code,
						Name: record.Name, DarkMoney: int64(len(result) % 48), MainMoneyInflow: 10, FetchedAt: closeAt})
				}
			}
		}
		return result
	}
	for _, record := range []graymarket.RankRecord{
		{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: graymarket.RankIndustry,
			Rank: 1, Market: 90, Code: "BK001", Name: "industry", QuoteAvailable: true,
			ClosePrice: 100, Turnover: 1000, DarkMoney: 10, FetchedAt: closeAt},
		{TradeDate: tradeDate, SnapshotAt: closeAt, RankType: graymarket.RankConcept,
			Rank: 1, Market: 90, Code: "BK002", Name: "concept", QuoteAvailable: true,
			ClosePrice: 200, Turnover: 2000, DarkMoney: 20, FetchedAt: closeAt},
	} {
		snapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: record.RankType,
			SnapshotAt: closeAt, Records: []graymarket.RankRecord{record}}
		if err := store.SaveBoardArchive(ctx, "board-"+string(record.RankType), snapshot, moneyPoints(snapshot)); err != nil {
			t.Fatal(err)
		}
	}
	stock := graymarket.RankRecord{TradeDate: tradeDate, SnapshotAt: closeAt,
		RankType: graymarket.RankStock, Rank: 1, Market: 0, Code: "000001", Name: "stock",
		QuoteAvailable: true, ClosePrice: 10, Turnover: 500, DarkMoney: 50,
		DarkActivity: 0.1, FetchedAt: closeAt}
	stockSnapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: graymarket.RankStock,
		SnapshotAt: closeAt, Records: []graymarket.RankRecord{stock}}
	if err := store.SaveStockArchive(ctx, "stock", stockSnapshot, moneyPoints(stockSnapshot)); err != nil {
		t.Fatal(err)
	}
	klines := make([]graymarket.StockKlinePoint, 0, 48)
	for _, session := range []struct{ start, end int }{{9*60 + 35, 11*60 + 30}, {13*60 + 5, 15 * 60}} {
		for minute := session.start; minute <= session.end; minute += 5 {
			at := time.Date(2026, 8, 14, minute/60, minute%60, 0, 0, location)
			klines = append(klines, graymarket.StockKlinePoint{TradeDate: tradeDate, SnapshotAt: at,
				Market: 0, Code: stock.Code, OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10})
		}
	}
	if err := store.SaveStockKlines(ctx, "kline", klines); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SealArchiveRevision(ctx, tradeDate, "api-revision"); err != nil {
		t.Fatal(err)
	}
	for target, expected := range map[string]string{
		"/api/v1/research/revisions?trade_date=" + tradeDate:                                                       `"revision_id":"api-revision"`,
		"/api/v1/research/features?trade_date=" + tradeDate + "&type=stock":                                        `"feature_version":"daily-features-v1"`,
		"/api/v1/research/labels?trade_date=" + tradeDate + "&horizon=5":                                           `"data":[]`,
		"/api/v1/ranks/daily-close?type=stock&trade_date=" + tradeDate + "&revision_id=api-revision":               `"revision_id":"api-revision"`,
		"/api/v1/boards/industry/BK001/trend?from=" + tradeDate + "&to=" + tradeDate + "&revision_id=api-revision": `"revision_id":"api-revision"`,
		"/api/v1/stocks/000001/research-5m?trade_date=" + tradeDate + "&revision_id=api-revision":                  `"money_points":48`,
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("%s: status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/research/daily-close/export?trade_date="+tradeDate+"&revision_id=api-revision", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "revision_id,trade_date") ||
		!strings.Contains(response.Body.String(), "api-revision,"+tradeDate) {
		t.Fatalf("versioned export is incomplete: status=%d body=%s", response.Code, response.Body.String())
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
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(response.Body.String(), "shadowflow_collector_runs_total") ||
		!strings.Contains(response.Body.String(), "shadowflow_trading_calendar_days_remaining") {
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

func TestLatestTradingDayUsesPreviousWeekdayOnWeekend(t *testing.T) {
	server, store := testServer(t, "")
	defer store.Close()
	weekend := time.Date(2026, 8, 15, 9, 0, 0, 0, server.location)
	if actual := server.latestTradingDay(weekend); actual != "2026-08-14" {
		t.Fatalf("expected previous Friday, got %s", actual)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"latest_trading_day":`) ||
		!strings.Contains(response.Body.String(), `"trading_calendar":`) {
		t.Fatalf("status response did not expose latest trading day: status=%d body=%s", response.Code, response.Body.String())
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
