package api

import (
	"fmt"
	"math"
	"testing"

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
	if todayRankingProven([]limitup.TodayCandidate{{Code: "000001", Score: bounds[1].upper - 1, LiveAvailable: true}}, 1, bounds[1:]) {
		t.Fatal("a lower-static candidate may still overtake with stronger live confirmation")
	}
	if todayRankingProven([]limitup.TodayCandidate{{Code: "000001", Score: bounds[1].upper, LiveAvailable: true}}, 1, bounds[1:]) {
		t.Fatal("a tie at the upper bound must not be pruned")
	}
	if !todayRankingProven([]limitup.TodayCandidate{{Code: "000001", Score: bounds[1].upper + 0.001, LiveAvailable: true}}, 1, bounds[1:]) {
		t.Fatal("a strictly lower remaining upper bound should be pruned")
	}
}

func TestTodayCandidateBatchEndStaysBoundedAndIncludesRequestedCount(t *testing.T) {
	bounds := make([]todayBound, 65)
	for index := range bounds {
		bounds[index] = todayBound{code: fmt.Sprintf("%06d", index), upper: 100 - float64(index)}
	}
	if got := todayCandidateBatchEnd(bounds, 0, 50); got != 16 {
		t.Fatalf("first batch should be bounded by the batch size, got %d", got)
	}
	if got := todayCandidateBatchEnd(bounds, 0, 5); got != 5 {
		t.Fatalf("a request smaller than the batch size should stop at the requested count, got %d", got)
	}
	if got := todayCandidateBatchEnd(bounds, 16, 50); got != 32 {
		t.Fatalf("second batch should remain bounded, got %d", got)
	}
	if got := todayCandidateBatchEnd(bounds, 48, 50); got != 50 {
		t.Fatalf("the requested boundary should be included without over-reading, got %d", got)
	}
	if got := todayCandidateBatchEnd(bounds, 50, 50); got != 65 {
		t.Fatalf("after the requested boundary the next batch should cover the tail, got %d", got)
	}
	if math.Abs(limitup.BlendedCandidateScoreUpperBound(limitup.TodayCandidate{MainlineScore: 0.5})-50) > 1e-9 {
		t.Fatal("upper bound must include the full 30-point live allowance")
	}
}
