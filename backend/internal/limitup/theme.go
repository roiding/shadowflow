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
// industry/concept membership: the labels come from the historical Top20
// theme blocks and are therefore independent of the slower board taxonomy.
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

// ComputeThemeMainline finds themes shared by at least two currently sealed
// members. Unclassified buckets are excluded, and themes carried by many
// stocks receive a smaller IDF weight so the result does not collapse into a
// few ubiquitous labels.
func ComputeThemeMainline(members []TodayMember, profiles map[string]ThemeStockProfile) []ThemeCluster {
	if len(members) == 0 || len(profiles) == 0 {
		return []ThemeCluster{}
	}
	type aggregate struct {
		code    string
		name    string
		members int
		weight  float64
		recency float64
	}
	documentFrequency := themeDocumentFrequency(profiles)
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
				item = &aggregate{code: tag.Code, name: tag.Name}
				byTheme[tag.Code] = item
			}
			item.members++
			item.weight += (1 + member.CoreScore/100) * themeIDF(tag.Code, documentFrequency, len(profiles))
			item.recency = math.Max(item.recency, themeRecency(tag, member.LastLimitAt))
		}
	}
	list := make([]aggregate, 0, len(byTheme))
	for _, item := range byTheme {
		// A label carried by only one sealed stock is not a shared theme,
		// even when that stock is the strongest stock in the pool.
		if item.members >= 2 {
			list = append(list, *item)
		}
	}
	if len(list) == 0 {
		return []ThemeCluster{}
	}
	maxWeight := 0.0
	for index := range list {
		list[index].weight *= 0.7 + 0.3*clamp01(list[index].recency)
		maxWeight = math.Max(maxWeight, list[index].weight)
	}
	if maxWeight <= 0 {
		maxWeight = 1
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
	if len(list) > 12 {
		list = list[:12]
	}
	result := make([]ThemeCluster, 0, len(list))
	for _, item := range list {
		result = append(result, ThemeCluster{
			Code: item.code, Name: item.name, MemberCount: item.members,
			Weight: item.weight / maxWeight, Recency: clamp01(item.recency),
		})
	}
	return result
}

// themeRecency distinguishes a label that the market is using now from one
// that merely exists in the stock's 365-day profile. It uses the label's last
// appearance and deliberately stays in [0,1]; the mainline weight is still
// driven by current member breadth and idf.
func themeRecency(tag ThemeTag, lastSealAt *time.Time) float64 {
	if tag.LastDate == "" {
		return 0
	}
	last, err := time.ParseInLocation("2006-01-02", tag.LastDate, Shanghai())
	if err != nil {
		return 0
	}
	reference := time.Time{}
	if lastSealAt != nil {
		reference = *lastSealAt
	}
	if reference.IsZero() {
		reference = time.Now().In(Shanghai())
	}
	days := reference.Sub(last).Hours() / 24
	if days < 0 {
		days = 0
	}
	// A label used in the current week keeps most of its weight; a label that
	// has not appeared for two months retains only the minimum.
	return 1 / (1 + days/21)
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
			byTheme[tag.Code] = themeIDF(tag.Code, documentFrequency, len(profiles))
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

func themeMatchScore(tags []ThemeTag, mainline []ThemeCluster) (float64, []ThemeTag) {
	tags = normaliseThemeTags(tags)
	if len(tags) == 0 || len(mainline) == 0 {
		return 0, []ThemeTag{}
	}
	weights := make(map[string]float64, len(mainline))
	for _, cluster := range mainline {
		weights[cluster.Code] = cluster.Weight
	}
	type weightedTag struct {
		tag    ThemeTag
		weight float64
	}
	seen := make(map[string]struct{}, len(tags))
	weighted := make([]weightedTag, 0, len(tags))
	for _, tag := range tags {
		weight, exists := weights[tag.Code]
		if !exists {
			continue
		}
		if _, duplicate := seen[tag.Code]; duplicate {
			continue
		}
		seen[tag.Code] = struct{}{}
		weighted = append(weighted, weightedTag{tag: tag, weight: weight})
	}
	if len(weighted) == 0 {
		return 0, []ThemeTag{}
	}
	sort.SliceStable(weighted, func(left, right int) bool {
		if weighted[left].weight != weighted[right].weight {
			return weighted[left].weight > weighted[right].weight
		}
		if weighted[left].tag.Occurrences != weighted[right].tag.Occurrences {
			return weighted[left].tag.Occurrences > weighted[right].tag.Occurrences
		}
		return weighted[left].tag.Code < weighted[right].tag.Code
	})
	// A stock matching a single theme should not automatically receive a
	// perfect score. Reward the strongest match, with a small contribution
	// from the second match; broad tags then cannot saturate the mainline.
	score := weighted[0].weight
	if len(weighted) > 1 {
		score = 0.8*weighted[0].weight + 0.2*weighted[1].weight
	} else {
		score *= 0.8
	}
	if score > 1 {
		score = 1
	}
	matched := make([]ThemeTag, 0, min(5, len(weighted)))
	for _, item := range weighted[:min(5, len(weighted))] {
		matched = append(matched, item.tag)
	}
	return score, matched
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
		themeScore, matched := 0.0, []ThemeTag{}
		if exists {
			themeScore, matched = themeMatchScore(profile.Tags, mainline)
			if len(matched) == 0 {
				matched = topThemeTags(profile.Tags, 3)
			}
		}
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
		themeScore, matched := themeMatchScore(profile.Tags, mainline)
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
			Themes: []ThemeRef{}, TopLinks: []CandidateLink{},
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
			if _, duplicate := seen[tag.Code]; duplicate {
				continue
			}
			seen[tag.Code] = struct{}{}
			result[tag.Code]++
		}
	}
	return result
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
		if tag.RecentOccurrences > existing.RecentOccurrences {
			existing.RecentOccurrences = tag.RecentOccurrences
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
		if tags[left].Occurrences != tags[right].Occurrences {
			return tags[left].Occurrences > tags[right].Occurrences
		}
		if tags[left].LastDate != tags[right].LastDate {
			return tags[left].LastDate > tags[right].LastDate
		}
		return tags[left].Code < tags[right].Code
	})
}
