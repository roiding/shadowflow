package quote

import (
	"context"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type liveQuoteTestSource struct {
	relations []graymarket.StockBoardRelation
	quotes    []graymarket.StockQuote
}

func (source *liveQuoteTestSource) FetchStockQuotes(_ context.Context, relations []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	source.relations = append([]graymarket.StockBoardRelation(nil), relations...)
	return source.quotes, nil
}

func TestLimitUpLiveQuoteSourceRequestsOnlyDeduplicatedCandidates(t *testing.T) {
	fetchedAt := time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)
	upstream := &liveQuoteTestSource{quotes: []graymarket.StockQuote{{
		StockCode: "600001", StockMarket: 1, Available: true, LatestPrice: 9.8,
		PreviousClose: 10, HighPrice: 10.5, ChangePct: -0.02, TurnoverRate: 0.031,
		Turnover: 123_000_000, VolumeRatio: 1.4, VolumeRatioAvailable: true,
		MainNetInflow: 12_000_000, MainNetInflowPct: 3.2, FundFlowAvailable: true,
		ListingDate: "20200101", QuoteTime: "14:30:00", FetchedAt: fetchedAt,
	}}}
	source := NewLimitUpLiveQuoteSource(upstream)
	result, err := source.ReadLiveQuotes(context.Background(), []string{"600001", "600001", "bad", "000001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.relations) != 2 || upstream.relations[0].StockCode != "600001" ||
		upstream.relations[0].StockMarket != 1 || upstream.relations[1].StockCode != "000001" ||
		upstream.relations[1].StockMarket != 0 {
		t.Fatalf("unexpected quote relations: %+v", upstream.relations)
	}
	quote := result["600001"]
	if !quote.Available || quote.PreviousClose != 10 || quote.VolumeRatio == nil ||
		*quote.VolumeRatio != 1.4 || quote.MainNetInflow == nil || *quote.MainNetInflow != 12_000_000 ||
		quote.MainFlowPct == nil || *quote.MainFlowPct != 3.2 || quote.ListingDate != "20200101" {
		t.Fatalf("unexpected live quote projection: %+v", quote)
	}
}
