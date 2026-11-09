package limitup

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	// TodayCoreWeightStrength, TodayCoreWeightCoverage and
	// TodayCoreWeightNeighbors sum to one. Keeping the weights in code makes
	// the API's "core score" reproducible instead of an unexplained rank.
	TodayCoreWeightStrength  = 0.45
	TodayCoreWeightCoverage  = 0.30
	TodayCoreWeightNeighbors = 0.25
)

type todayPair struct {
	overlap int
	heat    float64
	dates   []string
}

type TodayInput struct {
	Snapshot     IntradaySnapshot
	History      Dataset
	HistoryFrom  string
	LookbackDays int
	MinOverlap   int
	TopCores     int
}

// AnalyzeToday computes a relationship graph whose nodes are the stocks sealed
// at the time of the intraday snapshot. History must contain only complete
// archive days strictly before the snapshot date. It reads each history day at
// most once and never materialises all-market stock pairs.
func AnalyzeToday(ctx context.Context, input TodayInput) (TodayNetwork, error) {
	if input.Snapshot.TradeDate == "" || input.Snapshot.Source != SourceTHS || !ValidScope(input.Snapshot.Scope) {
		return TodayNetwork{}, fmt.Errorf("%w: invalid intraday snapshot", ErrInvalid)
	}
	parsedDate, err := time.ParseInLocation("2006-01-02", input.Snapshot.TradeDate, Shanghai())
	if err != nil || parsedDate.Format("2006-01-02") != input.Snapshot.TradeDate {
		return TodayNetwork{}, fmt.Errorf("%w: intraday snapshot date must use YYYY-MM-DD", ErrInvalid)
	}
	if input.Snapshot.SealedCount != len(input.Snapshot.Events) {
		return TodayNetwork{}, fmt.Errorf("%w: intraday sealed count does not match its members", ErrInvalid)
	}
	if input.MinOverlap < 1 {
		input.MinOverlap = 1
	}
	if input.TopCores < 1 {
		input.TopCores = 5
	}
	if input.LookbackDays < 1 {
		input.LookbackDays = 365
	}
	historyDays := append([]Day(nil), input.History.Days...)
	sort.Slice(historyDays, func(left, right int) bool {
		return historyDays[left].TradeDate < historyDays[right].TradeDate
	})
	uniqueDays := historyDays[:0]
	for _, day := range historyDays {
		if len(uniqueDays) > 0 && uniqueDays[len(uniqueDays)-1].TradeDate == day.TradeDate {
			continue
		}
		uniqueDays = append(uniqueDays, day)
	}
	historyDays = uniqueDays
	for _, day := range historyDays {
		if day.TradeDate >= input.Snapshot.TradeDate {
			return TodayNetwork{}, fmt.Errorf("%w: today's archive must not participate in history", ErrInvalid)
		}
		if input.Snapshot.ArchivedThrough != "" && day.TradeDate > input.Snapshot.ArchivedThrough {
			return TodayNetwork{}, fmt.Errorf("%w: historical day is newer than the resolved archive cutoff", ErrInvalid)
		}
	}
	completeDays := make(map[string]struct{}, len(historyDays))
	for _, day := range historyDays {
		completeDays[day.TradeDate] = struct{}{}
	}
	for _, event := range input.History.Events {
		if event.TradeDate >= input.Snapshot.TradeDate {
			return TodayNetwork{}, fmt.Errorf("%w: today's event must not participate in history", ErrInvalid)
		}
		if input.Snapshot.ArchivedThrough != "" && event.TradeDate > input.Snapshot.ArchivedThrough {
			return TodayNetwork{}, fmt.Errorf("%w: historical event is newer than the resolved archive cutoff", ErrInvalid)
		}
		if _, complete := completeDays[event.TradeDate]; !complete {
			return TodayNetwork{}, fmt.Errorf("%w: historical event has no complete archive day", ErrInvalid)
		}
	}

	members := make(map[string]Event, len(input.Snapshot.Events))
	for _, event := range input.Snapshot.Events {
		if event.TradeDate != input.Snapshot.TradeDate {
			return TodayNetwork{}, fmt.Errorf("%w: intraday member has a different trade date", ErrInvalid)
		}
		if event.Pool != PoolSealed {
			return TodayNetwork{}, fmt.Errorf("%w: intraday pool contains a non-sealed record", ErrInvalid)
		}
		if _, duplicate := members[event.Code]; duplicate {
			return TodayNetwork{}, fmt.Errorf("%w: duplicate intraday member %s", ErrInvalid, event.Code)
		}
		members[event.Code] = event
	}
	codes := make([]string, 0, len(members))
	for code := range members {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	sealDates := make(map[string]map[string]struct{}, len(members))
	touchDates := make(map[string]map[string]struct{}, len(members))
	brokenDates := make(map[string]map[string]struct{}, len(members))
	downDates := make(map[string]map[string]struct{}, len(members))
	for code := range members {
		sealDates[code] = make(map[string]struct{})
		touchDates[code] = make(map[string]struct{})
		brokenDates[code] = make(map[string]struct{})
		downDates[code] = make(map[string]struct{})
	}
	for _, event := range input.History.Events {
		if _, ok := members[event.Code]; !ok {
			continue
		}
		switch event.Pool {
		case PoolSealed:
			sealDates[event.Code][event.TradeDate] = struct{}{}
			touchDates[event.Code][event.TradeDate] = struct{}{}
		case PoolBroken:
			brokenDates[event.Code][event.TradeDate] = struct{}{}
			touchDates[event.Code][event.TradeDate] = struct{}{}
		case PoolDown:
			downDates[event.Code][event.TradeDate] = struct{}{}
		}
	}

	sealedByDate := make(map[string]map[string]struct{})
	for _, event := range input.History.Events {
		if event.Pool != PoolSealed {
			continue
		}
		if sealedByDate[event.TradeDate] == nil {
			sealedByDate[event.TradeDate] = make(map[string]struct{})
		}
		sealedByDate[event.TradeDate][event.Code] = struct{}{}
	}
	dates := make([]string, 0, len(sealedByDate))
	for date := range sealedByDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	pairs := make(map[string]*todayPair)
	addPair := func(left, right string, heat float64, date string) {
		key := left + ":" + right
		if left > right {
			key = right + ":" + left
		}
		pair := pairs[key]
		if pair == nil {
			pair = &todayPair{}
			pairs[key] = pair
		}
		pair.overlap++
		pair.heat += heat
		pair.dates = append(pair.dates, date)
	}
	for _, date := range dates {
		fullSealedCount := len(sealedByDate[date])
		peers := make([]string, 0, fullSealedCount)
		for code := range sealedByDate[date] {
			if _, member := members[code]; member {
				peers = append(peers, code)
			}
		}
		if len(peers) < 2 {
			continue
		}
		sort.Strings(peers)
		// The graph is induced by today's members, but the heat denominator
		// stays on the complete archived sealed pool so scores remain
		// comparable with the stock linkage view.
		weight := 1 / float64(max(1, fullSealedCount-1))
		for i := 0; i < len(peers); i++ {
			for j := i + 1; j < len(peers); j++ {
				addPair(peers[i], peers[j], weight, date)
			}
		}
	}

	strongEdges := make(map[string][]Link, len(members))
	for key, pair := range pairs {
		if pair.overlap < input.MinOverlap {
			continue
		}
		separator := strings.IndexByte(key, ':')
		left, right := key[:separator], key[separator+1:]
		leftDays, rightDays := len(sealDates[left]), len(sealDates[right])
		common := pair.overlap
		sharedDates := append([]string(nil), pair.dates...)
		sort.Sort(sort.Reverse(sort.StringSlice(sharedDates)))
		link := Link{
			Code: right, Name: members[right].Name, MarketType: members[right].MarketType,
			AnchorDays: leftDays, PeerDays: rightDays, OverlapDays: common,
			Jaccard:                float64(common) / float64(max(1, leftDays+rightDays-common)),
			ConditionalProbability: float64(common) / float64(max(1, leftDays)),
			ReverseProbability:     float64(common) / float64(max(1, rightDays)),
			BaselineProbability:    float64(rightDays) / float64(max(1, len(historyDays))),
			HeatAdjustedOverlap:    pair.heat,
			SharedDates:            sharedDates,
			SampleSufficient:       len(historyDays) >= MinimumSamples && common >= 3,
		}
		if leftDays > 0 && rightDays > 0 {
			link.Lift = float64(common) * float64(len(historyDays)) / float64(leftDays*rightDays)
			denominator := math.Sqrt(float64(leftDays) * float64(rightDays) *
				float64(max(0, len(historyDays)-leftDays)) * float64(max(0, len(historyDays)-rightDays)))
			if denominator > 0 {
				phi := (float64(common)*float64(len(historyDays)) - float64(leftDays*rightDays)) / denominator
				link.Phi = &phi
			}
		}
		strongEdges[left] = append(strongEdges[left], link)
		reflected := link
		reflected.Code, reflected.Name, reflected.MarketType = left, members[left].Name, members[left].MarketType
		reflected.AnchorDays, reflected.PeerDays = rightDays, leftDays
		reflected.ConditionalProbability, reflected.ReverseProbability = link.ReverseProbability, link.ConditionalProbability
		strongEdges[right] = append(strongEdges[right], reflected)
	}

	membersOut := make([]TodayMember, 0, len(codes))
	memberByCode := make(map[string]TodayMember, len(codes))
	for _, code := range codes {
		event := members[code]
		links := strongEdges[code]
		sort.Slice(links, func(left, right int) bool {
			if links[left].HeatAdjustedOverlap != links[right].HeatAdjustedOverlap {
				return links[left].HeatAdjustedOverlap > links[right].HeatAdjustedOverlap
			}
			if links[left].OverlapDays != links[right].OverlapDays {
				return links[left].OverlapDays > links[right].OverlapDays
			}
			return links[left].Code < links[right].Code
		})
		strength := 0.0
		for _, link := range links {
			strength += link.HeatAdjustedOverlap
		}
		member := TodayMember{
			Code: event.Code, Name: event.Name, MarketType: event.MarketType,
			FirstLimitAt: event.FirstLimitAt, LastLimitAt: event.LastLimitAt,
			OpenCount: event.OpenCount, LimitType: event.LimitType, HighDays: event.HighDays,
			Reason: event.Reason, ChangePercent: event.ChangePercent, TurnoverRate: event.TurnoverRate,
			SealDays: len(sealDates[code]), TouchDays: len(touchDates[code]),
			BrokenDays: len(brokenDates[code]), DownDays: len(downDates[code]),
			EffectiveLinks: len(links), Strength: strength,
			Coverage: float64(len(links)) / float64(max(1, len(members)-1)),
		}
		if len(links) > 0 {
			member.StrongestPeerCode, member.StrongestPeerName = links[0].Code, links[0].Name
			member.StrongestOverlapDays = links[0].OverlapDays
		}
		membersOut = append(membersOut, member)
		memberByCode[code] = member
	}
	maxStrength, maxNeighborStrength := 0.0, 0.0
	for _, member := range membersOut {
		maxStrength = math.Max(maxStrength, member.Strength)
		neighborStrength := 0.0
		for _, link := range strongEdges[member.Code] {
			neighborStrength += memberByCode[link.Code].Strength
		}
		maxNeighborStrength = math.Max(maxNeighborStrength, neighborStrength)
	}
	for index := range membersOut {
		member := &membersOut[index]
		neighborStrength := 0.0
		for _, link := range strongEdges[member.Code] {
			neighborStrength += memberByCode[link.Code].Strength
		}
		member.CoreScore = 100 * (TodayCoreWeightStrength*normalise(member.Strength, maxStrength) +
			TodayCoreWeightCoverage*member.Coverage +
			TodayCoreWeightNeighbors*normalise(neighborStrength, maxNeighborStrength))
	}
	sort.SliceStable(membersOut, func(left, right int) bool {
		if membersOut[left].CoreScore != membersOut[right].CoreScore {
			return membersOut[left].CoreScore > membersOut[right].CoreScore
		}
		if membersOut[left].EffectiveLinks != membersOut[right].EffectiveLinks {
			return membersOut[left].EffectiveLinks > membersOut[right].EffectiveLinks
		}
		if membersOut[left].SealDays != membersOut[right].SealDays {
			return membersOut[left].SealDays > membersOut[right].SealDays
		}
		return membersOut[left].Code < membersOut[right].Code
	})

	historyAsOf := ""
	if len(historyDays) > 0 {
		historyAsOf = historyDays[len(historyDays)-1].TradeDate
	}
	coverage := Coverage{
		Source: SourceTHS, Scope: input.Snapshot.Scope,
		FromDate: input.HistoryFrom, AsOf: historyAsOf, LookbackDays: input.LookbackDays,
		ObservedDays: len(historyDays), MinimumSamples: MinimumSamples,
		SampleSufficient: len(historyDays) >= MinimumSamples,
	}
	if len(historyDays) > 0 {
		coverage.FromDate = historyDays[0].TradeDate
		coverage.FirstDate = historyDays[0].TradeDate
		coverage.LatestDate = historyAsOf
	}
	cores := make([]TodayCore, 0, min(input.TopCores, len(membersOut)))
	for _, member := range membersOut {
		if len(cores) >= input.TopCores {
			break
		}
		if member.EffectiveLinks == 0 {
			continue
		}
		cores = append(cores, TodayCore{Member: member, Links: strongEdges[member.Code]})
	}
	return TodayNetwork{
		TradeDate: input.Snapshot.TradeDate, SnapshotAt: input.Snapshot.FetchedAt,
		TradeStatus: input.Snapshot.TradeStatus, TradeStatusName: input.Snapshot.TradeStatusName,
		SealedCount: len(membersOut), HistoryAsOf: historyAsOf,
		LookbackDays: input.LookbackDays, Scope: input.Snapshot.Scope, MinOverlap: input.MinOverlap,
		Coverage: coverage, HistoryAvailable: len(historyDays) > 0,
		Members: membersOut, Cores: cores,
	}, ctx.Err()
}

func normalise(value, maximum float64) float64 {
	if maximum <= 0 || math.IsNaN(value) || math.IsInf(value, 0) ||
		math.IsNaN(maximum) || math.IsInf(maximum, 0) || value <= 0 {
		return 0
	}
	return math.Min(1, value/maximum)
}
