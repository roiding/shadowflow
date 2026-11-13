package marketdata

import (
	"context"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type StockQuoteProvider interface {
	FetchStockQuotes(context.Context, []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error)
}

// QuoteService is the only dependency upper layers need for constituent
// quotes. Provider choice, batching and cooldown stay inside this package.
type QuoteService struct {
	router *Router[StockQuoteProvider]
}

func NewQuoteService(router *Router[StockQuoteProvider]) *QuoteService {
	return &QuoteService{router: router}
}

func (s *QuoteService) FetchStockQuotes(ctx context.Context, relations []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	var result []graymarket.StockQuote
	err := s.router.Do(ctx, func(ctx context.Context, provider StockQuoteProvider) error {
		quotes, err := provider.FetchStockQuotes(ctx, relations)
		if err != nil {
			return err
		}
		result = quotes
		return nil
	})
	return result, err
}
