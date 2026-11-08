package eastmoney

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

// FetchBoardDailyKlinesRange fetches one board's DAILY kline bars over an
// inclusive date range in a single request and emits them grouped per trade
// date. This is the operator-run backfill path for board daily-close identity
// snapshots: EM serves board daily history (klt=101, secid 90.BKxxxx), so a
// past date's board close can be reconstructed once per board for the whole
// span.
func (c *Client) FetchBoardDailyKlinesRange(ctx context.Context, from, to time.Time,
	board graymarket.Board, emit func(tradeDate string, bar BoardDailyBar)) error {
	params := url.Values{
		"secid": {fmt.Sprintf("90.%s", board.Code)},
		"klt":   {"101"}, "fqt": {"0"},
		"beg": {from.Format("20060102")}, "end": {to.Format("20060102")},
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
	if payload.ReturnCode != 0 || payload.Data == nil {
		return fmt.Errorf("%w: board kline response has no data", graymarket.ErrNoData)
	}
	location := time.FixedZone("Asia/Shanghai", 8*3600)
	for _, raw := range payload.Data.Klines {
		fields, err := csv.NewReader(strings.NewReader(raw)).Read()
		if err != nil || len(fields) != 11 {
			return fmt.Errorf("invalid board kline row %q", raw)
		}
		at, err := time.ParseInLocation("2006-01-02 15:04", fields[0], location)
		if err != nil {
			return fmt.Errorf("invalid board kline date %q", fields[0])
		}
		tradeDate := at.Format("2006-01-02")
		numbers, err := klineNumbers(fields, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
		if err != nil {
			return err
		}
		emit(tradeDate, BoardDailyBar{
			TradeDate: tradeDate, Open: numbers[1], Close: numbers[2], High: numbers[3], Low: numbers[4],
			Volume: numbers[5], Amount: numbers[6], Amplitude: numbers[7] / 100,
			ChangePct: numbers[8] / 100, ChangeValue: numbers[9], TurnoverRate: numbers[10] / 100,
		})
	}
	return nil
}

// BoardDailyBar is one board daily bar from the historical kline endpoint.
type BoardDailyBar struct {
	TradeDate    string
	Open         float64
	Close        float64
	High         float64
	Low          float64
	Volume       float64
	Amount       float64
	Amplitude    float64
	ChangePct    float64
	ChangeValue  float64
	TurnoverRate float64
}
