package limitup

import (
	"testing"
	"time"
)

func TestAddBoardThemeFallbacksPrefersHistoricalTags(t *testing.T) {
	historical := ThemeTag{
		Code: "885517", Name: "机器人概念", Occurrences: 3,
		Source: ThemeSourceTHS,
	}
	profiles := map[string]ThemeStockProfile{
		"000001": {
			Code: "000001", Name: "历史标签股",
			Tags: []ThemeTag{historical},
		},
	}
	relations := []BoardMembership{{
		StockCode: "000001", StockName: "历史标签股",
		BoardCode: "BK1128", BoardName: "CPO概念",
		BoardType: "concept", SourceOrder: 371,
	}}
	result := AddBoardThemeFallbacks(
		profiles, []string{"000001"}, relations,
		map[string]int{BoardThemeKey("concept", "BK1128"): 99}, 5000,
	)
	tags := result["000001"].Tags
	if len(tags) != 1 || tags[0].Code != historical.Code || tags[0].Source != ThemeSourceTHS {
		t.Fatalf("historical tags must not be replaced by board fallback: %+v", tags)
	}
}

func TestAddBoardThemeFallbacksMapsEastmoneyConceptToTHSCluster(t *testing.T) {
	cpo := ThemeTag{
		Code: "885999", Name: "共封装光学(CPO)", Occurrences: 4,
		Source: ThemeSourceTHS,
	}
	profiles := map[string]ThemeStockProfile{
		"000001": {Code: "000001", Tags: []ThemeTag{cpo}},
		"000002": {Code: "000002", Tags: []ThemeTag{cpo}},
	}
	relations := []BoardMembership{
		{
			StockCode: "688498", StockName: "源杰科技",
			BoardCode: "BK1128", BoardName: "CPO概念",
			BoardType: "concept", SourceOrder: 371,
		},
		{
			StockCode: "688498", StockName: "源杰科技",
			BoardCode: "BK0596", BoardName: "融资融券",
			BoardType: "concept", SourceOrder: 51,
		},
		{
			StockCode: "688498", StockName: "源杰科技",
			BoardCode: "BK1327", BoardName: "分立器件",
			BoardType: "industry", SourceOrder: 198,
		},
	}
	counts := map[string]int{
		BoardThemeKey("concept", "BK1128"): 99,
		BoardThemeKey("concept", "BK0596"): 3892,
	}
	result := AddBoardThemeFallbacks(
		profiles, []string{"688498"}, relations, counts, 5659,
	)
	fallback, exists := result["688498"]
	if !exists || len(fallback.Tags) == 0 {
		t.Fatalf("board fallback was not created: %+v", result)
	}
	if fallback.MarketType != "STAR" {
		t.Fatalf("unexpected market type: %+v", fallback)
	}
	var mapped *ThemeTag
	for index := range fallback.Tags {
		if fallback.Tags[index].Code == cpo.Code {
			mapped = &fallback.Tags[index]
			break
		}
	}
	if mapped == nil || mapped.Name != cpo.Name ||
		mapped.Source != ThemeSourceEastmoneyConcept || mapped.weight <= 0 {
		t.Fatalf("CPO board was not mapped to the THS cluster: %+v", fallback.Tags)
	}
	for _, tag := range fallback.Tags {
		if tag.Name == "融资融券" {
			t.Fatalf("generic board leaked into fallback tags: %+v", fallback.Tags)
		}
	}

	mainline := ComputeThemeMainline([]TodayMember{
		{Code: "000001", CoreScore: 50},
		{Code: "000002", CoreScore: 50},
		{Code: "688498", CoreScore: 50},
	}, result, time.Date(2026, 11, 11, 14, 0, 0, 0, Shanghai()), nil, nil)
	if len(mainline) == 0 || mainline[0].Code != cpo.Code || mainline[0].MemberCount != 3 {
		t.Fatalf("fallback stock did not join the historical cluster: %+v", mainline)
	}
	if len(mainline[0].Sources) != 2 ||
		mainline[0].Sources[0] != ThemeSourceEastmoneyConcept ||
		mainline[0].Sources[1] != ThemeSourceTHS {
		t.Fatalf("mixed-source cluster was not disclosed: %+v", mainline[0])
	}
}

func TestSelectFallbackBoardsKeepsSpecificConceptsAndDeepestIndustry(t *testing.T) {
	relations := []BoardMembership{
		{BoardCode: "BK0596", BoardName: "融资融券", BoardType: "concept", SourceOrder: 51},
		{BoardCode: "BK1633", BoardName: "昨日高振幅", BoardType: "concept", SourceOrder: 435},
		{BoardCode: "BK0900", BoardName: "新能源车", BoardType: "concept", SourceOrder: 205},
		{BoardCode: "BK1128", BoardName: "CPO概念", BoardType: "concept", SourceOrder: 371},
		{BoardCode: "BK1136", BoardName: "光通信模块", BoardType: "concept", SourceOrder: 379},
		{BoardCode: "BK0448", BoardName: "通信设备", BoardType: "industry", SourceOrder: 12},
		{BoardCode: "BK1592", BoardName: "通信线缆及配套", BoardType: "industry", SourceOrder: 461},
	}
	counts := map[string]int{
		BoardThemeKey("concept", "BK0900"): 720,
		BoardThemeKey("concept", "BK1128"): 99,
		BoardThemeKey("concept", "BK1136"): 124,
	}
	selected := selectFallbackBoards(relations, counts, 5659)
	if len(selected) != 3 {
		t.Fatalf("unexpected fallback board selection: %+v", selected)
	}
	if selected[0].BoardCode != "BK1128" || selected[1].BoardCode != "BK1136" ||
		selected[2].BoardCode != "BK1592" {
		t.Fatalf("fallback boards are not specific or deepest: %+v", selected)
	}
}

func TestCanonicalThemeNameAliases(t *testing.T) {
	cases := map[string]string{
		"CPO概念":      "cpo",
		"共封装光学(CPO)": "cpo",
		"新能源车":       "新能源汽车",
		"5G概念":       "5g",
		"数据中心(AIDC)": "数据中心",
		"股权转让(并购重组)": "股权转让",
	}
	for input, expected := range cases {
		if actual := canonicalThemeName(input); actual != expected {
			t.Fatalf("canonicalThemeName(%q)=%q, want %q", input, actual, expected)
		}
	}
}
