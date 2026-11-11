package quote

import (
	"context"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/limitup"
)

const limitUpQuoteTimeout = 8 * time.Second

// LimitUpLiveQuoteSource is the read-only EastMoney adapter for today's
// spillover candidates. It requests only the supplied candidate codes and
// never persists the response.
type LimitUpLiveQuoteSource struct {
	source Source
}

func NewLimitUpLiveQuoteSource(source Source) *LimitUpLiveQuoteSource {
	return &LimitUpLiveQuoteSource{source: source}
}

func (source *LimitUpLiveQuoteSource) ReadLiveQuotes(ctx context.Context, codes []string) (map[string]limitup.LiveQuote, error) {
	if source == nil || source.source == nil {
		return nil, fmt.Errorf("limit-up live quote source is unavailable")
	}
	relations := make([]graymarket.StockBoardRelation, 0, len(codes))
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if len(code) != 6 {
			continue
		}
		if _, duplicate := seen[code]; duplicate {
			continue
		}
		valid := true
		for _, char := range code {
			if char < '0' || char > '9' {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		seen[code] = struct{}{}
		relations = append(relations, graymarket.StockBoardRelation{
			StockCode: code, StockMarket: stockMarketForCode(code),
		})
	}
	if len(relations) == 0 {
		return map[string]limitup.LiveQuote{}, nil
	}
	requestContext, cancel := context.WithTimeout(ctx, limitUpQuoteTimeout)
	defer cancel()
	quotes, err := source.source.FetchStockQuotes(requestContext, relations)
	if err != nil {
		return nil, err
	}
	result := make(map[string]limitup.LiveQuote, len(quotes))
	for _, quote := range quotes {
		live := limitup.LiveQuote{
			Code: quote.StockCode, Available: quote.Available,
			LatestPrice: quote.LatestPrice, PreviousClose: quote.PreviousClose,
			HighPrice: quote.HighPrice, ChangePct: quote.ChangePct,
			TurnoverRate: quote.TurnoverRate, Turnover: quote.Turnover,
			QuoteTime: quote.QuoteTime, FetchedAt: quote.FetchedAt,
			ListingDate: quote.ListingDate,
		}
		if quote.VolumeRatioAvailable {
			value := quote.VolumeRatio
			live.VolumeRatio = &value
		}
		if quote.FundFlowAvailable {
			inflow, percent := quote.MainNetInflow, quote.MainNetInflowPct
			live.MainNetInflow = &inflow
			live.MainFlowPct = &percent
		}
		result[live.Code] = live
	}
	return result, nil
}

func stockMarketForCode(code string) int64 {
	if len(code) == 6 && code[0] == '6' {
		return 1
	}
	return 0
}
