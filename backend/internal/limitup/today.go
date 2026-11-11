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
	// TodayCoreWeight* sum to one. Keeping the weights in code makes the
	// API's "core score" reproducible instead of an unexplained rank.
	TodayCoreWeightStrength  = 0.35
	TodayCoreWeightCoverage  = 0.25
	TodayCoreWeightNeighbors = 0.20
	TodayCoreWeightTheme     = 0.20

	// TodayCandidateWeight* rank stocks that are not sealed today. They sum to
	// one in live mode. There is deliberately no "recently limited up" term:
	// that made a stock's own limit-up frequency look like today's mainline.
	TodayCandidateWeightLive     = 0.30
	TodayCandidateWeightMainline = 0.40
	TodayCandidateWeightHistory  = 0.20
	TodayCandidateWeightCoverage = 0.10

	// Risk is a bounded discount, not a positive score component. A clean
	// candidate keeps its full score; ST, low liquidity and other flags can
	// reduce it by at most 15%.
	TodayCandidateMaxRiskDiscount = 0.15
)

type todayPair struct {
	overlap int
	heat    float64
	dates   []string
}

type TodayInput struct {
	Snapshot       IntradaySnapshot
	History        Dataset
	HistoryFrom    string
	LookbackDays   int
	MinOverlap     int
	TopCores       int
	CandidateLimit int
	// ThemeProfiles is the historical accumulated THS Top20 label index. It
	// is the primary clustering source for today's mainline; Eastmoney board
	// relations are intentionally not part of this path.
	ThemeProfiles map[string]ThemeStockProfile
	// LiveQuotes is the optional intraday quote snapshot used to confirm
	// spillover candidates. When it is nil the endpoint still returns the
	// historical view, but marks the response as not live-confirmed.
	LiveQuotes map[string]LiveQuote
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
	if input.CandidateLimit < 1 {
		input.CandidateLimit = 20
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
	allSealDates := make(map[string]map[string]struct{})
	allTouchDates := make(map[string]map[string]struct{})
	addDate := func(container map[string]map[string]struct{}, code, date string) {
		if container[code] == nil {
			container[code] = make(map[string]struct{})
		}
		container[code][date] = struct{}{}
	}
	lastSealDate := make(map[string]string)
	stockMeta := make(map[string]Event)
	for _, event := range input.History.Events {
		if current, ok := stockMeta[event.Code]; !ok || event.TradeDate >= current.TradeDate {
			stockMeta[event.Code] = event
		}
		switch event.Pool {
		case PoolSealed:
			addDate(allSealDates, event.Code, event.TradeDate)
			addDate(allTouchDates, event.Code, event.TradeDate)
			if event.TradeDate > lastSealDate[event.Code] {
				lastSealDate[event.Code] = event.TradeDate
			}
		case PoolBroken:
			addDate(allTouchDates, event.Code, event.TradeDate)
		}
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
	candidatePairs := make(map[string]*todayPair)
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
	addCandidatePair := func(candidate, member string, heat float64, date string) {
		key := candidate + ":" + member
		pair := candidatePairs[key]
		if pair == nil {
			pair = &todayPair{}
			candidatePairs[key] = pair
		}
		pair.overlap++
		pair.heat += heat
		pair.dates = append(pair.dates, date)
	}
	for _, date := range dates {
		fullSealedCount := len(sealedByDate[date])
		peers := make([]string, 0, fullSealedCount)
		others := make([]string, 0, fullSealedCount)
		for code := range sealedByDate[date] {
			if _, member := members[code]; member {
				peers = append(peers, code)
			} else {
				others = append(others, code)
			}
		}
		if len(peers) == 0 {
			continue
		}
		sort.Strings(peers)
		sort.Strings(others)
		// The graph is induced by today's members, but the heat denominator
		// stays on the complete archived sealed pool so scores remain
		// comparable with the stock linkage view.
		weight := 1 / float64(max(1, fullSealedCount-1))
		if len(peers) >= 2 {
			for i := 0; i < len(peers); i++ {
				for j := i + 1; j < len(peers); j++ {
					addPair(peers[i], peers[j], weight, date)
				}
			}
		}
		for _, candidate := range others {
			for _, member := range peers {
				addCandidatePair(candidate, member, weight, date)
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
			Coverage:  float64(len(links)) / float64(max(1, len(members)-1)),
			ThemeTags: []ThemeTag{},
		}
		if len(links) > 0 {
			member.StrongestPeerCode, member.StrongestPeerName = links[0].Code, links[0].Name
			member.StrongestOverlapDays = links[0].OverlapDays
		}
		membersOut = append(membersOut, member)
		memberByCode[code] = member
	}
	themeAffinity := map[string]memberThemeAffinity{}
	maxThemeStrength := 0.0
	if len(input.ThemeProfiles) > 0 {
		for index := range membersOut {
			if profile, exists := input.ThemeProfiles[membersOut[index].Code]; exists {
				membersOut[index].ThemeTags = topThemeTags(profile.Tags, 8)
			} else {
				membersOut[index].ThemeTags = []ThemeTag{}
			}
		}
		themeAffinity = computeMemberThemeAffinity(membersOut, input.ThemeProfiles)
		for index := range membersOut {
			affinity := themeAffinity[membersOut[index].Code]
			membersOut[index].ThemeNeighborCount = affinity.neighbors
			membersOut[index].ThemeStrength = affinity.strength
			maxThemeStrength = math.Max(maxThemeStrength, affinity.strength)
		}
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
		themeWeight := TodayCoreWeightTheme
		activeWeight := TodayCoreWeightStrength + TodayCoreWeightCoverage + TodayCoreWeightNeighbors + themeWeight
		if maxThemeStrength <= 0 {
			themeWeight = 0
			activeWeight = TodayCoreWeightStrength + TodayCoreWeightCoverage + TodayCoreWeightNeighbors
		}
		member.CoreScore = 100 * (TodayCoreWeightStrength*normalise(member.Strength, maxStrength) +
			TodayCoreWeightCoverage*member.Coverage +
			TodayCoreWeightNeighbors*normalise(neighborStrength, maxNeighborStrength) +
			themeWeight*normalise(member.ThemeStrength, maxThemeStrength)) / activeWeight
	}
	coreByCode := make(map[string]float64, len(membersOut))
	for _, member := range membersOut {
		coreByCode[member.Code] = member.CoreScore
	}
	themeMainline := []ThemeCluster{}
	if len(input.ThemeProfiles) > 0 {
		themeMainline = ComputeThemeMainline(membersOut, input.ThemeProfiles)
	}
	candidatesOut := buildTodayCandidates(candidatePairs, members, coreByCode, len(membersOut),
		len(historyDays), input.MinOverlap,
		allSealDates, allTouchDates, lastSealDate, stockMeta)
	if len(themeMainline) > 0 {
		candidatesOut = applyTodayThemeProfiles(candidatesOut, themeMainline, input.ThemeProfiles,
			members, allSealDates, allTouchDates, lastSealDate, candidatePairs, input.MinOverlap)
	}
	candidatesOut = scoreTodayCandidates(candidatesOut, input.LiveQuotes)
	if len(candidatesOut) > input.CandidateLimit {
		candidatesOut = candidatesOut[:input.CandidateLimit]
	}
	liveCandidate := input.LiveQuotes != nil
	liveUnavailable := false
	if liveCandidate {
		for _, candidate := range candidatesOut {
			if !candidate.LiveAvailable {
				liveUnavailable = true
				break
			}
		}
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
		if member.EffectiveLinks == 0 && member.ThemeNeighborCount == 0 {
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
		Candidates: candidatesOut, CandidateLimit: input.CandidateLimit,
		MainlineThemes: []MainlineTheme{}, ThemeMainline: themeMainline,
		LiveCandidate: liveCandidate, LiveUnavailable: liveUnavailable,
	}, ctx.Err()
}

type candidateRawLink struct {
	memberCode string
	overlap    int
	heat       float64
	dates      []string
}

// buildTodayCandidates ranks stocks that are not sealed today by their
// historical co-sealing with today's members. A member's core score weights
// each edge so links to today's leading stocks count more than links to the
// tail of the sealed pool.
func buildTodayCandidates(
	candidatePairs map[string]*todayPair,
	members map[string]Event,
	coreByCode map[string]float64,
	sealedCount, historyDays, minOverlap int,
	allSealDates, allTouchDates map[string]map[string]struct{},
	lastSealDate map[string]string,
	stockMeta map[string]Event,
) []TodayCandidate {
	byCandidate := make(map[string][]candidateRawLink)
	for key, pair := range candidatePairs {
		if pair.overlap < minOverlap {
			continue
		}
		separator := strings.IndexByte(key, ':')
		if separator < 0 {
			continue
		}
		candidate, member := key[:separator], key[separator+1:]
		if _, isMember := members[candidate]; isMember {
			continue
		}
		if _, isMember := members[member]; !isMember {
			continue
		}
		byCandidate[candidate] = append(byCandidate[candidate], candidateRawLink{
			memberCode: member, overlap: pair.overlap, heat: pair.heat,
			dates: append([]string(nil), pair.dates...),
		})
	}
	type affinityLink struct {
		link     candidateRawLink
		phi      float64
		affinity float64
	}
	type scoredCandidate struct {
		candidate TodayCandidate
		strength  float64
	}
	items := make([]scoredCandidate, 0, len(byCandidate))
	maxStrength := 0.0
	for code, links := range byCandidate {
		candidateSealDays := len(allSealDates[code])
		affinities := make([]affinityLink, 0, len(links))
		for _, link := range links {
			memberSealDays := len(allSealDates[link.memberCode])
			// Phi is bounded and already divides out both the candidate's and
			// the member's own seal frequency, so a stock that simply limits
			// up often cannot score high without a specific tie to the core.
			phi := 0.0
			denominator := math.Sqrt(float64(candidateSealDays) * float64(memberSealDays) *
				float64(max(0, historyDays-candidateSealDays)) * float64(max(0, historyDays-memberSealDays)))
			if denominator > 0 {
				phi = (float64(link.overlap)*float64(historyDays) -
					float64(candidateSealDays)*float64(memberSealDays)) / denominator
			}
			if phi < 0 {
				phi = 0
			}
			// A single shared day is weak evidence. Shrink sparse links
			// toward zero instead of letting a 1-of-1 match outrank a
			// repeatable relationship.
			confidence := float64(link.overlap) / (float64(link.overlap) + 2)
			affinities = append(affinities, affinityLink{
				link: link, phi: phi,
				affinity: (1 + coreByCode[link.memberCode]/100) * phi * confidence,
			})
		}
		sort.Slice(affinities, func(left, right int) bool {
			if affinities[left].affinity != affinities[right].affinity {
				return affinities[left].affinity > affinities[right].affinity
			}
			if affinities[left].link.overlap != affinities[right].link.overlap {
				return affinities[left].link.overlap > affinities[right].link.overlap
			}
			return affinities[left].link.memberCode < affinities[right].link.memberCode
		})
		// "Main line" is defined by the strongest ties, not by the total
		// number of weak co-seal links. Average the top three affinities so a
		// stock that simply limits up often cannot outrank a stock that
		// specifically follows today's leaders.
		topCount := min(3, len(affinities))
		strength := 0.0
		lastShared := ""
		for _, item := range affinities[:topCount] {
			strength += item.affinity
			for _, date := range item.link.dates {
				if date > lastShared {
					lastShared = date
				}
			}
		}
		if topCount > 0 {
			strength /= float64(topCount)
		}
		if strength <= 0 || lastShared == "" {
			continue
		}
		maxStrength = math.Max(maxStrength, strength)
		topLinks := make([]CandidateLink, 0, min(5, len(affinities)))
		for _, item := range affinities {
			if len(topLinks) >= 5 {
				break
			}
			linkLast := ""
			for _, date := range item.link.dates {
				if date > linkLast {
					linkLast = date
				}
			}
			member := members[item.link.memberCode]
			topLinks = append(topLinks, CandidateLink{
				CoreCode: item.link.memberCode, CoreName: member.Name,
				OverlapDays: item.link.overlap, Heat: item.link.heat,
				Phi: item.phi, LastSharedDate: linkLast,
			})
		}
		meta := stockMeta[code]
		items = append(items, scoredCandidate{
			strength: strength,
			candidate: TodayCandidate{
				Code: code, Name: meta.Name, MarketType: meta.MarketType,
				Strength: strength, EffectiveLinks: len(links),
				Coverage: float64(len(links)) / float64(max(1, sealedCount)),
				SealDays: candidateSealDays, TouchDays: len(allTouchDates[code]),
				LastSealDate: lastSealDate[code], LastSharedDate: lastShared,
				StrongestCoreCode: topLinks[0].CoreCode, StrongestCoreName: topLinks[0].CoreName,
				StrongestOverlapDays: topLinks[0].OverlapDays, StrongestHeat: topLinks[0].Heat,
				StrongestPhi: topLinks[0].Phi,
				TopLinks:     topLinks,
				Themes:       []ThemeRef{},
				ThemeTags:    []ThemeTag{},
			},
		})
	}
	for index := range items {
		item := &items[index]
		// Keep the historical evidence raw here. The final blend is applied
		// after live quotes and the explicit Top20 mainline are available.
		item.candidate.HistoryScore = normalise(item.strength, maxStrength)
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].strength != items[right].strength {
			return items[left].strength > items[right].strength
		}
		if items[left].candidate.EffectiveLinks != items[right].candidate.EffectiveLinks {
			return items[left].candidate.EffectiveLinks > items[right].candidate.EffectiveLinks
		}
		return items[left].candidate.Code < items[right].candidate.Code
	})
	result := make([]TodayCandidate, 0, len(items))
	for _, item := range items {
		result = append(result, item.candidate)
	}
	return result
}

// scoreTodayCandidates blends live confirmation, explicit Top20 mainline
// membership, historical specificity and coverage. Risk is applied as a
// bounded discount instead of a positive component. When live quotes were not
// requested, the remaining weights are renormalised so a historical-only view
// is still ordered explicitly as such.
func scoreTodayCandidates(candidates []TodayCandidate, live map[string]LiveQuote) []TodayCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	maxHistory := 0.0
	for _, candidate := range candidates {
		maxHistory = math.Max(maxHistory, candidate.HistoryScore)
	}
	liveMode := live != nil
	for index := range candidates {
		candidate := &candidates[index]
		candidate.MainlineScore = candidate.ThemeScore
		candidate.HistoryScore = normalise(candidate.HistoryScore, maxHistory)
		candidate.LimitRatio = limitUpRatio(candidate.Code) * 100
		candidate.RiskFlags = []string{}
		liveScore, liveAvailable := liveCandidateScore(candidate, live)
		candidate.LiveAvailable = liveAvailable
		candidate.LiveScore = liveScore
		if liveMode && !liveAvailable {
			candidate.RiskFlags = append(candidate.RiskFlags, "未取得盘中行情")
		}
		risk := candidateRiskScore(candidate)
		candidate.RiskScore = risk.score
		candidate.RiskFlags = append(candidate.RiskFlags, risk.flags...)
		score := 0.0
		if liveMode {
			score = 100 * (TodayCandidateWeightLive*liveScore +
				TodayCandidateWeightMainline*candidate.MainlineScore +
				TodayCandidateWeightHistory*candidate.HistoryScore +
				TodayCandidateWeightCoverage*candidate.Coverage)
		} else {
			activeWeight := TodayCandidateWeightMainline + TodayCandidateWeightHistory + TodayCandidateWeightCoverage
			score = 100 * (TodayCandidateWeightMainline*candidate.MainlineScore +
				TodayCandidateWeightHistory*candidate.HistoryScore +
				TodayCandidateWeightCoverage*candidate.Coverage) / activeWeight
		}
		score *= 1 - TodayCandidateMaxRiskDiscount*(1-risk.score)
		candidate.Score = clamp01(score/100) * 100
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].Score != candidates[right].Score {
			return candidates[left].Score > candidates[right].Score
		}
		if candidates[left].MainlineScore != candidates[right].MainlineScore {
			return candidates[left].MainlineScore > candidates[right].MainlineScore
		}
		if candidates[left].HistoryScore != candidates[right].HistoryScore {
			return candidates[left].HistoryScore > candidates[right].HistoryScore
		}
		return candidates[left].Code < candidates[right].Code
	})
	return candidates
}

func liveCandidateScore(candidate *TodayCandidate, quotes map[string]LiveQuote) (float64, bool) {
	if quotes == nil {
		return 0, false
	}
	quote, exists := quotes[candidate.Code]
	if !exists || !quote.Available || quote.PreviousClose <= 0 {
		return 0, false
	}
	change := quote.ChangePct
	turnover := quote.TurnoverRate
	limitRatio := limitUpRatio(candidate.Code)
	distance := math.Max(0, limitRatio-change)
	changePercent := change * 100
	turnoverPercent := turnover * 100
	distancePercent := distance * 100
	candidate.ChangePercent = &changePercent
	candidate.TurnoverRate = &turnoverPercent
	candidate.DistanceToLimit = &distancePercent
	candidate.quoteFetchedAt = quote.FetchedAt
	candidate.listingDate = quote.ListingDate
	candidate.TouchedLimit = quote.HighPrice+0.0001 >= limitPrice(quote.PreviousClose, limitRatio)
	if quote.VolumeRatio != nil {
		value := *quote.VolumeRatio
		candidate.VolumeRatio = &value
	}
	if quote.MainNetInflow != nil {
		value := *quote.MainNetInflow
		candidate.MainNetInflow = &value
	}
	if quote.MainFlowPct != nil {
		value := *quote.MainFlowPct
		candidate.MainNetInflowPct = &value
	}
	turnoverAmount := float64(quote.Turnover)
	candidate.TurnoverAmount = &turnoverAmount
	progress := clamp01(change / limitRatio)
	turnoverQuality := clamp01(turnover / 0.08)
	weightedScore := 0.55*progress + 0.20*turnoverQuality
	activeWeight := 0.75
	if quote.VolumeRatio != nil {
		weightedScore += 0.15 * clamp01(*quote.VolumeRatio/2)
		activeWeight += 0.15
	}
	if quote.MainFlowPct != nil {
		weightedScore += 0.10 * clamp01(0.5+*quote.MainFlowPct/20)
		activeWeight += 0.10
	}
	score := weightedScore / activeWeight
	if candidate.TouchedLimit {
		score += 0.08
	}
	return clamp01(score), true
}

type candidateRisk struct {
	score float64
	flags []string
}

func candidateRiskScore(candidate *TodayCandidate) candidateRisk {
	score := 1.0
	flags := make([]string, 0, 6)
	if isSTStock(candidate.Name) {
		score -= 0.30
		flags = append(flags, "ST")
	}
	if candidate.ChangePercent != nil && *candidate.ChangePercent < -2 {
		score -= 0.25
		flags = append(flags, "当日跌幅超过2%")
	}
	if candidate.TurnoverRate != nil && *candidate.TurnoverRate < 1 {
		score -= 0.10
		flags = append(flags, "换手不足1%")
	}
	if candidate.TurnoverRate != nil && *candidate.TurnoverRate >= 20 {
		score -= 0.10
		flags = append(flags, "换手超过20%")
	}
	if candidate.VolumeRatio != nil && *candidate.VolumeRatio < 0.5 {
		score -= 0.10
		flags = append(flags, "量比不足0.5")
	}
	if candidate.TurnoverAmount != nil && *candidate.TurnoverAmount < 20_000_000 {
		score -= 0.10
		flags = append(flags, "成交额不足2000万")
	}
	if candidate.LastSealDate == "" && candidate.SealDays == 0 {
		score -= 0.15
		flags = append(flags, "近一年无收盘封板")
	}
	if age, known := listingAgeDays(candidate.listingDate, candidate.quoteFetchedAt); known && age >= 0 && age < 60 {
		score -= 0.20
		flags = append(flags, "上市不足60日")
	}
	if candidate.LiveAvailable && candidate.DistanceToLimit != nil &&
		*candidate.DistanceToLimit <= 0.1 && !candidate.TouchedLimit {
		score -= 0.05
		flags = append(flags, "临近涨停未触板")
	}
	if score < 0 {
		score = 0
	}
	return candidateRisk{score: score, flags: flags}
}

func limitUpRatio(code string) float64 {
	switch {
	case strings.HasPrefix(code, "300"), strings.HasPrefix(code, "301"),
		strings.HasPrefix(code, "688"), strings.HasPrefix(code, "689"):
		return 0.20
	case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "8"), strings.HasPrefix(code, "92"):
		return 0.30
	default:
		return 0.10
	}
}

func limitPrice(previousClose, ratio float64) float64 {
	return math.Round(previousClose*(1+ratio)*100) / 100
}

func isSTStock(name string) bool {
	return strings.Contains(strings.ToUpper(strings.TrimSpace(name)), "ST")
}

func listingAgeDays(listingDate string, reference time.Time) (int, bool) {
	if listingDate == "" || reference.IsZero() {
		return 0, false
	}
	listed, err := time.ParseInLocation("20060102", listingDate, Shanghai())
	if err != nil {
		return 0, false
	}
	return int(reference.In(Shanghai()).Sub(listed).Hours() / 24), true
}

func clamp01(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func normalise(value, maximum float64) float64 {
	if maximum <= 0 || math.IsNaN(value) || math.IsInf(value, 0) ||
		math.IsNaN(maximum) || math.IsInf(maximum, 0) || value <= 0 {
		return 0
	}
	return math.Min(1, value/maximum)
}
