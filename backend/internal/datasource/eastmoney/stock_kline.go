package eastmoney

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
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

type stockKlineResult struct {
	points []graymarket.StockKlinePoint
	err    error
}

func (c *Client) FetchStockKlines5m(ctx context.Context, snapshot graymarket.RankSnapshot) ([]graymarket.StockKlinePoint, error) {
	if snapshot.RankType != graymarket.RankStock || snapshot.TradeDate == "" || len(snapshot.Records) == 0 {
		return nil, fmt.Errorf("invalid stock kline snapshot")
	}
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
		points, err := c.fetchStockKline(ctx, tradeDate, stock)
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
			TradeDate: tradeDate, SnapshotAt: at, Market: stock.Market, Code: stock.Code,
			OpenPrice: decimal(fields[1]), ClosePrice: decimal(fields[2]), HighPrice: decimal(fields[3]), LowPrice: decimal(fields[4]),
			Volume: integer(fields[5]), Turnover: integer(fields[6]), Amplitude: percent(fields[7]), ChangePct: percent(fields[8]),
			ChangeValue: decimal(fields[9]), TurnoverRate: percent(fields[10]), FetchedAt: fetchedAt,
		})
	}
	return points, nil
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
