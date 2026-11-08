package thsfocus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	focus "github.com/roiding/shadowflow/internal/thsfocus"
)

const (
	DefaultBaseURL     = "https://data.10jqka.com.cn"
	DefaultUserAgent   = "shadowflow-ths-focus/1.0"
	defaultHTTPTimeout = 30 * time.Second
	defaultMinInterval = time.Second
	maxResponseBytes   = focus.MaxRawPageBytes
	blockTopPath       = "/dataapi/limit_up/block_top"
	ladderPath         = "/dataapi/limit_up/continuous_limit_up"
	marketPath         = "/mobileapi/hotspot_focus/market_state/v1/overview"
	comparisonPath     = "/mobileapi/hotspot_focus/limit_up_compare/v1/two_days_compare"
)

const (
	topCoverageLimitation      = "block_top仅提供Top20板块列表，不代表市场全部板块或板块完整成份。"
	marketScopeLimitation      = "market_state与two_days_compare固定采用默认市场source_default口径，不受scope隔离，ST与NEW覆盖不受控，不能视为与三池同口径。"
	sourceDateLimitation       = "block_top与continuous_limit_up未回显日期，market_state亦未回显日期，因此source_date_verified固定为false。"
	timestampDateLimitation    = "block_top可用涨停时间戳校验日期，但ladder与market无逐股日期证据，故不会将source_date_verified提升为true。"
	emptyBlocksLimitation      = "block_top返回了显式空列表，不能据此推断市场没有涨停板块。"
	emptyBlockStocksLimitation = "block_top包含无成份股的板块，不能据此推断该板块没有涨停股。"
	emptyLadderLimitation      = "continuous_limit_up返回了显式空列表，不能据此推断市场没有连板股票。"
	emptyLevelLimitation       = "continuous_limit_up包含无股票的连板高度，源字段未提供额外解释。"
	emptyComparisonLimitation  = "two_days_compare返回了显式空比较数组。"
	expandedScopeLimitation    = "扩大的ST与NEW筛选仅作用于block_top和continuous_limit_up；market与comparison仍为默认市场口径。"
)

type Client struct {
	baseURL     string
	httpClient  *http.Client
	minInterval time.Duration
	clock       func() time.Time
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	targetBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if targetBaseURL == "" {
		targetBaseURL = DefaultBaseURL
	}
	timeout := defaultHTTPTimeout
	transport := http.DefaultTransport
	if httpClient != nil {
		if httpClient.Timeout > 0 {
			timeout = httpClient.Timeout
		}
		if httpClient.Transport != nil {
			transport = httpClient.Transport
		}
	}
	rejectingClient := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Client{
		baseURL:     targetBaseURL,
		httpClient:  rejectingClient,
		minInterval: defaultMinInterval,
		clock:       time.Now,
	}
}

func (client *Client) WithMinInterval(interval time.Duration) *Client {
	if interval < 0 {
		interval = 0
	}
	client.minInterval = interval
	return client
}

func (client *Client) WithClock(clock func() time.Time) *Client {
	if clock != nil {
		client.clock = clock
	}
	return client
}

func (client *Client) FetchDay(ctx context.Context, date string, scope string) (focus.Snapshot, error) {
	zone := focus.Shanghai()
	tradeDate, parseError := time.ParseInLocation("2006-01-02", date, zone)
	if parseError != nil || tradeDate.Format("2006-01-02") != date {
		return focus.Snapshot{}, fmt.Errorf("%w: invalid trade date format %s", focus.ErrInvalid, date)
	}
	if !focus.ValidScope(scope) {
		return focus.Snapshot{}, fmt.Errorf("%w: unsupported market scope %s", focus.ErrInvalid, scope)
	}
	currentTime := client.clock().In(zone)
	today := time.Date(currentTime.Year(), currentTime.Month(), currentTime.Day(), 0, 0, 0, 0, zone)
	if tradeDate.After(today) {
		return focus.Snapshot{}, fmt.Errorf("%w: cannot collect future trade date %s", focus.ErrInvalid, date)
	}
	if tradeDate.Equal(today) && currentTime.Before(tradeDate.Add(15*time.Hour+30*time.Minute)) {
		return focus.Snapshot{}, fmt.Errorf("%w: daily snapshot unavailable before 15:30 Asia/Shanghai", focus.ErrInvalid)
	}

	dateDigits := strings.ReplaceAll(date, "-", "")
	filterQuery := url.Values{
		"date":   []string{dateDigits},
		"filter": []string{scope},
	}

	blockData, blockRaw, blockError := client.fetchJSON(ctx, blockTopPath, filterQuery)
	if blockError != nil {
		return focus.Snapshot{}, blockError
	}
	blocks, blockLimitations, blockParseError := parseBlocks(blockData, date)
	if blockParseError != nil {
		return focus.Snapshot{}, blockParseError
	}

	if intervalError := client.wait(ctx); intervalError != nil {
		return focus.Snapshot{}, intervalError
	}
	ladderData, ladderRaw, ladderError := client.fetchJSON(ctx, ladderPath, filterQuery)
	if ladderError != nil {
		return focus.Snapshot{}, ladderError
	}
	ladder, ladderLimitations, ladderParseError := parseLadder(ladderData)
	if ladderParseError != nil {
		return focus.Snapshot{}, ladderParseError
	}

	if intervalError := client.wait(ctx); intervalError != nil {
		return focus.Snapshot{}, intervalError
	}
	marketQuery := url.Values{"date": []string{dateDigits}}
	marketData, marketRaw, marketError := client.fetchJSON(ctx, marketPath, marketQuery)
	if marketError != nil {
		return focus.Snapshot{}, marketError
	}
	if marketParseError := validateMarketData(marketData); marketParseError != nil {
		return focus.Snapshot{}, marketParseError
	}

	if intervalError := client.wait(ctx); intervalError != nil {
		return focus.Snapshot{}, intervalError
	}
	comparisonQuery := url.Values{
		"date":              []string{dateDigits},
		"query_probability": []string{"0"},
	}
	comparisonData, comparisonRaw, comparisonError := client.fetchJSON(ctx, comparisonPath, comparisonQuery)
	if comparisonError != nil {
		return focus.Snapshot{}, comparisonError
	}
	comparisonLimitations, comparisonParseError := parseComparison(comparisonData, date)
	if comparisonParseError != nil {
		return focus.Snapshot{}, comparisonParseError
	}

	limitations := []string{
		topCoverageLimitation,
		marketScopeLimitation,
		sourceDateLimitation,
		timestampDateLimitation,
	}
	if scope == focus.ScopeExpanded {
		limitations = append(limitations, expandedScopeLimitation)
	}
	limitations = append(limitations, blockLimitations...)
	limitations = append(limitations, ladderLimitations...)
	limitations = append(limitations, comparisonLimitations...)

	snapshot := focus.Snapshot{
		TradeDate:          date,
		Source:             focus.Source,
		Scope:              scope,
		MarketScope:        focus.MarketScope,
		FetchedAt:          client.clock().In(zone),
		Blocks:             blocks,
		Ladder:             ladder,
		Market:             marketData,
		Comparison:         comparisonData,
		SourceDateVerified: false,
		Limitations:        limitations,
		RawPages:           []json.RawMessage{blockRaw, ladderRaw, marketRaw, comparisonRaw},
	}
	if validateError := focus.ValidateSnapshot(snapshot); validateError != nil {
		return focus.Snapshot{}, validateError
	}
	return snapshot, nil
}

func (client *Client) fetchJSON(ctx context.Context, path string, query url.Values) (json.RawMessage, json.RawMessage, error) {
	requestURL := client.baseURL + path + "?" + query.Encode()
	httpRequest, requestError := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if requestError != nil {
		return nil, nil, requestError
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", DefaultUserAgent)

	httpResponse, roundTripError := client.httpClient.Do(httpRequest)
	if roundTripError != nil {
		return nil, nil, roundTripError
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode >= 300 && httpResponse.StatusCode < 400 {
		return nil, nil, fmt.Errorf("redirect rejected from %s with http status %d", path, httpResponse.StatusCode)
	}
	if httpResponse.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("unexpected http status %d from %s", httpResponse.StatusCode, path)
	}
	if httpResponse.ContentLength > maxResponseBytes {
		return nil, nil, fmt.Errorf("response from %s exceeds body limit", path)
	}

	bodyBuffer, readError := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
	if readError != nil {
		return nil, nil, readError
	}
	if len(bodyBuffer) > maxResponseBytes {
		return nil, nil, fmt.Errorf("response from %s exceeds body limit", path)
	}
	if len(bytes.TrimSpace(bodyBuffer)) == 0 {
		return nil, nil, fmt.Errorf("empty response body from %s", path)
	}
	trimmedBody := bytes.TrimSpace(bodyBuffer)
	if trimmedBody[0] == '<' {
		return nil, nil, fmt.Errorf("non-json response from %s", path)
	}

	var envelope struct {
		StatusCode json.RawMessage `json:"status_code"`
		StatusMsg  string          `json:"status_msg"`
		Data       json.RawMessage `json:"data"`
	}
	if unmarshalError := json.Unmarshal(bodyBuffer, &envelope); unmarshalError != nil {
		return nil, nil, fmt.Errorf("invalid json response from %s: %w", path, unmarshalError)
	}
	businessStatus, statusError := parseInteger(envelope.StatusCode)
	if statusError != nil {
		return nil, nil, fmt.Errorf("invalid business status from %s: %w", path, statusError)
	}
	if businessStatus != 0 {
		return nil, nil, fmt.Errorf("business error code %d from %s: %s", businessStatus, path, envelope.StatusMsg)
	}
	trimmedData := bytes.TrimSpace(envelope.Data)
	if len(trimmedData) == 0 || bytes.Equal(trimmedData, []byte("null")) {
		return nil, nil, fmt.Errorf("response from %s is missing data", path)
	}

	return append(json.RawMessage(nil), trimmedData...), append(json.RawMessage(nil), bodyBuffer...), nil
}

func (client *Client) wait(ctx context.Context) error {
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	if client.minInterval <= 0 {
		return nil
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

func parseBlocks(rawData json.RawMessage, tradeDate string) ([]focus.Block, []string, error) {
	rawBlocks, arrayError := decodeRawArray(rawData, "block_top data")
	if arrayError != nil {
		return nil, nil, arrayError
	}
	blocks := make([]focus.Block, 0, len(rawBlocks))
	hasEmptyBlockStocks := false
	for _, rawBlock := range rawBlocks {
		var item struct {
			Code         json.RawMessage `json:"code"`
			Name         json.RawMessage `json:"name"`
			Change       json.RawMessage `json:"change"`
			LimitUpCount json.RawMessage `json:"limit_up_num"`
			StockList    json.RawMessage `json:"stock_list"`
		}
		if unmarshalError := json.Unmarshal(rawBlock, &item); unmarshalError != nil {
			return nil, nil, fmt.Errorf("invalid block item: %w", unmarshalError)
		}
		code, codeError := requiredString(item.Code, "block code")
		if codeError != nil || !focus.ValidCode(code) {
			return nil, nil, fmt.Errorf("invalid block code")
		}
		name, nameError := requiredString(item.Name, "block name")
		if nameError != nil {
			return nil, nil, nameError
		}
		change, changeError := optionalFloat(item.Change)
		if changeError != nil {
			return nil, nil, fmt.Errorf("invalid change for block %s", code)
		}
		limitUpCount, countError := parseInteger(item.LimitUpCount)
		if countError != nil || limitUpCount < 0 {
			return nil, nil, fmt.Errorf("invalid limit_up_num for block %s", code)
		}
		rawStocks, stocksError := decodeRawArray(item.StockList, "stock_list for block "+code)
		if stocksError != nil {
			return nil, nil, stocksError
		}
		if limitUpCount != len(rawStocks) {
			return nil, nil, fmt.Errorf("limit_up_num %d does not match stock_list length %d for block %s", limitUpCount, len(rawStocks), code)
		}
		if len(rawStocks) == 0 {
			hasEmptyBlockStocks = true
		}
		stocks := make([]focus.Stock, 0, len(rawStocks))
		seenStocks := make(map[string]bool, len(rawStocks))
		for _, rawStock := range rawStocks {
			stock, stockError := parseBlockStock(rawStock, tradeDate)
			if stockError != nil {
				return nil, nil, fmt.Errorf("block %s: %w", code, stockError)
			}
			if seenStocks[stock.Code] {
				return nil, nil, fmt.Errorf("duplicate stock %s in block %s", stock.Code, code)
			}
			seenStocks[stock.Code] = true
			stocks = append(stocks, stock)
		}
		blocks = append(blocks, focus.Block{
			Code:         code,
			Name:         name,
			Change:       change,
			LimitUpCount: limitUpCount,
			Stocks:       stocks,
		})
	}
	limitations := []string{}
	if len(blocks) == 0 {
		limitations = append(limitations, emptyBlocksLimitation)
	}
	if hasEmptyBlockStocks {
		limitations = append(limitations, emptyBlockStocksLimitation)
	}
	return blocks, limitations, nil
}

func parseBlockStock(rawStock json.RawMessage, tradeDate string) (focus.Stock, error) {
	var item struct {
		Code           json.RawMessage `json:"code"`
		Name           json.RawMessage `json:"name"`
		MarketID       json.RawMessage `json:"market_id"`
		ContinueNum    json.RawMessage `json:"continue_num"`
		Reason         json.RawMessage `json:"reason_type"`
		ReasonDetail   json.RawMessage `json:"reason_info"`
		High           json.RawMessage `json:"high"`
		ChangePercent  json.RawMessage `json:"change_rate"`
		FirstLimitTime json.RawMessage `json:"first_limit_up_time"`
		LastLimitTime  json.RawMessage `json:"last_limit_up_time"`
	}
	if unmarshalError := json.Unmarshal(rawStock, &item); unmarshalError != nil {
		return focus.Stock{}, fmt.Errorf("invalid stock item: %w", unmarshalError)
	}
	code, codeError := requiredString(item.Code, "stock code")
	if codeError != nil || !focus.ValidCode(code) {
		return focus.Stock{}, fmt.Errorf("invalid stock code")
	}
	name, nameError := requiredString(item.Name, "stock name")
	if nameError != nil {
		return focus.Stock{}, nameError
	}
	marketID, marketError := parseInteger(item.MarketID)
	if marketError != nil || marketID <= 0 {
		return focus.Stock{}, fmt.Errorf("invalid market_id for stock %s", code)
	}
	continueNum, continueError := parseInteger(item.ContinueNum)
	if continueError != nil || continueNum < 0 {
		return focus.Stock{}, fmt.Errorf("invalid continue_num for stock %s", code)
	}
	reason, reasonError := optionalString(item.Reason)
	if reasonError != nil {
		return focus.Stock{}, fmt.Errorf("invalid reason_type for stock %s", code)
	}
	reasonDetail, reasonDetailError := optionalString(item.ReasonDetail)
	if reasonDetailError != nil {
		return focus.Stock{}, fmt.Errorf("invalid reason_info for stock %s", code)
	}
	highLabel, highError := optionalString(item.High)
	if highError != nil {
		return focus.Stock{}, fmt.Errorf("invalid high for stock %s", code)
	}
	changePercent, changeError := optionalFloat(item.ChangePercent)
	if changeError != nil {
		return focus.Stock{}, fmt.Errorf("invalid change_rate for stock %s", code)
	}
	firstLimitTime, firstTimeError := parseOptionalDateTimestamp(item.FirstLimitTime, tradeDate, "first_limit_up_time", code)
	if firstTimeError != nil {
		return focus.Stock{}, firstTimeError
	}
	lastLimitTime, lastTimeError := parseOptionalDateTimestamp(item.LastLimitTime, tradeDate, "last_limit_up_time", code)
	if lastTimeError != nil {
		return focus.Stock{}, lastTimeError
	}
	if firstLimitTime != nil && lastLimitTime != nil && lastLimitTime.Before(*firstLimitTime) {
		return focus.Stock{}, fmt.Errorf("last_limit_up_time precedes first_limit_up_time for stock %s", code)
	}
	return focus.Stock{
		Code:          code,
		Name:          name,
		MarketID:      marketID,
		ContinueNum:   continueNum,
		Reason:        reason,
		ReasonDetail:  reasonDetail,
		HighDays:      strings.TrimSpace(highLabel),
		ChangePercent: changePercent,
	}, nil
}

func parseLadder(rawData json.RawMessage) ([]focus.Level, []string, error) {
	rawLevels, arrayError := decodeRawArray(rawData, "continuous_limit_up data")
	if arrayError != nil {
		return nil, nil, arrayError
	}
	levels := make([]focus.Level, 0, len(rawLevels))
	hasEmptyLevel := false
	for _, rawLevel := range rawLevels {
		var item struct {
			Height   json.RawMessage `json:"height"`
			Number   json.RawMessage `json:"number"`
			CodeList json.RawMessage `json:"code_list"`
		}
		if unmarshalError := json.Unmarshal(rawLevel, &item); unmarshalError != nil {
			return nil, nil, fmt.Errorf("invalid ladder level: %w", unmarshalError)
		}
		height, heightError := parseInteger(item.Height)
		if heightError != nil || height <= 0 {
			return nil, nil, fmt.Errorf("invalid ladder height")
		}
		number, numberError := parseInteger(item.Number)
		if numberError != nil || number < 0 {
			return nil, nil, fmt.Errorf("invalid ladder number at height %d", height)
		}
		rawStocks, stocksError := decodeRawArray(item.CodeList, fmt.Sprintf("code_list at height %d", height))
		if stocksError != nil {
			return nil, nil, stocksError
		}
		if number != len(rawStocks) {
			return nil, nil, fmt.Errorf("ladder number %d does not match code_list length %d at height %d", number, len(rawStocks), height)
		}
		if len(rawStocks) == 0 {
			hasEmptyLevel = true
		}
		stocks := make([]focus.Stock, 0, len(rawStocks))
		seenStocks := make(map[string]bool, len(rawStocks))
		for _, rawStock := range rawStocks {
			stock, stockError := parseLadderStock(rawStock)
			if stockError != nil {
				return nil, nil, fmt.Errorf("ladder height %d: %w", height, stockError)
			}
			if seenStocks[stock.Code] {
				return nil, nil, fmt.Errorf("duplicate stock %s at ladder height %d", stock.Code, height)
			}
			seenStocks[stock.Code] = true
			stocks = append(stocks, stock)
		}
		levels = append(levels, focus.Level{Height: height, Stocks: stocks})
	}
	limitations := []string{}
	if len(levels) == 0 {
		limitations = append(limitations, emptyLadderLimitation)
	}
	if hasEmptyLevel {
		limitations = append(limitations, emptyLevelLimitation)
	}
	return levels, limitations, nil
}

func parseLadderStock(rawStock json.RawMessage) (focus.Stock, error) {
	var item struct {
		Code        json.RawMessage `json:"code"`
		Name        json.RawMessage `json:"name"`
		MarketID    json.RawMessage `json:"market_id"`
		ContinueNum json.RawMessage `json:"continue_num"`
	}
	if unmarshalError := json.Unmarshal(rawStock, &item); unmarshalError != nil {
		return focus.Stock{}, fmt.Errorf("invalid ladder stock: %w", unmarshalError)
	}
	code, codeError := requiredString(item.Code, "ladder stock code")
	if codeError != nil || !focus.ValidCode(code) {
		return focus.Stock{}, fmt.Errorf("invalid ladder stock code")
	}
	name, nameError := requiredString(item.Name, "ladder stock name")
	if nameError != nil {
		return focus.Stock{}, nameError
	}
	marketID, marketError := parseInteger(item.MarketID)
	if marketError != nil || marketID <= 0 {
		return focus.Stock{}, fmt.Errorf("invalid market_id for ladder stock %s", code)
	}
	continueNum, continueError := parseInteger(item.ContinueNum)
	if continueError != nil || continueNum < 0 {
		return focus.Stock{}, fmt.Errorf("invalid continue_num for ladder stock %s", code)
	}
	return focus.Stock{
		Code:        code,
		Name:        name,
		MarketID:    marketID,
		ContinueNum: continueNum,
	}, nil
}

func validateMarketData(rawData json.RawMessage) error {
	rawObject, objectError := decodeRawObject(rawData, "market_state data")
	if objectError != nil {
		return objectError
	}
	if len(rawObject) == 0 {
		return errors.New("market_state returned an empty data object")
	}
	rawRiseFall, exists := rawObject["rise_fall"]
	if !exists {
		return errors.New("market_state is missing rise_fall")
	}
	riseFall, structureError := decodeRawObject(rawRiseFall, "market_state rise_fall")
	if structureError != nil {
		return structureError
	}
	for _, fieldName := range []string{"rise", "fall", "deuce", "limit_up", "limit_down"} {
		rawValue, fieldExists := riseFall[fieldName]
		if !fieldExists {
			return fmt.Errorf("market_state rise_fall is missing %s", fieldName)
		}
		value, valueError := parseInteger(rawValue)
		if valueError != nil || value < 0 {
			return fmt.Errorf("market_state rise_fall.%s must be a non-negative integer", fieldName)
		}
	}
	return nil
}

func parseComparison(rawData json.RawMessage, tradeDate string) ([]string, error) {
	rawObject, objectError := decodeRawObject(rawData, "two_days_compare data")
	if objectError != nil {
		return nil, objectError
	}
	parsedToday, todayParseError := time.ParseInLocation(focus.TradeDateFormat, tradeDate, focus.Shanghai())
	if todayParseError != nil || parsedToday.Format(focus.TradeDateFormat) != tradeDate {
		return nil, fmt.Errorf("invalid requested trade date %s", tradeDate)
	}
	todayDate, todayError := requiredString(rawObject["today_date"], "today_date")
	if todayError != nil {
		return nil, todayError
	}
	if todayDate != tradeDate {
		return nil, fmt.Errorf("two_days_compare today_date mismatch: requested %s, got %s", tradeDate, todayDate)
	}
	yesterdayDate, yesterdayError := requiredString(rawObject["yesterday_date"], "yesterday_date")
	if yesterdayError != nil {
		return nil, yesterdayError
	}
	parsedYesterday, yesterdayParseError := time.ParseInLocation(focus.TradeDateFormat, yesterdayDate, focus.Shanghai())
	if yesterdayParseError != nil || parsedYesterday.Format(focus.TradeDateFormat) != yesterdayDate {
		return nil, fmt.Errorf("two_days_compare yesterday_date must use YYYY-MM-DD")
	}
	if !parsedYesterday.Before(parsedToday) {
		return nil, fmt.Errorf("two_days_compare yesterday_date must precede today_date")
	}
	boardList, boardError := decodeRawArray(rawObject["board_list"], "two_days_compare board_list")
	if boardError != nil {
		return nil, boardError
	}
	oneBoard, oneBoardError := decodeRawArray(rawObject["one_board"], "two_days_compare one_board")
	if oneBoardError != nil {
		return nil, oneBoardError
	}
	limitations := []string{}
	if len(boardList) == 0 && len(oneBoard) == 0 {
		limitations = append(limitations, emptyComparisonLimitation)
	}
	return limitations, nil
}

func decodeRawArray(raw json.RawMessage, fieldName string) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("%s requires an explicit array", fieldName)
	}
	if trimmed[0] != '[' {
		return nil, fmt.Errorf("%s must be an array", fieldName)
	}
	var values []json.RawMessage
	if unmarshalError := json.Unmarshal(trimmed, &values); unmarshalError != nil {
		return nil, fmt.Errorf("invalid %s: %w", fieldName, unmarshalError)
	}
	if values == nil {
		values = []json.RawMessage{}
	}
	return values, nil
}

func decodeRawObject(raw json.RawMessage, fieldName string) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("%s requires an explicit object", fieldName)
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("%s must be an object", fieldName)
	}
	var values map[string]json.RawMessage
	if unmarshalError := json.Unmarshal(trimmed, &values); unmarshalError != nil {
		return nil, fmt.Errorf("invalid %s: %w", fieldName, unmarshalError)
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	return values, nil
}

func requiredString(raw json.RawMessage, fieldName string) (string, error) {
	text, textError := optionalString(raw)
	if textError != nil {
		return "", fmt.Errorf("invalid %s", fieldName)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("missing %s", fieldName)
	}
	return text, nil
}

func optionalString(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var text string
	if unmarshalError := json.Unmarshal(trimmed, &text); unmarshalError != nil {
		return "", unmarshalError
	}
	return text, nil
}

func parseInteger(raw json.RawMessage) (int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, errors.New("empty or null integer")
	}
	var integerValue int
	if unmarshalError := json.Unmarshal(trimmed, &integerValue); unmarshalError == nil {
		return integerValue, nil
	}
	var textValue string
	if unmarshalError := json.Unmarshal(trimmed, &textValue); unmarshalError == nil {
		parsedValue, parseError := strconv.Atoi(strings.TrimSpace(textValue))
		if parseError != nil {
			return 0, parseError
		}
		return parsedValue, nil
	}
	return 0, errors.New("not an integer")
}

func optionalFloat(raw json.RawMessage) (*float64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var numberValue float64
	if unmarshalError := json.Unmarshal(trimmed, &numberValue); unmarshalError == nil {
		if math.IsNaN(numberValue) || math.IsInf(numberValue, 0) {
			return nil, errors.New("non-finite float")
		}
		return &numberValue, nil
	}
	var textValue string
	if unmarshalError := json.Unmarshal(trimmed, &textValue); unmarshalError == nil {
		parsedValue, parseError := strconv.ParseFloat(strings.TrimSpace(textValue), 64)
		if parseError != nil {
			return nil, parseError
		}
		if math.IsNaN(parsedValue) || math.IsInf(parsedValue, 0) {
			return nil, errors.New("non-finite float")
		}
		return &parsedValue, nil
	}
	return nil, errors.New("not a float")
}

func parseOptionalDateTimestamp(raw json.RawMessage, tradeDate string, fieldName string, stockCode string) (*time.Time, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var textValue string
	if unmarshalError := json.Unmarshal(trimmed, &textValue); unmarshalError == nil {
		textValue = strings.TrimSpace(textValue)
		if textValue == "" {
			return nil, nil
		}
	} else {
		var numberValue json.Number
		if unmarshalNumberError := json.Unmarshal(trimmed, &numberValue); unmarshalNumberError != nil {
			return nil, fmt.Errorf("invalid %s for stock %s", fieldName, stockCode)
		}
		textValue = numberValue.String()
	}
	if len(textValue) > focus.MaxTimestampBytes {
		return nil, fmt.Errorf("%s exceeds the size limit for stock %s", fieldName, stockCode)
	}
	seconds, parseError := strconv.ParseInt(textValue, 10, 64)
	if parseError != nil || seconds < 0 {
		return nil, fmt.Errorf("invalid %s for stock %s", fieldName, stockCode)
	}
	if seconds == 0 {
		return nil, nil
	}
	timestamp := time.Unix(seconds, 0).In(focus.Shanghai())
	if timestamp.Format(focus.TradeDateFormat) != tradeDate {
		return nil, fmt.Errorf("%s date %s does not match trade date %s for stock %s", fieldName, timestamp.Format(focus.TradeDateFormat), tradeDate, stockCode)
	}
	return &timestamp, nil
}
