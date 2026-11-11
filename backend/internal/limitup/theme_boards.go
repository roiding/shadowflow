package limitup

import (
	"regexp"
	"sort"
	"strings"
)

const (
	maxFallbackBoardMembers = 300
	maxFallbackConcepts     = 5
)

var (
	themeParenthetical = regexp.MustCompile(`\([^)]*\)`)

	genericFallbackBoardNames = map[string]struct{}{
		"AH股":      {},
		"QFII重仓":   {},
		"MSCI中国":   {},
		"上证380":    {},
		"上证50":     {},
		"中证500":    {},
		"创业板综":     {},
		"历史新高":     {},
		"基金减仓":     {},
		"基金重仓":     {},
		"外资背景":     {},
		"大盘成长":     {},
		"大盘股":      {},
		"富时罗素":     {},
		"小盘成长":     {},
		"小盘股":      {},
		"机构调研":     {},
		"机构重仓":     {},
		"标准普尔":     {},
		"深成500":    {},
		"深股通":      {},
		"百元股":      {},
		"社保重仓":     {},
		"私募重仓":     {},
		"科创50":     {},
		"融资融券":     {},
		"股权激励":     {},
		"转融券":      {},
		"连续亏损":     {},
		"近期新高":     {},
		"回购增持":     {},
		"低价股":      {},
		"券商重仓":     {},
		"同花顺漂亮100": {},
		"保险重仓":     {},
		"信托重仓":     {},
		"昨日高振幅":    {},
		"沪股通":      {},
		"沪深300":    {},
		"深证100":    {},
		"破发股":      {},
		"破净股":      {},
		"破增发价股":    {},
		"长期破净":     {},
		"高市净率":     {},
		"低市净率":     {},
		"高成长股":     {},
		"科技风格":     {},
		"成长风格":     {},
		"价值风格":     {},
		"质量风格":     {},
		"低波风格":     {},
		"中盘成长":     {},
		"中盘股":      {},
		"百日新高":     {},
	}

	genericFallbackBoardPrefixes = []string{
		"2025", "2026", "MSCI", "QFII", "上证", "中证", "创业板", "券商",
		"基金", "外资", "大盘", "小盘", "机构", "标准普尔", "沪股通", "深股通",
		"深证", "社保", "私募", "科创", "融资", "转融券", "信托", "保险",
		"历史新高", "同花顺", "富时", "昨日", "近期", "百日",
	}

	canonicalThemeAliases = map[string]string{
		"新能源车":     "新能源汽车",
		"国产芯片":     "芯片",
		"半导体":      "芯片",
		"共封装光学":    "cpo",
		"并购重组":     "股权转让",
		"股权转让并购重组": "股权转让",
	}
)

// BoardThemeKey keeps Eastmoney board identifiers separate from THS theme
// identifiers while allowing a shared THS theme code when names can be
// canonically matched.
func BoardThemeKey(boardType, boardCode string) string {
	return "em:" + boardType + ":" + boardCode
}

// AddBoardThemeFallbacks fills the theme-profile gap for stocks that have no
// usable historical THS Top20 label. It uses only already stored Eastmoney
// board relations, never today's free-form limit-up reason.
func AddBoardThemeFallbacks(
	profiles map[string]ThemeStockProfile,
	codes []string,
	relations []BoardMembership,
	boardCounts map[string]int,
	boardDocuments int,
) map[string]ThemeStockProfile {
	result := make(map[string]ThemeStockProfile, len(profiles)+len(codes))
	for code, profile := range profiles {
		result[code] = profile
	}
	nameIndex := themeNameIndex(result)
	byStock := make(map[string][]BoardMembership)
	for _, relation := range relations {
		byStock[relation.StockCode] = append(byStock[relation.StockCode], relation)
	}
	for _, code := range codes {
		existing, exists := result[code]
		if exists && len(normaliseThemeTags(existing.Tags)) > 0 {
			continue
		}
		selected := selectFallbackBoards(byStock[code], boardCounts, boardDocuments)
		if len(selected) == 0 {
			continue
		}
		tags := make([]ThemeTag, 0, len(selected))
		seen := make(map[string]struct{}, len(selected))
		for _, board := range selected {
			boardKey := BoardThemeKey(board.BoardType, board.BoardCode)
			tag := ThemeTag{
				Code: boardKey, Name: board.BoardName,
				Occurrences: 1, RecentOccurrences: 1,
				Source: themeSourceForBoard(board.BoardType),
				weight: themeIDF(boardKey, boardCounts, boardDocuments),
			}
			if matched, ok := nameIndex[canonicalThemeName(board.BoardName)]; ok {
				tag.Code = matched.Code
				tag.Name = matched.Name
			}
			if _, duplicate := seen[tag.Code]; duplicate {
				continue
			}
			seen[tag.Code] = struct{}{}
			tags = append(tags, tag)
		}
		if len(tags) == 0 {
			continue
		}
		if !exists {
			existing = ThemeStockProfile{
				Code: code, Name: fallbackStockName(code, byStock[code]),
				MarketType: marketTypeForCode(code),
			}
		}
		existing.Tags = normaliseThemeTags(tags)
		existing.LabelDays = max(existing.LabelDays, 1)
		result[code] = existing
	}
	return result
}

func selectFallbackBoards(
	relations []BoardMembership,
	boardCounts map[string]int,
	boardDocuments int,
) []BoardMembership {
	concepts := make([]BoardMembership, 0, len(relations))
	industries := make([]BoardMembership, 0, 2)
	for _, relation := range relations {
		if relation.BoardCode == "" || relation.BoardName == "" ||
			isUnclassifiedThemeName(relation.BoardName) ||
			isGenericFallbackBoard(relation.BoardName) {
			continue
		}
		switch relation.BoardType {
		case "industry":
			industries = append(industries, relation)
		case "concept":
			if relation.SourceOrder < 100 {
				continue
			}
			count := boardCounts[BoardThemeKey(relation.BoardType, relation.BoardCode)]
			if count <= 0 || count > maxFallbackBoardMembers {
				continue
			}
			concepts = append(concepts, relation)
		}
	}
	sort.SliceStable(concepts, func(left, right int) bool {
		leftWeight := themeIDF(BoardThemeKey(concepts[left].BoardType, concepts[left].BoardCode), boardCounts, boardDocuments)
		rightWeight := themeIDF(BoardThemeKey(concepts[right].BoardType, concepts[right].BoardCode), boardCounts, boardDocuments)
		if leftWeight != rightWeight {
			return leftWeight > rightWeight
		}
		if concepts[left].SourceOrder != concepts[right].SourceOrder {
			return concepts[left].SourceOrder > concepts[right].SourceOrder
		}
		return concepts[left].BoardCode < concepts[right].BoardCode
	})
	if len(concepts) > maxFallbackConcepts {
		concepts = concepts[:maxFallbackConcepts]
	}
	sort.SliceStable(industries, func(left, right int) bool {
		if industries[left].SourceOrder != industries[right].SourceOrder {
			return industries[left].SourceOrder > industries[right].SourceOrder
		}
		return industries[left].BoardCode < industries[right].BoardCode
	})
	if len(industries) > 1 {
		industries = industries[:1]
	}
	return append(concepts, industries...)
}

func isGenericFallbackBoard(name string) bool {
	name = strings.TrimSpace(name)
	if _, exists := genericFallbackBoardNames[name]; exists {
		return true
	}
	for _, prefix := range genericFallbackBoardPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func themeSourceForBoard(boardType string) string {
	if boardType == "industry" {
		return ThemeSourceEastmoneyIndustry
	}
	return ThemeSourceEastmoneyConcept
}

func fallbackStockName(code string, relations []BoardMembership) string {
	for _, relation := range relations {
		if relation.StockName != "" {
			return relation.StockName
		}
	}
	return code
}

func marketTypeForCode(code string) string {
	switch {
	case strings.HasPrefix(code, "300"), strings.HasPrefix(code, "301"):
		return "GEM"
	case strings.HasPrefix(code, "688"):
		return "STAR"
	case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "8"):
		return "NEW"
	default:
		return "HS"
	}
}

func themeNameIndex(profiles map[string]ThemeStockProfile) map[string]ThemeTag {
	index := make(map[string]ThemeTag)
	for _, profile := range profiles {
		for _, tag := range normaliseThemeTags(profile.Tags) {
			if tag.Source != "" && tag.Source != ThemeSourceTHS {
				continue
			}
			key := canonicalThemeName(tag.Name)
			if key == "" {
				continue
			}
			existing, exists := index[key]
			if !exists || tag.Occurrences > existing.Occurrences ||
				(tag.Occurrences == existing.Occurrences && tag.LastDate > existing.LastDate) {
				index[key] = tag
			}
		}
	}
	return index
}

func canonicalThemeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "（", "(")
	name = strings.ReplaceAll(name, "）", ")")
	name = themeParenthetical.ReplaceAllString(name, "")
	name = strings.TrimSpace(name)
	for _, suffix := range []string{"概念", "板块", "指数", "行业"} {
		name = strings.TrimSuffix(name, suffix)
	}
	name = strings.ReplaceAll(name, " ", "")
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "/", "")
	if alias, exists := canonicalThemeAliases[name]; exists {
		return alias
	}
	return name
}
