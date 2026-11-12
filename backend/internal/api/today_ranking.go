package api

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
)

const (
	todayDarkMoneyTimeout       = 15 * time.Second
	todayScoreEpsilon           = 1e-9
	todayCandidatePrefetchBatch = 4
)

type todayBound struct {
	code  string
	upper float64
}

type todayRankingStats struct {
	StaticCount        int
	QuoteReadCount     int
	CoarseBatches      int
	AnalyzeCalls       int
	DarkPoolCount      int
	DarkRequestedCount int
	DarkAvailableCount int
	DarkStatus         string
}

type todayFinalCandidate struct {
	candidate limitup.TodayCandidate
	potential float64
}

type todayBatchSpec struct {
	start int
	end   int
	codes []string
}

type todayBatchResult struct {
	quotes     map[string]limitup.LiveQuote
	history    map[string]limitup.CandidateHistoryMetrics
	quoteErr   error
	historyErr error
}

func todayCandidateBounds(candidates []limitup.TodayCandidate) []todayBound {
	bounds := make([]todayBound, 0, len(candidates))
	for _, candidate := range candidates {
		upper := limitup.BlendedCandidateScoreUpperBound(candidate)
		if math.IsNaN(upper) || math.IsInf(upper, 0) {
			upper = 100
		}
		bounds = append(bounds, todayBound{code: candidate.Code, upper: upper})
	}
	sort.Slice(bounds, func(left, right int) bool {
		if bounds[left].upper != bounds[right].upper {
			return bounds[left].upper > bounds[right].upper
		}
		return bounds[left].code < bounds[right].code
	})
	return bounds
}

// todayRankingProvenNoDark reports whether the no-dark coarse ranking is
// already safe. The margin is the largest possible dark-money increase, so a
// remaining candidate cannot enter the final dark-money pool by surprise.
func todayRankingProvenNoDark(
	scored []limitup.TodayCandidate,
	requested int,
	remaining []todayBound,
	maxDarkBoost float64,
) bool {
	if requested < 1 || len(scored) < requested {
		return len(remaining) == 0
	}
	if len(remaining) == 0 {
		return true
	}
	cutoff := scored[requested-1].Score
	return remaining[0].upper+maxDarkBoost+todayScoreEpsilon < cutoff
}

func todayCandidateBatchEnd(bounds []todayBound, start, requested int) int {
	end := min(start+todayCandidateBatchSize, len(bounds))
	if start < requested && end > requested {
		end = min(requested, len(bounds))
	}
	return end
}

// rankTodayCandidates performs a two-stage live read:
//  1. quotes and same-minute self history are read in static-upper-bound
//     batches, without touching the per-stock dark-money endpoint;
//  2. dark money is requested once, and only for candidates whose no-dark
//     score plus their own dark-money upper bound can still reach the Nth
//     score. A dark-money timeout degrades the result instead of failing it.
func (server *Server) rankTodayCandidates(
	ctx context.Context,
	input limitup.TodayInput,
	static limitup.TodayNetwork,
	historyAsOf string,
	requested int,
) (limitup.TodayNetwork, todayRankingStats, error) {
	stats := todayRankingStats{StaticCount: len(static.Candidates), DarkStatus: "disabled"}
	bounds := todayCandidateBounds(static.Candidates)
	if len(bounds) == 0 {
		stats.DarkStatus = "empty"
		return static, stats, nil
	}
	if server.limitUpQuotes == nil {
		return static, stats, nil
	}

	quotes := make(map[string]limitup.LiveQuote)
	history := make(map[string]limitup.CandidateHistoryMetrics)
	selected := make(map[string]struct{})
	minuteIndex, validMinute := limitup.LatestResearchMinuteIndex(input.Snapshot.FetchedAt.In(server.location))
	historyReader, canReadHistory := server.store.(candidateHistoryReader)
	readHistory := validMinute && historyAsOf != "" && canReadHistory
	darkSourceAvailable := server.limitUpDarkMoney != nil
	globalDarkBoost := 0.0
	if darkSourceAvailable {
		globalDarkBoost = limitup.TodayCandidateDarkMoneyGlobalUpperBound()
	}

	readBatch := func(codes []string) todayBatchResult {
		var result todayBatchResult
		var wait sync.WaitGroup
		wait.Add(1)
		go func() {
			defer wait.Done()
			result.quotes, result.quoteErr = server.limitUpQuotes.ReadLiveQuotes(ctx, codes)
		}()
		if readHistory {
			wait.Add(1)
			go func() {
				defer wait.Done()
				result.history, result.historyErr = historyReader.ReadCandidateHistoryMetrics(
					ctx, historyAsOf, codes, limitup.TodayLiveHistoryWindowDays, minuteIndex,
				)
			}()
		}
		wait.Wait()
		return result
	}

	coarse := static
	proven := false
	quoteReadCount := 0
	for start := 0; start < len(bounds); {
		specs := make([]todayBatchSpec, 0, todayCandidatePrefetchBatch)
		cursor := start
		for len(specs) < todayCandidatePrefetchBatch && cursor < len(bounds) {
			end := todayCandidateBatchEnd(bounds, cursor, requested)
			if end <= cursor {
				end = min(cursor+todayCandidateBatchSize, len(bounds))
			}
			codes := make([]string, 0, end-cursor)
			for _, bound := range bounds[cursor:end] {
				codes = append(codes, bound.code)
			}
			specs = append(specs, todayBatchSpec{start: cursor, end: end, codes: codes})
			cursor = end
		}
		results := make([]todayBatchResult, len(specs))
		var batchWait sync.WaitGroup
		for index, spec := range specs {
			batchWait.Add(1)
			go func(index int, codes []string) {
				defer batchWait.Done()
				results[index] = readBatch(codes)
			}(index, spec.codes)
		}
		batchWait.Wait()
		for _, spec := range specs {
			quoteReadCount += len(spec.codes)
		}
		stats.CoarseBatches += len(specs)
		stats.QuoteReadCount = quoteReadCount
		if ctx.Err() != nil {
			return limitup.TodayNetwork{}, stats, ctx.Err()
		}
		for index, spec := range specs {
			batchResult := results[index]
			if batchResult.quoteErr != nil {
				return limitup.TodayNetwork{}, stats, fmt.Errorf("read live candidate quotes: %w", batchResult.quoteErr)
			}
			if batchResult.historyErr != nil {
				return limitup.TodayNetwork{}, stats, fmt.Errorf("read live candidate history: %w", batchResult.historyErr)
			}
			for _, code := range spec.codes {
				selected[code] = struct{}{}
				quotes[code] = batchResult.quotes[code]
				if readHistory {
					item, exists := batchResult.history[code]
					if !exists {
						return limitup.TodayNetwork{}, stats, fmt.Errorf("missing candidate history %s", code)
					}
					history[code] = item
				}
			}
		}
		input.CandidateFilter = selected
		input.LiveQuotes = quotes
		input.LiveDarkMoney = nil
		input.CandidateHistory = history
		input.IncludeAllCandidates = true
		analyzed, err := limitup.AnalyzeToday(ctx, input)
		if err != nil {
			return limitup.TodayNetwork{}, stats, err
		}
		stats.AnalyzeCalls++
		coarse = analyzed
		proven = todayRankingProvenNoDark(analyzed.Candidates, requested, bounds[cursor:], globalDarkBoost)
		if proven {
			break
		}
		start = cursor
	}
	if len(coarse.Candidates) != len(selected) {
		return limitup.TodayNetwork{}, stats, fmt.Errorf(
			"candidate ranking incomplete: scored %d of %d", len(coarse.Candidates), len(selected),
		)
	}
	if !proven && len(bounds) > 0 && len(selected) < len(bounds) {
		return limitup.TodayNetwork{}, stats, fmt.Errorf("candidate ranking did not converge")
	}

	cutoff := 0.0
	if requested > 0 && len(coarse.Candidates) >= requested {
		cutoff = coarse.Candidates[requested-1].Score
	}
	finalCandidates := make([]todayFinalCandidate, 0, len(coarse.Candidates))
	for _, candidate := range coarse.Candidates {
		boost := 0.0
		if darkSourceAvailable {
			boost = limitup.TodayCandidateDarkMoneyUpperBound(candidate)
		}
		if candidate.Score+boost+todayScoreEpsilon < cutoff {
			continue
		}
		finalCandidates = append(finalCandidates, todayFinalCandidate{
			candidate: candidate,
			potential: candidate.Score + boost,
		})
	}
	if len(finalCandidates) == 0 {
		for _, candidate := range coarse.Candidates {
			finalCandidates = append(finalCandidates, todayFinalCandidate{
				candidate: candidate,
				potential: candidate.Score,
			})
		}
	}
	sort.Slice(finalCandidates, func(left, right int) bool {
		if finalCandidates[left].potential != finalCandidates[right].potential {
			return finalCandidates[left].potential > finalCandidates[right].potential
		}
		if finalCandidates[left].candidate.Score != finalCandidates[right].candidate.Score {
			return finalCandidates[left].candidate.Score > finalCandidates[right].candidate.Score
		}
		return finalCandidates[left].candidate.Code < finalCandidates[right].candidate.Code
	})

	finalPool := make(map[string]struct{}, len(finalCandidates))
	darkCodes := make([]string, 0, len(finalCandidates))
	for _, item := range finalCandidates {
		candidate := item.candidate
		finalPool[candidate.Code] = struct{}{}
		if darkSourceAvailable && candidate.LiveAvailable &&
			candidate.TurnoverAmount != nil && *candidate.TurnoverAmount > 0 {
			darkCodes = append(darkCodes, candidate.Code)
		}
	}
	stats.DarkPoolCount = len(finalCandidates)
	stats.DarkRequestedCount = len(darkCodes)

	darkMoney := make(map[string]limitup.LiveDarkMoney)
	switch {
	case !darkSourceAvailable:
		stats.DarkStatus = "disabled"
	case len(darkCodes) == 0:
		stats.DarkStatus = "empty"
	default:
		darkContext, cancel := context.WithTimeout(ctx, todayDarkMoneyTimeout)
		batch, darkErr := server.limitUpDarkMoney.ReadLiveDarkMoney(
			darkContext, input.Snapshot.TradeDate, darkCodes, input.Snapshot.FetchedAt.In(server.location),
		)
		cancel()
		for _, code := range darkCodes {
			if item, exists := batch[code]; exists && item.Available {
				darkMoney[code] = item
				stats.DarkAvailableCount++
			}
		}
		switch {
		case stats.DarkAvailableCount == 0:
			stats.DarkStatus = "unavailable"
			server.logger.Warn("read today-core dark money", "error", darkErr,
				"requested", len(darkCodes), "available", stats.DarkAvailableCount)
		case darkErr != nil || stats.DarkAvailableCount < len(darkCodes):
			stats.DarkStatus = "partial"
			server.logger.Warn("read today-core dark money partially", "error", darkErr,
				"requested", len(darkCodes), "available", stats.DarkAvailableCount)
		default:
			stats.DarkStatus = "ready"
		}
	}

	input.CandidateFilter = finalPool
	input.LiveQuotes = quotes
	input.LiveDarkMoney = darkMoney
	input.CandidateHistory = history
	input.IncludeAllCandidates = false
	result, err := limitup.AnalyzeToday(ctx, input)
	if err != nil {
		return limitup.TodayNetwork{}, stats, err
	}
	if requested > 0 && len(result.Candidates) > requested {
		result.Candidates = result.Candidates[:requested]
	}
	result.CandidateLimit = requested
	return result, stats, nil
}
