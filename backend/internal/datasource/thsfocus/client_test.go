package thsfocus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	focus "github.com/roiding/shadowflow/internal/thsfocus"
)

const (
	testDate       = "2026-11-06"
	testDateDigits = "20261106"
)

type fixtureResponses struct {
	block      []byte
	ladder     []byte
	market     []byte
	comparison []byte
}

func loadFixture(testingT *testing.T, name string) []byte {
	testingT.Helper()
	content, readError := os.ReadFile(filepath.Join("testdata", name))
	if readError != nil {
		testingT.Fatalf("read fixture %s: %v", name, readError)
	}
	return content
}

func loadFixtures(testingT *testing.T, prefix string) fixtureResponses {
	testingT.Helper()
	return fixtureResponses{
		block:      loadFixture(testingT, "block_"+prefix+".json"),
		ladder:     loadFixture(testingT, "ladder_"+prefix+".json"),
		market:     loadFixture(testingT, "market_"+prefix+".json"),
		comparison: loadFixture(testingT, "comparison_"+prefix+".json"),
	}
}

func newFixtureServer(testingT *testing.T, fixtures fixtureResponses) *httptest.Server {
	testingT.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		var body []byte
		switch request.URL.Path {
		case blockTopPath:
			body = fixtures.block
		case ladderPath:
			body = fixtures.ladder
		case marketPath:
			body = fixtures.market
		case comparisonPath:
			body = fixtures.comparison
		default:
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write(body)
	}))
	testingT.Cleanup(server.Close)
	return server
}

func fixedClock(target time.Time) func() time.Time {
	return func() time.Time { return target }
}

func mockNow() time.Time {
	return time.Date(2026, 11, 6, 16, 0, 0, 0, focus.Shanghai())
}

func newTestClient(server *httptest.Server) *Client {
	return NewClient(server.URL, server.Client()).WithMinInterval(0).WithClock(fixedClock(mockNow()))
}

func TestNewClientUsesRootDefaultAndRejectsRedirects(t *testing.T) {
	client := NewClient("", nil)
	if client.baseURL != DefaultBaseURL {
		t.Fatalf("expected default root %s, got %s", DefaultBaseURL, client.baseURL)
	}
	if strings.HasSuffix(client.baseURL, "/dataapi/limit_up") {
		t.Fatal("default base URL must not include an API suffix")
	}
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"status_code":0,"data":[]}`))
	}))
	defer redirectTarget.Close()
	redirectSource := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, redirectTarget.URL+request.URL.Path, http.StatusFound)
	}))
	defer redirectSource.Close()
	redirectClient := NewClient(redirectSource.URL, nil).WithMinInterval(0).WithClock(fixedClock(mockNow()))
	_, _, fetchError := redirectClient.fetchJSON(context.Background(), blockTopPath, nil)
	if fetchError == nil || !strings.Contains(fetchError.Error(), "redirect rejected") {
		t.Fatalf("expected redirect rejection, got %v", fetchError)
	}
}

func TestFetchDaySyntheticSuccess(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	server := newFixtureServer(t, fixtures)
	snapshot, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError != nil {
		t.Fatalf("FetchDay failed: %v", fetchError)
	}
	if snapshot.Source != focus.Source || snapshot.MarketScope != focus.MarketScope || snapshot.Scope != focus.ScopeDefault {
		t.Fatalf("unexpected identity: %#v", snapshot)
	}
	if snapshot.SourceDateVerified {
		t.Fatal("source date must not be claimed verified without full date evidence")
	}
	if len(snapshot.Blocks) != 2 || snapshot.Blocks[0].LimitUpCount != 2 || len(snapshot.Blocks[0].Stocks) != 2 {
		t.Fatalf("unexpected blocks: %#v", snapshot.Blocks)
	}
	if snapshot.Blocks[0].Stocks[0].Reason != "合成理由甲" || snapshot.Blocks[0].Stocks[0].ReasonDetail != "合成详情甲" {
		t.Fatalf("reason fields not preserved: %#v", snapshot.Blocks[0].Stocks[0])
	}
	if len(snapshot.Ladder) != 2 || snapshot.Ladder[0].Height != 5 {
		t.Fatalf("unexpected ladder: %#v", snapshot.Ladder)
	}
	if snapshot.Ladder[0].Stocks[0].ContinueNum != 4 || snapshot.Ladder[0].Stocks[1].ContinueNum != 5 {
		t.Fatalf("ladder continue_num must be retained per stock: %#v", snapshot.Ladder[0].Stocks)
	}
	if !strings.Contains(string(snapshot.Market), `"limit_up"`) || !strings.Contains(string(snapshot.Comparison), `"today_date"`) {
		t.Fatal("market and comparison raw data not preserved")
	}
	if len(snapshot.RawPages) != 4 {
		t.Fatalf("expected four raw pages, got %d", len(snapshot.RawPages))
	}
	if len(snapshot.Limitations) == 0 || !containsChineseLimitation(snapshot.Limitations, "Top20") ||
		!containsChineseLimitation(snapshot.Limitations, "未回显") || !containsChineseLimitation(snapshot.Limitations, "source_default") {
		t.Fatalf("missing required limitations: %#v", snapshot.Limitations)
	}
	for _, limitation := range snapshot.Limitations {
		if strings.Contains(limitation, "无股票的连板高度") || strings.Contains(limitation, "无成份股的板块") {
			t.Fatalf("non-empty source data received an empty limitation: %q", limitation)
		}
	}
}

func containsChineseLimitation(limitations []string, needle string) bool {
	for _, limitation := range limitations {
		if strings.Contains(limitation, needle) {
			return true
		}
	}
	return false
}

func TestFetchDayRealFixtureKeepsHighDaysAndContinueNumIndependent(t *testing.T) {
	fixtures := loadFixtures(t, "real")
	server := newFixtureServer(t, fixtures)
	snapshot, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError != nil {
		t.Fatalf("FetchDay failed: %v", fetchError)
	}
	stock := snapshot.Blocks[0].Stocks[2]
	if stock.Code != "002088" || stock.HighDays != "6天5板" || stock.ContinueNum != 4 {
		t.Fatalf("expected source high_days and continue_num to stay independent, got %#v", stock)
	}
	if snapshot.Ladder[0].Height != 5 || snapshot.Ladder[0].Stocks[0].ContinueNum != 5 {
		t.Fatalf("unexpected real ladder level: %#v", snapshot.Ladder[0])
	}
}

func TestFetchDayChecksRequestParameters(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	seen := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("date") != testDateDigits {
			t.Errorf("date parameter mismatch for %s: %s", request.URL.Path, query.Get("date"))
		}
		switch request.URL.Path {
		case blockTopPath, ladderPath:
			if query.Get("filter") != focus.ScopeDefault {
				t.Errorf("filter parameter mismatch for %s: %s", request.URL.Path, query.Get("filter"))
			}
		case comparisonPath:
			if query.Get("query_probability") != "0" {
				t.Errorf("query_probability mismatch: %s", query.Get("query_probability"))
			}
		}
		seen[request.URL.Path] = true
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case blockTopPath:
			_, _ = response.Write(fixtures.block)
		case ladderPath:
			_, _ = response.Write(fixtures.ladder)
		case marketPath:
			_, _ = response.Write(fixtures.market)
		case comparisonPath:
			_, _ = response.Write(fixtures.comparison)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	_, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError != nil {
		t.Fatalf("FetchDay failed: %v", fetchError)
	}
	if len(seen) != 4 {
		t.Fatalf("expected all four endpoints, saw %#v", seen)
	}
}

func TestFetchDayEmptyArraysAreExplicitAndLimited(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	fixtures.block = []byte(`{"status_code":0,"status_msg":"success","data":[]}`)
	fixtures.ladder = []byte(`{"status_code":0,"status_msg":"success","data":[]}`)
	fixtures.comparison = []byte(`{"status_code":0,"status_msg":"success","data":{"today_date":"2026-11-06","yesterday_date":"2026-11-05","board_list":[],"one_board":[]}}`)
	server := newFixtureServer(t, fixtures)
	snapshot, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError != nil {
		t.Fatalf("explicit empty arrays should be accepted with limitations: %v", fetchError)
	}
	if snapshot.Blocks == nil || snapshot.Ladder == nil || len(snapshot.Blocks) != 0 || len(snapshot.Ladder) != 0 {
		t.Fatalf("explicit empty arrays must stay non-nil: %#v", snapshot)
	}
	if !containsChineseLimitation(snapshot.Limitations, "显式空") {
		t.Fatalf("empty-array limitation missing: %#v", snapshot.Limitations)
	}
}

func TestFetchDayAcceptsExplicitEmptyBlock(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	fixtures.block = []byte(`{"status_code":0,"status_msg":"success","data":[{"code":"885001","name":"合成空板块","change":0.0,"limit_up_num":0,"stock_list":[]}]}`)
	server := newFixtureServer(t, fixtures)
	snapshot, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError != nil {
		t.Fatalf("explicit empty block should be accepted: %v", fetchError)
	}
	if len(snapshot.Blocks) != 1 || snapshot.Blocks[0].Stocks == nil || snapshot.Blocks[0].LimitUpCount != 0 {
		t.Fatalf("empty block was not preserved: %#v", snapshot.Blocks)
	}
	if !containsChineseLimitation(snapshot.Limitations, "无成份股的板块") {
		t.Fatalf("empty block limitation missing: %#v", snapshot.Limitations)
	}
}

func TestParseLadderEmptyLevelLimitationOnlyForActuallyEmptyLevel(t *testing.T) {
	nonEmptyPayload := json.RawMessage(`[{"height":2,"number":1,"code_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":2}]}]`)
	levels, limitations, parseError := parseLadder(nonEmptyPayload)
	if parseError != nil {
		t.Fatalf("non-empty ladder failed: %v", parseError)
	}
	if len(levels) != 1 || len(limitations) != 0 {
		t.Fatalf("non-empty ladder must not have empty limitations: levels=%d limitations=%#v", len(levels), limitations)
	}

	emptyLevelPayload := json.RawMessage(`[{"height":2,"number":0,"code_list":[]}]`)
	levels, limitations, parseError = parseLadder(emptyLevelPayload)
	if parseError != nil {
		t.Fatalf("explicit empty level failed: %v", parseError)
	}
	if len(levels) != 1 || !containsChineseLimitation(limitations, "无股票的连板高度") {
		t.Fatalf("empty level limitation missing: levels=%#v limitations=%#v", levels, limitations)
	}
}

func TestParseBlocksEmptyBlockLimitationOnlyForActuallyEmptyBlock(t *testing.T) {
	nonEmptyPayload := json.RawMessage(`[{"code":"885001","name":"合成板块","change":1.0,"limit_up_num":1,"stock_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":1,"reason_type":"理由","reason_info":"详情","high":"首板","change_rate":10.0,"first_limit_up_time":"1793928725","last_limit_up_time":"1793928725"}]}]`)
	blocks, limitations, parseError := parseBlocks(nonEmptyPayload, testDate)
	if parseError != nil {
		t.Fatalf("non-empty block failed: %v", parseError)
	}
	if len(blocks) != 1 || len(limitations) != 0 {
		t.Fatalf("non-empty block must not have empty limitations: blocks=%d limitations=%#v", len(blocks), limitations)
	}
}

func TestFetchDayTreatsZeroTimestampAsMissingAndChecksNonzero(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	fixtures.block = []byte(`{"status_code":0,"data":[{"code":"885001","name":"合成板块","change":1.0,"limit_up_num":1,"stock_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":1,"reason_type":"理由","reason_info":"详情","high":"首板","high_days":65537,"change_rate":10.0,"first_limit_up_time":0,"last_limit_up_time":null}]}]}`)
	server := newFixtureServer(t, fixtures)
	if _, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault); fetchError != nil {
		t.Fatalf("zero timestamp should be treated as missing: %v", fetchError)
	}
}

func TestFetchDayRejectsMissingArrays(t *testing.T) {
	cases := map[string]func(*fixtureResponses){
		"block null": func(fixtures *fixtureResponses) {
			fixtures.block = []byte(`{"status_code":0,"data":null}`)
		},
		"block env missing": func(fixtures *fixtureResponses) {
			fixtures.block = []byte(`{"status_code":0}`)
		},
		"ladder empty object": func(fixtures *fixtureResponses) {
			fixtures.ladder = []byte(`{"status_code":0,"data":{}}`)
		},
		"comparison missing board list": func(fixtures *fixtureResponses) {
			fixtures.comparison = []byte(`{"status_code":0,"data":{"today_date":"2026-11-06","yesterday_date":"2026-11-05","one_board":[]}}`)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fixtures := loadFixtures(t, "synthetic")
			mutate(&fixtures)
			server := newFixtureServer(t, fixtures)
			if _, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault); fetchError == nil {
				t.Fatal("expected missing explicit array to fail")
			}
		})
	}
}

func TestFetchDayRejectsBlockCountMismatchAndDuplicate(t *testing.T) {
	cases := map[string]string{
		"count mismatch":   `{"status_code":0,"data":[{"code":"885001","name":"合成板块","change":1.0,"limit_up_num":2,"stock_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":1,"reason_type":"理由","reason_info":"详情","high":"首板","high_days":65537,"change_rate":10.0,"first_limit_up_time":"1793928725","last_limit_up_time":"1793928725"}]}]}`,
		"duplicate stock":  `{"status_code":0,"data":[{"code":"885001","name":"合成板块","change":1.0,"limit_up_num":2,"stock_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":1,"reason_type":"理由","reason_info":"详情","high":"首板","high_days":65537,"change_rate":10.0,"first_limit_up_time":"1793928725","last_limit_up_time":"1793928725"},{"code":"000001","name":"合成银行","market_id":33,"continue_num":1,"reason_type":"理由","reason_info":"详情","high":"首板","high_days":65537,"change_rate":10.0,"first_limit_up_time":"1793928725","last_limit_up_time":"1793928725"}]}]}`,
		"fractional count": `{"status_code":0,"data":[{"code":"885001","name":"合成板块","change":1.0,"limit_up_num":1.5,"stock_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":1,"reason_type":"理由","reason_info":"详情","high":"首板","high_days":65537,"change_rate":10.0,"first_limit_up_time":"1793928725","last_limit_up_time":"1793928725"}]}]}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			fixtures := loadFixtures(t, "synthetic")
			fixtures.block = []byte(payload)
			server := newFixtureServer(t, fixtures)
			_, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
			if fetchError == nil {
				t.Fatal("expected block validation failure")
			}
			if name == "count mismatch" && !strings.Contains(fetchError.Error(), "limit_up_num") {
				t.Fatalf("unexpected count error: %v", fetchError)
			}
			if name == "duplicate stock" && !strings.Contains(fetchError.Error(), "duplicate stock") {
				t.Fatalf("unexpected duplicate error: %v", fetchError)
			}
			if name == "fractional count" && !strings.Contains(fetchError.Error(), "invalid limit_up_num") {
				t.Fatalf("unexpected fractional count error: %v", fetchError)
			}
		})
	}
}

func TestFetchDayRejectsLadderNumberMismatch(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	fixtures.ladder = []byte(`{"status_code":0,"data":[{"height":5,"number":2,"code_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":5}]}]}`)
	server := newFixtureServer(t, fixtures)
	_, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError == nil || !strings.Contains(fetchError.Error(), "ladder number") {
		t.Fatalf("expected ladder number mismatch, got %v", fetchError)
	}
}

func TestFetchDayRejectsDuplicateLadderStock(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	fixtures.ladder = []byte(`{"status_code":0,"data":[{"height":5,"number":2,"code_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":5},{"code":"000001","name":"合成银行","market_id":33,"continue_num":5}]}]}`)
	server := newFixtureServer(t, fixtures)
	_, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError == nil || !strings.Contains(fetchError.Error(), "duplicate stock") {
		t.Fatalf("expected duplicate ladder stock rejection, got %v", fetchError)
	}
}

func TestValidateLevelRejectsDuplicateLadderStockInDomain(t *testing.T) {
	level := focus.Level{
		Height: 2,
		Stocks: []focus.Stock{
			{Code: "000001", Name: "合成银行", MarketID: 33, ContinueNum: 2},
			{Code: "000001", Name: "合成银行", MarketID: 33, ContinueNum: 2},
		},
	}
	snapshot := validSnapshotForClient()
	snapshot.Ladder = []focus.Level{level}
	if err := focus.ValidateSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "duplicate stock") {
		t.Fatalf("domain validator accepted duplicate ladder stocks: %v", err)
	}
}

func TestFetchDayLadderHeightAndContinueNumAreIndependent(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	fixtures.ladder = []byte(`{"status_code":0,"data":[{"height":5,"number":2,"code_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":4},{"code":"600000","name":"合成浦发","market_id":17,"continue_num":5}]}]}`)
	server := newFixtureServer(t, fixtures)
	snapshot, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError != nil {
		t.Fatalf("height and continue_num are independent source fields: %v", fetchError)
	}
	if snapshot.Ladder[0].Height != 5 || snapshot.Ladder[0].Stocks[0].ContinueNum != 4 {
		t.Fatalf("unexpected level data: %#v", snapshot.Ladder[0])
	}
}

func TestFetchDayRejectsStockTimestampOtherDate(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	fixtures.block = []byte(`{"status_code":0,"data":[{"code":"885001","name":"合成板块","change":1.0,"limit_up_num":1,"stock_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":1,"reason_type":"理由","reason_info":"详情","high":"首板","high_days":65537,"change_rate":10.0,"first_limit_up_time":"1793928725","last_limit_up_time":"1794015125"}]}]}`)
	server := newFixtureServer(t, fixtures)
	_, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError == nil || !strings.Contains(fetchError.Error(), "does not match trade date") {
		t.Fatalf("expected stock timestamp date mismatch, got %v", fetchError)
	}
}

func TestFetchDayRejectsComparisonDateMismatchAndRollback(t *testing.T) {
	cases := map[string]string{
		"today mismatch":     `{"status_code":0,"data":{"today_date":"2026-11-05","yesterday_date":"2026-11-04","board_list":[],"one_board":[]}}`,
		"yesterday same day": `{"status_code":0,"data":{"today_date":"2026-11-06","yesterday_date":"2026-11-06","board_list":[],"one_board":[]}}`,
		"invalid yesterday":  `{"status_code":0,"data":{"today_date":"2026-11-06","yesterday_date":"2026/11/05","board_list":[],"one_board":[]}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			fixtures := loadFixtures(t, "synthetic")
			fixtures.comparison = []byte(payload)
			server := newFixtureServer(t, fixtures)
			_, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
			if fetchError == nil {
				t.Fatal("expected comparison date validation failure")
			}
		})
	}
}

func TestFetchDayAcceptsFridayAsMondayComparisonBase(t *testing.T) {
	fixtures := fixtureResponses{
		block:      []byte(`{"status_code":0,"data":[]}`),
		ladder:     []byte(`{"status_code":0,"data":[]}`),
		market:     loadFixture(t, "market_synthetic.json"),
		comparison: []byte(`{"status_code":0,"data":{"today_date":"2026-11-09","yesterday_date":"2026-11-06","board_list":[],"one_board":[]}}`),
	}
	server := newFixtureServer(t, fixtures)
	client := NewClient(server.URL, server.Client()).WithMinInterval(0).WithClock(fixedClock(time.Date(2026, 11, 9, 16, 0, 0, 0, focus.Shanghai())))
	if _, fetchError := client.FetchDay(context.Background(), "2026-11-09", focus.ScopeDefault); fetchError != nil {
		t.Fatalf("Friday should be accepted as the previous trading day for Monday: %v", fetchError)
	}
}

func TestFetchDayRejectsBusinessAndHTTPFailures(t *testing.T) {
	cases := map[string]func(http.ResponseWriter, *http.Request){
		"business status": func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(`{"status_code":-1,"status_msg":"date参数不合法","data":[]}`))
		},
		"http 500": func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusInternalServerError)
			_, _ = response.Write([]byte(`{"status_code":0,"data":[]}`))
		},
		"html": func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(`<html>not json</html>`))
		},
		"empty": func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusOK)
		},
		"missing data": func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(`{"status_code":0}`))
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(handler))
			defer server.Close()
			_, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
			if fetchError == nil {
				t.Fatal("expected request failure")
			}
		})
	}
}

func TestFetchDayRejectsMalformedMarketOverview(t *testing.T) {
	cases := map[string]string{
		"empty object":         `{"status_code":0,"data":{}}`,
		"missing rise_fall":    `{"status_code":0,"data":{"limit_up":{"now":"5"}}}`,
		"rise_fall not object": `{"status_code":0,"data":{"rise_fall":[]}}`,
		"missing rise":         `{"status_code":0,"data":{"rise_fall":{"fall":4741,"deuce":74,"limit_up":33,"limit_down":15}}}`,
		"non-integer fall":     `{"status_code":0,"data":{"rise_fall":{"rise":758,"fall":"many","deuce":74,"limit_up":33,"limit_down":15}}}`,
		"negative limit_down":  `{"status_code":0,"data":{"rise_fall":{"rise":758,"fall":4741,"deuce":74,"limit_up":33,"limit_down":-1}}}`,
	}
	for name, market := range cases {
		t.Run(name, func(t *testing.T) {
			fixtures := loadFixtures(t, "synthetic")
			fixtures.market = []byte(market)
			server := newFixtureServer(t, fixtures)
			if _, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault); fetchError == nil {
				t.Fatal("malformed market overview was accepted")
			}
		})
	}
}

func TestFetchDayRejectsOversizeTimestampText(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	timestamp := strings.Repeat("0", focus.MaxTimestampBytes+1)
	fixtures.block = []byte(`{"status_code":0,"data":[{"code":"885001","name":"合成板块","change":1.0,"limit_up_num":1,"stock_list":[{"code":"000001","name":"合成银行","market_id":33,"continue_num":1,"reason_type":"理由","reason_info":"详情","high":"首板","change_rate":10.0,"first_limit_up_time":"` + timestamp + `","last_limit_up_time":"1793928725"}]}]}`)
	server := newFixtureServer(t, fixtures)
	_, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError == nil || !strings.Contains(fetchError.Error(), "exceeds the size limit") {
		t.Fatalf("oversize timestamp text was accepted: %v", fetchError)
	}
}

func TestFetchDayRejectsOversizeBody(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	oversize := append([]byte(`{"status_code":0,"data":"`), []byte(strings.Repeat("x", focus.MaxRawPageBytes+1))...)
	oversize = append(oversize, []byte(`"}`)...)
	fixtures.block = oversize
	server := newFixtureServer(t, fixtures)
	_, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError == nil || !strings.Contains(fetchError.Error(), "body limit") {
		t.Fatalf("expected body limit rejection, got %v", fetchError)
	}
}

func TestFetchDayValidatesDateScopeAndClose(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	server := newFixtureServer(t, fixtures)
	cases := map[string]struct {
		client *Client
		date   string
		scope  string
	}{
		"future": {
			client: newTestClient(server),
			date:   "2026-11-07",
			scope:  focus.ScopeDefault,
		},
		"today before close": {
			client: NewClient(server.URL, server.Client()).WithMinInterval(0).WithClock(fixedClock(time.Date(2026, 11, 6, 15, 29, 0, 0, focus.Shanghai()))),
			date:   testDate,
			scope:  focus.ScopeDefault,
		},
		"invalid date": {
			client: newTestClient(server),
			date:   "2026/11/06",
			scope:  focus.ScopeDefault,
		},
		"invalid scope": {
			client: newTestClient(server),
			date:   testDate,
			scope:  "HS",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, fetchError := testCase.client.FetchDay(context.Background(), testCase.date, testCase.scope); fetchError == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestFetchDayCancellation(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	server := newFixtureServer(t, fixtures)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, fetchError := newTestClient(server).FetchDay(ctx, testDate, focus.ScopeDefault)
	if !errors.Is(fetchError, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", fetchError)
	}
}

func TestFetchDayMinIntervalWaitsBetweenRequests(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	server := newFixtureServer(t, fixtures)
	client := NewClient(server.URL, server.Client()).WithMinInterval(35 * time.Millisecond).WithClock(fixedClock(mockNow()))
	start := time.Now()
	if _, fetchError := client.FetchDay(context.Background(), testDate, focus.ScopeDefault); fetchError != nil {
		t.Fatalf("FetchDay failed: %v", fetchError)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("expected three interval waits, elapsed %s", elapsed)
	}
}

func TestClientDoesNotSendCookieOrHexinHeader(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	var mu sync.Mutex
	var seenHeaders []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		seenHeaders = append(seenHeaders, request.Header.Clone())
		mu.Unlock()
		var body []byte
		switch request.URL.Path {
		case blockTopPath:
			body = fixtures.block
		case ladderPath:
			body = fixtures.ladder
		case marketPath:
			body = fixtures.market
		case comparisonPath:
			body = fixtures.comparison
		}
		_, _ = response.Write(body)
	}))
	defer server.Close()
	if _, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault); fetchError != nil {
		t.Fatalf("FetchDay failed: %v", fetchError)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, header := range seenHeaders {
		if header.Get("Cookie") != "" || header.Get("hexin-v") != "" {
			t.Fatalf("unexpected credential header: %v", header)
		}
	}
}

func TestFetchDayPartialFailureReturnsNoSnapshot(t *testing.T) {
	fixtures := loadFixtures(t, "synthetic")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case blockTopPath:
			_, _ = response.Write(fixtures.block)
		case ladderPath:
			_, _ = response.Write(fixtures.ladder)
		case marketPath:
			_, _ = response.Write(fixtures.market)
		case comparisonPath:
			response.WriteHeader(http.StatusBadGateway)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	snapshot, fetchError := newTestClient(server).FetchDay(context.Background(), testDate, focus.ScopeDefault)
	if fetchError == nil {
		t.Fatal("partial failure must not return a complete snapshot")
	}
	if len(snapshot.RawPages) != 0 || len(snapshot.Blocks) != 0 || len(snapshot.Ladder) != 0 {
		t.Fatalf("partial failure returned snapshot data: %#v", snapshot)
	}
}

func TestFetchDayJSONBodyIsReadable(t *testing.T) {
	payload := loadFixture(t, "block_real.json")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, string(payload))
	}))
	defer server.Close()
	client := newTestClient(server)
	_, _, fetchError := client.fetchJSON(context.Background(), blockTopPath, nil)
	if fetchError != nil {
		t.Fatalf("fetchJSON failed: %v", fetchError)
	}
}

func validSnapshotForClient() focus.Snapshot {
	return focus.Snapshot{
		TradeDate:          testDate,
		Source:             focus.Source,
		Scope:              focus.ScopeDefault,
		MarketScope:        focus.MarketScope,
		FetchedAt:          mockNow(),
		Blocks:             []focus.Block{},
		Ladder:             []focus.Level{{Height: 2, Stocks: []focus.Stock{{Code: "000001", Name: "合成银行", MarketID: 33, ContinueNum: 2}}}},
		Market:             json.RawMessage(`{"rise_fall":{"rise":1000,"fall":1000,"deuce":100,"limit_up":10,"limit_down":1}}`),
		Comparison:         json.RawMessage(`{"today_date":"2026-11-06"}`),
		SourceDateVerified: false,
		Limitations:        []string{"合成限制"},
		RawPages: []json.RawMessage{
			json.RawMessage(`{"status_code":0,"data":[]}`),
			json.RawMessage(`{"status_code":0,"data":[]}`),
			json.RawMessage(`{"status_code":0,"data":{}}`),
			json.RawMessage(`{"status_code":0,"data":{}}`),
		},
	}
}

func TestFixturesContainNoCredentialHeaders(t *testing.T) {
	entries, readError := os.ReadDir("testdata")
	if readError != nil {
		t.Fatalf("read testdata: %v", readError)
	}
	for _, entry := range entries {
		content, readFileError := os.ReadFile(filepath.Join("testdata", entry.Name()))
		if readFileError != nil {
			t.Fatalf("read fixture: %v", readFileError)
		}
		text := string(content)
		for _, marker := range []string{"Cookie", "hexin-v", "token", "user_id"} {
			if strings.Contains(strings.ToLower(text), strings.ToLower(marker)) {
				t.Fatalf("fixture %s contains credential marker %q", entry.Name(), marker)
			}
		}
		if !json.Valid(content) {
			t.Fatalf("fixture %s is not valid JSON", entry.Name())
		}
	}
}
