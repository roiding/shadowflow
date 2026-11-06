package ths

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
)

const (
	DefaultBaseURL     = "https://data.10jqka.com.cn/dataapi/limit_up"
	DefaultUserAgent   = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_2_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148"
	DefaultReferer     = "https://data.10jqka.com.cn/mobile/limitup/v2/index.html"
	DefaultFieldList   = "199112,10,9001,330323,330324,330325,9002,330329,133971,133970,1968584,3475914,9003,9004"
	DefaultOrderField  = "330324"
	DefaultOrderType   = "0"
	defaultPageLimit   = 100
	defaultMinInterval = 1 * time.Second
	maxPageBudget      = limitup.MaxPoolPages
	maxBodyLimit       = 4 << 20
)

type Client struct {
	baseURL     string
	httpClient  *http.Client
	minInterval time.Duration
	pageLimit   int
	clock       func() time.Time
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	targetURL := strings.TrimRight(baseURL, "/")
	if targetURL == "" {
		targetURL = DefaultBaseURL
	}
	transportClient := httpClient
	if transportClient == nil {
		transportClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL:     targetURL,
		httpClient:  transportClient,
		minInterval: defaultMinInterval,
		pageLimit:   defaultPageLimit,
		clock:       time.Now,
	}
}

func (client *Client) WithMinInterval(interval time.Duration) *Client {
	client.minInterval = interval
	return client
}

func (client *Client) WithPageLimit(pageLimit int) *Client {
	if pageLimit > 0 {
		client.pageLimit = pageLimit
	}
	return client
}

func (client *Client) WithClock(clock func() time.Time) *Client {
	if clock != nil {
		client.clock = clock
	}
	return client
}

func (client *Client) FetchDay(ctx context.Context, date string, scope string) (limitup.Snapshot, error) {
	shanghaiZone := limitup.Shanghai()
	parsedDate, parseError := time.ParseInLocation("2006-01-02", date, shanghaiZone)
	if parseError != nil || parsedDate.Format("2006-01-02") != date {
		return limitup.Snapshot{}, fmt.Errorf("%w: invalid trade date format %s", limitup.ErrInvalid, date)
	}
	if !limitup.ValidScope(scope) {
		return limitup.Snapshot{}, fmt.Errorf("%w: unsupported market scope %s", limitup.ErrInvalid, scope)
	}

	currentTime := client.clock().In(shanghaiZone)
	todayDate := time.Date(currentTime.Year(), currentTime.Month(), currentTime.Day(), 0, 0, 0, 0, shanghaiZone)
	if parsedDate.After(todayDate) {
		return limitup.Snapshot{}, fmt.Errorf("%w: cannot collect future trade date %s", limitup.ErrInvalid, date)
	}
	isToday := parsedDate.Equal(todayDate)
	if isToday {
		cutoffTime := parsedDate.Add(15*time.Hour + 30*time.Minute)
		if currentTime.Before(cutoffTime) {
			return limitup.Snapshot{}, fmt.Errorf("%w: daily snapshot unavailable before 15:30 Asia/Shanghai", limitup.ErrInvalid)
		}
	}

	dateDigits := strings.ReplaceAll(date, "-", "")
	seenStockPools := make(map[string]map[string]bool)

	sealedEvents, sealedRawPages, sealedSummaries, sealedFetchError := client.fetchPool(ctx, date, dateDigits, scope, limitup.PoolSealed, isToday, nil, seenStockPools)
	if sealedFetchError != nil {
		return limitup.Snapshot{}, sealedFetchError
	}

	if intervalError := client.sleep(ctx); intervalError != nil {
		return limitup.Snapshot{}, intervalError
	}

	brokenEvents, brokenRawPages, brokenSummaries, brokenFetchError := client.fetchPool(ctx, date, dateDigits, scope, limitup.PoolBroken, isToday, &sealedSummaries, seenStockPools)
	if brokenFetchError != nil {
		return limitup.Snapshot{}, brokenFetchError
	}
	if brokenSummaries != sealedSummaries {
		return limitup.Snapshot{}, fmt.Errorf("inconsistent daily summaries between sealed and broken pools")
	}

	if intervalError := client.sleep(ctx); intervalError != nil {
		return limitup.Snapshot{}, intervalError
	}

	downEvents, downRawPages, downSummaries, downFetchError := client.fetchPool(ctx, date, dateDigits, scope, limitup.PoolDown, isToday, &sealedSummaries, seenStockPools)
	if downFetchError != nil {
		return limitup.Snapshot{}, downFetchError
	}
	if downSummaries != sealedSummaries {
		return limitup.Snapshot{}, fmt.Errorf("inconsistent daily summaries between limit-up and limit-down pools")
	}

	rawPages := make([]json.RawMessage, 0, len(sealedRawPages)+len(brokenRawPages)+len(downRawPages))
	rawPages = append(rawPages, sealedRawPages...)
	rawPages = append(rawPages, brokenRawPages...)
	rawPages = append(rawPages, downRawPages...)

	allEvents := make([]limitup.Event, 0, len(sealedEvents)+len(brokenEvents)+len(downEvents))
	allEvents = append(allEvents, sealedEvents...)
	allEvents = append(allEvents, brokenEvents...)
	allEvents = append(allEvents, downEvents...)

	if len(allEvents) == 0 {
		if intervalError := client.sleep(ctx); intervalError != nil {
			return limitup.Snapshot{}, intervalError
		}
		isTradeDay, tradeDayError := client.checkTradeDay(ctx, dateDigits)
		if tradeDayError != nil {
			return limitup.Snapshot{}, tradeDayError
		}
		if !isTradeDay {
			return limitup.Snapshot{}, fmt.Errorf("%w: %s is not a trading day", limitup.ErrInvalid, date)
		}
	}

	snapshot := limitup.Snapshot{
		TradeDate:    date,
		Source:       limitup.SourceTHS,
		Scope:        scope,
		FetchedAt:    client.clock().In(shanghaiZone),
		SealedCount:  len(sealedEvents),
		BrokenCount:  len(brokenEvents),
		DownCount:    len(downEvents),
		TouchedCount: len(sealedEvents) + len(brokenEvents),
		Events:       allEvents,
		RawPages:     rawPages,
	}

	if validateError := limitup.ValidateSnapshot(snapshot); validateError != nil {
		return limitup.Snapshot{}, validateError
	}

	return snapshot, nil
}

type summaryCounts struct {
	Num        int
	OpenNum    int
	HistoryNum int
}

type dailySummaries struct {
	Up   summaryCounts
	Down summaryCounts
}

func (client *Client) fetchPool(
	ctx context.Context,
	tradeDate string,
	dateDigits string,
	scope string,
	poolName string,
	isToday bool,
	expectedSummaries *dailySummaries,
	seenStockPools map[string]map[string]bool,
) ([]limitup.Event, []json.RawMessage, dailySummaries, error) {
	poolPath := poolEndpoint(poolName)

	pageNumber := 1
	var poolEvents []limitup.Event
	var rawPages []json.RawMessage
	var resolvedSummaries dailySummaries
	expectedTotalCount := 0
	expectedPageCount := 0
	expectedPageLimit := 0

	for {
		if ctxError := ctx.Err(); ctxError != nil {
			return nil, nil, dailySummaries{}, ctxError
		}

		if pageNumber > 1 {
			if intervalError := client.sleep(ctx); intervalError != nil {
				return nil, nil, dailySummaries{}, intervalError
			}
		}

		pageEvents, rawPageBytes, pageMeta, pageSummaries, fetchPageError := client.fetchPage(
			ctx,
			poolPath,
			pageNumber,
			tradeDate,
			dateDigits,
			scope,
			poolName,
			isToday,
			seenStockPools,
		)
		if fetchPageError != nil {
			return nil, nil, dailySummaries{}, fetchPageError
		}

		rawPages = append(rawPages, rawPageBytes)

		if pageNumber == 1 {
			resolvedSummaries = pageSummaries
			if expectedSummaries != nil && pageSummaries != *expectedSummaries {
				return nil, nil, dailySummaries{}, fmt.Errorf("inconsistent daily summaries across pools")
			}

			expectedTotalCount = pageTotalForPool(poolName, resolvedSummaries)
			if expectedTotalCount < 0 || expectedTotalCount > limitup.MaxDailyEvents {
				return nil, nil, dailySummaries{}, fmt.Errorf("invalid total %d for %s", expectedTotalCount, poolName)
			}
			expectedPageLimit = pageMeta.Limit
			if expectedPageLimit <= 0 {
				return nil, nil, dailySummaries{}, fmt.Errorf("invalid page limit %d for %s", expectedPageLimit, poolName)
			}
			if pageMeta.Total != expectedTotalCount {
				return nil, nil, dailySummaries{}, fmt.Errorf("page total %d does not match summary count %d for %s", pageMeta.Total, expectedTotalCount, poolName)
			}

			if expectedTotalCount == 0 {
				if len(pageEvents) != 0 {
					return nil, nil, dailySummaries{}, fmt.Errorf("empty pool %s has non-empty event list", poolName)
				}
				if pageMeta.Count != 0 {
					return nil, nil, dailySummaries{}, fmt.Errorf("empty pool %s has page count %d", poolName, pageMeta.Count)
				}
				poolEvents = []limitup.Event{}
				break
			}

			expectedPageCount = (expectedTotalCount + expectedPageLimit - 1) / expectedPageLimit
			if expectedPageCount > maxPageBudget {
				return nil, nil, dailySummaries{}, fmt.Errorf("pool %s exceeds max page budget: %d pages", poolName, expectedPageCount)
			}
			if pageMeta.Count != expectedPageCount {
				return nil, nil, dailySummaries{}, fmt.Errorf("page count %d does not match total %d and limit %d for %s", pageMeta.Count, expectedTotalCount, expectedPageLimit, poolName)
			}
			poolEvents = make([]limitup.Event, 0, expectedTotalCount)
		} else {
			if pageSummaries != resolvedSummaries {
				return nil, nil, dailySummaries{}, fmt.Errorf("unstable daily summaries for %s on page %d", poolName, pageNumber)
			}
			if pageMeta.Total != expectedTotalCount {
				return nil, nil, dailySummaries{}, fmt.Errorf("unstable page total for %s: expected %d, got %d", poolName, expectedTotalCount, pageMeta.Total)
			}
			if pageMeta.Count != expectedPageCount {
				return nil, nil, dailySummaries{}, fmt.Errorf("unstable page count for %s: expected %d, got %d", poolName, expectedPageCount, pageMeta.Count)
			}
			if pageMeta.Limit != expectedPageLimit {
				return nil, nil, dailySummaries{}, fmt.Errorf("unstable page limit for %s: expected %d, got %d", poolName, expectedPageLimit, pageMeta.Limit)
			}
		}

		expectedEventsOnPage := expectedPageLimit
		if pageNumber == expectedPageCount {
			expectedEventsOnPage = expectedTotalCount - expectedPageLimit*(expectedPageCount-1)
		}
		if len(pageEvents) != expectedEventsOnPage {
			return nil, nil, dailySummaries{}, fmt.Errorf("received %d events on page %d for %s, expected %d", len(pageEvents), pageNumber, poolName, expectedEventsOnPage)
		}

		poolEvents = append(poolEvents, pageEvents...)

		if pageNumber >= expectedPageCount {
			break
		}
		pageNumber++
	}

	if len(poolEvents) != expectedTotalCount {
		return nil, nil, dailySummaries{}, fmt.Errorf("event count mismatch for %s: fetched %d, expected total %d", poolName, len(poolEvents), expectedTotalCount)
	}

	return poolEvents, rawPages, resolvedSummaries, nil
}

func poolEndpoint(poolName string) string {
	switch poolName {
	case limitup.PoolBroken:
		return "open_limit_pool"
	case limitup.PoolDown:
		return "lower_limit_pool"
	default:
		return "limit_up_pool"
	}
}

func pageTotalForPool(poolName string, summaries dailySummaries) int {
	switch poolName {
	case limitup.PoolBroken:
		return summaries.Up.OpenNum
	case limitup.PoolDown:
		return summaries.Down.Num
	default:
		return summaries.Up.Num
	}
}

type parsedPageMeta struct {
	Total int
	Count int
	Page  int
	Limit int
}

func (client *Client) fetchPage(
	ctx context.Context,
	poolPath string,
	pageNumber int,
	tradeDate string,
	dateDigits string,
	scope string,
	poolName string,
	isToday bool,
	seenStockPools map[string]map[string]bool,
) ([]limitup.Event, json.RawMessage, parsedPageMeta, dailySummaries, error) {
	requestURL := fmt.Sprintf(
		"%s/%s?page=%d&limit=%d&field=%s&filter=%s&date=%s&order_field=%s&order_type=%s",
		client.baseURL,
		poolPath,
		pageNumber,
		client.pageLimit,
		url.QueryEscape(DefaultFieldList),
		url.QueryEscape(scope),
		dateDigits,
		url.QueryEscape(DefaultOrderField),
		url.QueryEscape(DefaultOrderType),
	)

	httpRequest, requestError := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if requestError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, requestError
	}
	httpRequest.Header.Set("User-Agent", DefaultUserAgent)
	httpRequest.Header.Set("Referer", DefaultReferer)
	httpRequest.Header.Set("Accept", "application/json, text/plain, */*")

	httpResponse, roundTripError := client.httpClient.Do(httpRequest)
	if roundTripError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, roundTripError
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("unexpected http status %d from %s", httpResponse.StatusCode, requestURL)
	}

	bodyBuffer, readError := io.ReadAll(io.LimitReader(httpResponse.Body, maxBodyLimit+1))
	if readError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, readError
	}
	if len(bodyBuffer) > maxBodyLimit {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, errors.New("ths response exceeds body limit")
	}
	if len(bodyBuffer) == 0 {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, errors.New("empty response body from ths")
	}

	trimmedBody := bytes.TrimSpace(bodyBuffer)
	if len(trimmedBody) > 0 && trimmedBody[0] == '<' {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, errors.New("ths returned html instead of json")
	}

	var envelope struct {
		StatusCode json.RawMessage `json:"status_code"`
		StatusMsg  string          `json:"status_msg"`
		Data       json.RawMessage `json:"data"`
	}
	if unmarshalError := json.Unmarshal(bodyBuffer, &envelope); unmarshalError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("invalid json response from ths: %w", unmarshalError)
	}

	businessStatusCode, statusCodeError := parseInteger(envelope.StatusCode)
	if statusCodeError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("invalid status_code in ths response: %w", statusCodeError)
	}
	if businessStatusCode != 0 {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("ths business error code %d: %s", businessStatusCode, envelope.StatusMsg)
	}

	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, errors.New("ths response missing data payload")
	}

	var responseData struct {
		Date         json.RawMessage   `json:"date"`
		Page         json.RawMessage   `json:"page"`
		LimitUpCount json.RawMessage   `json:"limit_up_count"`
		LimitDownCnt json.RawMessage   `json:"limit_down_count"`
		TradeStatus  json.RawMessage   `json:"trade_status"`
		Info         []json.RawMessage `json:"info"`
	}
	if unmarshalDataError := json.Unmarshal(envelope.Data, &responseData); unmarshalDataError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("failed to decode ths data object: %w", unmarshalDataError)
	}
	if responseData.Info == nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, errors.New("ths response requires an explicit info array")
	}

	dateString, dateParseError := parseStringValue(responseData.Date)
	if dateParseError != nil || dateString != dateDigits {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("ths response date mismatch: requested %s, got %s", dateDigits, dateString)
	}

	if isToday {
		if len(responseData.TradeStatus) == 0 || string(responseData.TradeStatus) == "null" {
			return nil, nil, parsedPageMeta{}, dailySummaries{}, errors.New("missing trade_status for today snapshot")
		}
		var tradeStatusObj struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if unmarshalStatusError := json.Unmarshal(responseData.TradeStatus, &tradeStatusObj); unmarshalStatusError != nil {
			return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("invalid trade_status format: %w", unmarshalStatusError)
		}
		if tradeStatusObj.ID != "closed" {
			return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("market is not closed for today: trade status is %s (%s)", tradeStatusObj.ID, tradeStatusObj.Name)
		}
	}

	upSummary, upSummaryError := parseTodaySummary(responseData.LimitUpCount, "limit_up_count", true)
	if upSummaryError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, upSummaryError
	}
	downSummary, downSummaryError := parseTodaySummary(responseData.LimitDownCnt, "limit_down_count", false)
	if downSummaryError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, downSummaryError
	}
	summariesResult := dailySummaries{Up: upSummary, Down: downSummary}

	if len(responseData.Page) == 0 || string(responseData.Page) == "null" {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, errors.New("ths response has null or missing page object")
	}
	var pageRaw struct {
		Total json.RawMessage `json:"total"`
		Count json.RawMessage `json:"count"`
		Page  json.RawMessage `json:"page"`
		Limit json.RawMessage `json:"limit"`
	}
	if unmarshalPageError := json.Unmarshal(responseData.Page, &pageRaw); unmarshalPageError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("failed to decode page object: %w", unmarshalPageError)
	}

	totalValue, totalError := parseInteger(pageRaw.Total)
	countValue, countError := parseInteger(pageRaw.Count)
	pageIndexValue, pageIndexError := parseInteger(pageRaw.Page)
	limitValue, limitError := parseInteger(pageRaw.Limit)
	if totalError != nil || countError != nil || pageIndexError != nil || limitError != nil {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, errors.New("invalid integer values in page metadata")
	}
	if pageIndexValue != pageNumber && !(pageNumber == 1 && pageIndexValue == 0 && totalValue == 0) {
		return nil, nil, parsedPageMeta{}, dailySummaries{}, fmt.Errorf("page number mismatch: expected %d, got %d", pageNumber, pageIndexValue)
	}
	pageMeta := parsedPageMeta{
		Total: totalValue,
		Count: countValue,
		Page:  pageIndexValue,
		Limit: limitValue,
	}

	events := make([]limitup.Event, 0, len(responseData.Info))
	for _, rawStockBytes := range responseData.Info {
		event, parseStockError := parseStockEvent(rawStockBytes, tradeDate, poolName)
		if parseStockError != nil {
			return nil, nil, parsedPageMeta{}, dailySummaries{}, parseStockError
		}
		if conflictError := addStockPool(seenStockPools, event.Code, poolName); conflictError != nil {
			return nil, nil, parsedPageMeta{}, dailySummaries{}, conflictError
		}
		events = append(events, event)
	}

	return events, bodyBuffer, pageMeta, summariesResult, nil
}

func addStockPool(seenStockPools map[string]map[string]bool, code string, poolName string) error {
	pools := seenStockPools[code]
	if pools == nil {
		pools = make(map[string]bool)
		seenStockPools[code] = pools
	}
	if pools[poolName] {
		return fmt.Errorf("%w: duplicate stock code %s in %s", limitup.ErrInvalid, code, poolName)
	}
	conflictsWithSealed := poolName == limitup.PoolSealed && len(pools) > 0
	conflictsWithSealed = conflictsWithSealed || (poolName != limitup.PoolSealed && pools[limitup.PoolSealed])
	if conflictsWithSealed {
		return fmt.Errorf("%w: stock %s conflicts with sealed pool", limitup.ErrInvalid, code)
	}
	pools[poolName] = true
	return nil
}

func parseTodaySummary(rawSummary json.RawMessage, fieldName string, requireHistorySum bool) (summaryCounts, error) {
	if len(rawSummary) == 0 || string(rawSummary) == "null" {
		return summaryCounts{}, fmt.Errorf("ths response has null or missing %s", fieldName)
	}
	var countEnvelope struct {
		Today json.RawMessage `json:"today"`
	}
	if unmarshalError := json.Unmarshal(rawSummary, &countEnvelope); unmarshalError != nil {
		return summaryCounts{}, fmt.Errorf("failed to decode %s: %w", fieldName, unmarshalError)
	}
	if len(countEnvelope.Today) == 0 || string(countEnvelope.Today) == "null" {
		return summaryCounts{}, fmt.Errorf("ths response has null or missing %s.today", fieldName)
	}
	var countData struct {
		Num        json.RawMessage `json:"num"`
		OpenNum    json.RawMessage `json:"open_num"`
		HistoryNum json.RawMessage `json:"history_num"`
	}
	if unmarshalTodayError := json.Unmarshal(countEnvelope.Today, &countData); unmarshalTodayError != nil {
		return summaryCounts{}, fmt.Errorf("failed to decode %s.today: %w", fieldName, unmarshalTodayError)
	}
	todayNum, numError := parseInteger(countData.Num)
	todayOpenNum, openNumError := parseInteger(countData.OpenNum)
	todayHistoryNum, historyNumError := parseInteger(countData.HistoryNum)
	if numError != nil || openNumError != nil || historyNumError != nil {
		return summaryCounts{}, fmt.Errorf("invalid integer values in %s.today", fieldName)
	}
	if todayNum < 0 || todayOpenNum < 0 || todayHistoryNum < 0 {
		return summaryCounts{}, fmt.Errorf("negative %s counts: num=%d, open_num=%d, history_num=%d", fieldName, todayNum, todayOpenNum, todayHistoryNum)
	}
	if requireHistorySum && todayHistoryNum != todayNum+todayOpenNum {
		return summaryCounts{}, fmt.Errorf("inconsistent %s counts: num=%d, open_num=%d, history_num=%d", fieldName, todayNum, todayOpenNum, todayHistoryNum)
	}
	return summaryCounts{Num: todayNum, OpenNum: todayOpenNum, HistoryNum: todayHistoryNum}, nil
}

func (client *Client) checkTradeDay(ctx context.Context, dateDigits string) (bool, error) {
	requestURL := fmt.Sprintf("%s/trade_day?date=%s&stock=stock&prev=1&next=1", client.baseURL, dateDigits)
	httpRequest, requestError := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if requestError != nil {
		return false, requestError
	}
	httpRequest.Header.Set("User-Agent", DefaultUserAgent)
	httpRequest.Header.Set("Referer", DefaultReferer)
	httpRequest.Header.Set("Accept", "application/json, text/plain, */*")

	httpResponse, roundTripError := client.httpClient.Do(httpRequest)
	if roundTripError != nil {
		return false, roundTripError
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected http status %d from trade_day %s", httpResponse.StatusCode, requestURL)
	}

	bodyBuffer, readError := io.ReadAll(io.LimitReader(httpResponse.Body, maxBodyLimit+1))
	if readError != nil {
		return false, readError
	}
	if len(bodyBuffer) > maxBodyLimit {
		return false, errors.New("trade_day response exceeds body limit")
	}

	var envelope struct {
		StatusCode json.RawMessage `json:"status_code"`
		StatusMsg  string          `json:"status_msg"`
		Data       struct {
			Code     json.RawMessage `json:"code"`
			TradeDay json.RawMessage `json:"trade_day"`
		} `json:"data"`
	}
	if unmarshalError := json.Unmarshal(bodyBuffer, &envelope); unmarshalError != nil {
		return false, fmt.Errorf("invalid json from trade_day: %w", unmarshalError)
	}

	businessStatusCode, statusCodeError := parseInteger(envelope.StatusCode)
	if statusCodeError != nil || businessStatusCode != 0 {
		return false, fmt.Errorf("trade_day business error %d: %s", businessStatusCode, envelope.StatusMsg)
	}
	dataStatusCode, dataCodeError := parseInteger(envelope.Data.Code)
	if dataCodeError != nil || dataStatusCode != 0 {
		return false, fmt.Errorf("trade_day data error %d for %s", dataStatusCode, dateDigits)
	}

	var isTradeDay bool
	if boolError := json.Unmarshal(envelope.Data.TradeDay, &isTradeDay); boolError != nil {
		return false, fmt.Errorf("invalid trade_day boolean field: %w", boolError)
	}

	return isTradeDay, nil
}

func parseStockEvent(rawStockBytes json.RawMessage, tradeDate string, poolName string) (limitup.Event, error) {
	if !json.Valid(rawStockBytes) {
		return limitup.Event{}, fmt.Errorf("%w: invalid raw stock json", limitup.ErrInvalid)
	}

	var item struct {
		Code           json.RawMessage `json:"code"`
		Name           json.RawMessage `json:"name"`
		MarketID       json.RawMessage `json:"market_id"`
		MarketType     json.RawMessage `json:"market_type"`
		OpenNum        json.RawMessage `json:"open_num"`
		FirstLimitTime json.RawMessage `json:"first_limit_up_time"`
		LastLimitTime  json.RawMessage `json:"last_limit_up_time"`
		LimitUpType    json.RawMessage `json:"limit_up_type"`
		HighDays       json.RawMessage `json:"high_days"`
		ReasonType     json.RawMessage `json:"reason_type"`
		LimitUpSucRate json.RawMessage `json:"limit_up_suc_rate"`
		ChangeRate     json.RawMessage `json:"change_rate"`
		TurnoverRate   json.RawMessage `json:"turnover_rate"`
	}
	if unmarshalError := json.Unmarshal(rawStockBytes, &item); unmarshalError != nil {
		return limitup.Event{}, fmt.Errorf("%w: failed to decode stock item: %v", limitup.ErrInvalid, unmarshalError)
	}

	rawCodeString, codeError := parseStringValue(item.Code)
	if codeError != nil {
		return limitup.Event{}, fmt.Errorf("%w: missing or invalid code: %v", limitup.ErrInvalid, codeError)
	}
	trimmedCode := strings.TrimSpace(rawCodeString)
	if len(trimmedCode) > 0 && len(trimmedCode) < 6 {
		trimmedCode = fmt.Sprintf("%06s", trimmedCode)
	}
	if !limitup.ValidCode(trimmedCode) {
		return limitup.Event{}, fmt.Errorf("%w: invalid stock code %s", limitup.ErrInvalid, trimmedCode)
	}

	stockName, nameError := parseStringValue(item.Name)
	if nameError != nil || strings.TrimSpace(stockName) == "" {
		return limitup.Event{}, fmt.Errorf("%w: invalid stock name for %s", limitup.ErrInvalid, trimmedCode)
	}

	marketIDValue, marketIDError := parseInteger(item.MarketID)
	if marketIDError != nil || marketIDValue <= 0 {
		return limitup.Event{}, fmt.Errorf("%w: invalid market_id for %s", limitup.ErrInvalid, trimmedCode)
	}

	marketTypeString, marketTypeError := parseStringValue(item.MarketType)
	if marketTypeError != nil || strings.TrimSpace(marketTypeString) == "" {
		return limitup.Event{}, fmt.Errorf("%w: invalid market_type for %s", limitup.ErrInvalid, trimmedCode)
	}

	openCountPointer, openCountError := parseOptionalInteger(item.OpenNum)
	if openCountError != nil {
		return limitup.Event{}, fmt.Errorf("%w: invalid open_num for %s: %v", limitup.ErrInvalid, trimmedCode, openCountError)
	}
	if openCountPointer != nil && *openCountPointer < 0 {
		return limitup.Event{}, fmt.Errorf("%w: negative open_num for %s", limitup.ErrInvalid, trimmedCode)
	}

	firstLimitTimestamp, firstLimitError := parseTimestamp(item.FirstLimitTime, tradeDate)
	if firstLimitError != nil {
		return limitup.Event{}, fmt.Errorf("%w: first_limit_up_time error for %s: %v", limitup.ErrInvalid, trimmedCode, firstLimitError)
	}

	lastLimitTimestamp, lastLimitError := parseTimestamp(item.LastLimitTime, tradeDate)
	if lastLimitError != nil {
		return limitup.Event{}, fmt.Errorf("%w: last_limit_up_time error for %s: %v", limitup.ErrInvalid, trimmedCode, lastLimitError)
	}

	if firstLimitTimestamp != nil && lastLimitTimestamp != nil && lastLimitTimestamp.Before(*firstLimitTimestamp) {
		return limitup.Event{}, fmt.Errorf("%w: last limit time before first for %s", limitup.ErrInvalid, trimmedCode)
	}

	limitTypeString, _ := parseStringValue(item.LimitUpType)
	highDaysString, _ := parseStringValue(item.HighDays)
	reasonString, _ := parseStringValue(item.ReasonType)

	sealRatePointer, sealRateError := parseOptionalFloat(item.LimitUpSucRate)
	if sealRateError != nil {
		return limitup.Event{}, fmt.Errorf("%w: invalid limit_up_suc_rate for %s", limitup.ErrInvalid, trimmedCode)
	}
	if sealRatePointer != nil && (*sealRatePointer < 0 || *sealRatePointer > 1) {
		return limitup.Event{}, fmt.Errorf("%w: seal rate out of range [0, 1] for %s", limitup.ErrInvalid, trimmedCode)
	}

	changePercentPointer, changePercentError := parseOptionalFloat(item.ChangeRate)
	if changePercentError != nil {
		return limitup.Event{}, fmt.Errorf("%w: invalid change_rate for %s", limitup.ErrInvalid, trimmedCode)
	}

	turnoverRatePointer, turnoverRateError := parseOptionalFloat(item.TurnoverRate)
	if turnoverRateError != nil {
		return limitup.Event{}, fmt.Errorf("%w: invalid turnover_rate for %s", limitup.ErrInvalid, trimmedCode)
	}

	return limitup.Event{
		TradeDate:      tradeDate,
		Code:           trimmedCode,
		Name:           strings.TrimSpace(stockName),
		MarketID:       marketIDValue,
		MarketType:     strings.TrimSpace(marketTypeString),
		Pool:           poolName,
		OpenCount:      openCountPointer,
		FirstLimitAt:   firstLimitTimestamp,
		LastLimitAt:    lastLimitTimestamp,
		LimitType:      strings.TrimSpace(limitTypeString),
		HighDays:       strings.TrimSpace(highDaysString),
		Reason:         strings.TrimSpace(reasonString),
		SourceSealRate: sealRatePointer,
		ChangePercent:  changePercentPointer,
		TurnoverRate:   turnoverRatePointer,
		Raw:            rawStockBytes,
	}, nil
}

func parseStringValue(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var textValue string
	if unmarshalError := json.Unmarshal(raw, &textValue); unmarshalError == nil {
		return textValue, nil
	}
	var numberValue json.Number
	if unmarshalError := json.Unmarshal(raw, &numberValue); unmarshalError == nil {
		return numberValue.String(), nil
	}
	return "", errors.New("cannot parse as string or number")
}

func parseInteger(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, errors.New("empty or null integer")
	}
	var integerVal int
	if unmarshalError := json.Unmarshal(raw, &integerVal); unmarshalError == nil {
		return integerVal, nil
	}
	var textValue string
	if unmarshalError := json.Unmarshal(raw, &textValue); unmarshalError == nil {
		parsedVal, parseError := strconv.Atoi(strings.TrimSpace(textValue))
		if parseError != nil {
			return 0, parseError
		}
		return parsedVal, nil
	}
	return 0, errors.New("cannot parse as integer")
}

func parseOptionalInteger(raw json.RawMessage) (*int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	integerValue, integerError := parseInteger(raw)
	if integerError != nil {
		return nil, integerError
	}
	return &integerValue, nil
}

func parseOptionalFloat(raw json.RawMessage) (*float64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var floatVal float64
	if unmarshalError := json.Unmarshal(raw, &floatVal); unmarshalError == nil {
		return &floatVal, nil
	}
	var textValue string
	if unmarshalError := json.Unmarshal(raw, &textValue); unmarshalError == nil {
		parsedVal, parseError := strconv.ParseFloat(strings.TrimSpace(textValue), 64)
		if parseError != nil {
			return nil, parseError
		}
		return &parsedVal, nil
	}
	return nil, errors.New("cannot parse as float")
}

func parseTimestamp(raw json.RawMessage, tradeDate string) (*time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var seconds int64
	var directInt int64
	if unmarshalError := json.Unmarshal(raw, &directInt); unmarshalError == nil {
		seconds = directInt
	} else {
		var textValue string
		if unmarshalStringError := json.Unmarshal(raw, &textValue); unmarshalStringError == nil {
			parsedInt, parseError := strconv.ParseInt(strings.TrimSpace(textValue), 10, 64)
			if parseError != nil {
				return nil, parseError
			}
			seconds = parsedInt
		} else {
			return nil, errors.New("cannot parse timestamp as integer or string")
		}
	}
	if seconds <= 0 {
		return nil, nil
	}
	timestamp := time.Unix(seconds, 0).In(limitup.Shanghai())
	if timestamp.Format("2006-01-02") != tradeDate {
		return nil, fmt.Errorf("timestamp date %s does not match trade date %s", timestamp.Format("2006-01-02"), tradeDate)
	}
	return &timestamp, nil
}

func (client *Client) sleep(ctx context.Context) error {
	if client.minInterval <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(client.minInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
