package limitup

import (
	"math"
	"sort"
	"strings"
	"time"
)

var unclassifiedThemeNames = []string{
	"其他", "其它", "未分类", "未归类", "原因不明", "无明确原因",
	"无原因", "未知", "待分类", "未明确", "综合题材", "其他题材", "其他概念",
}

var unclassifiedThemePrefixes = []string{
	"其他概念", "其他题材", "未分类", "未归类", "原因不明",
	"无明确原因", "待分类", "未明确",
}

// BuildThemeAnalysis turns the accumulated THS theme labels into a
// stock-to-stock clustering view. It deliberately does not use Eastmoney
// industry/concept membership: the labels come from the historical limit-up
// chart categories and are therefore independent of the slower board taxonomy.
func BuildThemeAnalysis(centerCode string, profiles map[string]ThemeStockProfile, rows []ThemeCooccurrence, rawLinks []Link) ThemeAnalysis {
	center, ok := profiles[centerCode]
	if !ok {
		return ThemeAnalysis{Links: []ThemeLink{}, Profile: ThemeStockProfile{Code: centerCode, Tags: []ThemeTag{}}}
	}
	center.Tags = normaliseThemeTags(center.Tags)

	type peerAggregate struct {
		code   string
		name   string
		dates  map[string]struct{}
		themes map[string]ThemeTag
	}
	peers := make(map[string]*peerAggregate)
	for _, row := range rows {
		if row.PeerCode == "" || row.PeerCode == centerCode || row.TradeDate == "" {
			continue
		}
		peer := peers[row.PeerCode]
		if peer == nil {
			peer = &peerAggregate{
				code: row.PeerCode, name: row.PeerName,
				dates: make(map[string]struct{}), themes: make(map[string]ThemeTag),
			}
			peers[row.PeerCode] = peer
		}
		if peer.name == "" {
			peer.name = row.PeerName
		}
		peer.dates[row.TradeDate] = struct{}{}
		if _, exists := peer.themes[row.ThemeCode]; !exists {
			peer.themes[row.ThemeCode] = ThemeTag{Code: row.ThemeCode, Name: row.ThemeName}
		}
	}

	rawByCode := make(map[string]Link, len(rawLinks))
	for _, link := range rawLinks {
		rawByCode[link.Code] = link
	}

	themeDocumentFrequency := themeDocumentFrequency(profiles)
	centerWeights := themeVectorWeights(center.Tags, themeDocumentFrequency, len(profiles))
	links := make([]ThemeLink, 0, len(peers))
	for code, peer := range peers {
		peerProfile, hasProfile := profiles[code]
		peerTags := []ThemeTag(nil)
		if hasProfile {
			peerTags = normaliseThemeTags(peerProfile.Tags)
		}
		peerWeights := themeVectorWeights(peerTags, themeDocumentFrequency, len(profiles))

		sharedThemes := make([]ThemeTag, 0, len(peer.themes))
		for _, tag := range center.Tags {
			if _, shared := peer.themes[tag.Code]; shared {
				sharedThemes = append(sharedThemes, tag)
			}
		}
		sortThemeTags(sharedThemes)

		themeDays := len(peer.dates)
		peerLabelDays := 0
		if hasProfile {
			peerLabelDays = peerProfile.LabelDays
		}
		denominator := center.LabelDays + max(peerLabelDays, themeDays) - themeDays
		themeJaccard := 0.0
		if denominator > 0 {
			themeJaccard = float64(themeDays) / float64(denominator)
		}

		sharedDates := make([]string, 0, len(peer.dates))
		for date := range peer.dates {
			sharedDates = append(sharedDates, date)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(sharedDates)))

		coSealDays := 0
		if raw, exists := rawByCode[code]; exists {
			coSealDays = raw.OverlapDays
		}
		if coSealDays < themeDays {
			// A shared theme day is itself a same-day sealed pair. The raw
			// link can be absent when the user's min_overlap threshold is
			// higher than this peer's raw co-seal count.
			coSealDays = themeDays
		}
		purity := 0.0
		if coSealDays > 0 {
			purity = float64(themeDays) / float64(coSealDays)
			if purity > 1 {
				purity = 1
			}
		}
		labelCoverage := 0.0
		if hasProfile && peerLabelDays > 0 {
			labelCoverage = float64(themeDays) / float64(peerLabelDays)
			if labelCoverage > 1 {
				labelCoverage = 1
			}
		}
		lastSharedDate := ""
		if len(sharedDates) > 0 {
			lastSharedDate = sharedDates[0]
		}
		links = append(links, ThemeLink{
			Code: code, Name: peer.name, SharedThemes: sharedThemes,
			ThemeDays: themeDays, ThemeJaccard: themeJaccard,
			ThemeSimilarity: cosineSimilarity(centerWeights, peerWeights),
			ThemePurity:     purity, LabelCoverage: labelCoverage,
			CoSealDays: coSealDays, LastSharedDate: lastSharedDate,
			SharedDates: sharedDates,
		})
	}
	sort.SliceStable(links, func(left, right int) bool {
		if links[left].ThemeDays != links[right].ThemeDays {
			return links[left].ThemeDays > links[right].ThemeDays
		}
		if links[left].ThemeSimilarity != links[right].ThemeSimilarity {
			return links[left].ThemeSimilarity > links[right].ThemeSimilarity
		}
		if links[left].CoSealDays != links[right].CoSealDays {
			return links[left].CoSealDays > links[right].CoSealDays
		}
		return links[left].Code < links[right].Code
	})
	center.LabelDays = max(center.LabelDays, 0)
	return ThemeAnalysis{Profile: center, Links: links, TotalLinks: len(links), LabelDays: center.LabelDays}
}

type themePenalty struct {
	brokenCodes map[string]struct{}
	downCodes   map[string]struct{}
}

// ComputeThemeMainline finds themes shared by at least two currently sealed
// members. Unclassified buckets are excluded, and themes carried by many
// stocks receive a smaller IDF weight so the result does not collapse into a
// few ubiquitous labels. Broken and limit-down members are separate live
// evidence: they reduce the theme's seal quality without changing its sealed
// membership.
func ComputeThemeMainline(
	members []TodayMember,
	profiles map[string]ThemeStockProfile,
	asOf time.Time,
	brokenEvents []Event,
	downEvents []Event,
) []ThemeCluster {
	if len(members) == 0 || len(profiles) == 0 {
		return []ThemeCluster{}
	}
	type aggregate struct {
		code        string
		name        string
		memberCodes map[string]struct{}
		sources     map[string]struct{}
		weight      float64
		lastSealAt  *time.Time
		penalty     *themePenalty
		brokenCount int
		downCount   int
		freshness   float64
		sealQuality float64
	}
	documentFrequency := themeDocumentFrequency(profiles)
	sealedCodes := make(map[string]struct{}, len(members))
	for _, member := range members {
		sealedCodes[member.Code] = struct{}{}
	}
	penaltyByTheme := buildThemePenalties(profiles, sealedCodes, brokenEvents, downEvents)
	byTheme := make(map[string]*aggregate)
	for _, member := range members {
		profile, exists := profiles[member.Code]
		if !exists {
			continue
		}
		seen := make(map[string]struct{}, len(profile.Tags))
		for _, tag := range normaliseThemeTags(profile.Tags) {
			if _, duplicate := seen[tag.Code]; duplicate {
				continue
			}
			seen[tag.Code] = struct{}{}
			item := byTheme[tag.Code]
			if item == nil {
				item = &aggregate{
					code: tag.Code, name: tag.Name,
					memberCodes: make(map[string]struct{}),
					sources:     make(map[string]struct{}),
					penalty:     penaltyByTheme[tag.Code],
				}
				byTheme[tag.Code] = item
			}
			item.memberCodes[member.Code] = struct{}{}
			source := tag.Source
			if source == "" {
				source = ThemeSourceTHS
			}
			item.sources[source] = struct{}{}
			item.weight += (1 + member.CoreScore/100) * themeTagWeight(tag, documentFrequency, len(profiles))
			lastSealAt := member.LastLimitAt
			if lastSealAt == nil {
				lastSealAt = member.FirstLimitAt
			}
			if lastSealAt != nil && (item.lastSealAt == nil || lastSealAt.After(*item.lastSealAt)) {
				value := *lastSealAt
				item.lastSealAt = &value
			}
		}
	}
	list := make([]aggregate, 0, len(byTheme))
	for _, item := range byTheme {
		// A label carried by only one sealed stock is not a shared theme,
		// even when that stock is the strongest stock in the pool.
		if len(item.memberCodes) >= 2 {
			list = append(list, *item)
		}
	}
	if len(list) == 0 {
		return []ThemeCluster{}
	}
	maxWeight := 0.0
	for index := range list {
		if list[index].penalty != nil {
			list[index].downCount = len(list[index].penalty.downCodes)
			for code := range list[index].penalty.brokenCodes {
				if _, isDown := list[index].penalty.downCodes[code]; !isDown {
					list[index].brokenCount++
				}
			}
		}
		list[index].freshness = themeFreshness(
			list[index].lastSealAt, asOf, len(list[index].memberCodes),
		)
		list[index].sealQuality = themeSealQuality(
			len(list[index].memberCodes), list[index].brokenCount, list[index].downCount,
		)
		list[index].weight *= list[index].freshness
		list[index].weight *= list[index].sealQuality
		maxWeight = math.Max(maxWeight, list[index].weight)
	}
	if maxWeight <= 0 {
		maxWeight = 1
	}
	sort.Slice(list, func(left, right int) bool {
		if list[left].weight != list[right].weight {
			return list[left].weight > list[right].weight
		}
		if len(list[left].memberCodes) != len(list[right].memberCodes) {
			return len(list[left].memberCodes) > len(list[right].memberCodes)
		}
		return list[left].code < list[right].code
	})
	if len(list) > 12 {
		list = list[:12]
	}
	result := make([]ThemeCluster, 0, len(list))
	for _, item := range list {
		memberCodes := make([]string, 0, len(item.memberCodes))
		for code := range item.memberCodes {
			memberCodes = append(memberCodes, code)
		}
		sort.Strings(memberCodes)
		result = append(result, ThemeCluster{
			Code: item.code, Name: item.name, MemberCount: len(memberCodes),
			Weight: item.weight / maxWeight, Sources: sortedThemeSources(item.sources),
			Freshness: item.freshness, SealQuality: item.sealQuality,
			LastSealAt: item.lastSealAt, BrokenCount: item.brokenCount, DownCount: item.downCount,
			MemberCodes: memberCodes,
		})
	}
	return result
}

func sortedThemeSources(sources map[string]struct{}) []string {
	result := make([]string, 0, len(sources))
	for source := range sources {
		result = append(result, source)
	}
	sort.Strings(result)
	return result
}

func buildThemePenalties(
	profiles map[string]ThemeStockProfile,
	sealedCodes map[string]struct{},
	brokenEvents, downEvents []Event,
) map[string]*themePenalty {
	result := make(map[string]*themePenalty)
	add := func(events []Event, isDown bool) {
		for _, event := range events {
			if _, sealed := sealedCodes[event.Code]; sealed {
				continue
			}
			profile, exists := profiles[event.Code]
			if !exists {
				continue
			}
			seen := make(map[string]struct{}, len(profile.Tags))
			for _, tag := range normaliseThemeTags(profile.Tags) {
				if _, duplicate := seen[tag.Code]; duplicate {
					continue
				}
				seen[tag.Code] = struct{}{}
				item := result[tag.Code]
				if item == nil {
					item = &themePenalty{
						brokenCodes: make(map[string]struct{}),
						downCodes:   make(map[string]struct{}),
					}
					result[tag.Code] = item
				}
				if isDown {
					item.downCodes[event.Code] = struct{}{}
				} else {
					item.brokenCodes[event.Code] = struct{}{}
				}
			}
		}
	}
	add(brokenEvents, false)
	add(downEvents, true)
	return result
}

func themeFreshness(lastSealAt *time.Time, asOf time.Time, memberCount int) float64 {
	if lastSealAt == nil || asOf.IsZero() {
		return 1
	}
	ageMinutes := intradayElapsedMinutes(*lastSealAt, asOf)
	if ageMinutes <= 0 {
		return 1
	}
	// Use the latest reseal time, not the first touch. A stock that opened and
	// resealed refreshes the direction. Lunch break is excluded from the age
	// because no new limit-up can occur while the market is closed. The
	// breadth buffer uses only currently sealed members sharing this theme,
	// never the whole market or unsealed constituents.
	baseDecay := math.Exp(-ageMinutes / 90)
	breadth := float64(max(0, memberCount)) / float64(max(1, memberCount)+5)
	return clamp01(baseDecay + (1-baseDecay)*breadth)
}

func intradayElapsedMinutes(start, end time.Time) float64 {
	start, end = start.In(Shanghai()), end.In(Shanghai())
	if !end.After(start) {
		return 0
	}
	if start.Format("2006-01-02") != end.Format("2006-01-02") {
		return end.Sub(start).Minutes()
	}
	total := 0.0
	for _, session := range [][2]int{{9*60 + 30, 11*60 + 30}, {13 * 60, 15 * 60}} {
		sessionStart := time.Date(start.Year(), start.Month(), start.Day(), session[0]/60, session[0]%60, 0, 0, Shanghai())
		sessionEnd := time.Date(start.Year(), start.Month(), start.Day(), session[1]/60, session[1]%60, 0, 0, Shanghai())
		if start.After(sessionStart) {
			sessionStart = start
		}
		if end.Before(sessionEnd) {
			sessionEnd = end
		}
		if sessionEnd.After(sessionStart) {
			total += sessionEnd.Sub(sessionStart).Minutes()
		}
	}
	return total
}

func themeSealQuality(sealedCount, brokenCount, downCount int) float64 {
	if sealedCount <= 0 {
		return 0
	}
	// A broken board is a partial loss of a seal; a limit-down is a full loss
	// and therefore receives twice the penalty weight. This ratio is bounded
	// in [0,1]: one broken board among many sealed members is less destructive
	// than one among a small cluster, while freshness still has no breadth
	// exemption.
	penalty := 0.5*float64(max(0, brokenCount)) + float64(max(0, downCount))
	return clamp01(float64(sealedCount) / (float64(sealedCount) + penalty))
}

type memberThemeAffinity struct {
	neighbors int
	strength  float64
}

// computeMemberThemeAffinity measures how strongly each sealed member is
// connected to the other sealed members through accumulated THS labels. It is
// independent of the raw same-day co-seal graph, so a theme cluster can still
// surface a core when the stocks have not historically sealed on the same day.
func computeMemberThemeAffinity(members []TodayMember, profiles map[string]ThemeStockProfile) map[string]memberThemeAffinity {
	result := make(map[string]memberThemeAffinity, len(members))
	if len(members) < 2 || len(profiles) == 0 {
		return result
	}
	documentFrequency := themeDocumentFrequency(profiles)
	weights := make(map[string]map[string]float64, len(members))
	for _, member := range members {
		profile, exists := profiles[member.Code]
		if !exists {
			result[member.Code] = memberThemeAffinity{}
			continue
		}
		byTheme := make(map[string]float64)
		for _, tag := range normaliseThemeTags(profile.Tags) {
			byTheme[tag.Code] = themeTagWeight(tag, documentFrequency, len(profiles))
		}
		weights[member.Code] = byTheme
		result[member.Code] = memberThemeAffinity{}
	}
	for leftIndex := 0; leftIndex < len(members); leftIndex++ {
		left := members[leftIndex].Code
		leftWeights := weights[left]
		if len(leftWeights) == 0 {
			continue
		}
		for rightIndex := leftIndex + 1; rightIndex < len(members); rightIndex++ {
			right := members[rightIndex].Code
			rightWeights := weights[right]
			if len(rightWeights) == 0 {
				continue
			}
			sharedWeight := 0.0
			smaller, larger := leftWeights, rightWeights
			if len(smaller) > len(larger) {
				smaller, larger = larger, smaller
			}
			for themeCode, weight := range smaller {
				if _, shared := larger[themeCode]; shared {
					sharedWeight += weight
				}
			}
			if sharedWeight <= 0 {
				continue
			}
			leftAffinity := result[left]
			leftAffinity.neighbors++
			leftAffinity.strength += sharedWeight
			result[left] = leftAffinity
			rightAffinity := result[right]
			rightAffinity.neighbors++
			rightAffinity.strength += sharedWeight
			result[right] = rightAffinity
		}
	}
	return result
}

func topThemeTags(tags []ThemeTag, limit int) []ThemeTag {
	result := normaliseThemeTags(tags)
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result
}

type themeFit struct {
	Cluster ThemeCluster
	Tag     ThemeTag
	Fit     float64
}

func themeTagEvidence(tag ThemeTag) float64 {
	occurrences := math.Max(0, float64(tag.Occurrences))
	recent := math.Max(0, float64(tag.RecentOccurrences))
	evidence := occurrences + recent
	return evidence / (evidence + 2)
}

func candidateThemeFits(tags []ThemeTag, mainline []ThemeCluster) []themeFit {
	tags = normaliseThemeTags(tags)
	if len(tags) == 0 || len(mainline) == 0 {
		return []themeFit{}
	}
	clusters := make(map[string]ThemeCluster, len(mainline))
	for _, cluster := range mainline {
		clusters[cluster.Code] = cluster
	}
	seen := make(map[string]struct{}, len(tags))
	fits := make([]themeFit, 0, len(tags))
	for _, tag := range tags {
		cluster, exists := clusters[tag.Code]
		if !exists {
			continue
		}
		if _, duplicate := seen[tag.Code]; duplicate {
			continue
		}
		seen[tag.Code] = struct{}{}
		fit := clamp01(cluster.Weight * themeTagEvidence(tag))
		if fit <= 0 {
			continue
		}
		fits = append(fits, themeFit{Cluster: cluster, Tag: tag, Fit: fit})
	}
	sort.SliceStable(fits, func(left, right int) bool {
		if fits[left].Fit != fits[right].Fit {
			return fits[left].Fit > fits[right].Fit
		}
		if fits[left].Tag.Occurrences != fits[right].Tag.Occurrences {
			return fits[left].Tag.Occurrences > fits[right].Tag.Occurrences
		}
		return fits[left].Tag.Code < fits[right].Tag.Code
	})
	return fits
}

func themeFitScore(fits []themeFit) float64 {
	if len(fits) == 0 {
		return 0
	}
	// Noisy-OR treats every matched mainline as an independent piece of
	// evidence. A second and third direction increase resonance with
	// diminishing returns instead of being forced into a fixed 80/20 split.
	remaining := 1.0
	for _, fit := range fits {
		remaining *= 1 - clamp01(fit.Fit)
	}
	return clamp01(1 - remaining)
}

func themeTagsFromFits(fits []themeFit, limit int) []ThemeTag {
	if limit <= 0 {
		limit = len(fits)
	}
	if len(fits) > limit {
		fits = fits[:limit]
	}
	tags := make([]ThemeTag, 0, len(fits))
	for _, fit := range fits {
		tags = append(tags, fit.Tag)
	}
	return tags
}

func themeMatchScore(tags []ThemeTag, mainline []ThemeCluster) (float64, []ThemeTag) {
	fits := candidateThemeFits(tags, mainline)
	return themeFitScore(fits), themeTagsFromFits(fits, 5)
}

// applyTodayThemeProfiles blends the historical THS cluster score into the
// existing co-seal candidate score and adds theme-only candidates that have no
// direct co-seal edge with today's members. The latter remain explicitly
// separate through their zero EffectiveLinks/Strength fields.
func applyTodayThemeProfiles(
	candidates []TodayCandidate,
	mainline []ThemeCluster,
	profiles map[string]ThemeStockProfile,
	members map[string]Event,
	allSealDates, allTouchDates map[string]map[string]struct{},
	lastSealDate map[string]string,
	candidatePairs map[string]*todayPair,
	minOverlap int,
) []TodayCandidate {
	byCode := make(map[string]int, len(candidates))
	for index := range candidates {
		byCode[candidates[index].Code] = index
		profile, exists := profiles[candidates[index].Code]
		fits, themeScore, matched := []themeFit{}, 0.0, []ThemeTag{}
		if exists {
			fits = candidateThemeFits(profile.Tags, mainline)
			themeScore = themeFitScore(fits)
			matched = themeTagsFromFits(fits, 5)
			if len(matched) == 0 {
				matched = topThemeTags(profile.Tags, 3)
			}
		}
		candidates[index].themeFits = fits
		candidates[index].ThemeScore = themeScore
		candidates[index].ThemeTags = matched
	}
	for code, profile := range profiles {
		if _, sealed := members[code]; sealed {
			continue
		}
		if _, exists := byCode[code]; exists {
			continue
		}
		fits := candidateThemeFits(profile.Tags, mainline)
		themeScore := themeFitScore(fits)
		matched := themeTagsFromFits(fits, 5)
		if themeScore <= 0 || len(matched) == 0 {
			continue
		}
		sealDays := len(allSealDates[code])
		touchDays := len(allTouchDates[code])
		var links []candidateRawLink
		for memberCode := range members {
			key := code + ":" + memberCode
			if pair := candidatePairs[key]; pair != nil && pair.overlap >= minOverlap {
				links = append(links, candidateRawLink{
					memberCode: memberCode, overlap: pair.overlap, heat: pair.heat,
					dates: append([]string(nil), pair.dates...),
				})
			}
		}
		candidates = append(candidates, TodayCandidate{
			Code: code, Name: profile.Name, MarketType: profile.MarketType,
			SealDays:     sealDays,
			TouchDays:    touchDays,
			LastSealDate: lastSealDate[code],
			ThemeScore:   themeScore, ThemeTags: matched,
			themeFits: fits,
			Themes:    []ThemeRef{}, TopLinks: []CandidateLink{},
			EffectiveLinks: len(links),
			Coverage:       float64(len(links)) / float64(max(1, len(members))),
			RiskFlags:      []string{},
		})
	}
	return candidates
}

func themeDocumentFrequency(profiles map[string]ThemeStockProfile) map[string]int {
	result := make(map[string]int)
	for _, profile := range profiles {
		seen := make(map[string]struct{}, len(profile.Tags))
		for _, tag := range normaliseThemeTags(profile.Tags) {
			if tag.Source == ThemeSourceEastmoneyConcept || tag.Source == ThemeSourceEastmoneyIndustry {
				continue
			}
			if _, duplicate := seen[tag.Code]; duplicate {
				continue
			}
			seen[tag.Code] = struct{}{}
			result[tag.Code]++
		}
	}
	return result
}

func themeTagWeight(tag ThemeTag, documentFrequency map[string]int, documents int) float64 {
	if tag.weight > 0 {
		return tag.weight
	}
	return themeIDF(tag.Code, documentFrequency, documents)
}

func themeIDF(code string, documentFrequency map[string]int, documents int) float64 {
	if documents <= 0 {
		return 1
	}
	return math.Log(float64(1+documents)/float64(1+documentFrequency[code])) + 1
}

func themeVectorWeights(tags []ThemeTag, documentFrequency map[string]int, documents int) map[string]float64 {
	result := make(map[string]float64, len(tags))
	for _, tag := range tags {
		if tag.Code == "" || tag.Occurrences <= 0 {
			continue
		}
		result[tag.Code] = float64(tag.Occurrences) * themeIDF(tag.Code, documentFrequency, documents)
	}
	return result
}

func cosineSimilarity(left, right map[string]float64) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	dot := 0.0
	for code, leftValue := range left {
		if rightValue, exists := right[code]; exists {
			dot += leftValue * rightValue
		}
	}
	leftNorm, rightNorm := 0.0, 0.0
	for _, value := range left {
		leftNorm += value * value
	}
	for _, value := range right {
		rightNorm += value * value
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / math.Sqrt(leftNorm*rightNorm)
}

func normaliseThemeTags(tags []ThemeTag) []ThemeTag {
	byCode := make(map[string]ThemeTag, len(tags))
	for _, tag := range tags {
		if tag.Code == "" || tag.Name == "" || isUnclassifiedThemeName(tag.Name) {
			continue
		}
		existing, exists := byCode[tag.Code]
		if !exists {
			byCode[tag.Code] = tag
			continue
		}
		existing.Occurrences += tag.Occurrences
		if tag.weight > existing.weight {
			existing.weight = tag.weight
		}
		if tag.RecentOccurrences > existing.RecentOccurrences {
			existing.RecentOccurrences = tag.RecentOccurrences
		}
		if existing.Source == "" && tag.Source != "" {
			existing.Source = tag.Source
		}
		if existing.FirstDate == "" || (tag.FirstDate != "" && tag.FirstDate < existing.FirstDate) {
			existing.FirstDate = tag.FirstDate
		}
		if tag.LastDate > existing.LastDate {
			existing.LastDate = tag.LastDate
			existing.Name = tag.Name
		}
		byCode[tag.Code] = existing
	}
	result := make([]ThemeTag, 0, len(byCode))
	for _, tag := range byCode {
		result = append(result, tag)
	}
	sortThemeTags(result)
	return result
}

func isUnclassifiedThemeName(name string) bool {
	name = strings.TrimSpace(name)
	for _, term := range unclassifiedThemeNames {
		if name == term {
			return true
		}
	}
	for _, prefix := range unclassifiedThemePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func sortThemeTags(tags []ThemeTag) {
	sort.SliceStable(tags, func(left, right int) bool {
		if tags[left].weight != tags[right].weight {
			return tags[left].weight > tags[right].weight
		}
		if tags[left].Occurrences != tags[right].Occurrences {
			return tags[left].Occurrences > tags[right].Occurrences
		}
		if tags[left].LastDate != tags[right].LastDate {
			return tags[left].LastDate > tags[right].LastDate
		}
		return tags[left].Code < tags[right].Code
	})
}
