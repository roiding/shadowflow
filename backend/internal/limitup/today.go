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

	// TodayCandidateWeight* rank stocks that are not sealed today by how
	// strongly they co-sealed with today's core members in the lookback window.
	TodayCandidateWeightStrength = 0.60
	TodayCandidateWeightCoverage = 0.15
	TodayCandidateWeightRecency  = 0.25
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
	ThemeLookup    func(codes []string) (map[string][]ThemeRef, error)
}

// Broad market attributes are published as concept boards by the data source,
// but they do not describe a tradable theme. Keeping them would make almost
// every candidate match a "main line" and flatten the theme contribution.
var broadConceptTerms = []string{
	"融资融券", "沪股通", "深股通", "QFII", "重仓", "新进", "金股",
	"证金持股", "大基金持股", "增仓", "减仓",
	"昨日", "最近", "近期", "百日新高", "历史新高", "热股", "题材股", "连续亏损",
	"预增", "预减", "扭亏", "首亏", "破发", "破增发", "破净",
	"市净率", "市盈率", "小盘", "中盘", "大盘", "微盘", "价值股", "成长股",
	"周期股", "超跌股", "趋势股", "微利股", "百元股", "低价股", "权重股",
	"红利股", "行业龙头", "风格", "外资背景", "股权", "举牌", "增持计划", "密集调研",
	"专精特新", "央国企改革",
	"标准普尔", "富时罗素", "MSCI中国", "HS300", "上证50", "上证180",
	"上证380", "中证500", "深证100", "深成500", "创业成份", "创业板综",
	"央视50", "宁组合", "茅指数", "ST股", "次新股", "转债标的",
	"AB股", "AH股", "B股", "GDR", "IPO受益",
}

func isThematicConcept(name string) bool {
	for _, term := range broadConceptTerms {
		if strings.Contains(name, term) {
			return false
		}
	}
	return true
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
	coreByCode := make(map[string]float64, len(membersOut))
	for _, member := range membersOut {
		coreByCode[member.Code] = member.CoreScore
	}
	candidatesOut := buildTodayCandidates(candidatePairs, members, coreByCode, len(membersOut),
		len(historyDays), input.MinOverlap, parsedDate,
		allSealDates, allTouchDates, lastSealDate, stockMeta)
	mainlineThemes := []MainlineTheme{}
	if input.ThemeLookup != nil && len(candidatesOut) > 0 {
		if memberThemes, err := input.ThemeLookup(codes); err == nil {
			mainlineThemes = computeMainlineThemes(memberThemes, coreByCode)
			if len(mainlineThemes) > 0 {
				candidateCodes := make([]string, 0, len(candidatesOut))
				for _, candidate := range candidatesOut {
					candidateCodes = append(candidateCodes, candidate.Code)
				}
				if candidateThemes, err := input.ThemeLookup(candidateCodes); err == nil {
					applyThemeScores(candidatesOut, mainlineThemes, candidateThemes)
				} else {
					mainlineThemes = []MainlineTheme{}
				}
			}
		}
	}
	sort.SliceStable(candidatesOut, func(left, right int) bool {
		if candidatesOut[left].Score != candidatesOut[right].Score {
			return candidatesOut[left].Score > candidatesOut[right].Score
		}
		if candidatesOut[left].Strength != candidatesOut[right].Strength {
			return candidatesOut[left].Strength > candidatesOut[right].Strength
		}
		if candidatesOut[left].EffectiveLinks != candidatesOut[right].EffectiveLinks {
			return candidatesOut[left].EffectiveLinks > candidatesOut[right].EffectiveLinks
		}
		return candidatesOut[left].Code < candidatesOut[right].Code
	})
	if len(candidatesOut) > input.CandidateLimit {
		candidatesOut = candidatesOut[:input.CandidateLimit]
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
		Candidates: candidatesOut, CandidateLimit: input.CandidateLimit,
		MainlineThemes: mainlineThemes,
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
	snapshotDate time.Time,
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
		candidate  TodayCandidate
		strength   float64
		lastShared string
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
			strength: strength, lastShared: lastShared,
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
			},
		})
	}
	for index := range items {
		item := &items[index]
		recency := 0.0
		if parsed, err := time.ParseInLocation("2006-01-02", item.lastShared, Shanghai()); err == nil {
			days := snapshotDate.Sub(parsed).Hours() / 24
			if days < 0 {
				days = 0
			}
			recency = 1 / (1 + days/30)
		}
		item.candidate.Score = 100 * (TodayCandidateWeightStrength*normalise(item.strength, maxStrength) +
			TodayCandidateWeightCoverage*item.candidate.Coverage +
			TodayCandidateWeightRecency*recency)
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].candidate.Score != items[right].candidate.Score {
			return items[left].candidate.Score > items[right].candidate.Score
		}
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

// computeMainlineThemes ranks today's industry and concept boards by the
// core-weighted number of sealed members. A board needs at least two sealed
// members to count as a main line.
func computeMainlineThemes(memberThemes map[string][]ThemeRef, coreByCode map[string]float64) []MainlineTheme {
	type boardAggregate struct {
		code    string
		name    string
		typ     string
		members int
		weight  float64
	}
	byBoard := make(map[string]*boardAggregate)
	for code, refs := range memberThemes {
		core := coreByCode[code]
		seen := make(map[string]struct{}, len(refs))
		for _, ref := range refs {
			if ref.Type == "concept" && !isThematicConcept(ref.Name) {
				continue
			}
			key := ref.Type + ":" + ref.Code
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			aggregate := byBoard[key]
			if aggregate == nil {
				aggregate = &boardAggregate{code: ref.Code, name: ref.Name, typ: ref.Type}
				byBoard[key] = aggregate
			}
			aggregate.members++
			aggregate.weight += 1 + core/100
		}
	}
	list := make([]boardAggregate, 0, len(byBoard))
	for _, aggregate := range byBoard {
		if aggregate.members >= 2 {
			list = append(list, *aggregate)
		}
	}
	sort.Slice(list, func(left, right int) bool {
		if list[left].weight != list[right].weight {
			return list[left].weight > list[right].weight
		}
		if list[left].members != list[right].members {
			return list[left].members > list[right].members
		}
		return list[left].code < list[right].code
	})
	industry := make([]boardAggregate, 0, 5)
	concept := make([]boardAggregate, 0, 5)
	for _, aggregate := range list {
		switch aggregate.typ {
		case "industry":
			if len(industry) < 5 {
				industry = append(industry, aggregate)
			}
		case "concept":
			if len(concept) < 5 {
				concept = append(concept, aggregate)
			}
		}
	}
	selected := append(industry, concept...)
	if len(selected) == 0 {
		return []MainlineTheme{}
	}
	maxWeight := 0.0
	for _, aggregate := range selected {
		maxWeight = math.Max(maxWeight, aggregate.weight)
	}
	result := make([]MainlineTheme, 0, len(selected))
	for _, aggregate := range selected {
		result = append(result, MainlineTheme{
			Code: aggregate.code, Name: aggregate.name, Type: aggregate.typ,
			Weight: aggregate.weight / maxWeight, MemberCount: aggregate.members,
		})
	}
	return result
}

// applyThemeScores blends theme alignment into the candidate score. The base
// 60/15/25 score keeps 75% of its weight; matching today's main-line boards
// contributes the remaining 25%.
func applyThemeScores(candidates []TodayCandidate, mainline []MainlineTheme, candidateThemes map[string][]ThemeRef) {
	index := make(map[string]MainlineTheme, len(mainline))
	for _, theme := range mainline {
		index[theme.Type+":"+theme.Code] = theme
	}
	for position := range candidates {
		matched := make([]ThemeRef, 0, 4)
		total := 0.0
		seen := make(map[string]struct{}, 4)
		for _, ref := range candidateThemes[candidates[position].Code] {
			theme, ok := index[ref.Type+":"+ref.Code]
			if !ok {
				continue
			}
			key := theme.Type + ":" + theme.Code
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			matched = append(matched, ThemeRef{
				Code: theme.Code, Name: theme.Name, Type: theme.Type, Weight: theme.Weight,
			})
			total += theme.Weight
		}
		if total > 1 {
			total = 1
		}
		sort.Slice(matched, func(left, right int) bool {
			if matched[left].Weight != matched[right].Weight {
				return matched[left].Weight > matched[right].Weight
			}
			return matched[left].Code < matched[right].Code
		})
		if len(matched) > 5 {
			matched = matched[:5]
		}
		candidates[position].ThemeScore = total
		candidates[position].Themes = matched
		candidates[position].Score = 0.75*candidates[position].Score + 25*total
	}
}

func normalise(value, maximum float64) float64 {
	if maximum <= 0 || math.IsNaN(value) || math.IsInf(value, 0) ||
		math.IsNaN(maximum) || math.IsInf(maximum, 0) || value <= 0 {
		return 0
	}
	return math.Min(1, value/maximum)
}
