package eastmoney

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type quoteResponse struct {
	RC      int    `json:"rc"`
	Message string `json:"message"`
	Data    *struct {
		Total int             `json:"total"`
		Diff  json.RawMessage `json:"diff"`
	} `json:"data"`
}

const stockQuoteBatchSize = 100

// FetchBoardQuotes reads the authoritative quote list for industry or
// concept boards. It deliberately uses clist/get rather than ulist.np/get:
// BK board codes are not stock codes.
func (c *Client) FetchBoardQuotes(ctx context.Context, rankType graymarket.RankType) ([]graymarket.BoardQuote, error) {
	typeCode := ""
	switch rankType {
	case graymarket.RankIndustry:
		typeCode = "2"
	case graymarket.RankConcept:
		typeCode = "3"
	default:
		return nil, fmt.Errorf("unsupported board rank type %q", rankType)
	}
	result := make([]graymarket.BoardQuote, 0, 512)
	expectedTotal := 0
	seen := make(map[string]struct{})
	for page := 1; ; page++ {
		params := url.Values{
			"pn": {strconv.Itoa(page)}, "pz": {strconv.Itoa(c.pageSize)}, "po": {"1"}, "np": {"1"},
			"fltt": {"2"}, "invt": {"2"}, "fid": {"f3"},
			"fields": {"f2,f3,f4,f5,f6,f7,f8,f12,f13,f14,f15,f16,f17,f18,f124"},
			"fs":     {"m:90+t:" + typeCode + "+f:!50"},
		}
		payload, rows, err := c.fetchQuotePage(ctx, "/api/qt/clist/get", params)
		if err != nil {
			return nil, fmt.Errorf("fetch %s board quotes page %d: %w", rankType, page, err)
		}
		if page == 1 {
			expectedTotal = payload.Data.Total
		}
		fetchedAt := time.Now().UTC()
		for _, row := range rows {
			code := optionalString(row, "f12")
			if code == "" {
				return nil, fmt.Errorf("%s board quote contains an empty code", rankType)
			}
			if _, duplicate := seen[code]; duplicate {
				return nil, fmt.Errorf("duplicate %s board quote code %s", rankType, code)
			}
			seen[code] = struct{}{}
			latestPrice, available := optionalFloat(row, "f2")
			result = append(result, graymarket.BoardQuote{
				BoardCode: code, BoardMarket: intValue(row, "f13"), BoardName: optionalString(row, "f14"),
				LatestPrice: latestPrice, OpenPrice: floatValue(row, "f17"), HighPrice: floatValue(row, "f15"),
				LowPrice: floatValue(row, "f16"), PreviousClose: floatValue(row, "f18"),
				ChangePct: floatValue(row, "f3") / 100, ChangeValue: floatValue(row, "f4"),
				Volume: intValue(row, "f5"), Turnover: intValue(row, "f6"), TurnoverRate: floatValue(row, "f8") / 100,
				Amplitude: floatValue(row, "f7") / 100, QuoteTime: formatQuoteUpdateTime(optionalString(row, "f124")),
				FetchedAt: fetchedAt, Available: available,
			})
		}
		// Eastmoney returns rc=102 for pages beyond the declared total. Stop as
		// soon as the total is reached, or an exactly page-aligned board count
		// would request one page too many and fail the whole fetch.
		if expectedTotal > 0 && len(result) >= expectedTotal {
			break
		}
		if len(rows) < c.pageSize {
			break
		}
	}
	if len(result) == 0 {
		return nil, graymarket.ErrNoData
	}
	if expectedTotal <= 0 {
		return nil, fmt.Errorf("%s board quotes returned an invalid total", rankType)
	}
	if len(result) != expectedTotal {
		return nil, fmt.Errorf("incomplete %s board quotes: expected %d rows, got %d", rankType, expectedTotal, len(result))
	}
	return result, nil
}

// FetchStockQuotes reads the latest quote snapshot for the supplied
// constituents. The returned slice follows the input order and includes an
// unavailable row when the quote service did not return a stock.
func (c *Client) FetchStockQuotes(ctx context.Context, relations []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	quotes := make(map[string]graymarket.StockQuote, len(relations))
	for start := 0; start < len(relations); start += stockQuoteBatchSize {
		end := start + stockQuoteBatchSize
		if end > len(relations) {
			end = len(relations)
		}
		batch := relations[start:end]
		secids := make([]string, 0, len(batch))
		seen := make(map[string]struct{}, len(batch))
		for _, relation := range batch {
			if relation.StockCode == "" {
				continue
			}
			key := relation.StockCode
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			secids = append(secids, fmt.Sprintf("%d.%s", relation.StockMarket, relation.StockCode))
		}
		if len(secids) == 0 {
			continue
		}
		params := url.Values{
			"fltt":   {"2"},
			"invt":   {"2"},
			"fields": {"f2,f3,f4,f5,f6,f7,f8,f12,f13,f14,f15,f16,f17,f18,f124"},
			"secids": {strings.Join(secids, ",")},
		}
		_, rows, err := c.fetchQuotePage(ctx, "/api/qt/ulist.np/get", params)
		if err != nil {
			return nil, fmt.Errorf("fetch constituent quotes: %w", err)
		}
		fetchedAt := time.Now().UTC()
		for _, row := range rows {
			code := optionalString(row, "f12")
			if code == "" {
				continue
			}
			latestPrice, available := optionalFloat(row, "f2")
			quotes[code] = graymarket.StockQuote{
				StockCode:     code,
				StockMarket:   intValue(row, "f13"),
				StockName:     optionalString(row, "f14"),
				LatestPrice:   latestPrice,
				OpenPrice:     floatValue(row, "f17"),
				HighPrice:     floatValue(row, "f15"),
				LowPrice:      floatValue(row, "f16"),
				PreviousClose: floatValue(row, "f18"),
				ChangePct:     floatValue(row, "f3") / 100,
				ChangeValue:   floatValue(row, "f4"),
				Volume:        intValue(row, "f5"),
				Turnover:      intValue(row, "f6"),
				TurnoverRate:  floatValue(row, "f8") / 100,
				Amplitude:     floatValue(row, "f7") / 100,
				QuoteTime:     formatQuoteUpdateTime(optionalString(row, "f124")),
				FetchedAt:     fetchedAt,
				Available:     available,
			}
		}
	}

	result := make([]graymarket.StockQuote, 0, len(relations))
	for _, relation := range relations {
		quote, ok := quotes[relation.StockCode]
		if !ok {
			quote = graymarket.StockQuote{StockCode: relation.StockCode, StockMarket: relation.StockMarket, StockName: relation.StockName}
		}
		if quote.StockMarket == 0 {
			quote.StockMarket = relation.StockMarket
		}
		if quote.StockName == "" {
			quote.StockName = relation.StockName
		}
		result = append(result, quote)
	}
	return result, nil
}

func optionalFloat(row map[string]json.RawMessage, key string) (float64, bool) {
	text, err := stringValue(row, key)
	if err != nil || text == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func formatQuoteUpdateTime(value string) string {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return value
	}
	if seconds >= 1_000_000_000_000 {
		return time.UnixMilli(seconds).UTC().Format(time.RFC3339)
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}

func (c *Client) FetchBoardCatalog(ctx context.Context, boardType graymarket.BoardType) ([]graymarket.Board, error) {
	typeCode := ""
	switch boardType {
	case graymarket.BoardIndustry:
		typeCode = "2"
	case graymarket.BoardConcept:
		typeCode = "3"
	default:
		return nil, fmt.Errorf("unsupported board type %q", boardType)
	}
	seen := make(map[string]struct{})
	result := make([]graymarket.Board, 0, 512)
	expectedTotal := 0
	for page := 1; ; page++ {
		params := url.Values{
			"pn": {strconv.Itoa(page)}, "pz": {strconv.Itoa(c.pageSize)}, "po": {"1"}, "np": {"1"},
			"fltt": {"2"}, "invt": {"2"}, "fid": {"f3"}, "fields": {"f12,f14,f3"},
			"fs": {"m:90+t:" + typeCode + "+f:!50"},
		}
		payload, rows, err := c.fetchQuotePage(ctx, "/api/qt/clist/get", params)
		if err != nil {
			return nil, fmt.Errorf("fetch %s board catalog page %d: %w", boardType, page, err)
		}
		if page == 1 {
			expectedTotal = payload.Data.Total
		}
		for _, row := range rows {
			code := optionalString(row, "f12")
			if code == "" {
				return nil, fmt.Errorf("%s board catalog contains an empty code", boardType)
			}
			if _, duplicate := seen[code]; duplicate {
				return nil, fmt.Errorf("duplicate %s board code %s", boardType, code)
			}
			seen[code] = struct{}{}
			result = append(result, graymarket.Board{Code: code, Name: optionalString(row, "f14"), Type: boardType, SourceRank: len(result) + 1})
		}
		// Eastmoney returns rc=102 for pages beyond the declared total. Stop as
		// soon as the total is reached, or an exactly page-aligned catalog would
		// request one page too many and fail the whole fetch.
		if expectedTotal > 0 && len(result) >= expectedTotal {
			break
		}
		if len(rows) < c.pageSize {
			break
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s board catalog is empty", boardType)
	}
	if expectedTotal <= 0 {
		return nil, fmt.Errorf("%s board catalog returned an invalid total", boardType)
	}
	if len(result) != expectedTotal {
		return nil, fmt.Errorf("incomplete %s board catalog: expected %d records, got %d", boardType, expectedTotal, len(result))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	for index := range result {
		result[index].SourceRank = index + 1
	}
	return result, nil
}

func (c *Client) FetchBoardConstituents(ctx context.Context, board graymarket.Board) ([]graymarket.StockBoardRelation, error) {
	seen := make(map[string]struct{})
	result := make([]graymarket.StockBoardRelation, 0, 256)
	expectedTotal := 0
	rawRows := 0
	for page := 1; ; page++ {
		params := url.Values{
			"pn": {strconv.Itoa(page)}, "pz": {strconv.Itoa(c.pageSize)}, "po": {"1"}, "np": {"1"},
			// Sort by immutable stock code rather than changing intraday
			// performance. A moving f3 sort can make page boundaries overlap
			// while the upstream market is updating.
			"fltt": {"2"}, "invt": {"2"}, "fid": {"f12"}, "fields": {"f12,f13,f14"}, "fs": {"b:" + board.Code},
		}
		payload, rows, err := c.fetchQuotePage(ctx, "/api/qt/clist/get", params)
		if err != nil {
			return nil, fmt.Errorf("fetch constituents for %s %s page %d: %w", board.Type, board.Code, page, err)
		}
		if page == 1 {
			expectedTotal = payload.Data.Total
		}
		fetchedAt := time.Now().UTC()
		for _, row := range rows {
			rawRows++
			code := optionalString(row, "f12")
			if code == "" {
				return nil, fmt.Errorf("board %s contains a constituent with an empty code", board.Code)
			}
			if _, duplicate := seen[code]; duplicate {
				// Eastmoney may repeat a constituent across pages. A relation is
				// keyed by board + stock code, so duplicate source rows carry no
				// additional relation information and are safely ignored.
				continue
			}
			seen[code] = struct{}{}
			raw, _ := json.Marshal(row)
			result = append(result, graymarket.StockBoardRelation{
				StockCode: code, StockMarket: intValue(row, "f13"), StockName: optionalString(row, "f14"),
				BoardCode: board.Code, BoardName: board.Name, BoardType: board.Type, SourceOrder: board.SourceRank,
				RelationSource: graymarket.RelationSourceQuoteClist, RelationScope: graymarket.RelationScopeBoardConstituents,
				DetectedAt: fetchedAt, RawData: string(raw),
			})
		}
		// Eastmoney returns rc=102 when callers request a page beyond the
		// declared total for some boards. Stop as soon as the accumulated
		// source rows reach that total, including an exactly full final page.
		if expectedTotal > 0 && rawRows >= expectedTotal {
			break
		}
		if len(rows) < c.pageSize {
			break
		}
	}
	if expectedTotal <= 0 {
		return nil, fmt.Errorf("constituent list for %s returned an invalid total", board.Code)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("constituent list for %s is empty", board.Code)
	}
	// Completeness is judged on raw source rows, not unique constituents:
	// Eastmoney's declared total counts repeated rows (observed on real
	// boards, see TestFetchBoardConstituentsDeduplicatesRepeatedRows), so a
	// deduplicated count may legitimately fall short of the total.
	if rawRows < expectedTotal {
		return nil, fmt.Errorf("incomplete constituent list for %s: expected at least %d source rows, got %d (%d unique)",
			board.Code, expectedTotal, rawRows, len(result))
	}
	return result, nil
}

func (c *Client) fetchQuotePage(ctx context.Context, path string, params url.Values) (quoteResponse, []map[string]json.RawMessage, error) {
	var fallbackErr error
	for index, baseURL := range c.quoteBaseURLs {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path+"?"+params.Encode(), nil)
		if err != nil {
			return quoteResponse{}, nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "shadowflow/0.2")
		response, err := c.guard.Do(ctx, request)
		if err != nil {
			fallbackErr = errors.Join(fallbackErr, fmt.Errorf("%s: %w", baseURL, err))
			if index+1 < len(c.quoteBaseURLs) {
				continue
			}
			break
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		response.Body.Close()
		if readErr != nil {
			fallbackErr = errors.Join(fallbackErr, fmt.Errorf("%s: read response: %w", baseURL, readErr))
			if index+1 < len(c.quoteBaseURLs) {
				continue
			}
			break
		}
		if response.StatusCode != http.StatusOK {
			err = fmt.Errorf("%s returned HTTP %d", baseURL, response.StatusCode)
			if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
				fallbackErr = errors.Join(fallbackErr, err)
				if index+1 < len(c.quoteBaseURLs) {
					continue
				}
			}
			return quoteResponse{}, nil, err
		}
		if len(bytes.TrimSpace(body)) == 0 {
			fallbackErr = errors.Join(fallbackErr, fmt.Errorf("%s returned an empty response", baseURL))
			if index+1 < len(c.quoteBaseURLs) {
				continue
			}
			break
		}
		// f14 carries Chinese board/stock names; run the payload through the
		// same encoding detection as the push2 path so a GB-encoded response
		// cannot land in the database as mojibake.
		decoded, _, decodeErr := decodeJSON(body, response.Header.Get("Content-Type"))
		if decodeErr != nil {
			fallbackErr = errors.Join(fallbackErr, fmt.Errorf("%s: %w", baseURL, decodeErr))
			if index+1 < len(c.quoteBaseURLs) {
				continue
			}
			break
		}
		var payload quoteResponse
		decoder := json.NewDecoder(bytes.NewReader(decoded))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			fallbackErr = errors.Join(fallbackErr, fmt.Errorf("%s returned invalid JSON: %w", baseURL, err))
			if index+1 < len(c.quoteBaseURLs) {
				continue
			}
			break
		}
		if payload.RC != 0 {
			return quoteResponse{}, nil, fmt.Errorf("quote service error %d: %s", payload.RC, payload.Message)
		}
		if payload.Data == nil {
			fallbackErr = errors.Join(fallbackErr, fmt.Errorf("%s returned no data object", baseURL))
			if index+1 < len(c.quoteBaseURLs) {
				continue
			}
			break
		}
		rows, err := decodeQuoteRows(payload.Data.Diff)
		if err != nil {
			fallbackErr = errors.Join(fallbackErr, fmt.Errorf("%s: %w", baseURL, err))
			if index+1 < len(c.quoteBaseURLs) {
				continue
			}
			break
		}
		return payload, rows, nil
	}
	if fallbackErr == nil {
		fallbackErr = errors.New("no quote service base URL configured")
	}
	return quoteResponse{}, nil, fallbackErr
}

func decodeQuoteRows(raw json.RawMessage) ([]map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("decode data.diff array: %w", err)
		}
		return rows, nil
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("data.diff must be an object or array")
	}
	var object map[string]map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, fmt.Errorf("decode data.diff object: %w", err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, leftErr := strconv.Atoi(keys[i])
		right, rightErr := strconv.Atoi(keys[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return strings.Compare(keys[i], keys[j]) < 0
	})
	rows := make([]map[string]json.RawMessage, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, object[key])
	}
	return rows, nil
}
