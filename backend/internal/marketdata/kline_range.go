package marketdata

import (
	"context"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type StockKlineRangeProvider interface {
	FetchStockKlines5mRange(context.Context, time.Time, time.Time, graymarket.RankRecord, int,
		func(string, []graymarket.StockKlinePoint)) error
}

// StockKlineRangeService covers the operator-run long-range backfill. THS has
// only been verified to return the latest 400 five-minute bars, so it cannot
// satisfy a multi-month single-request range. Keep that limitation explicit:
// this capability is Eastmoney-only until a backup proves the same contract.
type StockKlineRangeService struct {
	provider StockKlineRangeProvider
}

func NewStockKlineRangeService(provider StockKlineRangeProvider) *StockKlineRangeService {
	return &StockKlineRangeService{provider: provider}
}

func (s *StockKlineRangeService) FetchStockKlines5mRange(ctx context.Context, from, to time.Time,
	stock graymarket.RankRecord, maxBars int, emit func(string, []graymarket.StockKlinePoint)) error {
	if s == nil || s.provider == nil {
		return fmt.Errorf("stock kline range provider is unavailable")
	}
	return s.provider.FetchStockKlines5mRange(ctx, from, to, stock, maxBars, emit)
}
