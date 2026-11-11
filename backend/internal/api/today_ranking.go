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

type todayBound struct {
	code  string
	upper float64
}

const todayDarkMoneyBatchTimeout = 15 * time.Second

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

func todayRankingProven(scored []limitup.TodayCandidate, requested int, remaining []todayBound) bool {
	if requested < 1 || len(scored) < requested {
		return len(remaining) == 0
	}
	if len(remaining) == 0 {
		return true
	}
	return scored[requested-1].Score > remaining[0].upper
}

func todayCandidateBatchEnd(bounds []todayBound, start, requested int) int {
	end := min(start+todayCandidateBatchSize, len(bounds))
	if start < requested && end > requested {
		end = min(requested, len(bounds))
	}
	return end
}

func (server *Server) rankTodayCandidates(
	ctx context.Context,
	input limitup.TodayInput,
	static limitup.TodayNetwork,
	historyAsOf string,
	requested int,
) (limitup.TodayNetwork, error) {
	bounds := todayCandidateBounds(static.Candidates)
	quotes := make(map[string]limitup.LiveQuote)
	darkMoney := make(map[string]limitup.LiveDarkMoney)
	history := make(map[string]limitup.CandidateHistoryMetrics)
	selected := make(map[string]struct{})
	minuteIndex, validMinute := limitup.LatestResearchMinuteIndex(input.Snapshot.FetchedAt.In(server.location))
	historyReader, canReadHistory := server.store.(candidateHistoryReader)
	readHistory := validMinute && historyAsOf != "" && canReadHistory
	for start := 0; start < len(bounds); {
		end := todayCandidateBatchEnd(bounds, start, requested)
		codes := make([]string, 0, end-start)
		for _, bound := range bounds[start:end] {
			codes = append(codes, bound.code)
			selected[bound.code] = struct{}{}
		}
		var batchQuotes map[string]limitup.LiveQuote
		var batchDark map[string]limitup.LiveDarkMoney
		var batchHistory map[string]limitup.CandidateHistoryMetrics
		var quoteErr, darkErr, historyErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			batchQuotes, quoteErr = server.limitUpQuotes.ReadLiveQuotes(ctx, codes)
		}()
		go func() {
			defer wait.Done()
			darkCtx, cancel := context.WithTimeout(ctx, todayDarkMoneyBatchTimeout)
			defer cancel()
			batchDark, darkErr = server.limitUpDarkMoney.ReadLiveDarkMoney(
				darkCtx, input.Snapshot.TradeDate, codes, input.Snapshot.FetchedAt.In(server.location),
			)
		}()
		if readHistory {
			wait.Add(1)
			go func() {
				defer wait.Done()
				batchHistory, historyErr = historyReader.ReadCandidateHistoryMetrics(
					ctx, historyAsOf, codes, limitup.TodayLiveHistoryWindowDays, minuteIndex,
				)
			}()
		}
		wait.Wait()
		if ctx.Err() != nil {
			return limitup.TodayNetwork{}, ctx.Err()
		}
		if quoteErr != nil {
			return limitup.TodayNetwork{}, fmt.Errorf("read live candidate quotes: %w", quoteErr)
		}
		if darkErr != nil {
			return limitup.TodayNetwork{}, fmt.Errorf("read live candidate dark money: %w", darkErr)
		}
		if historyErr != nil {
			return limitup.TodayNetwork{}, fmt.Errorf("read live candidate history: %w", historyErr)
		}
		for _, code := range codes {
			if !batchQuotes[code].Available || batchQuotes[code].Turnover <= 0 || !batchDark[code].Available {
				return limitup.TodayNetwork{}, fmt.Errorf("incomplete live candidate %s", code)
			}
			quotes[code] = batchQuotes[code]
			darkMoney[code] = batchDark[code]
			if readHistory {
				item, exists := batchHistory[code]
				if !exists {
					return limitup.TodayNetwork{}, fmt.Errorf("missing candidate history %s", code)
				}
				history[code] = item
			}
		}
		input.CandidateFilter = selected
		input.LiveQuotes = quotes
		input.LiveDarkMoney = darkMoney
		input.CandidateHistory = history
		input.IncludeAllCandidates = false
		result, err := limitup.AnalyzeToday(ctx, input)
		if err != nil {
			return limitup.TodayNetwork{}, err
		}
		if todayRankingProven(result.Candidates, requested, bounds[end:]) {
			return result, nil
		}
		start = end
	}
	return limitup.TodayNetwork{}, fmt.Errorf("candidate ranking did not converge")
}
