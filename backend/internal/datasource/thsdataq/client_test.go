package thsdataq

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (function doerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

type recordedRequest struct {
	url     string
	method  string
	headers http.Header
	body    []byte
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func validBody(codes []string, timestamp int64, total int, omit string) string {
	return string(buildBody(codes, timestamp, total, omit, nil, nil))
}

func indexOffset(indexID string) int {
	for position, expected := range tradeDailyIndex {
		if expected == indexID {
			return position
		}
	}
	return -1
}

func buildBody(codes []string, timestamp int64, total int, omit string, mutateIndexes func([]map[string]any), mutateRows func([]map[string]any)) []byte {
	indexes := make([]map[string]any, 0, len(tradeDailyIndex))
	for _, indexID := range tradeDailyIndex {
		if indexID == omit {
			continue
		}
		indexes = append(indexes, map[string]any{
			"index_id": indexID, "time_type": tradeDaily, "timestamp": strconv.FormatInt(timestamp, 10),
		})
	}
	data := make([]map[string]any, 0, total)
	for position := 0; position < len(codes) && position < total; position++ {
		values := make([]map[string]any, 0, len(tradeDailyIndex))
		for _, index := range tradeDailyIndex {
			if index == omit {
				continue
			}
			values = append(values, map[string]any{"idx": indexOffset(index), "value": "value"})
		}
		data = append(data, map[string]any{"code": codes[position], "values": values})
	}
	if mutateIndexes != nil {
		mutateIndexes(indexes)
	}
	if mutateRows != nil {
		mutateRows(data)
	}
	body, _ := json.Marshal(map[string]any{
		"status_code": 0,
		"status_msg":  "success",
		"data": map[string]any{
			"indexes": indexes,
			"data":    data,
			"total":   total,
		},
	})
	return body
}

func TestFetchBuildsShanghaiDailyRequestAndReplayableBody(testingT *testing.T) {
	date := "2026-11-06"
	parsedDate, parseError := time.ParseInLocation("2006-01-02", date, shanghai())
	if parseError != nil {
		testingT.Fatal(parseError)
	}
	timestamp := parsedDate.Unix()
	codes := []string{"33:002578", "17:603381"}
	var recorded recordedRequest
	responseBody := `{"data":{"indexes":[{"index_id":"up_down_stock_continue_limit_up_days","time_type":"TRADE_DAILY","timestamp":"1793894400"},{"index_id":"up_down_stock_final_limit_up_time","time_type":"TRADE_DAILY","timestamp":"1793894400"},{"index_id":"up_down_stock_limit_up_reason","time_type":"TRADE_DAILY","timestamp":"1793894400"},{"index_id":"up_down_stock_limit_up_volume_money_new","time_type":"TRADE_DAILY","timestamp":"1793894400"}],"data":[{"code":"33:002578","values":[{"idx":0,"value":"5"},{"idx":1,"value":"1793928609000"},{"idx":2,"value":"reason"},{"idx":3,"value":"220341800.0"}]},{"code":"17:603381","values":[{"idx":0,"value":"4"},{"idx":1,"value":"1793928301000"},{"idx":2,"value":"reason"},{"idx":3,"value":"146363710.0"}]}],"total":2,"extra":"preserve"},"status_code":0,"status_msg":"success"} `
	client := NewClient(doerFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		if request.GetBody == nil {
			testingT.Fatal("request body is not replayable")
		}
		recorded = recordedRequest{url: request.URL.String(), method: request.Method, headers: request.Header.Clone(), body: body}
		replayed, replayError := request.GetBody()
		if replayError != nil {
			testingT.Fatalf("GetBody failed: %v", replayError)
		}
		replayedBody, _ := io.ReadAll(replayed)
		if string(replayedBody) != string(body) {
			testingT.Fatalf("GetBody is not replayable: got %s, want %s", replayedBody, body)
		}
		return response(http.StatusOK, responseBody), nil
	}))

	payload, err := client.Fetch(context.Background(), date, codes)
	if err != nil {
		testingT.Fatal(err)
	}
	if string(payload) != responseBody {
		testingT.Fatalf("response bytes were not preserved: %s", payload)
	}
	if recorded.method != http.MethodPost || recorded.url != endpoint {
		testingT.Fatalf("unexpected request: %+v", recorded)
	}
	if recorded.headers.Get("Content-Type") != "application/json" ||
		recorded.headers.Get("Origin") != "https://data.10jqka.com.cn" ||
		recorded.headers.Get("Referer") != "https://data.10jqka.com.cn/" ||
		recorded.headers.Get("Source-ID") != "kamis-5862" ||
		recorded.headers.Get("Platform") != "mobileweb" ||
		recorded.headers.Get("Cookie") != "" {
		testingT.Fatalf("unexpected headers: %+v", recorded.headers)
	}
	var body struct {
		CodeSelectors struct {
			Intersection []struct {
				Type   string   `json:"type"`
				Values []string `json:"values"`
			} `json:"intersection"`
		} `json:"code_selectors"`
		Indexes []struct {
			IndexID   string `json:"index_id"`
			TimeType  string `json:"time_type"`
			Timestamp int64  `json:"timestamp"`
		} `json:"indexes"`
		PageInfo struct {
			PageBegin int `json:"page_begin"`
			PageSize  int `json:"page_size"`
		} `json:"page_info"`
	}
	if unmarshalError := json.Unmarshal(recorded.body, &body); unmarshalError != nil {
		testingT.Fatalf("invalid request JSON: %v", unmarshalError)
	}
	if len(body.CodeSelectors.Intersection) != 1 || body.CodeSelectors.Intersection[0].Type != "stock_code" ||
		strings.Join(body.CodeSelectors.Intersection[0].Values, ",") != strings.Join(codes, ",") {
		testingT.Fatalf("unexpected selectors: %+v", body.CodeSelectors)
	}
	if len(body.Indexes) != 4 {
		testingT.Fatalf("unexpected indexes: %+v", body.Indexes)
	}
	for position, index := range body.Indexes {
		if index.IndexID != tradeDailyIndex[position] || index.TimeType != tradeDaily || index.Timestamp != timestamp {
			testingT.Fatalf("unexpected daily index: %+v", body.Indexes)
		}
	}
	if body.PageInfo.PageBegin != 0 || body.PageInfo.PageSize != 20 {
		testingT.Fatalf("unexpected page info: %+v", body.PageInfo)
	}
}

func TestFetchRejectsInvalidDateAndCodes(testingT *testing.T) {
	doer := doerFunc(func(*http.Request) (*http.Response, error) {
		testingT.Fatal("invalid input must not reach transport")
		return nil, nil
	})
	client := NewClient(doer)
	date := "2026-11-06"
	future := "9999-12-31"
	tests := []struct {
		name  string
		date  string
		codes []string
	}{
		{name: "bad date", date: "2026-02-30", codes: []string{"33:002578"}},
		{name: "future", date: future, codes: []string{"33:002578"}},
		{name: "empty", date: date},
		{name: "too many", date: date, codes: []string{"33:000001", "33:000002", "33:000003", "33:000004", "33:000005", "33:000006", "33:000007", "33:000008", "33:000009", "33:000010", "33:000011", "33:000012", "33:000013", "33:000014", "33:000015", "33:000016", "33:000017", "33:000018", "33:000019", "33:000020", "33:000021"}},
		{name: "bad market", date: date, codes: []string{"32:000001"}},
		{name: "bad code", date: date, codes: []string{"33:00001"}},
		{name: "duplicate", date: date, codes: []string{"33:000001", "33:000001"}},
	}
	for _, testcase := range tests {
		testingT.Run(testcase.name, func(testingT *testing.T) {
			payload, err := client.Fetch(context.Background(), testcase.date, testcase.codes)
			if payload != nil || !errors.Is(err, ErrInvalid) {
				testingT.Fatalf("unexpected result: %q %v", payload, err)
			}
		})
	}
}

func TestFetchRejectsIncompleteOrMismatchedData(testingT *testing.T) {
	date := "2026-11-06"
	parsedDate, _ := time.ParseInLocation("2006-01-02", date, shanghai())
	timestamp := parsedDate.Unix()
	codes := []string{"33:002578", "17:603381"}
	tests := map[string]string{
		"total": validBody(codes, timestamp, 3, ""),
		"rows":  string(buildBody(codes, timestamp, len(codes), "", nil, func(rows []map[string]any) { rows[1]["code"] = "17:603999" })),
		"values": string(buildBody(codes, timestamp, len(codes), "", nil, func(rows []map[string]any) {
			values := rows[0]["values"].([]map[string]any)
			rows[0]["values"] = values[:3]
		})),
		"null idx": string(buildBody(codes, timestamp, len(codes), "", nil, func(rows []map[string]any) {
			values := rows[0]["values"].([]map[string]any)
			values[3]["idx"] = nil
		})),
		"index time": string(buildBody(codes, timestamp, len(codes), "", func(indexes []map[string]any) {
			indexes[0]["timestamp"] = "1793894401"
		}, nil)),
		"indexes":   validBody(codes, timestamp, len(codes), indexReason),
		"business":  `{"status_code":1,"status_msg":"private","data":{"indexes":[],"data":[],"total":2}}`,
		"truncated": `{"status_code":0,"data":{"indexes":[` + `]`,
	}
	for name, body := range tests {
		testingT.Run(name, func(testingT *testing.T) {
			client := NewClient(doerFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, body), nil
			}))
			payload, err := client.Fetch(context.Background(), date, codes)
			if payload != nil || !errors.Is(err, ErrUnavailable) {
				testingT.Fatalf("unexpected result: %q %v", payload, err)
			}
			if strings.Contains(err.Error(), "private") {
				testingT.Fatalf("error leaked response body: %v", err)
			}
		})
	}
}

func TestFetchPreservesAuthErrorsAndCancellation(testingT *testing.T) {
	date := "2026-11-06"
	transportError := errors.New("network down")
	client := NewClient(doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportError
	}))
	if _, err := client.Fetch(context.Background(), date, []string{"33:002578"}); !errors.Is(err, transportError) {
		testingT.Fatalf("doer error was not preserved: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Fetch(ctx, date, []string{"33:002578"}); !errors.Is(err, context.Canceled) {
		testingT.Fatalf("expected cancellation, got %v", err)
	}
}

func TestFetchRejectsOversizedBodyAndDoesNotLeakHTTPBody(testingT *testing.T) {
	date := "2026-11-06"
	codes := []string{"33:002578"}
	client := NewClient(doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: maxResponseBytes + 1,
			Body: io.NopCloser(strings.NewReader("private")),
		}, nil
	}))
	payload, err := client.Fetch(context.Background(), date, codes)
	if payload != nil || !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private") {
		testingT.Fatalf("unexpected oversized response result: %q %v", payload, err)
	}
	forbidden := NewClient(doerFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusForbidden, `{"status_code":403,"status_msg":"private"}`), nil
	}))
	payload, err = forbidden.Fetch(context.Background(), date, codes)
	if payload != nil || !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private") {
		testingT.Fatalf("unexpected HTTP failure result: %q %v", payload, err)
	}
}

func TestFetchRejectsExplicitNoAuthWithoutAuthError(testingT *testing.T) {
	date := "2026-11-06"
	parsedDate, _ := time.ParseInLocation("2006-01-02", date, shanghai())
	timestamp := parsedDate.Unix()
	codes := []string{"33:002578"}
	body := buildBody(codes, timestamp, len(codes), "", func(indexes []map[string]any) {
		for _, index := range indexes {
			index["attribute"] = map[string]any{"permission_flag": "NO_AUTH"}
		}
	}, nil)
	client := NewClient(doerFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, string(body)), nil
	}))
	payload, err := client.Fetch(context.Background(), date, codes)
	if payload != nil || !errors.Is(err, ErrPermissionDenied) {
		testingT.Fatalf("expected explicit permission denial, payload=%q err=%v", payload, err)
	}
	if errors.Is(err, ErrUnavailable) {
		testingT.Fatalf("permission denial must not be classified as transport unavailability: %v", err)
	}
}

func TestFetchAcceptsNullMetricValuesWithoutNoAuth(testingT *testing.T) {
	date := "2026-11-06"
	parsedDate, _ := time.ParseInLocation("2006-01-02", date, shanghai())
	timestamp := parsedDate.Unix()
	codes := []string{"33:002578"}
	body := buildBody(codes, timestamp, len(codes), "", nil, func(rows []map[string]any) {
		for _, value := range rows[0]["values"].([]map[string]any) {
			value["value"] = nil
		}
	})
	client := NewClient(doerFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, string(body)), nil
	}))
	payload, err := client.Fetch(context.Background(), date, codes)
	if err != nil {
		testingT.Fatalf("reasonable null values were rejected: %v", err)
	}
	records, parseError := ParseResponse(payload, date, codes)
	if parseError != nil || len(records) != 1 || len(records[0].Metrics) != len(tradeDailyIndex) {
		testingT.Fatalf("unexpected normalized record: %#v %v", records, parseError)
	}
	for _, metric := range records[0].Metrics {
		if string(metric.Value) != "null" {
			testingT.Fatalf("null value was not preserved for %s: %s", metric.IndexID, metric.Value)
		}
	}
	if !RecordComplete(string(payloadForRecord(testingT, records[0])), date, codes[0]) {
		testingT.Fatal("non-permission null record should remain complete")
	}
}

func TestFetchMapsRequestedMetricsByIndexPosition(testingT *testing.T) {
	date := "2026-11-06"
	parsedDate, _ := time.ParseInLocation("2006-01-02", date, shanghai())
	timestamp := strconv.FormatInt(parsedDate.Unix(), 10)
	codes := []string{"33:002578"}
	body, marshalError := json.Marshal(map[string]any{
		"status_code": 0,
		"data": map[string]any{
			"indexes": []map[string]any{
				{"index_id": indexVolumeMoney, "time_type": tradeDaily, "timestamp": timestamp},
				{"index_id": indexReason, "time_type": tradeDaily, "timestamp": timestamp},
				{"index_id": indexFinalTime, "time_type": tradeDaily, "timestamp": timestamp},
				{"index_id": indexContinueDays, "time_type": tradeDaily, "timestamp": timestamp},
			},
			"data": []map[string]any{{
				"code": codes[0],
				"values": []map[string]any{
					{"idx": 0, "value": "metric-volume"},
					{"idx": 1, "value": "metric-reason"},
					{"idx": 2, "value": "metric-final"},
					{"idx": 3, "value": "metric-continue"},
				},
			}},
			"total": 1,
		},
	})
	if marshalError != nil {
		testingT.Fatal(marshalError)
	}
	records, err := ParseResponse(body, date, codes)
	if err != nil {
		testingT.Fatal(err)
	}
	metrics := make(map[string]string, len(records[0].Metrics))
	for _, metric := range records[0].Metrics {
		metrics[metric.IndexID] = string(metric.Value)
	}
	if metrics[indexContinueDays] != `"metric-continue"` || metrics[indexFinalTime] != `"metric-final"` ||
		metrics[indexReason] != `"metric-reason"` || metrics[indexVolumeMoney] != `"metric-volume"` {
		testingT.Fatalf("index reordering changed metric mapping: %#v", metrics)
	}
	if len(records[0].Indexes) != len(tradeDailyIndex) {
		testingT.Fatalf("index metadata was not preserved: %#v", records[0].Indexes)
	}
}

func TestRecordCompleteRejectsLegacyAndNoAuthPayloads(testingT *testing.T) {
	date := "2026-11-06"
	identity := "33:002578"
	if RecordComplete(`{"code":"33:002578","values":[]}`, date, identity) {
		testingT.Fatal("legacy payload without index metadata was treated as complete")
	}
	parsedDate, _ := time.ParseInLocation("2006-01-02", date, shanghai())
	body := buildBody([]string{identity}, parsedDate.Unix(), 1, "", func(indexes []map[string]any) {
		indexes[0]["attribute"] = map[string]any{"permission_flag": "NO_AUTH"}
	}, nil)
	records, err := ParseResponse(body, date, []string{identity})
	if err == nil || records != nil {
		testingT.Fatalf("NO_AUTH response was normalized: %#v %v", records, err)
	}
}

func payloadForRecord(testingT *testing.T, record StockRecord) []byte {
	testingT.Helper()
	payload, err := json.Marshal(record)
	if err != nil {
		testingT.Fatal(err)
	}
	return payload
}
