package eastmoney

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type stockKlineResponse struct {
	ReturnCode int `json:"rc"`
	Data       *struct {
		Klines []string `json:"klines"`
	} `json:"data"`
}

type stockTrendResponse struct {
	ReturnCode int `json:"rc"`
	Data       *struct {
		Trends []string `json:"trends"`
	} `json:"data"`
}

type stockKlineResult struct {
	points []graymarket.StockKlinePoint
	err    error
}

func (c *Client) FetchStockKlines5m(ctx context.Context, snapshot graymarket.RankSnapshot) ([]graymarket.StockKlinePoint, error) {
	if snapshot.RankType != graymarket.RankStock || snapshot.TradeDate == "" || len(snapshot.Records) == 0 {
		return nil, fmt.Errorf("invalid stock kline snapshot")
	}
	c.stockKlineFailures.Store(0)
	c.stockKlineDisabled.Store(false)
	parentCtx := ctx
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	workerCount := min(2, len(snapshot.Records))
	jobs := make(chan graymarket.RankRecord, workerCount)
	results := make(chan stockKlineResult, workerCount)
	limiter := time.NewTicker(125 * time.Millisecond)
	defer limiter.Stop()
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for stock := range jobs {
				select {
				case <-ctx.Done():
					return
				case <-limiter.C:
				}
				points, err := c.fetchStockKlineWithRetry(ctx, snapshot.TradeDate, stock)
				select {
				case results <- stockKlineResult{points: points, err: err}:
				case <-parentCtx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, stock := range snapshot.Records {
			select {
			case jobs <- stock:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	points := make([]graymarket.StockKlinePoint, 0, len(snapshot.Records)*48)
	var firstErr error
	failedStocks := 0
	consecutiveFailures := 0
	const maxConsecutiveFailures = 8
	for result := range results {
		if result.err != nil {
			failedStocks++
			consecutiveFailures++
			if firstErr == nil {
				firstErr = result.err
			}
			if consecutiveFailures >= maxConsecutiveFailures {
				cancel()
			}
		} else {
			consecutiveFailures = 0
			points = append(points, result.points...)
		}
	}
	if parentCtx.Err() != nil {
		return points, parentCtx.Err()
	}
	if firstErr != nil {
		return points, fmt.Errorf("stock kline batch incomplete: completed %d/%d stocks, failed %d; first error: %w",
			len(points)/48, len(snapshot.Records), failedStocks, firstErr)
	}
	if len(points) != len(snapshot.Records)*48 {
		return nil, fmt.Errorf("incomplete stock kline archive: expected %d points, got %d", len(snapshot.Records)*48, len(points))
	}
	return points, nil
}

func (c *Client) fetchStockKlineWithRetry(ctx context.Context, tradeDate string, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		points, err := c.fetchStockKlineWithFallback(ctx, tradeDate, stock)
		if err == nil {
			return points, nil
		}
		lastErr = err
		if attempt < 4 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * c.stockKlineRetryGap):
			}
		}
	}
	return nil, fmt.Errorf("fetch %s kline: %w", stock.Code, lastErr)
}

func (c *Client) fetchStockKlineWithFallback(ctx context.Context, tradeDate string, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	var primaryErr error
	if !c.stockKlineDisabled.Load() {
		points, err := c.fetchStockKline(ctx, tradeDate, stock)
		if err == nil {
			c.stockKlineFailures.Store(0)
			return points, nil
		}
		primaryErr = fmt.Errorf("five-minute endpoint: %w", err)
		if c.stockKlineFailures.Add(1) >= 8 {
			c.stockKlineDisabled.Store(true)
		}
	}
	points, trendErr := c.fetchStockKlineFromTrends(ctx, tradeDate, stock)
	if trendErr == nil {
		return points, nil
	}
	trendErr = fmt.Errorf("one-minute fallback: %w", trendErr)
	if primaryErr == nil {
		return nil, trendErr
	}
	return nil, errors.Join(primaryErr, trendErr)
}

func (c *Client) fetchStockKline(ctx context.Context, tradeDate string, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	dateToken := strings.ReplaceAll(tradeDate, "-", "")
	params := url.Values{
		"secid": {fmt.Sprintf("%d.%s", stock.Market, stock.Code)}, "klt": {"5"}, "fqt": {"0"},
		"beg": {dateToken}, "end": {dateToken}, "fields1": {"f1,f2,f3,f4,f5,f6"},
		"fields2": {"f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.stockKlineBaseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 ShadowFlow/0.1")
	request.Header.Set("Referer", "https://quote.eastmoney.com/")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	var payload stockKlineResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: %v", graymarket.ErrDecode, err)
	}
	if payload.ReturnCode != 0 || payload.Data == nil || len(payload.Data.Klines) != 48 {
		count := 0
		if payload.Data != nil {
			count = len(payload.Data.Klines)
		}
		return nil, fmt.Errorf("expected 48 klines, got %d", count)
	}
	fetchedAt := time.Now().UTC()
	points := make([]graymarket.StockKlinePoint, 0, 48)
	for _, raw := range payload.Data.Klines {
		fields, err := csv.NewReader(strings.NewReader(raw)).Read()
		if err != nil || len(fields) != 11 {
			return nil, fmt.Errorf("invalid kline row %q", raw)
		}
		at, err := time.ParseInLocation("2006-01-02 15:04", fields[0], snapshotLocation(stock.SnapshotAt))
		if err != nil || at.Format("2006-01-02") != tradeDate {
			return nil, fmt.Errorf("kline date mismatch %q", fields[0])
		}
		if _, ok := researchMinuteIndexForSource(at); !ok {
			return nil, fmt.Errorf("unexpected kline time %s", fields[0])
		}
		points = append(points, graymarket.StockKlinePoint{
			TradeDate: tradeDate, SnapshotAt: at, Market: stock.Market, Code: stock.Code, Source: graymarket.KlineSourceFiveMinute,
			OpenPrice: decimal(fields[1]), ClosePrice: decimal(fields[2]), HighPrice: decimal(fields[3]), LowPrice: decimal(fields[4]),
			Volume: integer(fields[5]), Turnover: integer(fields[6]), Amplitude: percent(fields[7]), ChangePct: percent(fields[8]),
			ChangeValue: decimal(fields[9]), TurnoverRate: percent(fields[10]), FetchedAt: fetchedAt,
		})
	}
	return points, nil
}

type aggregatedTrendBar struct {
	point      graymarket.StockKlinePoint
	firstAt    time.Time
	lastAt     time.Time
	minuteRows int
	tradedRows int
}

func (c *Client) fetchStockKlineFromTrends(ctx context.Context, tradeDate string, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	var combined error
	for _, baseURL := range c.stockTrendBaseURLs {
		points, err := c.fetchStockKlineFromTrendURL(ctx, baseURL, tradeDate, stock)
		if err == nil {
			return points, nil
		}
		combined = errors.Join(combined, fmt.Errorf("%s: %w", baseURL, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, combined
}

func (c *Client) fetchStockKlineFromTrendURL(ctx context.Context, baseURL, tradeDate string, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	params := url.Values{
		"secid": {fmt.Sprintf("%d.%s", stock.Market, stock.Code)}, "ndays": {"5"}, "iscr": {"0"},
		"ut":      {"fa5fd1943c7b386f172d6893dbfba10b"},
		"fields1": {"f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13"},
		"fields2": {"f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 ShadowFlow/0.1")
	request.Header.Set("Referer", "https://quote.eastmoney.com/")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	var payload stockTrendResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: %v", graymarket.ErrDecode, err)
	}
	if payload.ReturnCode != 0 || payload.Data == nil {
		return nil, fmt.Errorf("trend response has no data")
	}

	location := snapshotLocation(stock.SnapshotAt)
	fetchedAt := time.Now().UTC()
	bars := make([]aggregatedTrendBar, 48)
	seenMinutes := make(map[string]struct{}, 241)
	minuteRows := 0
	var previousAt time.Time
	var cumulativeVolume int64
	var cumulativeTurnover int64
	for _, raw := range payload.Data.Trends {
		fields, err := csv.NewReader(strings.NewReader(raw)).Read()
		if err != nil || len(fields) < 12 {
			return nil, fmt.Errorf("invalid trend row %q", raw)
		}
		at, err := time.ParseInLocation("2006-01-02 15:04", fields[0], location)
		if err != nil {
			return nil, fmt.Errorf("invalid trend time %q", fields[0])
		}
		if at.Format("2006-01-02") != tradeDate {
			continue
		}
		index, ok := stockTrendBucket(at)
		if !ok {
			return nil, fmt.Errorf("unexpected trend time %s", fields[0])
		}
		if _, duplicate := seenMinutes[fields[0]]; duplicate {
			return nil, fmt.Errorf("duplicate trend minute %s", fields[0])
		}
		seenMinutes[fields[0]] = struct{}{}
		if !previousAt.IsZero() && !at.After(previousAt) {
			return nil, fmt.Errorf("trend rows are not chronological at %s", fields[0])
		}
		previousAt = at
		minuteRows++
		openPrice, closePrice, highPrice, lowPrice := decimal(fields[1]), decimal(fields[2]), decimal(fields[3]), decimal(fields[4])
		bar := &bars[index]
		nextCumulativeVolume := integer(fields[10])
		if nextCumulativeVolume < cumulativeVolume {
			return nil, fmt.Errorf("trend cumulative volume decreased at %s", fields[0])
		}
		minuteVolume := nextCumulativeVolume - cumulativeVolume
		bar.point.Volume += minuteVolume
		cumulativeVolume = nextCumulativeVolume
		nextCumulativeTurnover := integer(fields[11])
		if nextCumulativeTurnover < cumulativeTurnover {
			return nil, fmt.Errorf("trend cumulative turnover decreased at %s", fields[0])
		}
		bar.point.Turnover += nextCumulativeTurnover - cumulativeTurnover
		cumulativeTurnover = nextCumulativeTurnover
		if bar.minuteRows == 0 {
			bar.firstAt, bar.lastAt = at, at
			bar.point.OpenPrice, bar.point.ClosePrice = openPrice, closePrice
			bar.point.HighPrice, bar.point.LowPrice = highPrice, lowPrice
		}
		if minuteVolume > 0 {
			if bar.tradedRows == 0 {
				bar.firstAt, bar.lastAt = at, at
				bar.point.OpenPrice, bar.point.ClosePrice = openPrice, closePrice
				bar.point.HighPrice, bar.point.LowPrice = highPrice, lowPrice
			} else {
				if at.After(bar.lastAt) {
					bar.lastAt, bar.point.ClosePrice = at, closePrice
				}
				bar.point.HighPrice = max(bar.point.HighPrice, highPrice)
				bar.point.LowPrice = min(bar.point.LowPrice, lowPrice)
			}
			bar.tradedRows++
		}
		bar.minuteRows++
	}
	if minuteRows != 241 {
		return nil, fmt.Errorf("expected 241 one-minute trend rows, got %d", minuteRows)
	}

	points := make([]graymarket.StockKlinePoint, 0, 48)
	previousClose := stock.PreviousClose
	for index := range bars {
		bar := &bars[index]
		expectedRows := 5
		if index == 0 {
			expectedRows = 6
		}
		if bar.minuteRows != expectedRows || previousClose <= 0 {
			return nil, fmt.Errorf("incomplete five-minute trend bucket %d: expected %d rows, got %d", index, expectedRows, bar.minuteRows)
		}
		bar.point.TradeDate, bar.point.SnapshotAt = tradeDate, researchTimeForIndex(tradeDate, index, location)
		bar.point.Market, bar.point.Code, bar.point.Source, bar.point.FetchedAt = stock.Market, stock.Code, graymarket.KlineSourceTrend241, fetchedAt
		bar.point.Amplitude = (bar.point.HighPrice - bar.point.LowPrice) / previousClose
		bar.point.ChangeValue = bar.point.ClosePrice - previousClose
		bar.point.ChangePct = bar.point.ChangeValue / previousClose
		if stock.Volume > 0 {
			bar.point.TurnoverRate = stock.TurnoverRate * float64(bar.point.Volume) / float64(stock.Volume)
		}
		previousClose = bar.point.ClosePrice
		points = append(points, bar.point)
	}
	if !samePrice(points[0].OpenPrice, stock.OpenPrice) || !samePrice(maxKlinePrice(points), stock.HighPrice) ||
		!samePrice(minKlinePrice(points), stock.LowPrice) || !samePrice(points[47].ClosePrice, stock.ClosePrice) {
		return nil, fmt.Errorf("aggregated trend OHLC does not match daily bar")
	}
	return points, nil
}

func stockTrendBucket(value time.Time) (int, bool) {
	minutes := value.Hour()*60 + value.Minute()
	if minutes == 9*60+30 {
		return 0, true
	}
	if minutes >= 9*60+31 && minutes <= 11*60+30 {
		return (minutes - (9*60 + 31)) / 5, true
	}
	if minutes >= 13*60+1 && minutes <= 15*60 {
		return 24 + (minutes-(13*60+1))/5, true
	}
	return 0, false
}

func researchTimeForIndex(tradeDate string, index int, location *time.Location) time.Time {
	minute := 9*60 + 35 + index*5
	if index >= 24 {
		minute = 13*60 + 5 + (index-24)*5
	}
	value, _ := time.ParseInLocation("2006-01-02 15:04", fmt.Sprintf("%s %02d:%02d", tradeDate, minute/60, minute%60), location)
	return value
}

func samePrice(left, right float64) bool { return math.Abs(left-right) <= 0.0001 }
func maxKlinePrice(points []graymarket.StockKlinePoint) float64 {
	result := points[0].HighPrice
	for _, point := range points[1:] {
		result = max(result, point.HighPrice)
	}
	return result
}
func minKlinePrice(points []graymarket.StockKlinePoint) float64 {
	result := points[0].LowPrice
	for _, point := range points[1:] {
		result = min(result, point.LowPrice)
	}
	return result
}

func decimal(value string) float64 { result, _ := strconv.ParseFloat(value, 64); return result }
func integer(value string) int64 {
	result, _ := strconv.ParseInt(strings.SplitN(value, ".", 2)[0], 10, 64)
	return result
}
func percent(value string) float64 { return decimal(value) / 100 }

func researchMinuteIndexForSource(value time.Time) (int, bool) {
	minutes := value.Hour()*60 + value.Minute()
	if minutes >= 9*60+35 && minutes <= 11*60+30 && (minutes-(9*60+35))%5 == 0 {
		return (minutes - (9*60 + 35)) / 5, true
	}
	if minutes >= 13*60+5 && minutes <= 15*60 && (minutes-(13*60+5))%5 == 0 {
		return 24 + (minutes-(13*60+5))/5, true
	}
	return 0, false
}
