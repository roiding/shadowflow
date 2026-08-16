package eastmoney

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type darkTradeTickResponse struct {
	ErrorID   int             `json:"errid"`
	ErrorText string          `json:"errmsg"`
	Data      []darkTradeTick `json:"data"`
}

type darkTradeTick struct {
	Time            int64 `json:"time"`
	DarkMoney       int64 `json:"1"`
	RegularMoney    int64 `json:"2"`
	MainMoneyInflow int64 `json:"3"`
}

type boardTickResult struct {
	points []graymarket.MoneyPoint
	err    error
}

func (c *Client) FetchMoney5m(ctx context.Context, snapshot graymarket.RankSnapshot, includeClose bool) ([]graymarket.MoneyPoint, error) {
	if snapshot.TradeDate == "" || len(snapshot.Records) == 0 {
		return nil, graymarket.ErrNoData
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerCount := min(8, len(snapshot.Records))
	jobs := make(chan graymarket.RankRecord, workerCount)
	results := make(chan boardTickResult, workerCount)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for board := range jobs {
				points, err := c.fetchMoney5m(ctx, snapshot.TradeDate, board, includeClose)
				select {
				case results <- boardTickResult{points: points, err: err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, board := range snapshot.Records {
			select {
			case jobs <- board:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	pointsPerCode := 47
	if includeClose {
		pointsPerCode = 48
	}
	points := make([]graymarket.MoneyPoint, 0, len(snapshot.Records)*pointsPerCode)
	var firstErr error
	for result := range results {
		if result.err != nil && firstErr == nil {
			firstErr = result.err
			cancel()
		}
		points = append(points, result.points...)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if len(points) != len(snapshot.Records)*pointsPerCode {
		return nil, fmt.Errorf("incomplete %s money archive: expected %d points, got %d", snapshot.RankType, len(snapshot.Records)*pointsPerCode, len(points))
	}
	assignMoneyRanks(points)
	return points, nil
}

func (c *Client) fetchMoney5m(ctx context.Context, tradeDate string, board graymarket.RankRecord, includeClose bool) ([]graymarket.MoneyPoint, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		points, err := c.fetchMoney5mOnce(ctx, tradeDate, board, includeClose)
		if err == nil {
			return points, nil
		}
		lastErr = err
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
	}
	return nil, fmt.Errorf("fetch %s money curve: %w", board.Code, lastErr)
}

func (c *Client) fetchMoney5mOnce(ctx context.Context, tradeDate string, board graymarket.RankRecord, includeClose bool) ([]graymarket.MoneyPoint, error) {
	params := url.Values{
		"code": {board.Code}, "market": {strconv.FormatInt(board.Market, 10)},
		"time": {"0"}, "version": {"100"}, "cver": {"11.2.6"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.darkTradeTickBaseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "shadowflow/0.1")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	decoded, _, err := decodeJSON(body, response.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", graymarket.ErrDecode, err)
	}
	var payload darkTradeTickResponse
	if err := json.NewDecoder(bytes.NewReader(decoded)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: %v", graymarket.ErrDecode, err)
	}
	if payload.ErrorID != 0 {
		return nil, fmt.Errorf("upstream error %d: %s", payload.ErrorID, payload.ErrorText)
	}

	dateToken := strings.ReplaceAll(tradeDate, "-", "")
	if len(dateToken) != 8 {
		return nil, fmt.Errorf("invalid trade date %q", tradeDate)
	}
	fetchedAt := time.Now().UTC()
	expectedPoints := 47
	if includeClose {
		expectedPoints = 48
	}
	points := make([]graymarket.MoneyPoint, 0, expectedPoints)
	seen := make(map[string]struct{}, expectedPoints)
	for _, tick := range payload.Data {
		raw := fmt.Sprintf("%010d", tick.Time)
		if raw[:6] != dateToken[2:] {
			return nil, fmt.Errorf("curve date %s does not match %s", raw[:6], tradeDate)
		}
		clock := raw[6:8] + ":" + raw[8:10]
		if !isResearchClock(clock) && !(includeClose && clock == "15:00") {
			continue
		}
		if _, duplicate := seen[clock]; duplicate {
			return nil, fmt.Errorf("duplicate curve point %s", clock)
		}
		seen[clock] = struct{}{}
		at, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+clock, snapshotLocation(board.SnapshotAt))
		if err != nil {
			return nil, err
		}
		if tick.MainMoneyInflow != tick.DarkMoney+tick.RegularMoney {
			return nil, fmt.Errorf("inconsistent money fields at %s", clock)
		}
		points = append(points, graymarket.MoneyPoint{
			TradeDate: tradeDate, SnapshotAt: at, RankType: board.RankType, Market: board.Market,
			Code: board.Code, Name: board.Name, DarkMoney: tick.DarkMoney, RegularMoney: tick.RegularMoney,
			MainMoneyInflow: tick.MainMoneyInflow, SourceTime: tick.Time, FetchedAt: fetchedAt,
		})
	}
	if len(points) != expectedPoints {
		return nil, fmt.Errorf("expected %d research points, got %d", expectedPoints, len(points))
	}
	return points, nil
}

func isResearchClock(value string) bool {
	if len(value) != 5 || !((value >= "09:35" && value <= "11:30") || (value >= "13:05" && value <= "14:55")) {
		return false
	}
	minute, err := strconv.Atoi(value[3:])
	return err == nil && minute%5 == 0
}

func snapshotLocation(value time.Time) *time.Location {
	if value.Location() != nil {
		return value.Location()
	}
	return time.Local
}

func assignMoneyRanks(points []graymarket.MoneyPoint) {
	sort.Slice(points, func(i, j int) bool {
		if points[i].SnapshotAt.Equal(points[j].SnapshotAt) {
			if points[i].DarkMoney == points[j].DarkMoney {
				return points[i].Code < points[j].Code
			}
			return points[i].DarkMoney > points[j].DarkMoney
		}
		return points[i].SnapshotAt.Before(points[j].SnapshotAt)
	})
	var current time.Time
	var rank int64
	for index := range points {
		if !points[index].SnapshotAt.Equal(current) {
			current = points[index].SnapshotAt
			rank = 1
		} else {
			rank++
		}
		points[index].Rank = rank
	}
}
