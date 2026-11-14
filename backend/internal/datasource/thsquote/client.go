package thsquote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

const (
	DefaultBaseURL   = "https://quota-h.10jqka.com.cn/fuyao/common_hq_aggr/quote/v1/single_kline"
	defaultBodyLimit = 1 << 20
)

// sharesPerLot converts THS volume (shares) into the project-wide volume unit
// (lots). Eastmoney's kline and snapshot endpoints report lots, so storing the
// raw THS figure would make one provider exactly 100x the other and would also
// inflate derived turnover rates by the same factor.
const sharesPerLot = 100

// lotsFromShares rounds instead of truncating: a five-minute bar can contain
// odd-lot sells, so truncation would bias every bar downward by up to 99
// shares and break the sum-of-bars == daily-volume invariant.
func lotsFromShares(shares int64) int64 {
	if shares <= 0 {
		return 0
	}
	return (shares + sharesPerLot/2) / sharesPerLot
}

type Client struct {
	baseURL         string
	snapshotBaseURL string
	httpClient      *http.Client
	clock           func() time.Time
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{baseURL: baseURL, snapshotBaseURL: snapshotURL(baseURL), httpClient: httpClient, clock: time.Now}
}

func (c *Client) WithClock(clock func() time.Time) *Client {
	if clock != nil {
		c.clock = clock
	}
	return c
}

func (c *Client) WithSnapshotBaseURL(baseURL string) *Client {
	if value := strings.TrimSpace(baseURL); value != "" {
		c.snapshotBaseURL = value
	}
	return c
}

type klineRequest struct {
	CodeList   []codeGroup `json:"code_list"`
	TradeClass string      `json:"trade_class"`
	TimePeriod string      `json:"time_period"`
	TradeDate  int         `json:"trade_date"`
	BeginTime  int         `json:"begin_time"`
	EndTime    int64       `json:"end_time"`
	DataFields []string    `json:"data_fields,omitempty"`
	AdjustType string      `json:"adjust_type"`
	GPID       int         `json:"gpid"`
}

type snapshotRequest struct {
	CodeList   []codeGroup `json:"code_list"`
	TradeClass string      `json:"trade_class"`
	DataFields []string    `json:"data_fields"`
	Lang       string      `json:"lang"`
	GPID       int         `json:"gpid"`
}

type codeGroup struct {
	Codes  []string `json:"codes"`
	Market string   `json:"market"`
}

type snapshotResponse struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
	Data       *struct {
		QuoteData  []snapshotQuote `json:"quote_data"`
		FailParams json.RawMessage `json:"fail_params"`
	} `json:"data"`
}

type snapshotQuote struct {
	Market     string              `json:"market"`
	Code       string              `json:"code"`
	Delay      bool                `json:"delay"`
	DataFields []string            `json:"data_fields"`
	Value      [][]json.RawMessage `json:"value"`
}

// FetchStockQuotes fetches a live snapshot for the requested constituents.
// The endpoint accepts small multi-stock batches; the caller keeps its own
// bounded batch size.
func (c *Client) FetchStockQuotes(ctx context.Context, relations []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	if len(relations) == 0 {
		return []graymarket.StockQuote{}, nil
	}
	groups := make(map[string][]string, 2)
	order := make([]string, 0, len(relations))
	for _, relation := range relations {
		if relation.StockCode == "" {
			continue
		}
		market, err := marketForValues(relation.StockMarket, relation.StockCode)
		if err != nil {
			return nil, err
		}
		if _, exists := groups[market]; !exists {
			order = append(order, market)
		}
		groups[market] = append(groups[market], relation.StockCode)
	}
	wanted := make([]codeGroup, 0, len(order))
	for _, market := range order {
		wanted = append(wanted, codeGroup{Market: market, Codes: groups[market]})
	}
	if len(wanted) == 0 {
		return []graymarket.StockQuote{}, nil
	}
	payload := snapshotRequest{
		CodeList: wanted, TradeClass: "intraday",
		DataFields: []string{"6", "7", "8", "9", "10", "13", "19", "55"},
		Lang:       "zh_hans", GPID: 1,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.snapshotBaseURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("THS snapshot request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, fmt.Errorf("THS snapshot returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, defaultBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: THS snapshot body: %v", graymarket.ErrDecode, err)
	}
	if len(body) > defaultBodyLimit {
		return nil, fmt.Errorf("%w: THS snapshot body too large", graymarket.ErrDecode)
	}
	var decoded snapshotResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("%w: THS snapshot JSON: %v", graymarket.ErrDecode, err)
	}
	if decoded.StatusCode != 0 || decoded.StatusMsg != "ok" {
		return nil, fmt.Errorf("THS snapshot business error %d: %s", decoded.StatusCode, decoded.StatusMsg)
	}
	if decoded.Data == nil {
		return nil, fmt.Errorf("%w: THS snapshot has no data", graymarket.ErrNoData)
	}
	quotes := make(map[string]graymarket.StockQuote, len(decoded.Data.QuoteData))
	fetchedAt := c.clock()
	for _, quote := range decoded.Data.QuoteData {
		parsed, err := decodeSnapshotQuote(quote, fetchedAt)
		if err != nil {
			return nil, err
		}
		quotes[fmt.Sprintf("%d.%s", parsed.StockMarket, parsed.StockCode)] = parsed
	}
	result := make([]graymarket.StockQuote, 0, len(relations))
	for _, relation := range relations {
		quote := quotes[fmt.Sprintf("%d.%s", relation.StockMarket, relation.StockCode)]
		if quote.StockCode == "" {
			quote = graymarket.StockQuote{StockCode: relation.StockCode, StockMarket: relation.StockMarket, StockName: relation.StockName}
		}
		if quote.StockName == "" {
			quote.StockName = relation.StockName
		}
		result = append(result, quote)
	}
	return result, nil
}

func snapshotURL(baseURL string) string {
	if strings.HasSuffix(baseURL, "/single_kline") {
		return strings.TrimSuffix(baseURL, "/single_kline") + "/multi_last_snapshot"
	}
	return baseURL
}

func decodeSnapshotQuote(quote snapshotQuote, fetchedAt time.Time) (graymarket.StockQuote, error) {
	if len(quote.Value) != 1 {
		return graymarket.StockQuote{}, fmt.Errorf("%w: THS snapshot returned %d values for %s", graymarket.ErrDecode, len(quote.Value), quote.Code)
	}
	indexes := make(map[string]int, len(quote.DataFields))
	for index, field := range quote.DataFields {
		indexes[field] = index
	}
	for _, field := range []string{"6", "7", "8", "9", "10", "13", "19", "55"} {
		if _, ok := indexes[field]; !ok {
			return graymarket.StockQuote{}, fmt.Errorf("%w: THS snapshot lacks field %s", graymarket.ErrDecode, field)
		}
	}
	row := quote.Value[0]
	previousClose, err := rowFloat(row, indexes["6"])
	if err != nil {
		return graymarket.StockQuote{}, err
	}
	openPrice, err := rowFloat(row, indexes["7"])
	if err != nil {
		return graymarket.StockQuote{}, err
	}
	latestPrice, err := rowFloat(row, indexes["10"])
	if err != nil {
		return graymarket.StockQuote{}, err
	}
	highPrice, err := rowFloat(row, indexes["8"])
	if err != nil {
		return graymarket.StockQuote{}, err
	}
	lowPrice, err := rowFloat(row, indexes["9"])
	if err != nil {
		return graymarket.StockQuote{}, err
	}
	volume, err := rowInt(row, indexes["13"])
	if err != nil {
		return graymarket.StockQuote{}, err
	}
	turnover, err := rowMoney(row, indexes["19"])
	if err != nil {
		return graymarket.StockQuote{}, err
	}
	name, err := rowString(row, indexes["55"])
	if err != nil {
		return graymarket.StockQuote{}, err
	}
	market, err := marketNumber(quote.Market)
	if err != nil {
		return graymarket.StockQuote{}, err
	}
	result := graymarket.StockQuote{
		StockCode: quote.Code, StockMarket: market, StockName: name, LatestPrice: latestPrice,
		OpenPrice: openPrice, HighPrice: highPrice, LowPrice: lowPrice, PreviousClose: previousClose,
		Volume: lotsFromShares(volume), Turnover: turnover, FetchedAt: fetchedAt, Available: latestPrice > 0,
	}
	if previousClose > 0 {
		result.ChangeValue = latestPrice - previousClose
		result.ChangePct = result.ChangeValue / previousClose
		result.Amplitude = (highPrice - lowPrice) / previousClose
	}
	return result, nil
}

type klineResponse struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
	Data       *struct {
		QuoteData  []klineQuote    `json:"quote_data"`
		FailParams json.RawMessage `json:"fail_params"`
	} `json:"data"`
}

type klineQuote struct {
	Market     string              `json:"market"`
	Code       string              `json:"code"`
	DataFields []string            `json:"data_fields"`
	Value      [][]json.RawMessage `json:"value"`
}

// FetchStockKline5m retrieves the requested archive date from THS. It always
// asks for exactly one stock: the endpoint rejects multi-stock batches.
func (c *Client) FetchStockKline5m(ctx context.Context, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	if stock.Code == "" || stock.TradeDate == "" {
		return nil, fmt.Errorf("%w: stock code and trade date are required", graymarket.ErrNoData)
	}
	market, err := marketForRecord(stock)
	if err != nil {
		return nil, err
	}
	endTime, err := archiveEndTime(stock.TradeDate)
	if err != nil {
		return nil, err
	}
	payload := klineRequest{
		CodeList:   []codeGroup{{Codes: []string{stock.Code}, Market: market}},
		TradeClass: "intraday", TimePeriod: "min_5", TradeDate: -1,
		BeginTime: -400, EndTime: endTime, AdjustType: "actual", GPID: 1,
		DataFields: []string{"1", "7", "8", "9", "11", "13", "19"},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("THS five-minute request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, fmt.Errorf("THS five-minute returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, defaultBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: THS five-minute body: %v", graymarket.ErrDecode, err)
	}
	if len(body) > defaultBodyLimit {
		return nil, fmt.Errorf("%w: THS five-minute body too large", graymarket.ErrDecode)
	}
	var decoded klineResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("%w: THS five-minute JSON: %v", graymarket.ErrDecode, err)
	}
	if decoded.StatusCode != 0 || decoded.StatusMsg != "ok" {
		return nil, fmt.Errorf("THS five-minute business error %d: %s", decoded.StatusCode, decoded.StatusMsg)
	}
	if decoded.Data == nil || len(decoded.Data.QuoteData) != 1 {
		return nil, fmt.Errorf("%w: THS five-minute returned %d quote groups",
			graymarket.ErrInstrumentUnavailable, quoteGroupCount(decoded))
	}
	return decodePoints(decoded.Data.QuoteData[0], stock, c.clock())
}

func quoteGroupCount(payload klineResponse) int {
	if payload.Data == nil {
		return 0
	}
	return len(payload.Data.QuoteData)
}

func marketForRecord(stock graymarket.RankRecord) (string, error) {
	return marketForValues(stock.Market, stock.Code)
}

func marketForValues(market int64, code string) (string, error) {
	switch market {
	case 1:
		return "17", nil
	case 0:
		if strings.HasPrefix(code, "92") {
			return "151", nil
		}
		if strings.HasPrefix(code, "8") || strings.HasPrefix(code, "4") {
			return "151", nil
		}
		return "33", nil
	default:
		return "", fmt.Errorf("%w: unsupported Eastmoney market %d", graymarket.ErrNoData, market)
	}
}

func marketNumber(value string) (int64, error) {
	switch value {
	case "17":
		return 1, nil
	case "33", "151":
		return 0, nil
	default:
		return 0, fmt.Errorf("%w: unsupported THS market %s", graymarket.ErrDecode, value)
	}
}

func archiveEndTime(tradeDate string) (int64, error) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	at, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" 15:00", location)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid archive trade date %s", graymarket.ErrNoData, tradeDate)
	}
	return at.UnixMilli(), nil
}

func decodePoints(quote klineQuote, stock graymarket.RankRecord, fetchedAt time.Time) ([]graymarket.StockKlinePoint, error) {
	if quote.Code != stock.Code {
		return nil, fmt.Errorf("%w: THS returned code %s instead of %s", graymarket.ErrDecode, quote.Code, stock.Code)
	}
	indexes := make(map[string]int, len(quote.DataFields))
	for index, field := range quote.DataFields {
		indexes[field] = index
	}
	for _, field := range []string{"1", "7", "8", "9", "11", "13", "19"} {
		if _, ok := indexes[field]; !ok {
			return nil, fmt.Errorf("%w: THS response lacks field %s", graymarket.ErrDecode, field)
		}
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	points := make([]graymarket.StockKlinePoint, 0, 48)
	seen := make(map[int]struct{}, 48)
	for _, row := range quote.Value {
		timestamp, err := rowInt(row, indexes["1"])
		if err != nil {
			return nil, err
		}
		at := time.UnixMilli(timestamp).In(location)
		date := at.Format("2006-01-02")
		if date < stock.TradeDate {
			continue
		}
		if date > stock.TradeDate {
			return nil, fmt.Errorf("%w: THS five-minute includes later date %s", graymarket.ErrDecode, date)
		}
		index, ok := researchMinuteIndex(at)
		if !ok {
			return nil, fmt.Errorf("%w: unexpected THS five-minute time %s", graymarket.ErrDecode, at.Format("15:04"))
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, fmt.Errorf("%w: duplicate THS five-minute time %s", graymarket.ErrDecode, at)
		}
		open, err := rowFloat(row, indexes["7"])
		if err != nil {
			return nil, err
		}
		high, err := rowFloat(row, indexes["8"])
		if err != nil {
			return nil, err
		}
		low, err := rowFloat(row, indexes["9"])
		if err != nil {
			return nil, err
		}
		closePrice, err := rowFloat(row, indexes["11"])
		if err != nil {
			return nil, err
		}
		volume, err := rowInt(row, indexes["13"])
		if err != nil || volume < 0 {
			return nil, fmt.Errorf("%w: invalid THS volume at %s", graymarket.ErrDecode, at)
		}
		turnover, err := rowMoney(row, indexes["19"])
		if err != nil || turnover < 0 {
			return nil, fmt.Errorf("%w: invalid THS turnover at %s", graymarket.ErrDecode, at)
		}
		point := graymarket.StockKlinePoint{
			TradeDate: stock.TradeDate, SnapshotAt: at, Market: stock.Market, Code: stock.Code,
			Source: graymarket.KlineSourceTHSFiveMinute, OpenPrice: open, HighPrice: high, LowPrice: low, ClosePrice: closePrice,
			Volume: lotsFromShares(volume), Turnover: turnover, FetchedAt: fetchedAt,
		}
		points = append(points, point)
		seen[index] = struct{}{}
	}
	if len(points) != 48 || len(seen) != 48 {
		return nil, fmt.Errorf("%w: THS five-minute returned %d unique archive bars, want 48",
			graymarket.ErrInstrumentUnavailable, len(seen))
	}
	sort.Slice(points, func(left, right int) bool {
		return points[left].SnapshotAt.Before(points[right].SnapshotAt)
	})
	first := &points[0]
	first.OpenPrice = stock.OpenPrice
	first.HighPrice = math.Max(first.HighPrice, stock.OpenPrice)
	first.LowPrice = math.Min(first.LowPrice, stock.OpenPrice)
	last := &points[47]
	last.ClosePrice = stock.ClosePrice
	last.HighPrice = math.Max(last.HighPrice, stock.ClosePrice)
	last.LowPrice = math.Min(last.LowPrice, stock.ClosePrice)
	previousClose := stock.PreviousClose
	for index := range points {
		if index > 0 {
			previousClose = points[index-1].ClosePrice
		}
		if previousClose <= 0 {
			return nil, fmt.Errorf("%w: missing previous close", graymarket.ErrDecode)
		}
		points[index].Amplitude = (points[index].HighPrice - points[index].LowPrice) / previousClose
		points[index].ChangeValue = points[index].ClosePrice - previousClose
		points[index].ChangePct = points[index].ChangeValue / previousClose
		if stock.Volume > 0 {
			points[index].TurnoverRate = stock.TurnoverRate * float64(points[index].Volume) / float64(stock.Volume)
		}
	}
	if !samePrice(first.OpenPrice, stock.OpenPrice) || !samePrice(maxPrice(points), stock.HighPrice) ||
		!samePrice(minPrice(points), stock.LowPrice) || !samePrice(last.ClosePrice, stock.ClosePrice) {
		return nil, fmt.Errorf("%w: THS five-minute OHLC conflicts with the daily bar", graymarket.ErrDecode)
	}
	return points, nil
}

func researchMinuteIndex(at time.Time) (int, bool) {
	minutes := at.Hour()*60 + at.Minute()
	if minutes >= 9*60+35 && minutes <= 11*60+30 && (minutes-(9*60+35))%5 == 0 {
		return (minutes - (9*60 + 35)) / 5, true
	}
	if minutes >= 13*60+5 && minutes <= 15*60 && (minutes-(13*60+5))%5 == 0 {
		return 24 + (minutes-(13*60+5))/5, true
	}
	return 0, false
}

func rowInt(row []json.RawMessage, index int) (int64, error) {
	if index < 0 || index >= len(row) {
		return 0, fmt.Errorf("%w: missing THS integer field", graymarket.ErrDecode)
	}
	var value json.Number
	if err := json.Unmarshal(row[index], &value); err != nil {
		return 0, fmt.Errorf("%w: invalid THS integer field: %v", graymarket.ErrDecode, err)
	}
	if result, err := value.Int64(); err == nil {
		return result, nil
	}
	floatValue, err := value.Float64()
	if err != nil {
		return 0, fmt.Errorf("%w: invalid THS integer value", graymarket.ErrDecode)
	}
	if floatValue != math.Trunc(floatValue) {
		return 0, fmt.Errorf("%w: THS integer field is fractional", graymarket.ErrDecode)
	}
	if math.Abs(floatValue) > float64(math.MaxInt64) {
		return 0, fmt.Errorf("%w: THS integer field overflows", graymarket.ErrDecode)
	}
	return int64(floatValue), nil
}

func rowFloat(row []json.RawMessage, index int) (float64, error) {
	if index < 0 || index >= len(row) {
		return 0, fmt.Errorf("%w: missing THS price field", graymarket.ErrDecode)
	}
	var value float64
	if err := json.Unmarshal(row[index], &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%w: invalid THS price field", graymarket.ErrDecode)
	}
	return value, nil
}

func rowMoney(row []json.RawMessage, index int) (int64, error) {
	if index < 0 || index >= len(row) {
		return 0, fmt.Errorf("%w: missing THS money field", graymarket.ErrDecode)
	}
	var value float64
	if err := json.Unmarshal(row[index], &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%w: invalid THS money field", graymarket.ErrDecode)
	}
	if value < 0 || value >= float64(math.MaxInt64)/100 {
		return 0, fmt.Errorf("%w: THS money field out of range", graymarket.ErrDecode)
	}
	return int64(math.Round(value)), nil
}

func rowString(row []json.RawMessage, index int) (string, error) {
	if index < 0 || index >= len(row) {
		return "", fmt.Errorf("%w: missing THS text field", graymarket.ErrDecode)
	}
	var value string
	if err := json.Unmarshal(row[index], &value); err != nil {
		return "", fmt.Errorf("%w: invalid THS text field", graymarket.ErrDecode)
	}
	return value, nil
}

func samePrice(left, right float64) bool { return math.Abs(left-right) <= 0.0001 }

func maxPrice(points []graymarket.StockKlinePoint) float64 {
	result := points[0].HighPrice
	for _, point := range points[1:] {
		result = math.Max(result, point.HighPrice)
	}
	return result
}

func minPrice(points []graymarket.StockKlinePoint) float64 {
	result := points[0].LowPrice
	for _, point := range points[1:] {
		result = math.Min(result, point.LowPrice)
	}
	return result
}
