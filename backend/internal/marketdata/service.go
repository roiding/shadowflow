package marketdata

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type EastmoneyProvider interface {
	StockQuoteProvider
	StockKlineProvider
	BoardQuoteProvider
	StockKlineRangeProvider
	FetchLatestMoney5m(context.Context, string, []graymarket.RankRecord, time.Time) ([]graymarket.MoneyPoint, error)
	PrepareUpstreamRequests(context.Context, string, int, int) error
}

type Options struct {
	Cooldown time.Duration
	Logger   *slog.Logger
}

// EastmoneyPreparer is implemented by the Eastmoney client. Preparation is a
// proxy concern, not a supplier-neutral API: keep it explicit so a future
// supplier with a different transport cannot silently inherit Eastmoney's
// proxy assumptions.
type EastmoneyPreparer interface {
	PrepareUpstreamRequests(context.Context, string, int, int) error
}

// ProviderSet is the single boundary through which the application obtains
// market data. Adding a supplier means adding a provider here, not adding a
// fallback branch in API handlers, collectors or caches.
type ProviderSet struct {
	Quotes           *QuoteService
	DailyCloseQuotes *QuoteService
	Klines           *StockKlineService
	BoardQuotes      *BoardQuoteService
	KlineRanges      *StockKlineRangeService
	DarkMoney        LiveDarkMoneyProvider
	preparer         EastmoneyPreparer
}

// LiveDarkMoneyProvider stays on the Eastmoney contract because THS has not
// been shown to expose an equivalent dark-money measure. It must never be
// substituted with regular fund-flow fields.
type LiveDarkMoneyProvider interface {
	FetchLatestMoney5m(context.Context, string, []graymarket.RankRecord, time.Time) ([]graymarket.MoneyPoint, error)
}

func NewProviderSet(primary, backup interface{}, options Options) (*ProviderSet, error) {
	eastmoney, ok := primary.(EastmoneyProvider)
	if !ok {
		return nil, fmt.Errorf("primary market data provider is incomplete")
	}
	ths, ok := backup.(StockQuoteProvider)
	if !ok {
		return nil, fmt.Errorf("backup quote provider is unavailable")
	}
	thsKline, ok := backup.(StockKlineProvider)
	if !ok {
		return nil, fmt.Errorf("backup kline provider is unavailable")
	}
	quoteRouter, err := NewRouter("quotes", []NamedProvider[StockQuoteProvider]{
		{Name: "eastmoney", Source: eastmoney},
		{Name: "ths", Source: ths},
	}, RouterOptions{Cooldown: options.Cooldown, Logger: options.Logger})
	if err != nil {
		return nil, err
	}
	klineRouter, err := NewRouter("stock-kline-5m", []NamedProvider[StockKlineProvider]{
		{Name: "eastmoney", Source: eastmoney},
		{Name: "ths", Source: thsKline},
	}, RouterOptions{Cooldown: options.Cooldown, Logger: options.Logger})
	if err != nil {
		return nil, err
	}
	// End-of-day archives require a provider-supplied quote date so stale or
	// post-close snapshots cannot silently masquerade as today's close. THS
	// snapshots currently expose no verified quote-time field, so archive
	// enrichment remains on Eastmoney rather than accepting undated data.
	dailyCloseRouter, err := NewRouter("daily-close-quotes", []NamedProvider[StockQuoteProvider]{
		{Name: "eastmoney", Source: eastmoney},
	}, RouterOptions{Cooldown: options.Cooldown, Logger: options.Logger})
	if err != nil {
		return nil, err
	}
	boardRouter, err := NewRouter("board-quotes", []NamedProvider[BoardQuoteProvider]{
		{Name: "eastmoney", Source: eastmoney},
	}, RouterOptions{Cooldown: options.Cooldown, Logger: options.Logger})
	if err != nil {
		return nil, err
	}
	return &ProviderSet{
		Quotes:           NewQuoteService(quoteRouter),
		DailyCloseQuotes: NewQuoteService(dailyCloseRouter),
		Klines:           NewStockKlineService(klineRouter),
		BoardQuotes:      NewBoardQuoteService(boardRouter),
		KlineRanges:      NewStockKlineRangeService(eastmoney),
		DarkMoney:        eastmoney,
		preparer:         eastmoney,
	}, nil
}

// PrepareUpstreamRequests only reserves resources for Eastmoney when it is
// currently healthy. A cooling primary is skipped so the task goes straight to
// its configured backup instead of burning the proxy preparation budget.
func (s *ProviderSet) PrepareUpstreamRequests(ctx context.Context, purpose string, requests, concurrency int) error {
	if s == nil || s.preparer == nil || !s.Klines.primaryHealthy() {
		return nil
	}
	return s.preparer.PrepareUpstreamRequests(ctx, purpose, requests, concurrency)
}
