package marketdata

import (
	"context"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type BoardQuoteProvider interface {
	FetchBoardQuotes(context.Context, graymarket.RankType) ([]graymarket.BoardQuote, error)
}

// BoardQuoteService keeps board quote supplier selection inside this package.
// No second supplier has a verified board taxonomy/quote contract, so the
// router currently has a single Eastmoney provider rather than pretending a
// stock-quote backup can substitute for a board quote.
type BoardQuoteService struct {
	router *Router[BoardQuoteProvider]
}

func NewBoardQuoteService(router *Router[BoardQuoteProvider]) *BoardQuoteService {
	return &BoardQuoteService{router: router}
}

func (s *BoardQuoteService) FetchBoardQuotes(ctx context.Context, rankType graymarket.RankType) ([]graymarket.BoardQuote, error) {
	var result []graymarket.BoardQuote
	err := s.router.Do(ctx, func(ctx context.Context, provider BoardQuoteProvider) error {
		quotes, err := provider.FetchBoardQuotes(ctx, rankType)
		if err != nil {
			return err
		}
		result = quotes
		return nil
	})
	return result, err
}
