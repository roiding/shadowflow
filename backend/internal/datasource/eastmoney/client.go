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
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type Client struct {
	baseURL              string
	darkTradeTickBaseURL string
	stockKlineBaseURL    string
	stockTrendBaseURLs   []string
	quoteBaseURLs        []string
	httpClient           *http.Client
	pageSize             int
	stockKlineRetryGap   time.Duration
	stockKlineFailures   atomic.Int32
	stockKlineDisabled   atomic.Bool
}

func NewClient(baseURL string, httpClient *http.Client, pageSize int) *Client {
	return &Client{
		baseURL:              baseURL,
		darkTradeTickBaseURL: "https://quotederivates.eastmoney.com/datacenter/darktradetick",
		stockKlineBaseURL:    "https://push2his.eastmoney.com/api/qt/stock/kline/get",
		stockTrendBaseURLs: []string{
			"https://push2delay.eastmoney.com/api/qt/stock/trends2/get",
			"https://push2his.eastmoney.com/api/qt/stock/trends2/get",
			"https://push2.eastmoney.com/api/qt/stock/trends2/get",
		},
		quoteBaseURLs:      []string{"https://push2.eastmoney.com", "https://push2delay.eastmoney.com"},
		httpClient:         httpClient,
		pageSize:           pageSize,
		stockKlineRetryGap: time.Second,
	}
}

func (c *Client) WithStockKlineBaseURL(baseURL string) *Client {
	if value := strings.TrimSpace(baseURL); value != "" {
		c.stockKlineBaseURL = value
	}
	return c
}

func (c *Client) WithStockTrendBaseURLs(baseURLs []string) *Client {
	cleaned := make([]string, 0, len(baseURLs))
	for _, baseURL := range baseURLs {
		if value := strings.TrimSpace(baseURL); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) > 0 {
		c.stockTrendBaseURLs = cleaned
	}
	return c
}

func (c *Client) WithDarkTradeTickBaseURL(baseURL string) *Client {
	if value := strings.TrimSpace(baseURL); value != "" {
		c.darkTradeTickBaseURL = value
	}
	return c
}

func (c *Client) WithQuoteBaseURLs(baseURLs []string) *Client {
	cleaned := make([]string, 0, len(baseURLs))
	for _, baseURL := range baseURLs {
		if value := strings.TrimRight(strings.TrimSpace(baseURL), "/"); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) > 0 {
		c.quoteBaseURLs = cleaned
	}
	return c
}

type apiResponse struct {
	ErrorID   int                          `json:"errid"`
	ErrorText string                       `json:"errmsg"`
	Date      json.Number                  `json:"1"`
	Total     json.Number                  `json:"2"`
	Data      []map[string]json.RawMessage `json:"data"`
}

func (c *Client) FetchAll(ctx context.Context, rankType graymarket.RankType, date string, snapshotAt time.Time) (graymarket.RankSnapshot, error) {
	result := graymarket.RankSnapshot{
		RequestedDate: date,
		RankType:      rankType,
		SnapshotAt:    snapshotAt,
	}

	seen := make(map[string]struct{})
	for page := 1; ; page++ {
		payload, rawPage, err := c.fetchPage(ctx, rankType, date, page)
		if err != nil {
			return graymarket.RankSnapshot{}, err
		}
		if page == 1 {
			result.TradeDate = formatAPIDate(payload.Date)
			result.ExpectedTotal, _ = strconv.Atoi(payload.Total.String())
		}
		result.RawPages = append(result.RawPages, rawPage)

		for _, row := range payload.Data {
			rank := int64(len(result.Records) + 1)
			record, err := mapRecord(rankType, result.TradeDate, snapshotAt, rawPage.FetchedAt, rank, row)
			if err != nil {
				return graymarket.RankSnapshot{}, fmt.Errorf("map %s page %d: %w", rankType, page, err)
			}
			if _, duplicate := seen[record.Code]; duplicate {
				return graymarket.RankSnapshot{}, fmt.Errorf("duplicate %s code %s across pages", rankType, record.Code)
			}
			seen[record.Code] = struct{}{}
			result.Records = append(result.Records, record)
		}

		if result.FetchedAt.Before(rawPage.FetchedAt) {
			result.FetchedAt = rawPage.FetchedAt
		}
		if len(payload.Data) == 0 || len(payload.Data) < c.pageSize || (result.ExpectedTotal > 0 && len(result.Records) >= result.ExpectedTotal) {
			break
		}
	}

	if result.TradeDate == "" || len(result.Records) == 0 {
		return graymarket.RankSnapshot{}, graymarket.ErrNoData
	}
	if result.ExpectedTotal > 0 && len(result.Records) != result.ExpectedTotal {
		return graymarket.RankSnapshot{}, fmt.Errorf("incomplete %s snapshot: expected %d records, got %d", rankType, result.ExpectedTotal, len(result.Records))
	}
	return result, nil
}

func (c *Client) fetchPage(ctx context.Context, rankType graymarket.RankType, date string, page int) (apiResponse, graymarket.RawPage, error) {
	params := url.Values{
		"version":    {"101"},
		"cver":       {"100"},
		"date":       {date},
		"StartPage":  {strconv.Itoa(page)},
		"NumPerPage": {strconv.Itoa(c.pageSize)},
		"sortflag":   {"6"},
		"desc":       {"1"},
	}
	if rankType == graymarket.RankIndustry || rankType == graymarket.RankConcept {
		params.Set("market", "90")
		if rankType == graymarket.RankIndustry {
			params.Set("datetype", "2")
		} else {
			params.Set("datetype", "3")
		}
	} else if rankType != graymarket.RankStock {
		return apiResponse{}, graymarket.RawPage{}, fmt.Errorf("unsupported rank type %q", rankType)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return apiResponse{}, graymarket.RawPage{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "shadowflow/0.1")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return apiResponse{}, graymarket.RawPage{}, fmt.Errorf("request page %d: %w", page, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return apiResponse{}, graymarket.RawPage{}, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return apiResponse{}, graymarket.RawPage{}, fmt.Errorf("read page %d: %w", page, err)
	}
	fetchedAt := time.Now().UTC()
	decoded, encodingName, err := decodeJSON(body, response.Header.Get("Content-Type"))
	if err != nil {
		return apiResponse{}, graymarket.RawPage{}, fmt.Errorf("%w: page %d: %v", graymarket.ErrDecode, page, err)
	}

	var payload apiResponse
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return apiResponse{}, graymarket.RawPage{}, fmt.Errorf("%w: parse page %d: %v", graymarket.ErrDecode, page, err)
	}
	if payload.ErrorID != 0 {
		if payload.ErrorID == -2 || strings.EqualFold(payload.ErrorText, "no data") {
			return apiResponse{}, graymarket.RawPage{}, graymarket.ErrNoData
		}
		return apiResponse{}, graymarket.RawPage{}, fmt.Errorf("upstream error %d: %s", payload.ErrorID, payload.ErrorText)
	}

	return payload, graymarket.RawPage{Page: page, ContentEncoding: encodingName, Body: decoded, FetchedAt: fetchedAt}, nil
}

func decodeJSON(body []byte, contentType string) ([]byte, string, error) {
	if json.Valid(body) && !strings.Contains(strings.ToLower(contentType), "gbk") {
		return body, "utf-8", nil
	}
	if json.Valid(body) && utf8Text(body) {
		return body, "utf-8", nil
	}
	decoded, err := io.ReadAll(transform.NewReader(bytes.NewReader(body), simplifiedchinese.GBK.NewDecoder()))
	if err != nil {
		return nil, "", err
	}
	if !json.Valid(decoded) {
		return nil, "", errors.New("response is not valid JSON after UTF-8/GB18030 decoding")
	}
	return decoded, "gb18030", nil
}

func utf8Text(body []byte) bool {
	return utf8.Valid(body)
}

func formatAPIDate(value json.Number) string {
	raw := value.String()
	if len(raw) != 8 {
		return ""
	}
	return raw[:4] + "-" + raw[4:6] + "-" + raw[6:]
}
