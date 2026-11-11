package sqlite

import (
	"context"
	"strings"

	"github.com/roiding/shadowflow/internal/limitup"
)

// ReadCandidateHistoryMetrics returns each stock's own same-minute history.
// It never compares one stock with another; the caller computes a percentile
// within the returned slice for that code.
func (s *Store) ReadCandidateHistoryMetrics(
	ctx context.Context,
	asOf string,
	codes []string,
	days int,
	minuteIndex int,
) (map[string]limitup.CandidateHistoryMetrics, error) {
	result := make(map[string]limitup.CandidateHistoryMetrics, len(codes))
	unique := make([]string, 0, len(codes))
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		unique = append(unique, code)
		result[code] = limitup.CandidateHistoryMetrics{}
	}
	if asOf == "" || len(unique) == 0 || days < 1 || minuteIndex < 0 || minuteIndex > 47 {
		return result, nil
	}
	if days > 120 {
		days = 120
	}

	dateRows, err := s.readDB().QueryContext(ctx, `SELECT DISTINCT trade_date
FROM stock_research_5m
WHERE trade_date <= ? AND minute_index=? AND kline_available=1
ORDER BY trade_date DESC LIMIT ?`, asOf, minuteIndex, days)
	if err != nil {
		return nil, err
	}
	dates := make([]string, 0, days)
	for dateRows.Next() {
		var date string
		if err := dateRows.Scan(&date); err != nil {
			dateRows.Close()
			return nil, err
		}
		dates = append(dates, date)
	}
	if err := dateRows.Err(); err != nil {
		dateRows.Close()
		return nil, err
	}
	dateRows.Close()
	if len(dates) == 0 {
		return result, nil
	}

	datePlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(dates)), ",")
	codePlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, 0, len(dates)+1+len(unique))
	for _, date := range dates {
		args = append(args, date)
	}
	args = append(args, minuteIndex)
	for _, code := range unique {
		args = append(args, code)
	}
	rows, err := s.readDB().QueryContext(ctx, `SELECT code,turnover_rate_ppm,
main_money_inflow,dark_money,turnover,money_available
FROM stock_research_5m
WHERE trade_date IN (`+datePlaceholders+`) AND minute_index=? AND code IN (`+codePlaceholders+`)
  AND kline_available=1
ORDER BY code,trade_date`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var turnoverRatePPM, mainInflow, darkMoney, turnover int64
		var moneyAvailable int
		if err := rows.Scan(&code, &turnoverRatePPM, &mainInflow, &darkMoney, &turnover, &moneyAvailable); err != nil {
			return nil, err
		}
		item := result[code]
		item.TurnoverRates = append(item.TurnoverRates, float64(turnoverRatePPM)/1e6*100)
		if moneyAvailable != 0 && turnover > 0 {
			item.MainFlowPcts = append(item.MainFlowPcts, float64(mainInflow)/float64(turnover)*100)
			item.DarkFlowPcts = append(item.DarkFlowPcts, float64(darkMoney)/float64(turnover)*100)
		}
		result[code] = item
	}
	return result, rows.Err()
}
