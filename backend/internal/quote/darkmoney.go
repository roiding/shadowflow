package quote

import (
	"context"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/limitup"
)

type LiveDarkMoneySource interface {
	FetchLatestMoney5m(context.Context, string, []graymarket.RankRecord, time.Time) ([]graymarket.MoneyPoint, error)
}

type LimitUpLiveDarkMoneySource struct {
	source LiveDarkMoneySource
}

func NewLimitUpLiveDarkMoneySource(source LiveDarkMoneySource) *LimitUpLiveDarkMoneySource {
	return &LimitUpLiveDarkMoneySource{source: source}
}

func (source *LimitUpLiveDarkMoneySource) ReadLiveDarkMoney(
	ctx context.Context,
	tradeDate string,
	codes []string,
	at time.Time,
) (map[string]limitup.LiveDarkMoney, error) {
	if source == nil || source.source == nil {
		return nil, fmt.Errorf("limit-up live dark-money source is unavailable")
	}
	records := make([]graymarket.RankRecord, 0, len(codes))
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if len(code) != 6 {
			continue
		}
		if _, duplicate := seen[code]; duplicate {
			continue
		}
		seen[code] = struct{}{}
		records = append(records, graymarket.RankRecord{
			TradeDate: tradeDate, RankType: graymarket.RankStock,
			Market: stockMarketForCode(code), Code: code,
		})
	}
	if len(records) == 0 {
		return map[string]limitup.LiveDarkMoney{}, nil
	}
	requestContext, cancel := context.WithTimeout(ctx, limitUpQuoteTimeout)
	defer cancel()
	points, err := source.source.FetchLatestMoney5m(requestContext, tradeDate, records, at)
	result := make(map[string]limitup.LiveDarkMoney, len(points))
	for _, point := range points {
		result[point.Code] = limitup.LiveDarkMoney{
			Code: point.Code, Available: true, DarkMoney: float64(point.DarkMoney),
			SnapshotAt: point.SnapshotAt, FetchedAt: point.FetchedAt,
		}
	}
	return result, err
}
