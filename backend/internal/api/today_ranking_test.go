package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
)

func TestTodayRankingBoundsCannotSkipLiveOvertake(t *testing.T) {
	static := []limitup.TodayCandidate{
		{Code: "000001", MainlineScore: 0.6, HistoryScore: 0.7, Coverage: 0.5},
		{Code: "000002", MainlineScore: 0.5, HistoryScore: 0.4, Coverage: 0.4},
		{Code: "000003", MainlineScore: 0.2, HistoryScore: 0.1, Coverage: 0.1},
	}
	bounds := todayCandidateBounds(static)
	if bounds[0].code != "000001" || bounds[2].code != "000003" {
		t.Fatalf("unexpected static upper-bound order: %+v", bounds)
	}
	maxDarkBoost := limitup.TodayCandidateDarkMoneyGlobalUpperBound()
	if todayRankingProvenNoDark(
		[]limitup.TodayCandidate{{Code: "000001", Score: bounds[1].upper + maxDarkBoost - 1, LiveAvailable: true}},
		1, bounds[1:], maxDarkBoost,
	) {
		t.Fatal("a lower-static candidate may still overtake with stronger live confirmation or dark money")
	}
	if todayRankingProvenNoDark(
		[]limitup.TodayCandidate{{Code: "000001", Score: bounds[1].upper + maxDarkBoost, LiveAvailable: true}},
		1, bounds[1:], maxDarkBoost,
	) {
		t.Fatal("a tie at the upper bound plus the dark-money margin must not be pruned")
	}
	if !todayRankingProvenNoDark(
		[]limitup.TodayCandidate{{Code: "000001", Score: bounds[1].upper + maxDarkBoost + 0.001, LiveAvailable: true}},
		1, bounds[1:], maxDarkBoost,
	) {
		t.Fatal("a strictly lower remaining upper bound should be pruned")
	}
}

func TestTodayCandidateBatchEndStaysBoundedAndIncludesRequestedCount(t *testing.T) {
	bounds := make([]todayBound, 165)
	for index := range bounds {
		bounds[index] = todayBound{code: fmt.Sprintf("%06d", index), upper: 100 - float64(index)}
	}
	if got := todayCandidateBatchEnd(bounds, 0, 50); got != 50 {
		t.Fatalf("first batch should be bounded by the requested count, got %d", got)
	}
	if got := todayCandidateBatchEnd(bounds, 0, 5); got != 5 {
		t.Fatalf("a request smaller than the batch size should stop at the requested count, got %d", got)
	}
	if got := todayCandidateBatchEnd(bounds, 50, 50); got != 150 {
		t.Fatalf("after the requested boundary the next batch should use the full batch size, got %d", got)
	}
	if math.Abs(limitup.BlendedCandidateScoreUpperBound(limitup.TodayCandidate{MainlineScore: 0.5})-50) > 1e-9 {
		t.Fatal("upper bound must include the full 30-point live allowance")
	}
}

func TestTodayDarkMoneyUpperBoundIsTighterThanGlobal(t *testing.T) {
	base := limitup.TodayCandidate{
		LiveAvailable: true,
		ChangePercent: floatTestPointer(1), TurnoverRate: floatTestPointer(5),
		DistanceToLimit: floatTestPointer(9), TurnoverAmount: floatTestPointer(100_000_000),
		LiveScore: 0.5,
	}
	global := limitup.TodayCandidateDarkMoneyGlobalUpperBound()
	baseBoost := limitup.TodayCandidateDarkMoneyUpperBound(base)
	if baseBoost <= 0 || baseBoost > global {
		t.Fatalf("unexpected per-candidate dark-money bound: boost=%v global=%v", baseBoost, global)
	}
	full := base
	full.TurnoverPercentile = floatTestPointer(50)
	full.VolumeRatio = floatTestPointer(1)
	full.MainFlowPercentile = floatTestPointer(50)
	if fullBoost := limitup.TodayCandidateDarkMoneyUpperBound(full); fullBoost >= baseBoost {
		t.Fatalf("more active live components must reduce the dark-money bound: base=%v full=%v", baseBoost, fullBoost)
	}
	noAmount := base
	noAmount.TurnoverAmount = nil
	if got := limitup.TodayCandidateDarkMoneyUpperBound(noAmount); got != 0 {
		t.Fatalf("a candidate without turnover cannot use dark money, got %v", got)
	}
	noLive := base
	noLive.LiveAvailable = false
	if got := limitup.TodayCandidateDarkMoneyUpperBound(noLive); got != 0 {
		t.Fatalf("a candidate without live quotes cannot use dark money, got %v", got)
	}
}

type batchRecordingQuoteSource struct {
	batches [][]string
	quotes  map[string]limitup.LiveQuote
}

func (source *batchRecordingQuoteSource) ReadLiveQuotes(_ context.Context, codes []string) (map[string]limitup.LiveQuote, error) {
	source.batches = append(source.batches, append([]string(nil), codes...))
	result := make(map[string]limitup.LiveQuote, len(codes))
	for _, code := range codes {
		result[code] = source.quotes[code]
	}
	return result, nil
}

type failingDarkMoneySource struct {
	calls int
	err   error
}

func (source *failingDarkMoneySource) ReadLiveDarkMoney(
	_ context.Context, _ string, _ []string, _ time.Time,
) (map[string]limitup.LiveDarkMoney, error) {
	source.calls++
	return nil, source.err
}

func newTodayRankingFixture(t *testing.T) (*Server, limitup.TodayInput, limitup.TodayNetwork, *batchRecordingQuoteSource) {
	t.Helper()
	server, store := testServer(t, "")
	t.Cleanup(func() { store.Close() })
	snapshotAt := time.Date(2026, 11, 6, 10, 42, 0, 0, limitup.Shanghai())
	snapshot := limitup.IntradaySnapshot{
		TradeDate: "2026-11-06", Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt: snapshotAt, TradeStatus: "open", TradeStatusName: "交易中", SealedCount: 1,
		Events: []limitup.Event{{
			TradeDate: "2026-11-06", Code: "000001", Name: "甲",
			MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed,
		}},
	}
	history := limitup.Dataset{
		Days: []limitup.Day{
			{TradeDate: "2026-11-03", FetchedAt: time.Date(2026, 11, 3, 16, 0, 0, 0, limitup.Shanghai()), SealedCount: 1, TouchedCount: 1},
			{TradeDate: "2026-11-04", FetchedAt: time.Date(2026, 11, 4, 16, 0, 0, 0, limitup.Shanghai()), SealedCount: 2, TouchedCount: 2},
			{TradeDate: "2026-11-05", FetchedAt: time.Date(2026, 11, 5, 16, 0, 0, 0, limitup.Shanghai()), SealedCount: 2, TouchedCount: 2},
		},
		Events: []limitup.Event{
			{TradeDate: "2026-11-03", Code: "000099", Name: "庚", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{TradeDate: "2026-11-04", Code: "000001", Name: "甲", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{TradeDate: "2026-11-04", Code: "000002", Name: "乙", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{TradeDate: "2026-11-05", Code: "000001", Name: "甲", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{TradeDate: "2026-11-05", Code: "000003", Name: "丙", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
		},
	}
	input := limitup.TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-06",
		LookbackDays: 365, MinOverlap: 1, TopCores: 5, CandidateLimit: 1,
		IncludeAllCandidates: true,
	}
	static, err := limitup.AnalyzeToday(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(static.Candidates) != 2 {
		t.Fatalf("expected two coarse candidates, got %+v", static.Candidates)
	}
	quoteSource := &batchRecordingQuoteSource{quotes: map[string]limitup.LiveQuote{
		"000002": {
			Code: "000002", Available: true, PreviousClose: 10, LatestPrice: 10.4, HighPrice: 10.4,
			ChangePct: 0.04, TurnoverRate: 0.03, VolumeRatio: floatTestPointer(1.2),
			Turnover: 100_000_000, QuoteTime: "10:42:00", FetchedAt: snapshotAt, ListingDate: "20200101",
		},
		"000003": {
			Code: "000003", Available: true, PreviousClose: 10, LatestPrice: 10.4, HighPrice: 10.4,
			ChangePct: 0.04, TurnoverRate: 0.03, VolumeRatio: floatTestPointer(1.2),
			Turnover: 100_000_000, QuoteTime: "10:42:00", FetchedAt: snapshotAt, ListingDate: "20200101",
		},
	}}
	server.limitUpQuotes = quoteSource
	return server, input, static, quoteSource
}

func TestTodayRankingReadsDarkMoneyOnceAfterCoarseFilter(t *testing.T) {
	server, input, static, quoteSource := newTodayRankingFixture(t)
	darkSource := &recordingLimitUpDarkMoneySource{}
	server.limitUpDarkMoney = darkSource
	result, stats, err := server.rankTodayCandidates(context.Background(), input, static, "2026-11-05", 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.CoarseBatches < 2 || len(quoteSource.batches) < 2 {
		t.Fatalf("the first batch should be inconclusive: stats=%+v batches=%+v", stats, quoteSource.batches)
	}
	if darkSource.calls != 1 {
		t.Fatalf("dark money must be requested once after the coarse filter, got %d calls", darkSource.calls)
	}
	if len(darkSource.codes) == 0 || len(darkSource.codes) > 2 {
		t.Fatalf("unexpected dark-money candidate pool: %+v", darkSource.codes)
	}
	if stats.DarkStatus != "ready" || stats.DarkRequestedCount != len(darkSource.codes) ||
		stats.DarkAvailableCount != len(darkSource.codes) {
		t.Fatalf("unexpected dark-money stats: %+v", stats)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("the final result must be limited to the requested count: %+v", result.Candidates)
	}
}

func TestTodayRankingDegradesWhenDarkMoneyFails(t *testing.T) {
	server, input, static, _ := newTodayRankingFixture(t)
	server.limitUpDarkMoney = &failingDarkMoneySource{err: errors.New("upstream timeout")}
	result, stats, err := server.rankTodayCandidates(context.Background(), input, static, "2026-11-05", 1)
	if err != nil {
		t.Fatalf("dark-money failure must not fail the ranking: %v", err)
	}
	if stats.DarkStatus != "unavailable" || stats.DarkRequestedCount == 0 || stats.DarkAvailableCount != 0 {
		t.Fatalf("unexpected degraded dark-money stats: %+v", stats)
	}
	if len(result.Candidates) != 1 || !result.LiveCandidate {
		t.Fatalf("the no-dark ranking must still be returned: %+v", result)
	}
}
