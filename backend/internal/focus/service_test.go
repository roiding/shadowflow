package focus

import (
	"context"
	"errors"
	"testing"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type fakeSource struct {
	dates          []string
	records        map[string][]graymarket.RankRecord
	relations      map[string][]graymarket.StockBoardRelation
	batchCalls     int
	requestedAsOf  string
	requestedLimit int
}

func (f *fakeSource) DailyCloseTradeDates(_ context.Context, asOf string, limit int) ([]string, error) {
	f.requestedAsOf = asOf
	f.requestedLimit = limit
	return append([]string(nil), f.dates...), nil
}

func (f *fakeSource) DailyCloseRecords(_ context.Context, date string) ([]graymarket.RankRecord, error) {
	return f.records[date], nil
}

func (f *fakeSource) BoardStockRelations(_ context.Context, _ graymarket.BoardType, code, _ string) ([]graymarket.StockBoardRelation, error) {
	return f.relations[code], nil
}

func (f *fakeSource) BoardStockRelationsBatch(_ context.Context, _ graymarket.BoardType, codes []string, _ string) ([]graymarket.StockBoardRelation, error) {
	f.batchCalls++
	var result []graymarket.StockBoardRelation
	for _, code := range codes {
		for _, relation := range f.relations[code] {
			relation.BoardCode = code
			result = append(result, relation)
		}
	}
	return result, nil
}

func TestScanUsesThreeCompleteDailyClosesAndMainBoardNonSTRelations(t *testing.T) {
	dates := []string{"2026-08-12", "2026-08-13", "2026-08-14"}
	source := &fakeSource{dates: dates, records: make(map[string][]graymarket.RankRecord), relations: map[string][]graymarket.StockBoardRelation{
		"BK001": {
			{StockMarket: 1, StockCode: "600001", StockName: "主板一"},
			{StockMarket: 0, StockCode: "300001", StockName: "创业板"},
			{StockMarket: 1, StockCode: "600002", StockName: "*ST风险"},
		},
	}}
	for _, date := range dates {
		source.records[date] = []graymarket.RankRecord{
			focusRecord(date, graymarket.RankConcept, 90, "BK001", "目标概念", 60_000_000_000, 0.04, 0.03, 1_200_000_000),
			focusRecord(date, graymarket.RankConcept, 90, "BK002", "成交额边界", 50_000_000_000, 0.04, 0.03, 1_000_000_000),
			focusRecord(date, graymarket.RankStock, 1, "600001", "主板一", 300_000_000, 0.04, 0.03, 6_000_000),
			focusRecord(date, graymarket.RankStock, 0, "300001", "创业板", 300_000_000, 0.04, 0.03, 6_000_000),
			focusRecord(date, graymarket.RankStock, 1, "600002", "*ST风险", 300_000_000, 0.04, 0.03, 6_000_000),
		}
	}
	result, err := New(source).Scan(context.Background(), "2026-08-14")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.AsOf != "2026-08-14" || len(result.TradeDates) != 3 {
		t.Fatalf("unexpected readiness: %+v", result)
	}
	if len(result.Concepts) != 1 || result.Concepts[0].Code != "BK001" || len(result.Concepts[0].Days) != 3 {
		t.Fatalf("unexpected concepts: %+v", result.Concepts)
	}
	if len(result.Stocks) != 1 || result.Stocks[0].Code != "600001" || len(result.Stocks[0].Concepts) != 1 {
		t.Fatalf("unexpected stocks: %+v", result.Stocks)
	}
	if got := result.Stocks[0].Days[0].ControlCoefficient; got != 2 {
		t.Fatalf("control coefficient=%v, want 2", got)
	}
	if result.Stats.NonMainBoardExcluded != 1 || result.Stats.STExcluded != 1 {
		t.Fatalf("unexpected exclusions: %+v", result.Stats)
	}
	if source.batchCalls != 1 {
		t.Fatalf("qualified concept relations used %d batch calls, want 1", source.batchCalls)
	}
	if len(result.Concepts[0].Evaluations) != 3 || !result.Concepts[0].Evaluations[0].Matched ||
		len(result.Rejections) < 3 {
		t.Fatalf("condition explanations are missing: %+v", result)
	}
	var boundaryFailure *CandidateRejection
	for index := range result.Rejections {
		if result.Rejections[index].Code == "BK002" {
			boundaryFailure = &result.Rejections[index]
			break
		}
	}
	if boundaryFailure == nil || boundaryFailure.FailedDate != dates[0] ||
		boundaryFailure.Evaluation == nil || len(boundaryFailure.Evaluation.Conditions) == 0 ||
		boundaryFailure.Evaluation.Conditions[0].ActualValue != 50_000_000_000 ||
		boundaryFailure.Evaluation.Conditions[0].Passed {
		t.Fatalf("boundary failure explanation is incorrect: %+v", boundaryFailure)
	}
}

func TestScanReportsNotReadyWithoutThreeAccumulatedDays(t *testing.T) {
	result, err := New(&fakeSource{dates: []string{"2026-08-13", "2026-08-14"}}).Scan(context.Background(), "2026-08-14")
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.RequiredDays != 3 || len(result.TradeDates) != 2 || len(result.Concepts) != 0 || len(result.Stocks) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestScanWithSupportsDynamicDaysConditionsAndStockUniverse(t *testing.T) {
	dates := []string{"2026-08-13", "2026-08-14"}
	source := &fakeSource{dates: dates, records: make(map[string][]graymarket.RankRecord)}
	for _, date := range dates {
		source.records[date] = []graymarket.RankRecord{
			focusRecord(date, graymarket.RankConcept, 90, "BK001", "概念一", 10_000_000_000, 0.01, -0.01, 100_000_000),
			focusRecord(date, graymarket.RankStock, 0, "300001", "创业板一", 300_000_000, 0.02, 0.04, 6_000_000),
		}
	}
	changeMax := 0.05
	request := ScanRequest{
		AsOf: "2026-08-14", ConsecutiveDays: 2,
		ConceptMatch: MatchAny, ConceptConditions: []Condition{
			{Field: FieldTurnover, Operator: OperatorGT, Value: 50_000_000_000},
			{Field: FieldChangePct, Operator: OperatorLT, Value: 0},
		},
		StockMatch: MatchAll, StockConditions: []Condition{
			{Field: FieldChangePct, Operator: OperatorBetween, Value: 0.03, MaxValue: &changeMax},
			{Field: FieldControlCoefficient, Operator: OperatorGTE, Value: 2},
		},
		StockScope: StockScope{MainBoardOnly: false, ExcludeST: false, RequireQualifiedConcepts: false},
	}
	result, err := New(source).ScanWith(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.RequiredDays != 2 || len(result.Concepts) != 1 || len(result.Stocks) != 1 {
		t.Fatalf("unexpected dynamic result: %+v", result)
	}
	if result.Stocks[0].Code != "300001" || len(result.Stocks[0].Concepts) != 0 {
		t.Fatalf("unexpected unrestricted stock result: %+v", result.Stocks)
	}
	if source.requestedAsOf != request.AsOf || source.requestedLimit != request.ConsecutiveDays {
		t.Fatalf("daily close request=(%q, %d), want=(%q, %d)", source.requestedAsOf, source.requestedLimit, request.AsOf, request.ConsecutiveDays)
	}
}

func TestScanWithEvaluatesAllConditionsWithinEachDay(t *testing.T) {
	dates := []string{"2026-08-12", "2026-08-13", "2026-08-14"}
	source := &fakeSource{dates: dates, records: map[string][]graymarket.RankRecord{
		"2026-08-12": {
			focusRecord("2026-08-12", graymarket.RankConcept, 90, "BK001", "跨日拼接", 100, 0.01, -0.01, 0),
			focusRecord("2026-08-12", graymarket.RankConcept, 90, "BK002", "逐日通过", 100, 0.01, -0.01, 0),
		},
		"2026-08-13": {
			focusRecord("2026-08-13", graymarket.RankConcept, 90, "BK001", "跨日拼接", 10, 0.01, -0.01, 0),
			focusRecord("2026-08-13", graymarket.RankConcept, 90, "BK002", "逐日通过", 100, 0.01, -0.01, 0),
		},
		"2026-08-14": {
			focusRecord("2026-08-14", graymarket.RankConcept, 90, "BK001", "跨日拼接", 100, 0.01, 0.01, 0),
			focusRecord("2026-08-14", graymarket.RankConcept, 90, "BK002", "逐日通过", 100, 0.01, -0.01, 0),
		},
	}}
	request := ScanRequest{
		AsOf: "2026-08-14", ConsecutiveDays: 2,
		ConceptMatch: MatchAll, ConceptConditions: []Condition{
			{Field: FieldTurnover, Operator: OperatorGT, Value: 50},
			{Field: FieldChangePct, Operator: OperatorLT, Value: 0},
		},
		StockMatch: MatchAll,
		StockScope: StockScope{MainBoardOnly: false, ExcludeST: false, RequireQualifiedConcepts: false},
	}

	result, err := New(source).ScanWith(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TradeDates) != 2 || result.TradeDates[0] != "2026-08-13" || result.TradeDates[1] != "2026-08-14" {
		t.Fatalf("trade dates=%v, want last 2 dates", result.TradeDates)
	}
	if len(result.Concepts) != 1 || result.Concepts[0].Code != "BK002" || len(result.Concepts[0].Days) != 2 {
		t.Fatalf("conditions were not evaluated per day: %+v", result.Concepts)
	}
	if source.requestedLimit != 2 {
		t.Fatalf("daily close limit=%d, want 2", source.requestedLimit)
	}
}

func TestScanWithRejectsInvalidDynamicRequest(t *testing.T) {
	max := 1.0
	tests := []ScanRequest{
		{AsOf: "2026-08-14", ConsecutiveDays: 0, ConceptMatch: MatchAll, StockMatch: MatchAll},
		{AsOf: "2026-08-14", ConsecutiveDays: 3, ConceptMatch: "some", StockMatch: MatchAll},
		{AsOf: "2026-08-14", ConsecutiveDays: 3, ConceptMatch: MatchAll, StockMatch: MatchAll, ConceptConditions: []Condition{{Field: "unknown", Operator: OperatorGT}}},
		{AsOf: "2026-08-14", ConsecutiveDays: 3, ConceptMatch: MatchAll, StockMatch: MatchAll, StockConditions: []Condition{{Field: FieldTurnover, Operator: OperatorBetween, Value: 2, MaxValue: &max}}},
	}
	for _, request := range tests {
		if _, err := New(&fakeSource{}).ScanWith(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("expected validation error for %+v, got %v", request, err)
		}
	}
}

func focusRecord(date string, rankType graymarket.RankType, market int64, code, name string, turnover int64, turnoverRate, changePct float64, mainMoney int64) graymarket.RankRecord {
	return graymarket.RankRecord{TradeDate: date, RankType: rankType, Market: market, Code: code, Name: name,
		Turnover: turnover, TurnoverRate: turnoverRate, ChangePct: changePct, RegularMoney: mainMoney / 2,
		DarkMoney: mainMoney / 2, QuoteAvailable: true}
}
