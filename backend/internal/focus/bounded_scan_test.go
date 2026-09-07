package focus

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func TestScanOnlyMaterializesReturnedCandidates(t *testing.T) {
	source := &fakeSource{records: map[string][]graymarket.RankRecord{}}
	for day := 1; day <= 5; day++ {
		date := fmt.Sprintf("2026-09-%02d", day)
		source.dates = append(source.dates, date)
		for stock := 1; stock <= 600; stock++ {
			source.records[date] = append(source.records[date], graymarket.RankRecord{TradeDate: date, Code: fmt.Sprintf("%06d", stock), Name: "stock", RankType: graymarket.RankStock, QuoteAvailable: true, ClosePrice: 10})
		}
	}
	request := ScanRequest{AsOf: source.dates[4], ConsecutiveDays: 5, ConceptMatch: MatchAll, StockMatch: MatchAll}
	for range 20 {
		request.StockConditions = append(request.StockConditions, Condition{Field: FieldClosePrice, Operator: OperatorGT, Value: 0})
	}
	result, err := New(source).ScanWith(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stocks) != maxCandidates || cap(result.Stocks) != maxCandidates || !result.StocksTruncated || result.Stats.StocksQualified != 600 {
		t.Fatalf("unexpected bounded results: len=%d cap=%d stats=%v", len(result.Stocks), cap(result.Stocks), result.Stats)
	}
	for _, candidate := range result.Stocks {
		if len(candidate.Days) != 5 || len(candidate.Evaluations) != 5 {
			t.Fatalf("returned candidate lost its details: %v", candidate.Code)
		}
		for _, evaluation := range candidate.Evaluations {
			if !evaluation.Matched || len(evaluation.Conditions) != 20 || cap(evaluation.Conditions) != 20 {
				t.Fatalf("unexpected explanation allocation: %v", evaluation)
			}
		}
	}
	if allocations := testing.AllocsPerRun(10, func() { conditionsMatch(DailyMetric{ClosePrice: 10}, request.StockConditions, MatchAll) }); allocations != 0 {
		t.Fatalf("qualification allocates explanation data: %v", allocations)
	}
}

func TestTruncatedConceptDisplayDoesNotRestrictStockMembership(t *testing.T) {
	date := "2026-09-04"
	source := &fakeSource{dates: []string{date}, records: map[string][]graymarket.RankRecord{}, relations: map[string][]graymarket.StockBoardRelation{
		"BK0600": {{StockCode: "600001", StockMarket: 1, StockName: "stock", BoardCode: "BK0600"}},
	}}
	for i := 1; i <= 600; i++ {
		source.records[date] = append(source.records[date], graymarket.RankRecord{TradeDate: date, Code: fmt.Sprintf("BK%04d", i), Name: "concept", RankType: graymarket.RankConcept, QuoteAvailable: true})
	}
	source.records[date] = append(source.records[date], graymarket.RankRecord{TradeDate: date, Code: "600001", Name: "stock", RankType: graymarket.RankStock, Market: 1, QuoteAvailable: true})
	result, err := New(source).ScanWith(context.Background(), ScanRequest{AsOf: date, ConsecutiveDays: 1, ConceptMatch: MatchAll, StockMatch: MatchAll, StockScope: StockScope{RequireQualifiedConcepts: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConceptsTruncated || len(result.Concepts) != 500 || len(result.Stocks) != 1 || result.Stocks[0].Concepts[0].Code != "BK0600" {
		t.Fatalf("display cap changed qualification universe: %v", result.Stats)
	}
}

func TestScanHonorsCancellationBeforeSourceWork(t *testing.T) {
	source := &fakeSource{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(source).Scan(ctx, "2026-09-04"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan error=%v", err)
	}
	if source.requestedAsOf != "" {
		t.Fatal("cancelled scan reached source")
	}
	if _, _, err := explainCandidateDays(ctx, []string{"2026-09-04"}, nil, "code", nil, MatchAll); !errors.Is(err, context.Canceled) {
		t.Fatalf("explanation ignored cancellation: %v", err)
	}
}

func TestSTScopeUsesDailyIdentityInsteadOfRelationshipName(t *testing.T) {
	for _, tc := range []struct {
		name, daily, relation string
		want                  int
	}{
		{"new ST", "*ST Current", "Former", 0},
		{"ST removed", "Current", "*ST Former", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			date := "2026-09-04"
			source := &fakeSource{dates: []string{date}, records: map[string][]graymarket.RankRecord{date: {
				{TradeDate: date, Code: "BK001", Name: "concept", RankType: graymarket.RankConcept, QuoteAvailable: true},
				{TradeDate: date, Code: "600001", Market: 1, Name: tc.daily, RankType: graymarket.RankStock, QuoteAvailable: true},
			}}, relations: map[string][]graymarket.StockBoardRelation{"BK001": {{StockCode: "600001", StockMarket: 1, StockName: tc.relation, BoardCode: "BK001"}}}}
			result, err := New(source).ScanWith(context.Background(), ScanRequest{AsOf: date, ConsecutiveDays: 1, ConceptMatch: MatchAll, StockMatch: MatchAll, StockScope: StockScope{MainBoardOnly: true, ExcludeST: true, RequireQualifiedConcepts: true}})
			if err != nil || len(result.Stocks) != tc.want {
				t.Fatalf("ST scope used stale identity: stocks=%v err=%v", result.Stocks, err)
			}
			if tc.want > 0 && result.Stocks[0].Name != tc.daily {
				t.Fatalf("result shows stale identity: %s", result.Stocks[0].Name)
			}
		})
	}
}
