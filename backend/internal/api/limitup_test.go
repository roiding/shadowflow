package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/limitup"
	"github.com/roiding/shadowflow/internal/repository"
)

func TestLimitUpAPIsReadIndependentEvents(testingT *testing.T) {
	server, store := testServer(testingT, "")
	defer store.Close()
	server, err := New(store, server.calendar, server.logger, Options{ScanRatePerMinute: 10000})
	if err != nil {
		testingT.Fatal(err)
	}
	empty := httptest.NewRecorder()
	server.Handler().ServeHTTP(empty, httptest.NewRequest(http.MethodGet, "/api/v1/limit-up/overview?as_of=2026-08-03", nil))
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"observed_days":0`) || !strings.Contains(empty.Body.String(), `"stocks":[]`) {
		testingT.Fatalf("empty source must not invent data: %d %s", empty.Code, empty.Body.String())
	}
	snapshot := limitup.Snapshot{
		TradeDate: "2026-08-03", Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt:   time.Date(2026, 8, 3, 16, 0, 0, 0, limitup.Shanghai()),
		SealedCount: 2, TouchedCount: 2,
		Events: []limitup.Event{
			{TradeDate: "2026-08-03", Code: "000001", Name: "甲", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{TradeDate: "2026-08-03", Code: "000002", Name: "乙", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
		},
		RawPages: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
	if err := store.SaveLimitUpSnapshot(context.Background(), snapshot); err != nil {
		testingT.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limit-up/stocks/000001?as_of=2026-08-03&min_overlap=1", nil))
	var result struct {
		Data limitup.Analysis `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		testingT.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Data.Profile.TouchDays != 1 || len(result.Data.Links) != 1 ||
		result.Data.Links[0].Code != "000002" || result.Data.Links[0].Phi != nil || result.Data.Events[0].OpenCount != nil {
		testingT.Fatalf("unexpected result without OHLC/relations archives: %d %+v", response.Code, result)
	}
	for target, status := range map[string]int{
		"/api/v1/limit-up/stocks/000003?as_of=2026-08-03":           http.StatusNotFound,
		"/api/v1/limit-up/stocks/000001?as_of=2026-08-02":           http.StatusNotFound,
		"/api/v1/limit-up/stocks/ABC001":                            http.StatusBadRequest,
		"/api/v1/limit-up/overview?days=367":                        http.StatusBadRequest,
		"/api/v1/limit-up/overview?days=0":                          http.StatusBadRequest,
		"/api/v1/limit-up/overview?days=30&days=90":                 http.StatusBadRequest,
		"/api/v1/limit-up/overview?scope=HS":                        http.StatusBadRequest,
		"/api/v1/limit-up/stocks/000001?event=minute":               http.StatusBadRequest,
		"/api/v1/limit-up/stocks/000001?as_of=2026-08-03&limit=101": http.StatusBadRequest,
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != status {
			testingT.Fatalf("%s: got %d, want %d: %s", target, response.Code, status, response.Body.String())
		}
	}
}

func TestLimitUpAPIsRequireConfiguredToken(testingT *testing.T) {
	server, store := testServer(testingT, "")
	defer store.Close()
	token := "limit-up-research-test-token"
	server, err := New(store, server.calendar, server.logger, Options{APIToken: token})
	if err != nil {
		testingT.Fatal(err)
	}
	for _, path := range []string{"/api/v1/limit-up/overview", "/api/v1/limit-up/stocks/000001"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			testingT.Fatalf("research route must require bearer token: %s got %d", path, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/limit-up/overview", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		testingT.Fatalf("valid token rejected: %d %s", response.Code, response.Body.String())
	}
}

func TestLimitUpDownOnlyStockAPI(testingT *testing.T) {
	server, store := testServer(testingT, "")
	defer store.Close()
	snapshot := limitup.Snapshot{
		TradeDate: "2026-08-03", Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt: time.Date(2026, 8, 3, 16, 0, 0, 0, limitup.Shanghai()), DownCount: 2,
		Events: []limitup.Event{
			{TradeDate: "2026-08-03", Code: "000003", Name: "丙", MarketID: 33, MarketType: "HS", Pool: limitup.PoolDown},
			{TradeDate: "2026-08-03", Code: "000004", Name: "丁", MarketID: 33, MarketType: "HS", Pool: limitup.PoolDown},
		},
		RawPages: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
	if err := store.SaveLimitUpSnapshot(context.Background(), snapshot); err != nil {
		testingT.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limit-up/stocks/000003?as_of=2026-08-03&event=down&min_overlap=1", nil))
	var result struct {
		Data limitup.Analysis `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		testingT.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Data.Event != limitup.EventDown || result.Data.Profile.DownDays != 1 ||
		result.Data.Profile.SealRate != nil || len(result.Data.Links) != 1 || result.Data.Links[0].Code != "000004" {
		testingT.Fatalf("invalid down-only API result: %d %s", response.Code, response.Body.String())
	}
}

func TestLimitUpQualityDistinguishesStates(testingT *testing.T) {
	server, store := testServer(testingT, "")
	defer store.Close()
	ctx := context.Background()

	server.limitUpArchiveScheduled = false
	quality := readLimitUpQuality(testingT, server, "2026-08-03")
	if quality.Status != limitup.ArchiveStatusDisabled || quality.Scheduled {
		testingT.Fatalf("disabled state was not explicit: %+v", quality)
	}

	server.limitUpArchiveScheduled = true
	quality = readLimitUpQuality(testingT, server, "2026-08-03")
	if quality.Status != limitup.ArchiveStatusNotCollected || !quality.Scheduled {
		testingT.Fatalf("not_collected state was not explicit: %+v", quality)
	}

	startedAt := time.Date(2026, 8, 3, 16, 0, 0, 0, limitup.Shanghai())
	run := repository.CollectionRun{
		RunID: "quality-failed", SnapshotAt: startedAt, SnapshotKind: graymarket.SnapshotLimitUpEvents,
		RankType: graymarket.RankStock, Status: repository.RunRunning, RequestedDate: "2026-08-03",
		AttemptCount: 1, StartedAt: startedAt,
	}
	if err := store.StartLimitUpRun(ctx, run, limitup.ScopeDefault); err != nil {
		testingT.Fatal(err)
	}
	finishedAt := startedAt.Add(time.Minute)
	run.Status, run.FinishedAt = repository.RunFailed, &finishedAt
	run.ErrorCode, run.ErrorMessage = "upstream_error", "source unavailable"
	if err := store.FinishRun(ctx, run); err != nil {
		testingT.Fatal(err)
	}
	quality = readLimitUpQuality(testingT, server, "2026-08-03")
	if quality.Status != limitup.ArchiveStatusFailed || quality.LastRun == nil || quality.LastRun.ErrorCode != "upstream_error" {
		testingT.Fatalf("failed state or run details were lost: %+v", quality)
	}

	completeAt := time.Date(2026, 8, 4, 16, 0, 0, 0, limitup.Shanghai())
	completeSnapshot := limitup.Snapshot{
		TradeDate: "2026-08-04", Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt: completeAt, RawPages: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
	if err := store.SaveLimitUpSnapshot(ctx, completeSnapshot); err != nil {
		testingT.Fatal(err)
	}
	quality = readLimitUpQuality(testingT, server, "2026-08-04")
	if quality.Status != limitup.ArchiveStatusComplete || !quality.Complete || quality.Day == nil || quality.Day.DownCount != 0 {
		testingT.Fatalf("complete zero-event day was not published: %+v", quality)
	}
	server.limitUpArchiveScheduled = false
	quality = readLimitUpQuality(testingT, server, "2026-08-04")
	if quality.Status != limitup.ArchiveStatusComplete {
		testingT.Fatalf("published archive must remain complete when automatic scheduling is off: %+v", quality)
	}
}

func readLimitUpQuality(testingT *testing.T, server *Server, tradeDate string) limitup.ArchiveQuality {
	testingT.Helper()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/limit-up/quality?trade_date="+tradeDate, nil))
	if response.Code != http.StatusOK {
		testingT.Fatalf("quality request failed: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Data limitup.ArchiveQuality `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		testingT.Fatal(err)
	}
	return result.Data
}
