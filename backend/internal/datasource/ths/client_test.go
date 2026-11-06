package ths

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
)

func fixedClock(targetTime time.Time) func() time.Time {
	return func() time.Time {
		return targetTime
	}
}

func loadFixtureBytes(testingT *testing.T, filename string) []byte {
	testingT.Helper()
	fixturePath := filepath.Join("testdata", filename)
	contentBytes, readError := os.ReadFile(fixturePath)
	if readError != nil {
		testingT.Fatalf("failed to read fixture %s: %v", filename, readError)
	}
	return contentBytes
}

func newPoolServer(
	testingT *testing.T,
	sealedPages map[string][]byte,
	brokenPage []byte,
	downPage []byte,
	tradeDayPage []byte,
) *httptest.Server {
	testingT.Helper()
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		requestPath := httpRequest.URL.Path
		responseWriter.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(requestPath, "/limit_up_pool"):
			pageBytes, exists := sealedPages[httpRequest.URL.Query().Get("page")]
			if !exists {
				responseWriter.WriteHeader(http.StatusNotFound)
				return
			}
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(pageBytes)
		case strings.HasSuffix(requestPath, "/open_limit_pool"):
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(brokenPage)
		case strings.HasSuffix(requestPath, "/lower_limit_pool"):
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(downPage)
		case strings.HasSuffix(requestPath, "/trade_day"):
			if tradeDayPage == nil {
				responseWriter.WriteHeader(http.StatusNotFound)
				return
			}
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(tradeDayPage)
		default:
			responseWriter.WriteHeader(http.StatusNotFound)
		}
	}))
	testingT.Cleanup(testServer.Close)
	return testServer
}

func TestFetchDay_Success(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")
	sealedPageTwoBytes := loadFixtureBytes(testingT, "sealed_page2.json")
	brokenPageOneBytes := loadFixtureBytes(testingT, "broken_page1.json")
	downPageOneBytes := loadFixtureBytes(testingT, "down_page1.json")

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		requestPath := httpRequest.URL.Path
		requestQuery := httpRequest.URL.Query()
		pageParam := requestQuery.Get("page")

		responseWriter.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(requestPath, "/limit_up_pool") && pageParam == "1":
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(sealedPageOneBytes)
		case strings.HasSuffix(requestPath, "/limit_up_pool") && pageParam == "2":
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(sealedPageTwoBytes)
		case strings.HasSuffix(requestPath, "/open_limit_pool") && pageParam == "1":
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(brokenPageOneBytes)
		case strings.HasSuffix(requestPath, "/lower_limit_pool") && pageParam == "1":
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(downPageOneBytes)
		default:
			responseWriter.WriteHeader(http.StatusNotFound)
		}
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	snapshot, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError != nil {
		testingT.Fatalf("expected FetchDay success, got %v", fetchError)
	}

	if snapshot.TradeDate != "2026-11-05" {
		testingT.Errorf("expected TradeDate 2026-11-05, got %s", snapshot.TradeDate)
	}
	if snapshot.Source != limitup.SourceTHS {
		testingT.Errorf("expected Source %s, got %s", limitup.SourceTHS, snapshot.Source)
	}
	if snapshot.Scope != limitup.ScopeDefault {
		testingT.Errorf("expected Scope %s, got %s", limitup.ScopeDefault, snapshot.Scope)
	}
	if snapshot.SealedCount != 3 {
		testingT.Errorf("expected SealedCount 3, got %d", snapshot.SealedCount)
	}
	if snapshot.BrokenCount != 2 {
		testingT.Errorf("expected BrokenCount 2, got %d", snapshot.BrokenCount)
	}
	if snapshot.DownCount != 2 {
		testingT.Errorf("expected DownCount 2, got %d", snapshot.DownCount)
	}
	if snapshot.TouchedCount != 5 {
		testingT.Errorf("expected TouchedCount 5, got %d", snapshot.TouchedCount)
	}
	if len(snapshot.Events) != 7 {
		testingT.Errorf("expected 7 events, got %d", len(snapshot.Events))
	}
	if len(snapshot.RawPages) != 4 {
		testingT.Errorf("expected 4 raw pages, got %d", len(snapshot.RawPages))
	}

	firstEvent := snapshot.Events[0]
	if firstEvent.Code != "003033" || firstEvent.Name != "征和工业" || firstEvent.MarketID != 33 {
		testingT.Errorf("unexpected first event: %+v", firstEvent)
	}
	if firstEvent.OpenCount == nil || *firstEvent.OpenCount != 12 {
		testingT.Errorf("expected OpenCount 12, got %v", firstEvent.OpenCount)
	}
	if firstEvent.Pool != limitup.PoolSealed {
		testingT.Errorf("expected pool %s, got %s", limitup.PoolSealed, firstEvent.Pool)
	}
	if firstEvent.FirstLimitAt == nil || firstEvent.FirstLimitAt.In(limitup.Shanghai()).Format("15:04:05") != "09:46:45" {
		testingT.Errorf("unexpected FirstLimitAt: %v", firstEvent.FirstLimitAt)
	}
	if firstEvent.LastLimitAt == nil || firstEvent.LastLimitAt.In(limitup.Shanghai()).Format("15:04:05") != "14:56:03" {
		testingT.Errorf("unexpected LastLimitAt: %v", firstEvent.LastLimitAt)
	}

	thirdEvent := snapshot.Events[2]
	if thirdEvent.Code != "603313" || thirdEvent.OpenCount != nil {
		testingT.Errorf("expected stock 603313 with nil OpenCount, got %+v", thirdEvent)
	}

	fourthEvent := snapshot.Events[3]
	if fourthEvent.Code != "001337" || fourthEvent.Pool != limitup.PoolBroken {
		testingT.Errorf("expected broken stock 001337, got %+v", fourthEvent)
	}

	fifthEvent := snapshot.Events[5]
	if fifthEvent.Code != "600001" || fifthEvent.Pool != limitup.PoolDown {
		testingT.Errorf("expected down stock 600001, got %+v", fifthEvent)
	}
	if fifthEvent.FirstLimitAt != nil || fifthEvent.LastLimitAt != nil || fifthEvent.OpenCount != nil {
		testingT.Errorf("expected null limit times and open count for down stock, got %+v", fifthEvent)
	}
	if fifthEvent.ChangePercent == nil || *fifthEvent.ChangePercent != -10.0 {
		testingT.Errorf("expected down stock change rate -10, got %v", fifthEvent.ChangePercent)
	}

	if validateError := limitup.ValidateSnapshot(snapshot); validateError != nil {
		testingT.Fatalf("limitup.ValidateSnapshot rejected snapshot: %v", validateError)
	}
}

func TestFetchDay_EmptyTradingDay(testingT *testing.T) {
	emptyPoolBytes := loadFixtureBytes(testingT, "empty_pool.json")
	emptyDownBytes := loadFixtureBytes(testingT, "empty_pool_low.json")
	tradeDayTrueBytes := loadFixtureBytes(testingT, "trade_day_true.json")

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		requestPath := httpRequest.URL.Path
		responseWriter.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(requestPath, "/limit_up_pool") || strings.HasSuffix(requestPath, "/open_limit_pool"):
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(emptyPoolBytes)
		case strings.HasSuffix(requestPath, "/lower_limit_pool"):
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(emptyDownBytes)
		case strings.HasSuffix(requestPath, "/trade_day"):
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(tradeDayTrueBytes)
		default:
			responseWriter.WriteHeader(http.StatusNotFound)
		}
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithClock(fixedClock(mockNow))

	snapshot, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError != nil {
		testingT.Fatalf("expected empty day success, got %v", fetchError)
	}

	if snapshot.SealedCount != 0 || snapshot.BrokenCount != 0 || snapshot.DownCount != 0 || snapshot.TouchedCount != 0 {
		testingT.Errorf("expected 0 counts, got (%d, %d, %d, %d)", snapshot.SealedCount, snapshot.BrokenCount, snapshot.DownCount, snapshot.TouchedCount)
	}
	if len(snapshot.Events) != 0 {
		testingT.Errorf("expected empty events slice, got len %d", len(snapshot.Events))
	}
	if len(snapshot.RawPages) != 3 {
		testingT.Errorf("expected 3 raw pages for empty day, got %d", len(snapshot.RawPages))
	}

	if validateError := limitup.ValidateSnapshot(snapshot); validateError != nil {
		testingT.Fatalf("limitup.ValidateSnapshot rejected empty snapshot: %v", validateError)
	}
}

func TestFetchDay_DownPoolUsesFinalLimitDownCount(testingT *testing.T) {
	withZeroDownSummary := func(fixture string, emptyDownPool bool) []byte {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(loadFixtureBytes(testingT, fixture), &envelope); err != nil {
			testingT.Fatal(err)
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(envelope["data"], &data); err != nil {
			testingT.Fatal(err)
		}
		data["limit_down_count"] = json.RawMessage(`{"today":{"num":0,"open_num":4,"history_num":4}}`)
		if emptyDownPool {
			data["page"] = json.RawMessage(`{"limit":2,"total":0,"count":0,"page":0}`)
			data["info"] = json.RawMessage(`[]`)
		}
		dataBytes, err := json.Marshal(data)
		if err != nil {
			testingT.Fatal(err)
		}
		envelope["data"] = dataBytes
		result, err := json.Marshal(envelope)
		if err != nil {
			testingT.Fatal(err)
		}
		return result
	}
	sealedPageOneBytes := withZeroDownSummary("sealed_page1.json", false)
	sealedPageTwoBytes := withZeroDownSummary("sealed_page2.json", false)
	brokenPageBytes := withZeroDownSummary("broken_page1.json", false)
	downWithNoFinalLimitDown := withZeroDownSummary("down_page1.json", true)
	testServer := newPoolServer(testingT, map[string][]byte{"1": sealedPageOneBytes, "2": sealedPageTwoBytes}, brokenPageBytes, downWithNoFinalLimitDown, nil)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	snapshot, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError != nil {
		testingT.Fatalf("expected zero final limit-down stocks to succeed, got %v", fetchError)
	}
	if snapshot.DownCount != 0 {
		testingT.Errorf("expected DownCount 0 from limit_down_count.today.num, got %d", snapshot.DownCount)
	}
	if len(snapshot.RawPages) != 4 {
		testingT.Errorf("expected all four evidence pages to be retained, got %d", len(snapshot.RawPages))
	}
}

func TestFetchDay_EmptyPoolNeedsExplicitArray(testingT *testing.T) {
	for _, replacement := range []string{`"info": null`, `"missing_info": []`} {
		testingT.Run(replacement, func(testingT *testing.T) {
			payload := strings.Replace(string(loadFixtureBytes(testingT, "empty_pool.json")), `"info": []`, replacement, 1)
			testServer := newPoolServer(testingT, map[string][]byte{"1": []byte(payload)}, nil, nil, nil)
			client := NewClient(testServer.URL, testServer.Client()).WithMinInterval(0).
				WithClock(fixedClock(time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())))
			if _, err := client.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault); err == nil || !strings.Contains(err.Error(), "explicit info array") {
				testingT.Fatalf("missing/null info must not count as a verified empty pool: %v", err)
			}
		})
	}
}

func TestFetchDay_EmptyPoolRejectsNonZeroPageCount(testingT *testing.T) {
	payload := strings.Replace(string(loadFixtureBytes(testingT, "empty_pool.json")), `"count": 0`, `"count": 1`, 1)
	testServer := newPoolServer(testingT, map[string][]byte{"1": []byte(payload)}, nil, nil, nil)
	client := NewClient(testServer.URL, testServer.Client()).WithMinInterval(0).
		WithClock(fixedClock(time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())))
	if _, err := client.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault); err == nil || !strings.Contains(err.Error(), "empty pool limit_up has page count 1") {
		testingT.Fatalf("zero total with a non-zero page count must be rejected: %v", err)
	}
}

func TestFetchDay_RejectsOversizeBody(testingT *testing.T) {
	payload := []byte(`{}` + strings.Repeat(" ", maxBodyLimit))
	testServer := newPoolServer(testingT, map[string][]byte{"1": payload}, nil, nil, nil)
	client := NewClient(testServer.URL, testServer.Client()).WithMinInterval(0).
		WithClock(fixedClock(time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())))
	if _, err := client.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault); err == nil || !strings.Contains(err.Error(), "body limit") {
		testingT.Fatalf("response truncation must not become accepted JSON: %v", err)
	}
}

func TestFetchDay_NonTradingDayRejected(testingT *testing.T) {
	emptyPoolBytes := loadFixtureBytes(testingT, "empty_pool.json")
	emptyDownBytes := loadFixtureBytes(testingT, "empty_pool_low.json")
	tradeDayFalseBytes := loadFixtureBytes(testingT, "trade_day_false.json")

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		requestPath := httpRequest.URL.Path
		responseWriter.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(requestPath, "/limit_up_pool") || strings.HasSuffix(requestPath, "/open_limit_pool"):
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(emptyPoolBytes)
		case strings.HasSuffix(requestPath, "/lower_limit_pool"):
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(emptyDownBytes)
		case strings.HasSuffix(requestPath, "/trade_day"):
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(tradeDayFalseBytes)
		default:
			responseWriter.WriteHeader(http.StatusNotFound)
		}
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected non-trading day error, got nil")
	}
	if !errors.Is(fetchError, limitup.ErrInvalid) {
		testingT.Errorf("expected ErrInvalid wrapping, got %v", fetchError)
	}
}

func TestFetchDay_DuplicatePage(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write(sealedPageOneBytes)
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected duplicate page error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "page number mismatch") {
		testingT.Errorf("expected page number mismatch error, got %v", fetchError)
	}
}

func TestFetchDay_UnstablePageTotal(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")
	pageTwoCorrupted := strings.Replace(string(loadFixtureBytes(testingT, "sealed_page2.json")), `"total": 3`, `"total": 4`, 1)

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		pageParam := httpRequest.URL.Query().Get("page")
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusOK)
		if pageParam == "1" {
			_, _ = responseWriter.Write(sealedPageOneBytes)
		} else {
			_, _ = responseWriter.Write([]byte(pageTwoCorrupted))
		}
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected unstable total error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "unstable page total") {
		testingT.Errorf("expected unstable page total error, got %v", fetchError)
	}
}

func TestFetchDay_DateRollback(testingT *testing.T) {
	pageWithDifferentDate := strings.Replace(string(loadFixtureBytes(testingT, "sealed_page1.json")), `"date": 20261105`, `"date": 20261104`, 1)

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(pageWithDifferentDate))
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected date mismatch error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "date mismatch") {
		testingT.Errorf("expected date mismatch error, got %v", fetchError)
	}
}

func TestFetchDay_NullCounts(testingT *testing.T) {
	pageWithNullCount := strings.Replace(string(loadFixtureBytes(testingT, "sealed_page1.json")), `"limit_up_count": {`, `"limit_up_count": null, "_ignored": {`, 1)

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(pageWithNullCount))
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected null limit_up_count error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "null or missing limit_up_count") {
		testingT.Errorf("expected null count error, got %v", fetchError)
	}
}

func TestFetchDay_HTMLResponse(testingT *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte("<!DOCTYPE html><html><body>Access Denied</body></html>"))
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected html response error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "ths returned html") {
		testingT.Errorf("expected html detection error, got %v", fetchError)
	}
}

func TestFetchDay_NonZeroStatusCode(testingT *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status_code": -1, "status_msg": "date参数不合法"}`))
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected business error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "business error code -1") {
		testingT.Errorf("expected status_code -1 error, got %v", fetchError)
	}
}

func TestFetchDay_ContextCanceled(testingT *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		time.Sleep(50 * time.Millisecond)
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithClock(fixedClock(mockNow))

	cancelCtx, cancelFunc := context.WithCancel(context.Background())
	cancelFunc()

	_, fetchError := thsClient.FetchDay(cancelCtx, "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected context canceled error, got nil")
	}
	if !errors.Is(fetchError, context.Canceled) {
		testingT.Errorf("expected context.Canceled, got %v", fetchError)
	}
}

func TestFetchDay_FutureDate(testingT *testing.T) {
	mockNow := time.Date(2026, 11, 5, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient("http://localhost", nil).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-06", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected future date error, got nil")
	}
	if !errors.Is(fetchError, limitup.ErrInvalid) {
		testingT.Errorf("expected ErrInvalid wrapping, got %v", fetchError)
	}
}

func TestFetchDay_SameDayBefore1530(testingT *testing.T) {
	mockNow := time.Date(2026, 11, 5, 14, 30, 0, 0, limitup.Shanghai())
	thsClient := NewClient("http://localhost", nil).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected before 15:30 error, got nil")
	}
	if !errors.Is(fetchError, limitup.ErrInvalid) {
		testingT.Errorf("expected ErrInvalid wrapping, got %v", fetchError)
	}
}

func TestFetchDay_SameDayMarketNotClosed(testingT *testing.T) {
	pageWithTradingStatus := strings.Replace(string(loadFixtureBytes(testingT, "sealed_page1.json")), `"id": "closed"`, `"id": "morning_trade"`, 1)

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(pageWithTradingStatus))
	}))
	defer testServer.Close()

	mockNow := time.Date(2026, 11, 5, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected market not closed error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "market is not closed") {
		testingT.Errorf("expected market not closed error, got %v", fetchError)
	}
}

func TestFetchDay_StockPoolOverlap(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")
	sealedPageTwoBytes := loadFixtureBytes(testingT, "sealed_page2.json")
	brokenWithOverlap := strings.Replace(string(loadFixtureBytes(testingT, "broken_page1.json")), `"001337"`, `"003033"`, 1)
	downPageBytes := loadFixtureBytes(testingT, "down_page1.json")
	testServer := newPoolServer(
		testingT,
		map[string][]byte{"1": sealedPageOneBytes, "2": sealedPageTwoBytes},
		[]byte(brokenWithOverlap),
		downPageBytes,
		nil,
	)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(100).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected overlap error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "conflicts with sealed pool") {
		testingT.Errorf("expected overlap error, got %v", fetchError)
	}
}

func TestFetchDay_BrokenAndDownOverlapAllowed(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")
	sealedPageTwoBytes := loadFixtureBytes(testingT, "sealed_page2.json")
	brokenPageBytes := loadFixtureBytes(testingT, "broken_page1.json")
	downWithOverlap := strings.Replace(
		string(loadFixtureBytes(testingT, "down_page1.json")),
		`"600001"`,
		`"001337"`,
		1,
	)
	testServer := newPoolServer(testingT, map[string][]byte{"1": sealedPageOneBytes, "2": sealedPageTwoBytes}, brokenPageBytes, []byte(downWithOverlap), nil)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	snapshot, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError != nil {
		testingT.Fatalf("expected broken and limit-down overlap to succeed, got %v", fetchError)
	}
	if snapshot.TouchedCount != 5 || snapshot.DownCount != 2 || len(snapshot.Events) != 7 {
		testingT.Errorf("unexpected counts with broken/down overlap: touched=%d down=%d events=%d", snapshot.TouchedCount, snapshot.DownCount, len(snapshot.Events))
	}
	seenPools := make(map[string]bool)
	for _, event := range snapshot.Events {
		if event.Code == "001337" {
			seenPools[event.Pool] = true
		}
	}
	if !seenPools[limitup.PoolBroken] || !seenPools[limitup.PoolDown] {
		testingT.Errorf("expected both broken and limit-down records for 001337, got %v", seenPools)
	}
}

func TestFetchDay_SealedAndDownOverlapRejected(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")
	sealedPageTwoBytes := loadFixtureBytes(testingT, "sealed_page2.json")
	brokenPageBytes := loadFixtureBytes(testingT, "broken_page1.json")
	downWithOverlap := strings.Replace(
		string(loadFixtureBytes(testingT, "down_page1.json")),
		`"600001"`,
		`"003033"`,
		1,
	)
	testServer := newPoolServer(testingT, map[string][]byte{"1": sealedPageOneBytes, "2": sealedPageTwoBytes}, brokenPageBytes, []byte(downWithOverlap), nil)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected sealed and limit-down overlap error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "conflicts with sealed pool") {
		testingT.Errorf("expected sealed pool conflict, got %v", fetchError)
	}
}

func TestFetchDay_NumericStringCompatibility(testingT *testing.T) {
	pageRaw := loadFixtureBytes(testingT, "sealed_page1.json")
	modifiedString := strings.Replace(string(pageRaw), `"market_id": 33`, `"market_id": "33"`, 1)
	modifiedString = strings.Replace(modifiedString, `"open_num": 12`, `"open_num": "12"`, 1)
	modifiedString = strings.Replace(modifiedString, `"first_limit_up_time": "1793843205"`, `"first_limit_up_time": 1793843205`, 1)
	modifiedString = strings.Replace(modifiedString, `"total": 3`, `"total": 2`, 1)
	modifiedString = strings.Replace(modifiedString, `"count": 2`, `"count": 1`, 1)
	modifiedString = strings.Replace(modifiedString, `"num": 3`, `"num": 2`, 1)
	modifiedString = strings.Replace(modifiedString, `"history_num": 5`, `"history_num": 4`, 1)
	brokenString := strings.Replace(string(loadFixtureBytes(testingT, "broken_page1.json")), `"num": 3`, `"num": 2`, 1)
	brokenString = strings.Replace(brokenString, `"history_num": 5`, `"history_num": 4`, 1)
	downString := strings.Replace(string(loadFixtureBytes(testingT, "down_page1.json")), `"num": 3`, `"num": 2`, 1)
	downString = strings.Replace(downString, `"history_num": 5`, `"history_num": 4`, 1)

	testServer := newPoolServer(
		testingT,
		map[string][]byte{"1": []byte(modifiedString)},
		[]byte(brokenString),
		[]byte(downString),
		nil,
	)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(100).
		WithClock(fixedClock(mockNow))

	snapshot, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError != nil {
		testingT.Fatalf("expected success with string compatibility, got %v", fetchError)
	}
	if snapshot.Events[0].MarketID != 33 || snapshot.Events[0].OpenCount == nil || *snapshot.Events[0].OpenCount != 12 {
		testingT.Errorf("unexpected parsed values: MarketID=%d OpenCount=%v", snapshot.Events[0].MarketID, snapshot.Events[0].OpenCount)
	}
}

func TestFetchDay_CrossPoolSummaryMismatch(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")
	sealedPageTwoBytes := loadFixtureBytes(testingT, "sealed_page2.json")
	brokenPageBytes := loadFixtureBytes(testingT, "broken_page1.json")
	downSummaryMismatch := strings.Replace(
		string(loadFixtureBytes(testingT, "down_page1.json")),
		`"num": 2`,
		`"num": 1`,
		1,
	)
	downSummaryMismatch = strings.Replace(downSummaryMismatch, `"history_num": 10`, `"history_num": 9`, 1)
	testServer := newPoolServer(testingT, map[string][]byte{"1": sealedPageOneBytes, "2": sealedPageTwoBytes}, brokenPageBytes, []byte(downSummaryMismatch), nil)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected cross-pool summary mismatch error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "inconsistent daily summaries") {
		testingT.Errorf("expected daily summary mismatch, got %v", fetchError)
	}
}

func TestFetchDay_DownPoolDateMismatch(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")
	sealedPageTwoBytes := loadFixtureBytes(testingT, "sealed_page2.json")
	brokenPageBytes := loadFixtureBytes(testingT, "broken_page1.json")
	downWithWrongDate := strings.Replace(string(loadFixtureBytes(testingT, "down_page1.json")), `"date": 20261105`, `"date": 20261104`, 1)
	testServer := newPoolServer(testingT, map[string][]byte{"1": sealedPageOneBytes, "2": sealedPageTwoBytes}, brokenPageBytes, []byte(downWithWrongDate), nil)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected down pool date mismatch error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "date mismatch") {
		testingT.Errorf("expected date mismatch, got %v", fetchError)
	}
}

func TestFetchDay_DownPoolPageCountMismatch(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")
	sealedPageTwoBytes := loadFixtureBytes(testingT, "sealed_page2.json")
	brokenPageBytes := loadFixtureBytes(testingT, "broken_page1.json")
	downWithWrongPageCount := strings.Replace(string(loadFixtureBytes(testingT, "down_page1.json")), `"count": 1`, `"count": 2`, 1)
	testServer := newPoolServer(testingT, map[string][]byte{"1": sealedPageOneBytes, "2": sealedPageTwoBytes}, brokenPageBytes, []byte(downWithWrongPageCount), nil)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected down pool page count mismatch error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "page count") {
		testingT.Errorf("expected page count mismatch, got %v", fetchError)
	}
}

func TestFetchDay_DownPoolMissingSummary(testingT *testing.T) {
	sealedPageOneBytes := loadFixtureBytes(testingT, "sealed_page1.json")
	sealedPageTwoBytes := loadFixtureBytes(testingT, "sealed_page2.json")
	brokenPageBytes := loadFixtureBytes(testingT, "broken_page1.json")
	downWithoutSummary := strings.Replace(
		string(loadFixtureBytes(testingT, "down_page1.json")),
		`"limit_down_count": {`,
		`"limit_down_count": null, "_ignored": {`,
		1,
	)
	testServer := newPoolServer(testingT, map[string][]byte{"1": sealedPageOneBytes, "2": sealedPageTwoBytes}, brokenPageBytes, []byte(downWithoutSummary), nil)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected missing down summary error, got nil")
	}
	if !strings.Contains(fetchError.Error(), "null or missing limit_down_count") {
		testingT.Errorf("expected missing limit_down_count, got %v", fetchError)
	}
}

func TestFetchDay_HARSampleSinglePageFailsIncomplete(testingT *testing.T) {
	harSampleBytes := loadFixtureBytes(testingT, "har_sample_limit_up_pool.json")
	brokenBytes := strings.Replace(
		string(loadFixtureBytes(testingT, "empty_pool.json")),
		`"num": 0`,
		`"num": 83`,
		1,
	)
	brokenBytes = strings.Replace(brokenBytes, `"history_num": 0`, `"history_num": 130`, 1)
	brokenBytes = strings.Replace(brokenBytes, `"num": 0`, `"num": 47`, 1)
	brokenBytes = strings.Replace(brokenBytes, `"history_num": 0`, `"history_num": 130`, 1)
	downBytes := strings.Replace(string(harSampleBytes), `"limit_down_count": {`, `"limit_down_count": null, "_ignored": {`, 1)
	testServer := newPoolServer(
		testingT,
		map[string][]byte{"1": harSampleBytes},
		[]byte(brokenBytes),
		[]byte(downBytes),
		nil,
	)

	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	thsClient := NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(15).
		WithClock(fixedClock(mockNow))

	_, fetchError := thsClient.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError == nil {
		testingT.Fatalf("expected incomplete HAR page set to fail, got nil")
	}
}
