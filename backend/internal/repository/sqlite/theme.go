package sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
	"github.com/roiding/shadowflow/internal/thsfocus"
)

func validateThemeWindow(fromDate, asOf, scope string) (time.Time, time.Time, error) {
	from, fromErr := time.ParseInLocation("2006-01-02", fromDate, limitup.Shanghai())
	until, untilErr := time.ParseInLocation("2006-01-02", asOf, limitup.Shanghai())
	if fromErr != nil || untilErr != nil || fromDate != from.Format("2006-01-02") ||
		asOf != until.Format("2006-01-02") || from.After(until) ||
		until.Sub(from) > 365*24*time.Hour || !limitup.ValidScope(scope) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: invalid theme window", limitup.ErrInvalid)
	}
	return from, until, nil
}

func (store *Store) ReadThemeProfiles(
	ctx context.Context,
	fromDate, asOf, scope string,
	codes []string,
) ([]limitup.ThemeStockProfile, error) {
	_, until, err := validateThemeWindow(fromDate, asOf, scope)
	if err != nil {
		return nil, err
	}
	unique := make([]string, 0, len(codes))
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if !limitup.ValidCode(code) {
			return nil, fmt.Errorf("%w: theme profiles require six-digit codes", limitup.ErrInvalid)
		}
		if _, duplicate := seen[code]; duplicate {
			continue
		}
		seen[code] = struct{}{}
		unique = append(unique, code)
	}
	recentFrom := until.AddDate(0, 0, -59).Format("2006-01-02")
	args := []any{recentFrom, thsfocus.Source, scope, fromDate, asOf}
	where := ""
	if len(unique) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
		where = " AND stock_code IN (" + placeholders + ")"
		for _, code := range unique {
			args = append(args, code)
		}
	}
	rows, err := store.reader.QueryContext(ctx, `SELECT stock_code,MAX(stock_name),theme_code,MAX(theme_name),
COUNT(*),SUM(CASE WHEN trade_date>=? THEN 1 ELSE 0 END),MIN(trade_date),MAX(trade_date)
FROM ths_theme_stock_day
WHERE source=? AND scope=? AND trade_date>=? AND trade_date<=?`+where+`
GROUP BY stock_code,theme_code
ORDER BY stock_code,theme_code,MAX(trade_date) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type profileBuilder struct {
		profile  limitup.ThemeStockProfile
		nameDate string
	}
	builders := make(map[string]*profileBuilder)
	for rows.Next() {
		var code, name, themeCode, themeName, firstDate, lastDate string
		var occurrences, recentOccurrences int
		if err := rows.Scan(&code, &name, &themeCode, &themeName, &occurrences, &recentOccurrences, &firstDate, &lastDate); err != nil {
			return nil, err
		}
		builder := builders[code]
		if builder == nil {
			builder = &profileBuilder{profile: limitup.ThemeStockProfile{Code: code, Tags: []limitup.ThemeTag{}}}
			builders[code] = builder
		}
		if builder.profile.Name == "" || lastDate > builder.nameDate {
			builder.profile.Name = name
			builder.nameDate = lastDate
		}
		builder.profile.Tags = append(builder.profile.Tags, limitup.ThemeTag{
			Code: themeCode, Name: themeName, Occurrences: occurrences,
			RecentOccurrences: recentOccurrences, FirstDate: firstDate, LastDate: lastDate,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	labelArgs := []any{thsfocus.Source, scope, fromDate, asOf}
	labelWhere := ""
	if len(unique) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
		labelWhere = " AND stock_code IN (" + placeholders + ")"
		for _, code := range unique {
			labelArgs = append(labelArgs, code)
		}
	}
	labelRows, err := store.reader.QueryContext(ctx, `SELECT stock_code,COUNT(DISTINCT trade_date)
FROM ths_theme_stock_day
WHERE source=? AND scope=? AND trade_date>=? AND trade_date<=?`+labelWhere+`
GROUP BY stock_code`, labelArgs...)
	if err != nil {
		return nil, err
	}
	labelDays := make(map[string]int, len(builders))
	for labelRows.Next() {
		var code string
		var days int
		if err := labelRows.Scan(&code, &days); err != nil {
			labelRows.Close()
			return nil, err
		}
		labelDays[code] = days
	}
	if err := labelRows.Err(); err != nil {
		labelRows.Close()
		return nil, err
	}
	if err := labelRows.Close(); err != nil {
		return nil, err
	}
	result := make([]limitup.ThemeStockProfile, 0, len(builders))
	for code, builder := range builders {
		builder.profile.LabelDays = labelDays[code]
		sort.SliceStable(builder.profile.Tags, func(left, right int) bool {
			if builder.profile.Tags[left].Occurrences != builder.profile.Tags[right].Occurrences {
				return builder.profile.Tags[left].Occurrences > builder.profile.Tags[right].Occurrences
			}
			if builder.profile.Tags[left].LastDate != builder.profile.Tags[right].LastDate {
				return builder.profile.Tags[left].LastDate > builder.profile.Tags[right].LastDate
			}
			return builder.profile.Tags[left].Code < builder.profile.Tags[right].Code
		})
		result = append(result, builder.profile)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Code < result[right].Code })
	return result, nil
}

func (store *Store) ReadThemeCooccurrences(
	ctx context.Context,
	fromDate, asOf, scope, code string,
) ([]limitup.ThemeCooccurrence, error) {
	if !limitup.ValidCode(code) {
		return nil, fmt.Errorf("%w: theme cooccurrence requires a six-digit code", limitup.ErrInvalid)
	}
	if _, _, err := validateThemeWindow(fromDate, asOf, scope); err != nil {
		return nil, err
	}
	rows, err := store.reader.QueryContext(ctx, `SELECT c.trade_date,c.theme_code,c.theme_name,
p.stock_code,p.stock_name
FROM ths_theme_stock_day c
JOIN ths_theme_stock_day p
  ON p.source=c.source AND p.scope=c.scope AND p.trade_date=c.trade_date AND p.theme_code=c.theme_code
WHERE c.source=? AND c.scope=? AND c.stock_code=? AND c.trade_date>=? AND c.trade_date<=?
  AND p.stock_code<>?
ORDER BY c.trade_date DESC,c.theme_code,p.stock_code`, thsfocus.Source, scope, code, fromDate, asOf, code)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]limitup.ThemeCooccurrence, 0)
	for rows.Next() {
		var item limitup.ThemeCooccurrence
		if err := rows.Scan(&item.TradeDate, &item.ThemeCode, &item.ThemeName, &item.PeerCode, &item.PeerName); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

var _ limitup.ThemeReader = (*Store)(nil)
