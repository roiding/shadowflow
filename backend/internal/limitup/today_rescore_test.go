package limitup

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestRescoreTodayCandidatesMatchesFullAnalysis(t *testing.T) {
	snapshotAt := time.Date(2026, 11, 6, 10, 42, 0, 0, Shanghai())
	snapshot := IntradaySnapshot{
		TradeDate: "2026-11-06", Source: SourceTHS, Scope: ScopeDefault,
		FetchedAt: snapshotAt, TradeStatus: "open", SealedCount: 1,
		Events: []Event{{
			TradeDate: "2026-11-06", Code: "000001", Name: "甲",
			MarketID: 33, MarketType: "HS", Pool: PoolSealed,
		}},
	}
	history := Dataset{
		Days: []Day{
			{TradeDate: "2026-11-03", FetchedAt: time.Date(2026, 11, 3, 16, 0, 0, 0, Shanghai()), SealedCount: 1},
			{TradeDate: "2026-11-04", FetchedAt: time.Date(2026, 11, 4, 16, 0, 0, 0, Shanghai()), SealedCount: 2},
			{TradeDate: "2026-11-05", FetchedAt: time.Date(2026, 11, 5, 16, 0, 0, 0, Shanghai()), SealedCount: 2},
		},
		Events: []Event{
			{TradeDate: "2026-11-03", Code: "000099", Name: "庚", MarketID: 33, MarketType: "HS", Pool: PoolSealed},
			{TradeDate: "2026-11-04", Code: "000001", Name: "甲", MarketID: 33, MarketType: "HS", Pool: PoolSealed},
			{TradeDate: "2026-11-04", Code: "000002", Name: "乙", MarketID: 33, MarketType: "HS", Pool: PoolSealed},
			{TradeDate: "2026-11-05", Code: "000001", Name: "甲", MarketID: 33, MarketType: "HS", Pool: PoolSealed},
			{TradeDate: "2026-11-05", Code: "000003", Name: "丙", MarketID: 33, MarketType: "HS", Pool: PoolSealed},
		},
	}
	input := TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-06",
		LookbackDays: 365, MinOverlap: 1, TopCores: 5, CandidateLimit: 1,
		IncludeAllCandidates: true,
	}
	static, err := AnalyzeToday(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(static.Candidates) != 2 {
		t.Fatalf("expected two static candidates, got %+v", static.Candidates)
	}
	quotes := map[string]LiveQuote{
		"000002": {
			Code: "000002", Available: true, PreviousClose: 10, HighPrice: 10.4,
			ChangePct: 0.04, TurnoverRate: 0.03, VolumeRatio: floatPointer(1.2),
			Turnover: 100_000_000, FetchedAt: snapshotAt, ListingDate: "20200101",
		},
		"000003": {
			Code: "000003", Available: true, PreviousClose: 10, HighPrice: 10.5,
			ChangePct: 0.05, TurnoverRate: 0.04, VolumeRatio: floatPointer(1.4),
			Turnover: 100_000_000, FetchedAt: snapshotAt, ListingDate: "20200101",
		},
	}
	histories := map[string]CandidateHistoryMetrics{
		"000002": {TurnoverRates: []float64{1, 2, 3, 4, 5}, MainFlowPcts: []float64{1, 2, 3, 4, 5}, DarkFlowPcts: []float64{1, 2, 3, 4, 5}},
		"000003": {TurnoverRates: []float64{1, 2, 3, 4, 5}, MainFlowPcts: []float64{1, 2, 3, 4, 5}, DarkFlowPcts: []float64{1, 2, 3, 4, 5}},
	}
	dark := map[string]LiveDarkMoney{
		"000002": {Code: "000002", Available: true, DarkMoney: 5_000_000},
		"000003": {Code: "000003", Available: true, DarkMoney: 10_000_000},
	}

	rescored := RescoreTodayCandidates(static, quotes, histories, dark, nil, 1, false)
	directInput := input
	directInput.IncludeAllCandidates = false
	directInput.LiveQuotes = quotes
	directInput.CandidateHistory = histories
	directInput.LiveDarkMoney = dark
	direct, err := AnalyzeToday(context.Background(), directInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(rescored.Candidates) != 1 || len(direct.Candidates) != 1 {
		t.Fatalf("unexpected candidate counts: rescored=%+v direct=%+v", rescored.Candidates, direct.Candidates)
	}
	if rescored.Candidates[0].Code != direct.Candidates[0].Code ||
		math.Abs(rescored.Candidates[0].Score-direct.Candidates[0].Score) > 1e-9 ||
		rescored.Candidates[0].LiveScore != direct.Candidates[0].LiveScore {
		t.Fatalf("rescored candidate differs from full analysis: rescored=%+v direct=%+v",
			rescored.Candidates[0], direct.Candidates[0])
	}
}

func TestRescoreTodayCandidatesKeepsFullUniverseHistoryNormalisation(t *testing.T) {
	base := TodayNetwork{Candidates: []TodayCandidate{
		{Code: "000001", MainlineScore: 0.9, HistoryScore: 1, Coverage: 0.8},
		{Code: "000002", MainlineScore: 0.5, HistoryScore: 0.5, Coverage: 0.4},
		{Code: "000003", MainlineScore: 0.2, HistoryScore: 0.25, Coverage: 0.1},
	}}
	filter := map[string]struct{}{"000002": {}, "000003": {}}
	rescored := RescoreTodayCandidates(base, nil, nil, nil, filter, 2, true)
	expected := map[string]float64{
		"000002": 0.5,
		"000003": 0.25,
	}
	if len(rescored.Candidates) != len(expected) {
		t.Fatalf("unexpected rescored candidates: %+v", rescored.Candidates)
	}
	for _, candidate := range rescored.Candidates {
		if math.Abs(candidate.HistoryScore-expected[candidate.Code]) > 1e-9 {
			t.Fatalf("history score was renormalised on a filtered subset: %+v", candidate)
		}
	}
}
