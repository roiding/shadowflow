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

func decodeKlineBody(body io.Reader, payload any) error {
	// Consume through EOF before accepting JSON so chunked-body truncation
	// reaches the upstream guard even when it follows a complete JSON value.
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return fmt.Errorf("%w: %v", graymarket.ErrDecode, err)
	}
	if len(data) > limit {
		return fmt.Errorf("%w: kline body exceeds %d bytes", graymarket.ErrDecode, limit)
	}
	if err := json.Unmarshal(data, payload); err != nil {
		return fmt.Errorf("%w: %v", graymarket.ErrDecode, err)
	}
	return nil
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

// FetchStockKline5m exposes the per-day, per-stock archive fetch to the
// market-data router. The native five-minute series is the only source here;
// the router owns cross-provider failover.
func (c *Client) FetchStockKline5m(ctx context.Context, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	return c.fetchStockKlineWithFallback(ctx, stock.TradeDate, stock)
}

func (c *Client) FetchStockKlines5mIncremental(ctx context.Context, snapshot graymarket.RankSnapshot, persist func([]graymarket.StockKlinePoint) error) (int, error) {
	if snapshot.RankType != graymarket.RankStock || snapshot.TradeDate == "" || len(snapshot.Records) == 0 {
		return 0, fmt.Errorf("invalid stock kline snapshot")
	}
	if err := c.PrepareUpstreamRequests(ctx, "stock-kline", len(snapshot.Records), min(4, len(snapshot.Records))); err != nil {
		return 0, err
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
				points, err := c.fetchStockKlineWithFallback(ctx, snapshot.TradeDate, stock)
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
		points, err := c.fetchStockKlineFromHistory(ctx, tradeDate, stock)
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

// fetchStockKlineWithFallback intentionally has no one-minute aggregation
// fallback. Aggregating 241 one-minute quote snapshots into 48 five-minute
// bars produced endpoint drift (auction rows without trades), so the archive
// now uses the native five-minute series only. The one-minute endpoint is
// still consulted inside that path, but only to correct a first bar whose
// open disagrees with the authoritative daily bar. Cross-provider failover
// belongs to the marketdata router, not here.
func (c *Client) fetchStockKlineWithFallback(ctx context.Context, tradeDate string, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	return c.fetchStockKlineWithRetry(ctx, tradeDate, stock)
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
	if err := decodeKlineBody(response.Body, &payload); err != nil {
		return nil, err
	}
	if payload.ReturnCode != 0 || payload.Data == nil || len(payload.Data.Klines) != 48 {
		count := 0
		if payload.Data != nil {
			count = len(payload.Data.Klines)
		}
		return nil, fmt.Errorf("%w: expected 48 historical klines, got %d", graymarket.ErrInstrumentUnavailable, count)
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
			return nil, fmt.Errorf("%w: historical kline date mismatch %q", graymarket.ErrInstrumentUnavailable, fields[0])
		}
		index, ok := researchMinuteIndexForSource(at)
		if !ok {
			return nil, fmt.Errorf("%w: unexpected historical kline time %s", graymarket.ErrInstrumentUnavailable, fields[0])
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, fmt.Errorf("%w: duplicate historical kline time %s", graymarket.ErrInstrumentUnavailable, fields[0])
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
		return nil, fmt.Errorf("%w: historical kline has %d distinct points", graymarket.ErrInstrumentUnavailable, len(seen))
	}
	first := &points[0]
	// When the opening auction prints no volume, the five-minute endpoint
	// reuses the previous close as the first bar's open and low. The daily bar
	// is authoritative for the auction price, and the one-minute series still
	// carries the real first trades, so rebuild the bar from 09:31-09:35
	// before normalizing the open.
	corrected := false
	if stock.PreviousClose > 0 &&
		samePrice(first.OpenPrice, stock.PreviousClose) &&
		!samePrice(stock.OpenPrice, stock.PreviousClose) {
		if err := c.correctZeroAuctionFirstBar(ctx, tradeDate, stock, first); err == nil {
			corrected = true
		}
	}
	first.OpenPrice = stock.OpenPrice
	if first.OpenPrice > first.HighPrice || first.OpenPrice < first.LowPrice {
		return nil, fmt.Errorf("daily open is outside the first five-minute bar")
	}
	if corrected && stock.PreviousClose > 0 {
		first.Amplitude = (first.HighPrice - first.LowPrice) / stock.PreviousClose
		first.ChangeValue = first.ClosePrice - stock.PreviousClose
		first.ChangePct = first.ChangeValue / stock.PreviousClose
	}
	if err := validateKlinePrices(points, stock); err != nil {
		return nil, err
	}
	return points, nil
}

// correctZeroAuctionFirstBar rebuilds the first five-minute bar from the
// one-minute series when the opening auction printed no volume. Eastmoney's
// five-minute endpoint then reuses the previous close as the bar's open and
// low, while the one-minute rows carry the real first trades.
func (c *Client) correctZeroAuctionFirstBar(ctx context.Context, tradeDate string, stock graymarket.RankRecord, bar *graymarket.StockKlinePoint) error {
	dateToken := strings.ReplaceAll(tradeDate, "-", "")
	params := url.Values{
		"secid": {fmt.Sprintf("%d.%s", stock.Market, stock.Code)},
		"klt":   {"1"}, "fqt": {"0"}, "beg": {dateToken}, "end": {dateToken},
		"fields1": {"f1,f2,f3,f4,f5,f6"},
		"fields2": {"f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.stockKlineBaseURL+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 ShadowFlow/0.1")
	request.Header.Set("Referer", "https://quote.eastmoney.com/")
	response, err := c.guard.Do(ctx, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return fmt.Errorf("one-minute kline returned HTTP %d", response.StatusCode)
	}
	var payload stockKlineResponse
	if err := decodeKlineBody(response.Body, &payload); err != nil {
		return err
	}
	if payload.ReturnCode != 0 || payload.Data == nil {
		return fmt.Errorf("one-minute kline response has no data")
	}
	location := snapshotLocation(stock.SnapshotAt)
	var openPrice, highPrice, lowPrice, closePrice float64
	var volume, turnover int64
	seen := 0
	for _, raw := range payload.Data.Klines {
		fields, err := csv.NewReader(strings.NewReader(raw)).Read()
		if err != nil || len(fields) != 11 {
			return fmt.Errorf("invalid one-minute kline row %q", raw)
		}
		at, err := time.ParseInLocation("2006-01-02 15:04", fields[0], location)
		if err != nil || at.Format("2006-01-02") != tradeDate {
			return fmt.Errorf("one-minute kline date mismatch %q", fields[0])
		}
		minutes := at.Hour()*60 + at.Minute()
		if minutes < 9*60+31 || minutes > 9*60+35 {
			continue
		}
		numbers, err := klineNumbers(fields, 1, 2, 3, 4, 5, 6)
		if err != nil {
			return err
		}
		barVolume, err := klineAmount(numbers[5], true)
		if err != nil {
			return fmt.Errorf("one-minute volume at %s: %w", fields[0], err)
		}
		barTurnover, err := klineAmount(numbers[6], false)
		if err != nil {
			return fmt.Errorf("one-minute turnover at %s: %w", fields[0], err)
		}
		if seen == 0 {
			openPrice, highPrice, lowPrice = numbers[1], numbers[3], numbers[4]
		} else {
			highPrice = max(highPrice, numbers[3])
			lowPrice = min(lowPrice, numbers[4])
		}
		closePrice = numbers[2]
		volume += barVolume
		turnover += barTurnover
		seen++
	}
	if seen != 5 {
		return fmt.Errorf("expected 5 one-minute bars in the opening five minutes, got %d", seen)
	}
	if !samePrice(openPrice, stock.OpenPrice) {
		return fmt.Errorf("one-minute open %.4f does not match daily open %.4f", openPrice, stock.OpenPrice)
	}
	if !samePrice(highPrice, bar.HighPrice) || !samePrice(closePrice, bar.ClosePrice) {
		return fmt.Errorf("one-minute OHLC does not match the five-minute bar")
	}
	// The five-minute bucket can include one extra auction print that the
	// one-minute series does not, so allow a one-lot drift while still
	// rejecting a materially different volume.
	volumeDrift := volume - bar.Volume
	if volumeDrift < 0 {
		volumeDrift = -volumeDrift
	}
	if volumeDrift > 1 {
		return fmt.Errorf("one-minute volume %d does not match five-minute volume %d", volume, bar.Volume)
	}
	bar.OpenPrice, bar.HighPrice, bar.LowPrice, bar.ClosePrice = openPrice, highPrice, lowPrice, closePrice
	bar.Volume, bar.Turnover = volume, turnover
	return nil
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
	if err := decodeKlineBody(response.Body, &payload); err != nil {
		return nil, err
	}
	if payload.ReturnCode != 0 || payload.Data == nil {
		return nil, fmt.Errorf("%w: trend response has no data", graymarket.ErrInstrumentUnavailable)
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
	// The daily bar is authoritative at both auction boundaries. One-minute
	// trend rows without a trade are quote updates, so ignoring them as trades
	// must not leave the first open or final close stuck at the nearest traded
	// minute. Normalize the two endpoints and then recompute derived fields.
	first := &points[0]
	first.OpenPrice = stock.OpenPrice
	first.HighPrice = max(first.HighPrice, stock.OpenPrice)
	first.LowPrice = min(first.LowPrice, stock.OpenPrice)
	last := &points[47]
	last.ClosePrice = stock.ClosePrice
	last.HighPrice = max(last.HighPrice, stock.ClosePrice)
	last.LowPrice = min(last.LowPrice, stock.ClosePrice)
	previousClose = stock.PreviousClose
	for index := range points {
		if index > 0 {
			previousClose = points[index-1].ClosePrice
		}
		points[index].Amplitude = (points[index].HighPrice - points[index].LowPrice) / previousClose
		points[index].ChangeValue = points[index].ClosePrice - previousClose
		points[index].ChangePct = points[index].ChangeValue / previousClose
	}
	if err := validateKlinePrices(points, stock); err != nil {
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

// FetchStockKlines5mRange fetches one stock's 5-minute bars over an inclusive
// date range in a single upstream request and emits them grouped per trade
// date. This is the operator-run backfill path: bars are NOT cross-checked
// against the stock's daily bar (the upstream's own BSE series disagrees with
// its daily bar on auction and after-hours prints), and every point is marked
// with the backfill source. Dates whose 5-minute series is incomplete are
// skipped, so the regular per-day task can retry them later.
func (c *Client) FetchStockKlines5mRange(ctx context.Context, from, to time.Time,
	stock graymarket.RankRecord, maxBars int, emit func(tradeDate string, points []graymarket.StockKlinePoint)) error {
	if maxBars < 48 || maxBars > 3000 {
		return fmt.Errorf("%w: range backfill bar budget out of bounds: %d", graymarket.ErrNoData, maxBars)
	}
	params := url.Values{
		"secid": {fmt.Sprintf("%d.%s", stock.Market, stock.Code)},
		"klt":   {"5"}, "fqt": {"0"},
		"beg": {from.Format("20060102")}, "end": {to.Format("20060102")},
		"lmt":     {strconv.Itoa(maxBars)},
		"fields1": {"f1,f2,f3,f4,f5,f6"},
		"fields2": {"f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.stockKlineBaseURL+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 ShadowFlow/0.1")
	request.Header.Set("Referer", "https://quote.eastmoney.com/")
	response, err := c.guard.Do(ctx, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	var payload stockKlineResponse
	if err := decodeKlineBody(response.Body, &payload); err != nil {
		return err
	}
	if payload.ReturnCode != 0 || payload.Data == nil || len(payload.Data.Klines) == 0 {
		count := 0
		if payload.Data != nil {
			count = len(payload.Data.Klines)
		}
		return fmt.Errorf("%w: range kline response has %d bars", graymarket.ErrNoData, count)
	}
	fetchedAt := time.Now().UTC()
	type dayBatch struct {
		points []graymarket.StockKlinePoint
		seen   map[int]struct{}
	}
	days := map[string]*dayBatch{}
	var order []string
	for _, raw := range payload.Data.Klines {
		fields, err := csv.NewReader(strings.NewReader(raw)).Read()
		if err != nil || len(fields) != 11 {
			return fmt.Errorf("invalid historical kline row %q", raw)
		}
		at, err := time.ParseInLocation("2006-01-02 15:04", fields[0], snapshotLocation(stock.SnapshotAt))
		if err != nil {
			return fmt.Errorf("invalid historical kline time %q", fields[0])
		}
		tradeDate := at.Format("2006-01-02")
		index, ok := researchMinuteIndexForSource(at)
		if !ok {
			return fmt.Errorf("unexpected historical kline time %s", fields[0])
		}
		batch := days[tradeDate]
		if batch == nil {
			batch = &dayBatch{seen: make(map[int]struct{}, 48)}
			days[tradeDate] = batch
			order = append(order, tradeDate)
		}
		if _, duplicate := batch.seen[index]; duplicate {
			return fmt.Errorf("duplicate historical kline time %s", fields[0])
		}
		batch.seen[index] = struct{}{}
		numbers, err := klineNumbers(fields, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
		if err != nil {
			return err
		}
		volume, err := klineAmount(numbers[5], true)
		if err != nil {
			return fmt.Errorf("volume at %s: %w", fields[0], err)
		}
		turnover, err := klineAmount(numbers[6], false)
		if err != nil {
			return fmt.Errorf("turnover at %s: %w", fields[0], err)
		}
		batch.points = append(batch.points, graymarket.StockKlinePoint{
			TradeDate: tradeDate, SnapshotAt: at, Market: stock.Market, Code: stock.Code,
			Source:    graymarket.KlineSourceFiveMinuteBackfill,
			OpenPrice: numbers[1], ClosePrice: numbers[2], HighPrice: numbers[3], LowPrice: numbers[4],
			Volume: volume, Turnover: turnover, Amplitude: numbers[7] / 100, ChangePct: numbers[8] / 100,
			ChangeValue: numbers[9], TurnoverRate: numbers[10] / 100, FetchedAt: fetchedAt,
		})
	}
	for _, tradeDate := range order {
		batch := days[tradeDate]
		if len(batch.seen) != 48 {
			continue
		}
		emit(tradeDate, batch.points)
	}
	return nil
}
