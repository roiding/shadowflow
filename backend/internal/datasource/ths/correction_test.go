package ths

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
)

type correctionFixtureSet struct {
	SealedPages map[string]map[string]any
	BrokenPage  map[string]any
	DownPage    map[string]any
}

func correctionFixtureObject(testingT *testing.T, filename string) map[string]any {
	testingT.Helper()
	var fixtureObject map[string]any
	if unmarshalError := json.Unmarshal(loadFixtureBytes(testingT, filename), &fixtureObject); unmarshalError != nil {
		testingT.Fatal(unmarshalError)
	}
	return fixtureObject
}

func correctionFixtureData(testingT *testing.T, fixtureObject map[string]any) map[string]any {
	testingT.Helper()
	dataObject, valid := fixtureObject["data"].(map[string]any)
	if !valid {
		testingT.Fatal("fixture data object is missing")
	}
	return dataObject
}

func correctionObjectField(testingT *testing.T, parentObject map[string]any, fieldName string) map[string]any {
	testingT.Helper()
	childObject, valid := parentObject[fieldName].(map[string]any)
	if !valid {
		testingT.Fatalf("fixture field %s is not an object", fieldName)
	}
	return childObject
}

func correctionEventByCode(testingT *testing.T, dataObject map[string]any, code string) map[string]any {
	testingT.Helper()
	eventList, valid := dataObject["info"].([]any)
	if !valid {
		testingT.Fatal("fixture info is not an array")
	}
	for _, rawEvent := range eventList {
		eventObject, eventValid := rawEvent.(map[string]any)
		if eventValid && eventObject["code"] == code {
			return eventObject
		}
	}
	testingT.Fatalf("fixture event %s is missing", code)
	return nil
}

func correctionReplaceEvents(testingT *testing.T, dataObject map[string]any, events []map[string]any) {
	testingT.Helper()
	eventList := make([]any, 0, len(events))
	for _, eventObject := range events {
		eventList = append(eventList, eventObject)
	}
	dataObject["info"] = eventList
}

func correctionSetUpSummary(testingT *testing.T, dataObject map[string]any, openCount int, touchedCount int) {
	testingT.Helper()
	todaySummary := correctionObjectField(testingT, correctionObjectField(testingT, dataObject, "limit_up_count"), "today")
	todaySummary["open_num"] = openCount
	todaySummary["history_num"] = touchedCount
}

func correctionSetDownCount(testingT *testing.T, dataObject map[string]any, downCount int) {
	testingT.Helper()
	todaySummary := correctionObjectField(testingT, correctionObjectField(testingT, dataObject, "limit_down_count"), "today")
	todaySummary["num"] = downCount
}

func correctionCloneValue(testingT *testing.T, sourceValue any) any {
	testingT.Helper()
	encodedValue, encodeError := json.Marshal(sourceValue)
	if encodeError != nil {
		testingT.Fatal(encodeError)
	}
	var clonedValue any
	if unmarshalError := json.Unmarshal(encodedValue, &clonedValue); unmarshalError != nil {
		testingT.Fatal(unmarshalError)
	}
	return clonedValue
}

func correctionCloneEvent(testingT *testing.T, sourceEvent map[string]any) map[string]any {
	testingT.Helper()
	clonedValue := correctionCloneValue(testingT, sourceEvent)
	clonedEvent, valid := clonedValue.(map[string]any)
	if !valid {
		testingT.Fatal("cloned fixture event is not an object")
	}
	return clonedEvent
}

func correctionEncodeFixture(testingT *testing.T, fixtureObject map[string]any) []byte {
	testingT.Helper()
	encodedObject, encodeError := json.Marshal(fixtureObject)
	if encodeError != nil {
		testingT.Fatal(encodeError)
	}
	return encodedObject
}

func newCorrectionFixtureSet(testingT *testing.T) correctionFixtureSet {
	testingT.Helper()
	sealedOne := correctionFixtureObject(testingT, "sealed_page1.json")
	sealedTwo := correctionFixtureObject(testingT, "sealed_page2.json")
	broken := correctionFixtureObject(testingT, "broken_page1.json")
	down := correctionFixtureObject(testingT, "down_page1.json")
	for _, fixtureObject := range []map[string]any{sealedOne, sealedTwo, broken, down} {
		correctionSetUpSummary(testingT, correctionFixtureData(testingT, fixtureObject), 1, 4)
	}
	brokenData := correctionFixtureData(testingT, broken)
	brokenEvidence := correctionEventByCode(testingT, brokenData, "001337")
	brokenEvidence["change_tag"] = "LIMIT_DOWN"
	brokenEvidence["open_num"] = 2
	brokenEvidence["change_rate"] = -10.0
	downData := correctionFixtureData(testingT, down)
	downEvidence := correctionEventByCode(testingT, downData, "600001")
	downEvidence["code"] = "001337"
	downEvidence["name"] = "四川黄金"
	downEvidence["market_id"] = 33
	downEvidence["market_type"] = "HS"
	downEvidence["change_tag"] = "LIMIT_DOWN"
	downEvidence["change_rate"] = -10.0
	return correctionFixtureSet{
		SealedPages: map[string]map[string]any{"1": sealedOne, "2": sealedTwo},
		BrokenPage:  broken,
		DownPage:    down,
	}
}

func (fixtures correctionFixtureSet) encodedPages(testingT *testing.T) (map[string][]byte, []byte, []byte) {
	testingT.Helper()
	sealedPages := make(map[string][]byte, len(fixtures.SealedPages))
	for pageNumber, fixtureObject := range fixtures.SealedPages {
		sealedPages[pageNumber] = correctionEncodeFixture(testingT, fixtureObject)
	}
	return sealedPages, correctionEncodeFixture(testingT, fixtures.BrokenPage), correctionEncodeFixture(testingT, fixtures.DownPage)
}

func correctionTestClient(testServer *httptest.Server) *Client {
	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	return NewClient(testServer.URL, testServer.Client()).
		WithMinInterval(0).
		WithPageLimit(2).
		WithClock(fixedClock(mockNow))
}

func TestFetchDayCountCorrectionFromBrokenDownIntersection(testingT *testing.T) {
	fixtures := newCorrectionFixtureSet(testingT)
	sealedPages, brokenPage, downPage := fixtures.encodedPages(testingT)
	testServer := newPoolServer(testingT, sealedPages, brokenPage, downPage, nil)
	snapshot, fetchError := correctionTestClient(testServer).FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError != nil {
		testingT.Fatalf("expected evidenced count correction, got %v", fetchError)
	}
	if snapshot.SealedCount != 3 || snapshot.BrokenCount != 2 || snapshot.DownCount != 2 || snapshot.TouchedCount != 5 {
		testingT.Fatalf("unexpected local counts: sealed=%d broken=%d down=%d touched=%d",
			snapshot.SealedCount, snapshot.BrokenCount, snapshot.DownCount, snapshot.TouchedCount)
	}
	correction := snapshot.CountCorrection
	if correction == nil || correction.Reason != limitup.CountCorrectionBrokenDownSummaryUndercount ||
		correction.SourceBrokenCount != 1 || correction.SourceTouchedCount != 4 ||
		len(correction.Codes) != 1 || correction.Codes[0] != "001337" {
		testingT.Fatalf("unexpected count correction: %+v", correction)
	}
	if correction.SourceBrokenCount+(snapshot.BrokenCount-correction.SourceBrokenCount) != snapshot.BrokenCount ||
		correction.SourceTouchedCount+(snapshot.TouchedCount-correction.SourceTouchedCount) != snapshot.TouchedCount {
		testingT.Fatalf("correction arithmetic does not reconcile source and local counts: %+v", correction)
	}
	if len(snapshot.RawPages) != 4 || string(snapshot.RawPages[0]) != string(sealedPages["1"]) ||
		string(snapshot.RawPages[1]) != string(sealedPages["2"]) || string(snapshot.RawPages[2]) != string(brokenPage) ||
		string(snapshot.RawPages[3]) != string(downPage) {
		testingT.Fatal("source raw pages were modified or reordered")
	}
	if validateError := limitup.ValidateSnapshot(snapshot); validateError != nil {
		testingT.Fatalf("corrected snapshot failed validation: %v", validateError)
	}
}

func TestFetchDayCountCorrectionFetchesAllBrokenPages(testingT *testing.T) {
	sealedOne := correctionFixtureObject(testingT, "sealed_page1.json")
	sealedTwo := correctionFixtureObject(testingT, "sealed_page2.json")
	brokenOne := correctionFixtureObject(testingT, "broken_page1.json")
	down := correctionFixtureObject(testingT, "down_page1.json")
	for _, fixtureObject := range []map[string]any{sealedOne, sealedTwo, brokenOne, down} {
		correctionSetUpSummary(testingT, correctionFixtureData(testingT, fixtureObject), 1, 4)
	}
	brokenOneData := correctionFixtureData(testingT, brokenOne)
	firstEvidence := correctionEventByCode(testingT, brokenOneData, "001337")
	firstEvidence["change_tag"] = "LIMIT_DOWN"
	firstEvidence["open_num"] = 2
	firstEvidence["change_rate"] = -10.0
	secondEvidence := correctionCloneEvent(testingT, firstEvidence)
	secondEvidence["code"] = "001338"
	secondEvidence["name"] = "测试乙"
	secondEvidence["market_id"] = 33
	secondEvidence["market_type"] = "HS"
	secondEvidence["change_tag"] = "LIMIT_DOWN"
	secondEvidence["open_num"] = 1
	secondEvidence["change_rate"] = -10.0
	correctionReplaceEvents(testingT, brokenOneData, []map[string]any{firstEvidence, secondEvidence})
	brokenOneData["page"] = map[string]any{"limit": 2, "total": 3, "count": 2, "page": 1}
	brokenTwo := correctionCloneValue(testingT, brokenOne).(map[string]any)
	brokenTwoData := correctionFixtureData(testingT, brokenTwo)
	thirdBroken := correctionCloneEvent(testingT, secondEvidence)
	thirdBroken["code"] = "001339"
	thirdBroken["name"] = "测试丙"
	thirdBroken["change_tag"] = "LIMIT_FAILED"
	correctionReplaceEvents(testingT, brokenTwoData, []map[string]any{thirdBroken})
	brokenTwoData["page"] = map[string]any{"limit": 2, "total": 3, "count": 2, "page": 2}
	downData := correctionFixtureData(testingT, down)
	downFirst := correctionEventByCode(testingT, downData, "600001")
	downFirst["code"] = "001337"
	downFirst["name"] = "四川黄金"
	downFirst["market_id"] = 33
	downFirst["market_type"] = "HS"
	downFirst["change_tag"] = "LIMIT_DOWN"
	downFirst["change_rate"] = -10.0
	downSecond := correctionEventByCode(testingT, downData, "002002")
	downSecond["code"] = "001338"
	downSecond["name"] = "测试乙"
	downSecond["market_id"] = 33
	downSecond["market_type"] = "HS"
	downSecond["change_tag"] = "LIMIT_DOWN"
	downSecond["change_rate"] = -10.0
	sealedOneBytes := correctionEncodeFixture(testingT, sealedOne)
	sealedTwoBytes := correctionEncodeFixture(testingT, sealedTwo)
	brokenOneBytes := correctionEncodeFixture(testingT, brokenOne)
	brokenTwoBytes := correctionEncodeFixture(testingT, brokenTwo)
	downBytes := correctionEncodeFixture(testingT, down)
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		requestPath := httpRequest.URL.Path
		pageNumber := httpRequest.URL.Query().Get("page")
		switch {
		case strings.HasSuffix(requestPath, "/limit_up_pool") && pageNumber == "1":
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(sealedOneBytes)
		case strings.HasSuffix(requestPath, "/limit_up_pool") && pageNumber == "2":
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(sealedTwoBytes)
		case strings.HasSuffix(requestPath, "/open_limit_pool") && pageNumber == "1":
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(brokenOneBytes)
		case strings.HasSuffix(requestPath, "/open_limit_pool") && pageNumber == "2":
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(brokenTwoBytes)
		case strings.HasSuffix(requestPath, "/lower_limit_pool"):
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(downBytes)
		default:
			responseWriter.WriteHeader(http.StatusNotFound)
		}
	}))
	defer testServer.Close()
	snapshot, fetchError := correctionTestClient(testServer).FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError != nil {
		testingT.Fatalf("expected fully paginated broken correction, got %v", fetchError)
	}
	correction := snapshot.CountCorrection
	if snapshot.BrokenCount != 3 || snapshot.TouchedCount != 6 || correction == nil ||
		correction.SourceBrokenCount != 1 || correction.SourceTouchedCount != 4 ||
		len(correction.Codes) != 2 || correction.Codes[0] != "001337" || correction.Codes[1] != "001338" {
		testingT.Fatalf("broken pagination or correction evidence was truncated: counts=%d/%d correction=%+v",
			snapshot.BrokenCount, snapshot.TouchedCount, correction)
	}
	if len(snapshot.RawPages) != 5 {
		testingT.Fatalf("expected every broken page in raw evidence, got %d pages", len(snapshot.RawPages))
	}
}

func TestFetchDayRejectsInvalidCountCorrection(testingT *testing.T) {
	testCases := []struct {
		Name          string
		Mutate        func(*testing.T, correctionFixtureSet)
		ErrorContains string
	}{
		{
			Name: "missing broken tag",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				delete(correctionEventByCode(testingT, correctionFixtureData(testingT, fixtures.BrokenPage), "001337"), "change_tag")
			},
			ErrorContains: "missing change_tag evidence",
		},
		{
			Name: "missing down tag",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				delete(correctionEventByCode(testingT, correctionFixtureData(testingT, fixtures.DownPage), "001337"), "change_tag")
			},
			ErrorContains: "requires LIMIT_DOWN source tags",
		},
		{
			Name: "non broken-down difference",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				correctionEventByCode(testingT, correctionFixtureData(testingT, fixtures.BrokenPage), "001337")["change_tag"] = "LIMIT_FAILED"
			},
			ErrorContains: "does not match 0 evidenced",
		},
		{
			Name: "identity mismatch",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				correctionEventByCode(testingT, correctionFixtureData(testingT, fixtures.DownPage), "001337")["name"] = "另一只股票"
			},
			ErrorContains: "identity or change rate differs",
		},
		{
			Name: "difference evidence mismatch",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				correctionSetUpSummary(testingT, correctionFixtureData(testingT, fixtures.SealedPages["1"]), 0, 3)
				correctionSetUpSummary(testingT, correctionFixtureData(testingT, fixtures.SealedPages["2"]), 0, 3)
				correctionSetUpSummary(testingT, correctionFixtureData(testingT, fixtures.BrokenPage), 0, 3)
				correctionSetUpSummary(testingT, correctionFixtureData(testingT, fixtures.DownPage), 0, 3)
			},
			ErrorContains: "does not match 1 evidenced",
		},
		{
			Name: "no intersection evidence",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				correctionEventByCode(testingT, correctionFixtureData(testingT, fixtures.DownPage), "001337")["code"] = "009999"
			},
			ErrorContains: "does not match 0 evidenced",
		},
		{
			Name: "truncated broken detail",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				brokenData := correctionFixtureData(testingT, fixtures.BrokenPage)
				correctionReplaceEvents(testingT, brokenData, []map[string]any{
					correctionEventByCode(testingT, brokenData, "001337"),
				})
			},
			ErrorContains: "expected 2",
		},
		{
			Name: "duplicate broken detail",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				brokenData := correctionFixtureData(testingT, fixtures.BrokenPage)
				correctionEventByCode(testingT, brokenData, "600519")["code"] = "001337"
			},
			ErrorContains: "duplicate stock code",
		},
		{
			Name: "broken page total below source",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				for _, fixtureObject := range []map[string]any{fixtures.SealedPages["1"], fixtures.SealedPages["2"], fixtures.BrokenPage, fixtures.DownPage} {
					correctionSetUpSummary(testingT, correctionFixtureData(testingT, fixtureObject), 3, 6)
				}
			},
			ErrorContains: "below summary count",
		},
		{
			Name: "down detail differs from summary",
			Mutate: func(testingT *testing.T, fixtures correctionFixtureSet) {
				for _, fixtureObject := range []map[string]any{fixtures.SealedPages["1"], fixtures.SealedPages["2"], fixtures.BrokenPage, fixtures.DownPage} {
					correctionSetDownCount(testingT, correctionFixtureData(testingT, fixtureObject), 1)
				}
			},
			ErrorContains: "does not match summary count",
		},
	}
	for _, testCase := range testCases {
		testingT.Run(testCase.Name, func(testingT *testing.T) {
			fixtures := newCorrectionFixtureSet(testingT)
			testCase.Mutate(testingT, fixtures)
			sealedPages, brokenPage, downPage := fixtures.encodedPages(testingT)
			testServer := newPoolServer(testingT, sealedPages, brokenPage, downPage, nil)
			_, fetchError := correctionTestClient(testServer).FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
			if fetchError == nil || !strings.Contains(fetchError.Error(), testCase.ErrorContains) {
				testingT.Fatalf("expected rejection containing %q, got %v", testCase.ErrorContains, fetchError)
			}
		})
	}
}

func TestFetchDayNormalBrokenDownOverlapHasNoCorrection(testingT *testing.T) {
	fixtures := newCorrectionFixtureSet(testingT)
	for _, fixtureObject := range []map[string]any{fixtures.SealedPages["1"], fixtures.SealedPages["2"], fixtures.BrokenPage, fixtures.DownPage} {
		correctionSetUpSummary(testingT, correctionFixtureData(testingT, fixtureObject), 2, 5)
	}
	sealedPages, brokenPage, downPage := fixtures.encodedPages(testingT)
	testServer := newPoolServer(testingT, sealedPages, brokenPage, downPage, nil)
	snapshot, fetchError := correctionTestClient(testServer).FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault)
	if fetchError != nil {
		testingT.Fatalf("expected normal overlap without correction, got %v", fetchError)
	}
	if snapshot.CountCorrection != nil || snapshot.BrokenCount != 2 || snapshot.TouchedCount != 5 {
		testingT.Fatalf("normal source totals must not be marked corrected: counts=%d/%d correction=%+v",
			snapshot.BrokenCount, snapshot.TouchedCount, snapshot.CountCorrection)
	}
}

func TestParseStockEventDownTimestampsOnlyForDownPool(testingT *testing.T) {
	baseEvent := `{
		"code":"600001","name":"邯郸钢铁","market_id":17,"market_type":"HS",
		"open_num":1,"first_limit_down_time":"1793860800","last_limit_down_time":"1793864400",
		"first_limit_up_time":"1793859000","last_limit_up_time":"1793859600",
		"change_rate":-10.0,"turnover_rate":3.2
	}`
	downEvent, parseError := parseStockEvent(json.RawMessage(baseEvent), "2026-11-05", limitup.PoolDown)
	if parseError != nil {
		testingT.Fatalf("valid limit-down timestamps were rejected: %v", parseError)
	}
	if downEvent.FirstDownAt == nil || downEvent.FirstDownAt.In(limitup.Shanghai()).Format("15:04:05") != "14:40:00" {
		testingT.Fatalf("unexpected first down time: %v", downEvent.FirstDownAt)
	}
	if downEvent.LastDownAt == nil || downEvent.LastDownAt.In(limitup.Shanghai()).Format("15:04:05") != "15:40:00" {
		testingT.Fatalf("unexpected last down time: %v", downEvent.LastDownAt)
	}
	if downEvent.FirstLimitAt == nil || downEvent.FirstLimitAt.In(limitup.Shanghai()).Format("15:04:05") != "14:10:00" {
		testingT.Fatalf("legacy up-limit time was not preserved: %v", downEvent.FirstLimitAt)
	}
	brokenEvent, parseError := parseStockEvent(json.RawMessage(baseEvent), "2026-11-05", limitup.PoolBroken)
	if parseError != nil || brokenEvent.FirstDownAt != nil || brokenEvent.LastDownAt != nil {
		testingT.Fatalf("down timestamps leaked into broken pool: %+v %v", brokenEvent, parseError)
	}
}

func TestParseStockEventDownTimestampsMissingAndNumericStrings(testingT *testing.T) {
	missingEvent, parseError := parseStockEvent(json.RawMessage(`{
		"code":"600001","name":"邯郸钢铁","market_id":17,"market_type":"HS",
		"first_limit_down_time":null,"last_limit_down_time":""
	}`), "2026-11-05", limitup.PoolDown)
	if parseError != nil || missingEvent.FirstDownAt != nil || missingEvent.LastDownAt != nil {
		testingT.Fatalf("missing down timestamps must remain nil: %+v %v", missingEvent, parseError)
	}
	numericEvent, parseError := parseStockEvent(json.RawMessage(`{
		"code":"600001","name":"邯郸钢铁","market_id":17,"market_type":"HS",
		"first_limit_down_time":1793860800,"last_limit_down_time":"1793864400"
	}`), "2026-11-05", limitup.PoolDown)
	if parseError != nil || numericEvent.FirstDownAt == nil || numericEvent.LastDownAt == nil {
		testingT.Fatalf("numeric and numeric-string down timestamps were not accepted: %+v %v", numericEvent, parseError)
	}
}

func TestParseStockEventRejectsInvalidDownTimestampOrderAndDate(testingT *testing.T) {
	testCases := map[string]string{
		"date mismatch": `{
			"code":"600001","name":"邯郸钢铁","market_id":17,"market_type":"HS",
			"first_limit_down_time":"1793774400","last_limit_down_time":"1793864400"
		}`,
		"reversed": `{
			"code":"600001","name":"邯郸钢铁","market_id":17,"market_type":"HS",
			"first_limit_down_time":"1793864400","last_limit_down_time":"1793860800"
		}`,
	}
	for name, rawEvent := range testCases {
		testingT.Run(name, func(testingT *testing.T) {
			if _, parseError := parseStockEvent(json.RawMessage(rawEvent), "2026-11-05", limitup.PoolDown); parseError == nil {
				testingT.Fatal("invalid down timestamp was accepted")
			}
		})
	}
}

func TestFetchDayUsesDownOrderFieldAndRequestedFields(testingT *testing.T) {
	sealedPageOne := loadFixtureBytes(testingT, "sealed_page1.json")
	sealedPageTwo := loadFixtureBytes(testingT, "sealed_page2.json")
	brokenPage := loadFixtureBytes(testingT, "broken_page1.json")
	downPage := loadFixtureBytes(testingT, "down_page1.json")
	requests := make(map[string]url.Values)
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		requests[httpRequest.URL.Path] = httpRequest.URL.Query()
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusOK)
		switch {
		case strings.HasSuffix(httpRequest.URL.Path, "/limit_up_pool") && httpRequest.URL.Query().Get("page") == "1":
			_, _ = responseWriter.Write(sealedPageOne)
		case strings.HasSuffix(httpRequest.URL.Path, "/limit_up_pool"):
			_, _ = responseWriter.Write(sealedPageTwo)
		case strings.HasSuffix(httpRequest.URL.Path, "/open_limit_pool"):
			_, _ = responseWriter.Write(brokenPage)
		case strings.HasSuffix(httpRequest.URL.Path, "/lower_limit_pool"):
			_, _ = responseWriter.Write(downPage)
		default:
			responseWriter.WriteHeader(http.StatusNotFound)
		}
	}))
	defer testServer.Close()
	mockNow := time.Date(2026, 11, 6, 16, 0, 0, 0, limitup.Shanghai())
	client := NewClient(testServer.URL, testServer.Client()).WithMinInterval(0).
		WithPageLimit(2).WithClock(fixedClock(mockNow))
	if _, fetchError := client.FetchDay(context.Background(), "2026-11-05", limitup.ScopeDefault); fetchError != nil {
		testingT.Fatal(fetchError)
	}
	downQuery := requests["/lower_limit_pool"]
	if downQuery.Get("order_field") != "330334" {
		testingT.Fatalf("down pool did not use 330334 ordering: %s", downQuery.Get("order_field"))
	}
	requestedFields := strings.Split(downQuery.Get("field"), ",")
	if len(requestedFields) != 16 || requestedFields[14] != "330333" || requestedFields[15] != "330334" {
		testingT.Fatalf("down-limit timestamp fields were not requested: %v", requestedFields)
	}
	if requests["/limit_up_pool"].Get("order_field") != "330324" || requests["/open_limit_pool"].Get("order_field") != "330324" {
		testingT.Fatal("up-side pools must keep 330324 ordering")
	}
}
