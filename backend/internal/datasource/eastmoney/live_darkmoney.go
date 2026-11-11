package eastmoney

import (
	"bytes"
	"context"
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

type latestMoneyResult struct {
	point graymarket.MoneyPoint
	err   error
}

// FetchLatestMoney5m reads the latest already-observed dark-money tick for
// each requested stock. Unlike the archive path it does not require all 48
// points, so it is usable during the session.
func (c *Client) FetchLatestMoney5m(
	ctx context.Context,
	tradeDate string,
	records []graymarket.RankRecord,
	at time.Time,
) ([]graymarket.MoneyPoint, error) {
	if len(records) == 0 {
		return []graymarket.MoneyPoint{}, nil
	}
	cutoff, valid := latestMoneyCutoff(at)
	if !valid {
		return []graymarket.MoneyPoint{}, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerCount := min(8, len(records))
	jobs := make(chan graymarket.RankRecord, workerCount)
	results := make(chan latestMoneyResult, workerCount)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for record := range jobs {
				point, err := c.fetchLatestMoney5m(ctx, tradeDate, record, cutoff)
				select {
				case results <- latestMoneyResult{point: point, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, record := range records {
			select {
			case jobs <- record:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	points := make([]graymarket.MoneyPoint, 0, len(records))
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		points = append(points, result.point)
	}
	if len(points) == 0 && firstErr != nil {
		return nil, firstErr
	}
	if firstErr != nil {
		return points, fmt.Errorf("completed %d/%d live dark-money ticks: %w", len(points), len(records), firstErr)
	}
	return points, nil
}

func (c *Client) fetchLatestMoney5m(
	ctx context.Context,
	tradeDate string,
	record graymarket.RankRecord,
	cutoff time.Time,
) (graymarket.MoneyPoint, error) {
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		point, err := c.fetchLatestMoney5mOnce(ctx, tradeDate, record, cutoff)
		if err == nil {
			return point, nil
		}
		lastErr = err
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return graymarket.MoneyPoint{}, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return graymarket.MoneyPoint{}, fmt.Errorf("fetch %s live dark money: %w", record.Code, lastErr)
}

func (c *Client) fetchLatestMoney5mOnce(
	ctx context.Context,
	tradeDate string,
	record graymarket.RankRecord,
	cutoff time.Time,
) (graymarket.MoneyPoint, error) {
	params := url.Values{
		"code": {record.Code}, "market": {strconv.FormatInt(record.Market, 10)},
		"time": {"0"}, "version": {"100"}, "cver": {"11.2.6"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.darkTradeTickBaseURL+"?"+params.Encode(), nil)
	if err != nil {
		return graymarket.MoneyPoint{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "shadowflow/0.1")
	response, err := c.guard.Do(ctx, request)
	if err != nil {
		return graymarket.MoneyPoint{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return graymarket.MoneyPoint{}, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return graymarket.MoneyPoint{}, err
	}
	decoded, _, err := decodeJSON(body, response.Header.Get("Content-Type"))
	if err != nil {
		return graymarket.MoneyPoint{}, fmt.Errorf("%w: %v", graymarket.ErrDecode, err)
	}
	var payload darkTradeTickResponse
	if err := json.NewDecoder(bytes.NewReader(decoded)).Decode(&payload); err != nil {
		return graymarket.MoneyPoint{}, fmt.Errorf("%w: %v", graymarket.ErrDecode, err)
	}
	if payload.ErrorID != 0 {
		return graymarket.MoneyPoint{}, fmt.Errorf("upstream error %d: %s", payload.ErrorID, payload.ErrorText)
	}
	dateToken := strings.ReplaceAll(tradeDate, "-", "")
	if len(dateToken) != 8 {
		return graymarket.MoneyPoint{}, fmt.Errorf("invalid trade date %q", tradeDate)
	}
	location := snapshotLocation(record.SnapshotAt)
	var latest *darkTradeTick
	var latestAt time.Time
	for index := range payload.Data {
		tick := &payload.Data[index]
		raw := fmt.Sprintf("%010d", tick.Time)
		if len(raw) != 10 || raw[:6] != dateToken[2:] {
			continue
		}
		tickAt, parseErr := time.ParseInLocation(
			"2006-01-02 15:04", tradeDate+" "+raw[6:8]+":"+raw[8:10], location,
		)
		if parseErr != nil || tickAt.After(cutoff) {
			continue
		}
		if tick.MainMoneyInflow != tick.DarkMoney+tick.RegularMoney {
			continue
		}
		if latest == nil || tickAt.After(latestAt) {
			latest, latestAt = tick, tickAt
		}
	}
	if latest == nil {
		return graymarket.MoneyPoint{}, graymarket.ErrNoData
	}
	return graymarket.MoneyPoint{
		TradeDate: tradeDate, SnapshotAt: latestAt, RankType: record.RankType,
		Market: record.Market, Code: record.Code, Name: record.Name,
		DarkMoney: latest.DarkMoney, RegularMoney: latest.RegularMoney,
		MainMoneyInflow: latest.MainMoneyInflow, SourceTime: latest.Time,
		FetchedAt: time.Now().UTC(),
	}, nil
}

func latestMoneyCutoff(at time.Time) (time.Time, bool) {
	if at.IsZero() {
		return time.Time{}, false
	}
	location := snapshotLocation(at)
	local := at.In(location)
	minutes := local.Hour()*60 + local.Minute()
	var clock string
	switch {
	case minutes < 9*60+35:
		return time.Time{}, false
	case minutes <= 11*60+30:
		clock = fmt.Sprintf("%02d:%02d", local.Hour(), local.Minute()/5*5)
	case minutes < 13*60+5:
		clock = "11:30"
	case minutes <= 15*60:
		clock = fmt.Sprintf("%02d:%02d", local.Hour(), local.Minute()/5*5)
	default:
		clock = "15:00"
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", local.Format("2006-01-02")+" "+clock, location)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
