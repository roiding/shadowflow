package thsdataq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	endpoint          = "https://dataq.10jqka.com.cn/fetch-data-server/fetch/v1/specific_data"
	maxResponseBytes  = 8 << 20
	maxCodes          = 20
	pageBegin         = 0
	pageSize          = 20
	tradeDaily        = "TRADE_DAILY"
	indexContinueDays = "up_down_stock_continue_limit_up_days"
	indexFinalTime    = "up_down_stock_final_limit_up_time"
	indexReason       = "up_down_stock_limit_up_reason"
	indexVolumeMoney  = "up_down_stock_limit_up_volume_money_new"
)

var (
	ErrInvalid          = errors.New("invalid THS account data request")
	ErrUnavailable      = errors.New("THS account data unavailable")
	ErrPermissionDenied = errors.New("THS account data permission denied")
	codePattern         = regexp.MustCompile(`^(17|33):[0-9]{6}$`)
	tradeDailyIndex     = []string{indexContinueDays, indexFinalTime, indexReason, indexVolumeMoney}
)

type Client struct {
	doer interface {
		Do(*http.Request) (*http.Response, error)
	}
}

type selector struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

type codeSelectors struct {
	Intersection []selector `json:"intersection"`
}

type indexRequest struct {
	IndexID   string `json:"index_id"`
	TimeType  string `json:"time_type,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

type pageRequest struct {
	PageBegin int `json:"page_begin"`
	PageSize  int `json:"page_size"`
}

type requestBody struct {
	CodeSelectors codeSelectors  `json:"code_selectors"`
	Indexes       []indexRequest `json:"indexes"`
	PageInfo      pageRequest    `json:"page_info"`
}

type responseIndex struct {
	IndexID   string                 `json:"index_id"`
	TimeType  string                 `json:"time_type"`
	Timestamp string                 `json:"timestamp"`
	Attribute responseIndexAttribute `json:"attribute"`
}

type responseIndexAttribute struct {
	PermissionFlag string `json:"permission_flag"`
}

type responseValue struct {
	Index *int            `json:"idx"`
	Value json.RawMessage `json:"value"`
}

type responseStock struct {
	Code   string          `json:"code"`
	Values []responseValue `json:"values"`
}

type responseData struct {
	Indexes []responseIndex `json:"indexes"`
	Data    []responseStock `json:"data"`
	Total   int             `json:"total"`
}

type responseEnvelope struct {
	Data       responseData    `json:"data"`
	StatusCode json.RawMessage `json:"status_code"`
}

type IndexMetadata struct {
	IndexID        string `json:"index_id"`
	TimeType       string `json:"time_type"`
	Timestamp      string `json:"timestamp"`
	PermissionFlag string `json:"permission_flag,omitempty"`
}

type MetricValue struct {
	IndexID        string          `json:"index_id"`
	TimeType       string          `json:"time_type"`
	Timestamp      string          `json:"timestamp"`
	PermissionFlag string          `json:"permission_flag,omitempty"`
	Value          json.RawMessage `json:"value"`
}

type StockRecord struct {
	Code      string          `json:"code"`
	TradeDate string          `json:"trade_date"`
	Source    string          `json:"source"`
	Indexes   []IndexMetadata `json:"indexes"`
	Metrics   []MetricValue   `json:"metrics"`
}

const RecordSource = "ths_dataq"

func NewClient(doer interface {
	Do(*http.Request) (*http.Response, error)
}) *Client {
	return &Client{doer: doer}
}

func (client *Client) Fetch(ctx context.Context, date string, codes []string) (json.RawMessage, error) {
	if client == nil || client.doer == nil {
		return nil, fmt.Errorf("%w: missing HTTP transport", ErrUnavailable)
	}
	timestamp, validationError := validate(date, codes)
	if validationError != nil {
		return nil, validationError
	}
	payload, marshalError := json.Marshal(requestBody{
		CodeSelectors: codeSelectors{Intersection: []selector{{Type: "stock_code", Values: codes}}},
		Indexes:       dailyIndexRequests(timestamp),
		PageInfo:      pageRequest{PageBegin: pageBegin, PageSize: pageSize},
	})
	if marshalError != nil {
		return nil, fmt.Errorf("%w: encode request", ErrInvalid)
	}
	httpRequest, requestError := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if requestError != nil {
		return nil, fmt.Errorf("%w: create request", ErrInvalid)
	}
	httpRequest.Header.Set("Accept", "application/json, text/plain, */*")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Origin", "https://data.10jqka.com.cn")
	httpRequest.Header.Set("Referer", "https://data.10jqka.com.cn/")
	httpRequest.Header.Set("Source-ID", "kamis-5862")
	httpRequest.Header.Set("Platform", "mobileweb")

	httpResponse, doError := client.doer.Do(httpRequest)
	if doError != nil {
		if ctxError := ctx.Err(); ctxError != nil {
			return nil, ctxError
		}
		return nil, doError
	}
	if httpResponse == nil || httpResponse.Body == nil {
		return nil, fmt.Errorf("%w: empty HTTP response", ErrUnavailable)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP status %d", ErrUnavailable, httpResponse.StatusCode)
	}
	if httpResponse.ContentLength > maxResponseBytes {
		return nil, fmt.Errorf("%w: response exceeds size limit", ErrUnavailable)
	}
	body, readError := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
	if readError != nil {
		return nil, fmt.Errorf("%w: read response", ErrUnavailable)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("%w: response exceeds size limit", ErrUnavailable)
	}
	trimmedBody := bytes.TrimSpace(body)
	if len(trimmedBody) == 0 {
		return nil, fmt.Errorf("%w: empty response", ErrUnavailable)
	}
	if trimmedBody[0] != '{' {
		return nil, fmt.Errorf("%w: non-object response", ErrUnavailable)
	}
	if _, validationError := ParseResponse(trimmedBody, date, codes); validationError != nil {
		return nil, validationError
	}
	return json.RawMessage(append([]byte(nil), body...)), nil
}

func validate(date string, codes []string) (int64, error) {
	timestamp, parseError := tradeDateTimestamp(date)
	if parseError != nil {
		return 0, parseError
	}
	if len(codes) == 0 || len(codes) > maxCodes {
		return 0, fmt.Errorf("%w: codes must contain 1 to %d entries", ErrInvalid, maxCodes)
	}
	seen := make(map[string]bool, len(codes))
	for _, code := range codes {
		if !codePattern.MatchString(code) {
			return 0, fmt.Errorf("%w: code must be 17 or 33 plus six digits", ErrInvalid)
		}
		if seen[code] {
			return 0, fmt.Errorf("%w: duplicate code", ErrInvalid)
		}
		seen[code] = true
	}
	return timestamp, nil
}

func tradeDateTimestamp(date string) (int64, error) {
	parsedDate, parseError := time.ParseInLocation("2006-01-02", date, shanghai())
	if parseError != nil || parsedDate.Format("2006-01-02") != date {
		return 0, fmt.Errorf("%w: date must be YYYY-MM-DD", ErrInvalid)
	}
	current := time.Now().In(shanghai())
	today := time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, current.Location())
	if parsedDate.After(today) {
		return 0, fmt.Errorf("%w: future date is not allowed", ErrInvalid)
	}
	return parsedDate.Unix(), nil
}

func dailyIndexRequests(timestamp int64) []indexRequest {
	requests := make([]indexRequest, 0, len(tradeDailyIndex))
	for _, indexID := range tradeDailyIndex {
		requests = append(requests, indexRequest{IndexID: indexID, TimeType: tradeDaily, Timestamp: timestamp})
	}
	return requests
}

func MetricIndexIDs() []string {
	return append([]string(nil), tradeDailyIndex...)
}

func ParseResponse(payload json.RawMessage, date string, codes []string) ([]StockRecord, error) {
	timestamp, timestampError := tradeDateTimestamp(date)
	if timestampError != nil {
		return nil, timestampError
	}
	expectedCodes := make(map[string]bool, len(codes))
	for _, code := range codes {
		if !codePattern.MatchString(code) {
			return nil, fmt.Errorf("%w: code must be 17 or 33 plus six digits", ErrInvalid)
		}
		if expectedCodes[code] {
			return nil, fmt.Errorf("%w: duplicate code", ErrInvalid)
		}
		expectedCodes[code] = true
	}
	var envelope responseEnvelope
	if unmarshalError := json.Unmarshal(payload, &envelope); unmarshalError != nil {
		return nil, fmt.Errorf("%w: invalid response JSON", ErrUnavailable)
	}
	status, statusError := parseStatus(envelope.StatusCode)
	if statusError != nil || status != 0 {
		return nil, fmt.Errorf("%w: business status is not zero", ErrUnavailable)
	}
	return normalizeResponse(envelope.Data, date, timestamp, expectedCodes)
}

func ParseStoredResponse(payload json.RawMessage, date string) ([]StockRecord, error) {
	var parsed struct {
		Data struct {
			Data []struct {
				Code string `json:"code"`
			} `json:"data"`
		} `json:"data"`
	}
	if unmarshalError := json.Unmarshal(payload, &parsed); unmarshalError != nil {
		return nil, fmt.Errorf("%w: invalid response JSON", ErrUnavailable)
	}
	codes := make([]string, 0, len(parsed.Data.Data))
	for _, row := range parsed.Data.Data {
		codes = append(codes, row.Code)
	}
	if len(codes) == 0 {
		return nil, fmt.Errorf("%w: response contains no rows", ErrUnavailable)
	}
	return ParseResponse(payload, date, codes)
}

func normalizeResponse(data responseData, date string, timestamp int64, expectedCodes map[string]bool) ([]StockRecord, error) {
	expectedTimestamp := strconv.FormatInt(timestamp, 10)
	if data.Total != len(data.Data) {
		return nil, fmt.Errorf("%w: response total or row count does not match requested codes", ErrUnavailable)
	}
	if len(data.Data) == 0 {
		if len(expectedCodes) == 0 {
			return []StockRecord{}, nil
		}
		return nil, fmt.Errorf("%w: response total or row count does not match requested codes", ErrUnavailable)
	}
	seenIndexes := make(map[string]bool, len(data.Indexes))
	for _, index := range data.Indexes {
		if strings.TrimSpace(index.IndexID) == "" || seenIndexes[index.IndexID] {
			return nil, fmt.Errorf("%w: response index metadata is invalid or duplicated", ErrUnavailable)
		}
		seenIndexes[index.IndexID] = true
	}
	indexPositions := make(map[string]int, len(tradeDailyIndex))
	metadata := make(map[string]IndexMetadata, len(tradeDailyIndex))
	for _, indexID := range tradeDailyIndex {
		position, exists := indexPosition(data.Indexes, indexID)
		if !exists {
			return nil, fmt.Errorf("%w: response indexes are incomplete", ErrUnavailable)
		}
		index := data.Indexes[position]
		if index.TimeType != tradeDaily || strings.TrimSpace(index.Timestamp) != expectedTimestamp {
			return nil, fmt.Errorf("%w: response index metadata does not match request", ErrUnavailable)
		}
		if strings.EqualFold(strings.TrimSpace(index.Attribute.PermissionFlag), "NO_AUTH") {
			return nil, fmt.Errorf("%w: index %s returned NO_AUTH", ErrPermissionDenied, indexID)
		}
		indexPositions[indexID] = position
		metadata[indexID] = IndexMetadata{
			IndexID: indexID, TimeType: index.TimeType, Timestamp: index.Timestamp,
			PermissionFlag: strings.TrimSpace(index.Attribute.PermissionFlag),
		}
	}
	seenCodes := make(map[string]bool, len(data.Data))
	records := make([]StockRecord, 0, len(data.Data))
	for _, stock := range data.Data {
		if !expectedCodes[stock.Code] || seenCodes[stock.Code] {
			return nil, fmt.Errorf("%w: response stock code set does not match request", ErrUnavailable)
		}
		seenCodes[stock.Code] = true
		valuesByIndex := make(map[int]json.RawMessage, len(stock.Values))
		for _, value := range stock.Values {
			if value.Index == nil || *value.Index < 0 || *value.Index >= len(data.Indexes) {
				return nil, fmt.Errorf("%w: response stock index is invalid or duplicated", ErrUnavailable)
			}
			if _, exists := valuesByIndex[*value.Index]; exists {
				return nil, fmt.Errorf("%w: response stock index is invalid or duplicated", ErrUnavailable)
			}
			if len(bytes.TrimSpace(value.Value)) == 0 {
				return nil, fmt.Errorf("%w: response stock value is missing", ErrUnavailable)
			}
			valuesByIndex[*value.Index] = value.Value
		}
		metrics := make([]MetricValue, 0, len(tradeDailyIndex))
		for _, indexID := range tradeDailyIndex {
			value, exists := valuesByIndex[indexPositions[indexID]]
			if !exists {
				return nil, fmt.Errorf("%w: response stock values are incomplete", ErrUnavailable)
			}
			item := metadata[indexID]
			metrics = append(metrics, MetricValue{
				IndexID: item.IndexID, TimeType: item.TimeType, Timestamp: item.Timestamp,
				PermissionFlag: item.PermissionFlag, Value: append(json.RawMessage(nil), value...),
			})
		}
		records = append(records, StockRecord{
			Code: stock.Code, TradeDate: date, Source: RecordSource,
			Indexes: []IndexMetadata{
				metadata[indexContinueDays], metadata[indexFinalTime], metadata[indexReason], metadata[indexVolumeMoney],
			},
			Metrics: metrics,
		})
	}
	if len(seenCodes) != len(expectedCodes) {
		return nil, fmt.Errorf("%w: response stock code set does not match request", ErrUnavailable)
	}
	return records, nil
}

func indexPosition(indexes []responseIndex, indexID string) (int, bool) {
	for position, index := range indexes {
		if index.IndexID == indexID {
			return position, true
		}
	}
	return 0, false
}

func RecordComplete(payload string, date string, identity string) bool {
	if !codePattern.MatchString(identity) {
		return false
	}
	timestamp, timestampError := tradeDateTimestamp(date)
	if timestampError != nil {
		return false
	}
	var record StockRecord
	if unmarshalError := json.Unmarshal([]byte(payload), &record); unmarshalError != nil {
		return false
	}
	if record.Code != identity || record.TradeDate != date || record.Source != RecordSource {
		return false
	}
	expectedTimestamp := strconv.FormatInt(timestamp, 10)
	indexes := make(map[string]IndexMetadata, len(record.Indexes))
	for _, index := range record.Indexes {
		if index.IndexID == "" {
			return false
		}
		if _, exists := indexes[index.IndexID]; exists {
			return false
		}
		indexes[index.IndexID] = index
	}
	for _, indexID := range tradeDailyIndex {
		index, exists := indexes[indexID]
		if !exists || index.TimeType != tradeDaily || index.Timestamp != expectedTimestamp {
			return false
		}
		if strings.EqualFold(strings.TrimSpace(index.PermissionFlag), "NO_AUTH") {
			return false
		}
	}
	metrics := make(map[string]MetricValue, len(record.Metrics))
	for _, metric := range record.Metrics {
		if _, exists := metrics[metric.IndexID]; exists {
			return false
		}
		if len(bytes.TrimSpace(metric.Value)) == 0 {
			return false
		}
		metrics[metric.IndexID] = metric
	}
	for _, indexID := range tradeDailyIndex {
		metric, exists := metrics[indexID]
		if !exists || metric.TimeType != tradeDaily || metric.Timestamp != expectedTimestamp {
			return false
		}
		if strings.EqualFold(strings.TrimSpace(metric.PermissionFlag), "NO_AUTH") {
			return false
		}
	}
	return true
}

func parseStatus(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, errors.New("missing status")
	}
	var status int
	if unmarshalError := json.Unmarshal(raw, &status); unmarshalError == nil {
		return status, nil
	}
	var text string
	if unmarshalError := json.Unmarshal(raw, &text); unmarshalError == nil {
		return strconv.Atoi(strings.TrimSpace(text))
	}
	return 0, errors.New("invalid status")
}

func shanghai() *time.Location {
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}
