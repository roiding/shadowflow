package limitup

import (
	"math"
	"testing"
	"time"
)

func TestBuildThemeAnalysisDeduplicatesSharedThemeDays(t *testing.T) {
	profiles := map[string]ThemeStockProfile{
		"000001": {
			Code: "000001", Name: "中心股", LabelDays: 3,
			Tags: []ThemeTag{
				{Code: "885517", Name: "机器人概念", Occurrences: 3, LastDate: "2026-11-06"},
				{Code: "885431", Name: "新能源汽车", Occurrences: 1, LastDate: "2026-11-04"},
			},
		},
		"000002": {
			Code: "000002", Name: "同题材股", LabelDays: 2,
			Tags: []ThemeTag{{Code: "885517", Name: "机器人概念", Occurrences: 2, LastDate: "2026-11-06"}},
		},
	}
	rows := []ThemeCooccurrence{
		{TradeDate: "2026-11-06", ThemeCode: "885517", ThemeName: "机器人概念", PeerCode: "000002", PeerName: "同题材股"},
		{TradeDate: "2026-11-05", ThemeCode: "885517", ThemeName: "机器人概念", PeerCode: "000002", PeerName: "同题材股"},
		// A second shared theme on the same day must not inflate the day count.
		{TradeDate: "2026-11-05", ThemeCode: "885431", ThemeName: "新能源汽车", PeerCode: "000002", PeerName: "同题材股"},
	}
	raw := []Link{{Code: "000002", Name: "同题材股", OverlapDays: 3}}
	analysis := BuildThemeAnalysis("000001", profiles, rows, raw)
	if analysis.TotalLinks != 1 || len(analysis.Links) != 1 {
		t.Fatalf("unexpected theme links: %+v", analysis)
	}
	link := analysis.Links[0]
	if link.Code != "000002" || link.ThemeDays != 2 || link.CoSealDays != 3 ||
		link.ThemePurity != float64(2)/3 || link.LastSharedDate != "2026-11-06" ||
		len(link.SharedThemes) != 2 || len(link.SharedDates) != 2 {
		t.Fatalf("unexpected theme link: %+v", link)
	}
	if link.ThemeJaccard <= 0 || link.ThemeSimilarity <= 0 || link.LabelCoverage <= 0 {
		t.Fatalf("theme similarity metrics were not calculated: %+v", link)
	}
}

func TestComputeThemeMainlineExcludesUnclassifiedBuckets(t *testing.T) {
	profiles := map[string]ThemeStockProfile{
		"000001": {
			Code: "000001", Name: "A", LabelDays: 3,
			Tags: []ThemeTag{
				{Code: "885517", Name: "机器人概念", Occurrences: 3},
				{Code: "000000", Name: "其他概念", Occurrences: 2},
			},
		},
		"000002": {
			Code: "000002", Name: "B", LabelDays: 3,
			Tags: []ThemeTag{
				{Code: "885517", Name: "机器人概念", Occurrences: 2},
				{Code: "000000", Name: "其他概念", Occurrences: 2},
			},
		},
	}
	mainline := ComputeThemeMainline([]TodayMember{
		{Code: "000001", CoreScore: 50},
		{Code: "000002", CoreScore: 40},
	}, profiles, time.Date(2026, 11, 11, 14, 0, 0, 0, Shanghai()), nil, nil)
	if len(mainline) != 1 || mainline[0].Code != "885517" || mainline[0].MemberCount != 2 {
		t.Fatalf("unclassified bucket leaked into mainline: %+v", mainline)
	}
}

func TestThemeMatchScoreIgnoresUnclassifiedTags(t *testing.T) {
	if isUnclassifiedThemeName("其他电子") {
		t.Fatal("a real theme containing 其他 must not be filtered")
	}
	score, tags := themeMatchScore([]ThemeTag{
		{Code: "000000", Name: "其他概念", Occurrences: 5},
		{Code: "885517", Name: "机器人概念", Occurrences: 2},
	}, []ThemeCluster{{Code: "885517", Name: "机器人概念", Weight: 0.8}})
	if math.Abs(score-0.4) > 1e-9 || len(tags) != 1 || tags[0].Code != "885517" {
		t.Fatalf("unexpected theme match: score=%v tags=%+v", score, tags)
	}
}

func TestThemeMatchScoreUsesAllMatchingMainlinesWithDiminishingReturns(t *testing.T) {
	tags := []ThemeTag{
		{Code: "885517", Name: "机器人概念", Occurrences: 4},
		{Code: "885431", Name: "新能源汽车", Occurrences: 4},
		{Code: "885806", Name: "华为概念", Occurrences: 4},
	}
	mainline := []ThemeCluster{
		{Code: "885517", Name: "机器人概念", Weight: 1},
		{Code: "885431", Name: "新能源汽车", Weight: 0.8},
		{Code: "885806", Name: "华为概念", Weight: 0.6},
	}
	fits := candidateThemeFits(tags, mainline)
	if len(fits) != 3 {
		t.Fatalf("all matching mainlines must be retained: %+v", fits)
	}
	expected := 1.0
	for _, fit := range fits {
		expected *= 1 - fit.Fit
	}
	expected = 1 - expected
	score, matched := themeMatchScore(tags, mainline)
	if math.Abs(score-expected) > 1e-9 || len(matched) != 3 {
		t.Fatalf("unexpected multi-mainline resonance: score=%v expected=%v tags=%+v", score, expected, matched)
	}
	if score <= fits[0].Fit {
		t.Fatalf("additional mainline matches must add resonance: score=%v best=%v", score, fits[0].Fit)
	}
}

func TestThemeMainlinePrefersRecentlyUsedLabel(t *testing.T) {
	recent := ThemeTag{Code: "885517", Name: "机器人概念", Occurrences: 3, LastDate: "2026-11-10"}
	stale := ThemeTag{Code: "885431", Name: "新能源汽车", Occurrences: 3, LastDate: "2026-01-10"}
	profiles := map[string]ThemeStockProfile{
		"000001": {Code: "000001", Name: "A", LabelDays: 3, Tags: []ThemeTag{recent, stale}},
		"000002": {Code: "000002", Name: "B", LabelDays: 3, Tags: []ThemeTag{recent, stale}},
	}
	mainline := ComputeThemeMainline([]TodayMember{
		{Code: "000001", CoreScore: 50, LastLimitAt: timePointer(time.Date(2026, 11, 11, 9, 30, 0, 0, Shanghai()))},
		{Code: "000002", CoreScore: 50, LastLimitAt: timePointer(time.Date(2026, 11, 11, 9, 30, 0, 0, Shanghai()))},
	}, profiles, time.Date(2026, 11, 11, 14, 0, 0, 0, Shanghai()), nil, nil)
	if len(mainline) != 2 || mainline[0].Code != recent.Code || mainline[0].Recency <= mainline[1].Recency {
		t.Fatalf("recently used label did not outrank stale label: %+v", mainline)
	}
}

func TestThemeMainlineUsesLastResealForFreshness(t *testing.T) {
	oldLimit := time.Date(2026, 11, 11, 9, 30, 0, 0, Shanghai())
	resealLimit := time.Date(2026, 11, 11, 13, 30, 0, 0, Shanghai())
	robot := ThemeTag{Code: "885517", Name: "机器人概念", Occurrences: 4, RecentOccurrences: 2}
	energy := ThemeTag{Code: "885431", Name: "新能源汽车", Occurrences: 4, RecentOccurrences: 2}
	profiles := map[string]ThemeStockProfile{
		"000001": {Code: "000001", Name: "旧方向甲", Tags: []ThemeTag{robot}},
		"000002": {Code: "000002", Name: "旧方向乙", Tags: []ThemeTag{robot}},
		"000003": {Code: "000003", Name: "新方向甲", Tags: []ThemeTag{energy}},
		"000004": {Code: "000004", Name: "新方向乙", Tags: []ThemeTag{energy}},
	}
	members := []TodayMember{
		{Code: "000001", CoreScore: 50, FirstLimitAt: &oldLimit, LastLimitAt: &oldLimit},
		{Code: "000002", CoreScore: 50, FirstLimitAt: &oldLimit, LastLimitAt: &oldLimit},
		{Code: "000003", CoreScore: 50, FirstLimitAt: &oldLimit, LastLimitAt: &resealLimit},
		{Code: "000004", CoreScore: 50, FirstLimitAt: &oldLimit, LastLimitAt: &resealLimit},
	}
	mainline := ComputeThemeMainline(members, profiles, time.Date(2026, 11, 11, 14, 0, 0, 0, Shanghai()), nil, nil)
	byCode := make(map[string]ThemeCluster, len(mainline))
	for _, theme := range mainline {
		byCode[theme.Code] = theme
	}
	oldTheme, oldExists := byCode[robot.Code]
	newTheme, newExists := byCode[energy.Code]
	if !oldExists || !newExists || oldTheme.Freshness >= newTheme.Freshness ||
		oldTheme.Weight >= newTheme.Weight {
		t.Fatalf("reseal freshness did not promote the newer direction: old=%+v new=%+v", oldTheme, newTheme)
	}
	if oldTheme.LastSealAt == nil || newTheme.LastSealAt == nil ||
		!oldTheme.LastSealAt.Equal(oldLimit) || !newTheme.LastSealAt.Equal(resealLimit) {
		t.Fatalf("last seal time was not preserved: old=%+v new=%+v", oldTheme, newTheme)
	}
}

func TestThemeFreshnessUsesSealedMemberBreadth(t *testing.T) {
	oldLimit := time.Date(2026, 11, 11, 9, 30, 0, 0, Shanghai())
	smallTag := ThemeTag{Code: "885001", Name: "小方向", Occurrences: 2, RecentOccurrences: 1}
	bigTag := ThemeTag{Code: "885002", Name: "大方向", Occurrences: 2, RecentOccurrences: 1}
	profiles := map[string]ThemeStockProfile{}
	members := []TodayMember{}
	for index := 0; index < 2; index++ {
		code := "00000" + string(rune('1'+index))
		profiles[code] = ThemeStockProfile{Code: code, Name: code, Tags: []ThemeTag{smallTag}}
		members = append(members, TodayMember{Code: code, CoreScore: 50, FirstLimitAt: &oldLimit, LastLimitAt: &oldLimit})
	}
	for index := 0; index < 8; index++ {
		code := "00001" + string(rune('0'+index))
		profiles[code] = ThemeStockProfile{Code: code, Name: code, Tags: []ThemeTag{bigTag}}
		members = append(members, TodayMember{Code: code, CoreScore: 50, FirstLimitAt: &oldLimit, LastLimitAt: &oldLimit})
	}
	mainline := ComputeThemeMainline(members, profiles, time.Date(2026, 11, 11, 14, 0, 0, 0, Shanghai()), nil, nil)
	byCode := make(map[string]ThemeCluster, len(mainline))
	for _, theme := range mainline {
		byCode[theme.Code] = theme
	}
	small, smallExists := byCode[smallTag.Code]
	big, bigExists := byCode[bigTag.Code]
	if !smallExists || !bigExists || big.MemberCount <= small.MemberCount ||
		big.Freshness <= small.Freshness {
		t.Fatalf("sealed member breadth did not damp freshness: small=%+v big=%+v", small, big)
	}
}

func TestThemeFreshnessSkipsLunchBreak(t *testing.T) {
	beforeLunch := time.Date(2026, 11, 11, 11, 25, 0, 0, Shanghai())
	afterLunch := time.Date(2026, 11, 11, 13, 5, 0, 0, Shanghai())
	freshness := themeFreshness(&beforeLunch, afterLunch, 2)
	base := math.Exp(-10.0 / 90.0)
	expected := base + (1-base)*(2.0/7.0)
	if math.Abs(freshness-expected) > 1e-9 {
		t.Fatalf("lunch break must not age the direction: got=%v want=%v", freshness, expected)
	}
}

func TestThemeMainlinePenalizesBrokenAndDownMembers(t *testing.T) {
	lastSeal := time.Date(2026, 11, 11, 13, 30, 0, 0, Shanghai())
	robot := ThemeTag{Code: "885517", Name: "机器人概念", Occurrences: 4, RecentOccurrences: 2}
	energy := ThemeTag{Code: "885431", Name: "新能源汽车", Occurrences: 4, RecentOccurrences: 2}
	profiles := map[string]ThemeStockProfile{
		"000001": {Code: "000001", Tags: []ThemeTag{robot}},
		"000002": {Code: "000002", Tags: []ThemeTag{robot}},
		"000003": {Code: "000003", Tags: []ThemeTag{energy}},
		"000004": {Code: "000004", Tags: []ThemeTag{energy}},
		"000011": {Code: "000011", Tags: []ThemeTag{robot}},
		"000012": {Code: "000012", Tags: []ThemeTag{energy}},
	}
	members := []TodayMember{
		{Code: "000001", CoreScore: 50, LastLimitAt: &lastSeal},
		{Code: "000002", CoreScore: 50, LastLimitAt: &lastSeal},
		{Code: "000003", CoreScore: 50, LastLimitAt: &lastSeal},
		{Code: "000004", CoreScore: 50, LastLimitAt: &lastSeal},
	}
	broken := []Event{{TradeDate: "2026-11-11", Code: "000011", Pool: PoolBroken}}
	down := []Event{{TradeDate: "2026-11-11", Code: "000012", Pool: PoolDown}}
	mainline := ComputeThemeMainline(
		members, profiles, time.Date(2026, 11, 11, 14, 0, 0, 0, Shanghai()), broken, down,
	)
	byCode := make(map[string]ThemeCluster, len(mainline))
	for _, theme := range mainline {
		byCode[theme.Code] = theme
	}
	robotTheme, robotExists := byCode[robot.Code]
	energyTheme, energyExists := byCode[energy.Code]
	if !robotExists || !energyExists {
		t.Fatalf("missing penalized themes: %+v", mainline)
	}
	if robotTheme.BrokenCount != 1 || robotTheme.DownCount != 0 ||
		energyTheme.BrokenCount != 0 || energyTheme.DownCount != 1 {
		t.Fatalf("side-pool counts were not attached: robot=%+v energy=%+v", robotTheme, energyTheme)
	}
	if math.Abs(robotTheme.SealQuality-0.8) > 1e-9 ||
		math.Abs(energyTheme.SealQuality-2.0/3.0) > 1e-9 ||
		robotTheme.SealQuality <= energyTheme.SealQuality {
		t.Fatalf("limit-down must be a stronger penalty than a broken board: robot=%+v energy=%+v", robotTheme, energyTheme)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}
