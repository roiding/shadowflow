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
	"strings"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type stockTrendResponse struct {
	ReturnCode int `json:"rc"`
	Data       *struct {
		Trends []string `json:"trends"`
	} `json:"data"`
}

type stockKlineResponse struct {
	ReturnCode int `json:"rc"`
	Data       *struct {
		Klines []string `json:"klines"`
	} `json:"data"`
}

type stockKlineResult struct {
	points []graymarket.StockKlinePoint
	err    error
}

func (c *Client) FetchStockKlines5m(ctx context.Context, snapshot graymarket.RankSnapshot) ([]graymarket.StockKlinePoint, error) {
	points := make([]graymarket.StockKlinePoint, 0, len(snapshot.Records)*48)
	_, err := c.FetchStockKlines5mIncremental(ctx, snapshot, func(batch []graymarket.StockKlinePoint) error {
		points = append(points, batch...)
		return nil
	})
	if err != nil {
		return points, err
	}
	if len(points) != len(snapshot.Records)*48 {
		return nil, fmt.Errorf("incomplete stock kline archive: expected %d points, got %d", len(snapshot.Records)*48, len(points))
	}
	return points, nil
}

// FetchStockKlines5mIncremental fetches the 241-point one-minute curve for each
// stock, aggregates it into one complete 48-point five-minute curve, and
// invokes persist immediately. The callback is called serially, so callers
// can safely write each batch in its own transaction.
func (c *Client) FetchStockKlines5mIncremental(ctx context.Context, snapshot graymarket.RankSnapshot, persist func([]graymarket.StockKlinePoint) error) (int, error) {
	if snapshot.RankType != graymarket.RankStock || snapshot.TradeDate == "" || len(snapshot.Records) == 0 {
		return 0, fmt.Errorf("invalid stock kline snapshot")
	}
	parentCtx := ctx
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	// Four workers keep the upstream connection pool busy while the shared
	// limiter caps request bursts. Persistence is batched by the collector, so
	// this no longer creates one SQLite write transaction per stock.
	workerCount := min(4, len(snapshot.Records))
	jobs := make(chan graymarket.RankRecord, workerCount)
	results := make(chan stockKlineResult, workerCount)
	limiter := time.NewTicker(100 * time.Millisecond)
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
				case <-ctx.Done():
					// Watch the cancellable child context, not parentCtx: when the
					// consumer aborts on a persist error it stops draining results
					// and cancels ctx; blocking on parentCtx here would leak every
					// worker still holding a result.
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
	var firstErr error
	failedStocks := 0
	completedStocks := 0
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
			if err := persist(result.points); err != nil {
				cancel()
				return completedStocks, fmt.Errorf("persist stock kline batch: %w", err)
			}
			completedStocks++
		}
	}
	if parentCtx.Err() != nil {
		return completedStocks, parentCtx.Err()
	}
	if firstErr != nil {
		return completedStocks, fmt.Errorf("stock kline batch incomplete: completed %d/%d stocks, failed %d; first error: %w",
			completedStocks, len(snapshot.Records), failedStocks, firstErr)
	}
	if completedStocks != len(snapshot.Records) {
		return completedStocks, fmt.Errorf("incomplete stock kline archive: expected %d stocks, got %d", len(snapshot.Records), completedStocks)
	}
	return completedStocks, nil
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

func (c *Client) fetchStockKlineFromTrendsWithRetry(ctx context.Context, tradeDate string, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		points, err := c.fetchStockKlineFromTrends(ctx, tradeDate, stock)
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

type aggregatedTrendBar struct {
	point                graymarket.StockKlinePoint
	firstAt              time.Time
	lastAt               time.Time
	minuteRows           int
	tradedRows           int
	openingAuctionPrice  float64
	openingAuctionUsable bool
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

func (c *Client) fetchStockKlineWithFallback(ctx context.Context, tradeDate string, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	trendPoints, trendErr := c.fetchStockKlineFromTrends(ctx, tradeDate, stock)
	if trendErr == nil {
		return trendPoints, nil
	}
	historyPoints, historyErr := c.fetchStockKlineFromHistory(ctx, tradeDate, stock)
	if historyErr == nil {
		return historyPoints, nil
	}
	return nil, errors.Join(fmt.Errorf("trends2: %w", trendErr), fmt.Errorf("historical kline: %w", historyErr))
}

func (c *Client) fetchStockKlineFromHistory(ctx context.Context, tradeDate string, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	dateToken := strings.ReplaceAll(tradeDate, "-", "")
	params := url.Values{
		"secid": {fmt.Sprintf("%d.%s", stock.Market, stock.Code)},
		"klt":   {"5"}, "fqt": {"0"}, "beg": {dateToken}, "end": {dateToken},
		"fields1": {"f1,f2,f3,f4,f5,f6"},
		"fields2": {"f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.stockKlineBaseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 ShadowFlow/0.1")
	request.Header.Set("Referer", "https://quote.eastmoney.com/")
	response, err := c.guard.Do(ctx, request)
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
		return nil, fmt.Errorf("expected 48 historical klines, got %d", count)
	}
	fetchedAt := time.Now().UTC()
	points := make([]graymarket.StockKlinePoint, 48)
	seen := make(map[int]struct{}, 48)
	for _, raw := range payload.Data.Klines {
		fields, err := csv.NewReader(strings.NewReader(raw)).Read()
		if err != nil || len(fields) != 11 {
			return nil, fmt.Errorf("invalid historical kline row %q", raw)
		}
		at, err := time.ParseInLocation("2006-01-02 15:04", fields[0], snapshotLocation(stock.SnapshotAt))
		if err != nil || at.Format("2006-01-02") != tradeDate {
			return nil, fmt.Errorf("historical kline date mismatch %q", fields[0])
		}
		index, ok := researchMinuteIndexForSource(at)
		if !ok {
			return nil, fmt.Errorf("unexpected historical kline time %s", fields[0])
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, fmt.Errorf("duplicate historical kline time %s", fields[0])
		}
		seen[index] = struct{}{}
		numbers, err := klineNumbers(fields, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
		if err != nil {
			return nil, err
		}
		volume, err := klineAmount(numbers[5], true)
		if err != nil {
			return nil, fmt.Errorf("volume at %s: %w", fields[0], err)
		}
		turnover, err := klineAmount(numbers[6], false)
		if err != nil {
			return nil, fmt.Errorf("turnover at %s: %w", fields[0], err)
		}
		points[index] = graymarket.StockKlinePoint{
			TradeDate: tradeDate, SnapshotAt: at, Market: stock.Market, Code: stock.Code, Source: graymarket.KlineSourceFiveMinute,
			OpenPrice: numbers[1], ClosePrice: numbers[2], HighPrice: numbers[3], LowPrice: numbers[4],
			Volume: volume, Turnover: turnover, Amplitude: numbers[7] / 100, ChangePct: numbers[8] / 100,
			ChangeValue: numbers[9], TurnoverRate: numbers[10] / 100, FetchedAt: fetchedAt,
		}
	}
	if len(seen) != 48 {
		return nil, fmt.Errorf("historical kline has %d distinct points", len(seen))
	}
	if err := validateKlinePrices(points, stock, false); err != nil {
		return nil, err
	}
	return points, nil
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
	response, err := c.guard.Do(ctx, request)
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
	var closeAuctionVolume int64
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
		numbers, err := klineNumbers(fields, 1, 2, 3, 4, 10, 11)
		if err != nil {
			return nil, err
		}
		openPrice, closePrice, highPrice, lowPrice := numbers[1], numbers[2], numbers[3], numbers[4]
		bar := &bars[index]
		nextCumulativeVolume, err := klineAmount(numbers[10], true)
		if err != nil {
			return nil, fmt.Errorf("volume at %s: %w", fields[0], err)
		}
		if nextCumulativeVolume < cumulativeVolume {
			return nil, fmt.Errorf("trend cumulative volume decreased at %s", fields[0])
		}
		minuteVolume := nextCumulativeVolume - cumulativeVolume
		if at.Hour() == 15 && at.Minute() == 0 {
			closeAuctionVolume = minuteVolume
		}
		bar.point.Volume += minuteVolume
		cumulativeVolume = nextCumulativeVolume
		nextCumulativeTurnover, err := klineAmount(numbers[11], false)
		if err != nil {
			return nil, fmt.Errorf("turnover at %s: %w", fields[0], err)
		}
		if nextCumulativeTurnover < cumulativeTurnover {
			return nil, fmt.Errorf("trend cumulative turnover decreased at %s", fields[0])
		}
		bar.point.Turnover += nextCumulativeTurnover - cumulativeTurnover
		cumulativeTurnover = nextCumulativeTurnover
		if bar.minuteRows == 0 {
			bar.firstAt, bar.lastAt = at, at
			bar.point.OpenPrice, bar.point.ClosePrice = openPrice, closePrice
			bar.point.HighPrice, bar.point.LowPrice = highPrice, lowPrice
			// Some instruments publish the opening auction price at 09:30 with
			// zero volume. Preserve it for the first five-minute bar only when
			// it agrees with the authoritative daily open; a zero-volume prior
			// close placeholder must not contaminate the bar.
			if index == 0 && at.Hour() == 9 && at.Minute() == 30 &&
				minuteVolume == 0 && samePrice(openPrice, stock.OpenPrice) {
				bar.openingAuctionPrice = openPrice
				bar.openingAuctionUsable = true
			}
		}
		if minuteVolume > 0 {
			if bar.tradedRows == 0 {
				bar.firstAt, bar.lastAt = at, at
				bar.point.OpenPrice, bar.point.ClosePrice = openPrice, closePrice
				bar.point.HighPrice, bar.point.LowPrice = highPrice, lowPrice
				if bar.openingAuctionUsable {
					bar.point.OpenPrice = bar.openingAuctionPrice
					bar.point.HighPrice = max(bar.point.HighPrice, bar.openingAuctionPrice)
					bar.point.LowPrice = min(bar.point.LowPrice, bar.openingAuctionPrice)
				}
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
	// The closing auction can publish an official daily close without a
	// matching trade. In that case the final one-minute row has zero volume,
	// so the last traded five-minute close may legitimately differ from the
	// daily close. The daily close remains authoritative for the daily bar.
	if err := validateKlinePrices(points, stock, closeAuctionVolume == 0); err != nil {
		return nil, err
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
