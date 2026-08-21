package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

const recordColumns = `snapshot_at,trade_date,rank_type,rank,market,code,name,quote_time,
	latest_price_raw,open_price,high_price,low_price,close_price,previous_close,change_value,change_pct,
	volume,turnover,turnover_rate,amplitude,quote_available,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at`

func (s *Store) LatestRank(ctx context.Context, rankType graymarket.RankType) ([]graymarket.RankRecord, error) {
	rows, err := s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_intraday_work
WHERE rank_type=? AND snapshot_at=(SELECT max(snapshot_at) FROM rank_intraday_work WHERE rank_type=?)
ORDER BY rank`, string(rankType), string(rankType))
	if err != nil {
		return nil, err
	}
	result, err := scanRecords(rows)
	if err != nil || len(result) > 0 {
		return result, err
	}
	// The post-close full snapshot is authoritative after intraday cleanup.
	rows, err = s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE rank_type=? AND snapshot_kind='daily_close'
AND snapshot_at=(SELECT max(snapshot_at) FROM rank_snapshot WHERE rank_type=? AND snapshot_kind='daily_close')
ORDER BY rank`, string(rankType), string(rankType))
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

func (s *Store) RankAt(ctx context.Context, rankType graymarket.RankType, tradeDate string, at time.Time) ([]graymarket.RankRecord, error) {
	// During the current session the work table is authoritative, including
	// five-minute boundaries that have not yet been compacted. Historical
	// dates fall back to the long-term research table after cleanup.
	rows, err := s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_intraday_work
WHERE trade_date=? AND rank_type=? AND snapshot_at=? ORDER BY rank`, tradeDate, string(rankType), at.Format(timestampLayout))
	if err != nil {
		return nil, err
	}
	result, err := scanRecords(rows)
	if err != nil || len(result) > 0 {
		return result, err
	}
	kind := graymarket.SnapshotResearch5m
	if at.Format("15:04") == "15:00" {
		kind = graymarket.SnapshotDailyClose
	} else {
		result, err = s.boardMoneyRecords(ctx, `trade_date=? AND rank_type=? AND snapshot_at=?`, tradeDate, string(rankType), at.Format(timestampLayout))
		if err != nil || len(result) > 0 {
			return result, err
		}
	}
	rows, err = s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE trade_date=? AND rank_type=? AND snapshot_at=? AND snapshot_kind=? ORDER BY rank`, tradeDate, string(rankType), at.Format(timestampLayout), string(kind))
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

func (s *Store) IntradaySeries(ctx context.Context, rankType graymarket.RankType, code, tradeDate string) ([]graymarket.RankRecord, error) {
	rows, err := s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_intraday_work
WHERE trade_date=? AND rank_type=? AND code=? ORDER BY snapshot_at`, tradeDate, string(rankType), code)
	if err != nil {
		return nil, err
	}
	result, err := scanRecords(rows)
	if err != nil || len(result) > 0 {
		return result, err
	}
	result, err = s.boardMoneyRecords(ctx, `trade_date=? AND rank_type=? AND code=?`, tradeDate, string(rankType), code)
	if err != nil {
		return nil, err
	}
	if len(result) > 0 {
		rows, closeErr := s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE trade_date=? AND rank_type=? AND code=? AND snapshot_kind='daily_close'
ORDER BY snapshot_at`, tradeDate, string(rankType), code)
		if closeErr != nil {
			return nil, closeErr
		}
		closeRecords, closeErr := scanRecords(rows)
		if closeErr != nil {
			return nil, closeErr
		}
		return append(result, closeRecords...), nil
	}
	rows, err = s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE trade_date=? AND rank_type=? AND code=? AND snapshot_kind='daily_close'
ORDER BY snapshot_at`, tradeDate, string(rankType), code)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

func (s *Store) ResearchSeries(ctx context.Context, rankType graymarket.RankType, code string, from, to time.Time) ([]graymarket.RankRecord, error) {
	result, err := s.boardMoneyRecords(ctx, `rank_type=? AND code=? AND snapshot_at>=? AND snapshot_at<=?`,
		string(rankType), code, from.Format(timestampLayout), to.Format(timestampLayout))
	return result, err
}

func (s *Store) BoardResearchRevisionSeries(ctx context.Context, revisionID string, rankType graymarket.RankType, code string) ([]graymarket.RankRecord, error) {
	tradeDate, err := archiveTradeDateByRevision(ctx, s.db, revisionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.readDB().QueryContext(ctx, `SELECT snapshot_at,trade_date,rank_type,rank,market,code,name,
dark_money,regular_money,main_money_inflow,money_available,source_time,fetched_at
FROM board_money_5m WHERE trade_date=? AND rank_type=? AND code=?
ORDER BY snapshot_at,rank`, tradeDate, string(rankType), code)
	if err != nil {
		return nil, err
	}
	return scanBoardMoneyRecords(rows)
}

func (s *Store) StockResearchSeries(ctx context.Context, code, tradeDate string) ([]graymarket.StockResearchPoint, error) {
	rows, err := s.readDB().QueryContext(ctx, `SELECT minute_index,market,code,money_rank,dark_money,regular_money,main_money_inflow,money_available,
open_price_e4,high_price_e4,low_price_e4,close_price_e4,volume,turnover,amplitude_ppm,change_pct_ppm,
change_value_e4,turnover_rate_ppm,kline_available
FROM stock_research_5m WHERE trade_date=? AND code=? ORDER BY minute_index`, tradeDate, code)
	if err != nil {
		return nil, err
	}
	return scanStockResearchRows(rows, tradeDate)
}

func (s *Store) StockResearchRevisionSeries(ctx context.Context, revisionID, code string) ([]graymarket.StockResearchPoint, error) {
	tradeDate, err := archiveTradeDateByRevision(ctx, s.db, revisionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.readDB().QueryContext(ctx, `SELECT minute_index,market,code,money_rank,dark_money,regular_money,main_money_inflow,money_available,
open_price_e4,high_price_e4,low_price_e4,close_price_e4,volume,turnover,amplitude_ppm,change_pct_ppm,
change_value_e4,turnover_rate_ppm,kline_available
FROM stock_research_5m WHERE trade_date=? AND code=? ORDER BY minute_index`, tradeDate, code)
	if err != nil {
		return nil, err
	}
	return scanStockResearchRows(rows, tradeDate)
}

func scanStockResearchRows(rows *sql.Rows, tradeDate string) ([]graymarket.StockResearchPoint, error) {
	defer rows.Close()
	clocks := expectedResearchTimes()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	result := make([]graymarket.StockResearchPoint, 0, 48)
	for rows.Next() {
		var point graymarket.StockResearchPoint
		var minuteIndex, klineAvailable, moneyAvailable int
		var open, high, low, close, amplitude, changePct, changeValue, turnoverRate int64
		if err := rows.Scan(&minuteIndex, &point.Market, &point.Code, &point.MoneyRank, &point.DarkMoney,
			&point.RegularMoney, &point.MainMoneyInflow, &moneyAvailable, &open, &high, &low, &close, &point.Volume,
			&point.Turnover, &amplitude, &changePct, &changeValue, &turnoverRate, &klineAvailable); err != nil {
			return nil, err
		}
		if minuteIndex < 0 || minuteIndex >= len(clocks) {
			return nil, fmt.Errorf("invalid stock research minute index %d", minuteIndex)
		}
		point.TradeDate = tradeDate
		snapshotAt, parseErr := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+clocks[minuteIndex], location)
		if parseErr != nil {
			return nil, parseErr
		}
		point.SnapshotAt = snapshotAt
		point.OpenPrice, point.HighPrice, point.LowPrice, point.ClosePrice = unscaleE4(open), unscaleE4(high), unscaleE4(low), unscaleE4(close)
		point.Amplitude, point.ChangePct = unscalePPM(amplitude), unscalePPM(changePct)
		point.ChangeValue, point.TurnoverRate = unscaleE4(changeValue), unscalePPM(turnoverRate)
		point.KlineAvailable = klineAvailable != 0
		point.MoneyAvailable = moneyAvailable != 0
		result = append(result, point)
	}
	return result, rows.Err()
}

func (s *Store) boardMoneyRecords(ctx context.Context, where string, args ...any) ([]graymarket.RankRecord, error) {
	rows, err := s.readDB().QueryContext(ctx, `SELECT snapshot_at,trade_date,rank_type,rank,market,code,name,
dark_money,regular_money,main_money_inflow,money_available,source_time,fetched_at
FROM board_money_5m WHERE `+where+` ORDER BY snapshot_at,rank`, args...)
	if err != nil {
		return nil, err
	}
	return scanBoardMoneyRecords(rows)
}

func scanBoardMoneyRecords(rows *sql.Rows) ([]graymarket.RankRecord, error) {
	defer rows.Close()
	result := make([]graymarket.RankRecord, 0)
	for rows.Next() {
		var record graymarket.RankRecord
		var snapshotAt, rankType, fetchedAt string
		var sourceTime int64
		var moneyAvailable int
		if err := rows.Scan(&snapshotAt, &record.TradeDate, &rankType, &record.Rank, &record.Market, &record.Code, &record.Name,
			&record.DarkMoney, &record.RegularMoney, &record.MainMoneyInflow, &moneyAvailable, &sourceTime, &fetchedAt); err != nil {
			return nil, err
		}
		record.MoneyAvailable = moneyAvailable != 0
		record.RankType = graymarket.RankType(rankType)
		record.QuoteTime = fmt.Sprintf("%010d", sourceTime)
		record.SnapshotAt, _ = time.Parse(timestampLayout, snapshotAt)
		record.FetchedAt, _ = time.Parse(timestampLayout, fetchedAt)
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) DailyClosePage(ctx context.Context, rankType graymarket.RankType, tradeDate, search, sort string, descending bool, limit, offset int) ([]graymarket.RankRecord, int, error) {
	sortColumns := map[string]string{
		"rank": "rank", "name": "name", "code": "code", "dark_money": "dark_money",
		"main_money_inflow": "main_money_inflow", "change_pct": "change_pct", "dark_activity": "dark_activity",
		"open_price": "open_price", "high_price": "high_price", "low_price": "low_price", "close_price": "close_price",
		"previous_close": "previous_close", "volume": "volume", "turnover": "turnover", "turnover_rate": "turnover_rate", "amplitude": "amplitude",
	}
	column, ok := sortColumns[sort]
	if !ok {
		column = "rank"
	}
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	where := `trade_date=? AND snapshot_kind='daily_close' AND rank_type=?`
	args := []any{tradeDate, string(rankType)}
	if search != "" {
		where += ` AND (name LIKE ? ESCAPE '\' OR code LIKE ? ESCAPE '\')`
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
		pattern := "%" + escaped + "%"
		args = append(args, pattern, pattern)
	}
	var total int
	if err := s.readDB().QueryRowContext(ctx, `SELECT count(*) FROM rank_snapshot WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot WHERE `+where+
		` ORDER BY `+column+` `+direction+`,rank ASC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	records, err := scanRecords(rows)
	return records, total, err
}

func (s *Store) DailyCloseRevisionPage(ctx context.Context, revisionID string, rankType graymarket.RankType, search, sort string, descending bool, limit, offset int) ([]graymarket.RankRecord, int, error) {
	tradeDate, err := archiveTradeDateByRevision(ctx, s.db, revisionID)
	if err != nil {
		return nil, 0, err
	}
	sortColumns := map[string]string{
		"rank": "rank", "name": "name", "code": "code", "dark_money": "dark_money",
		"main_money_inflow": "main_money_inflow", "change_pct": "change_pct", "dark_activity": "dark_activity",
		"open_price": "open_price", "high_price": "high_price", "low_price": "low_price", "close_price": "close_price",
		"previous_close": "previous_close", "volume": "volume", "turnover": "turnover", "turnover_rate": "turnover_rate", "amplitude": "amplitude",
	}
	column, ok := sortColumns[sort]
	if !ok {
		column = "rank"
	}
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	where := `trade_date=? AND snapshot_kind='daily_close' AND rank_type=?`
	args := []any{tradeDate, string(rankType)}
	if search != "" {
		where += ` AND (name LIKE ? ESCAPE '\' OR code LIKE ? ESCAPE '\')`
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
		pattern := "%" + escaped + "%"
		args = append(args, pattern, pattern)
	}
	var total int
	if err := s.readDB().QueryRowContext(ctx, `SELECT count(*) FROM rank_snapshot WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot WHERE `+where+
		` ORDER BY `+column+` `+direction+`,rank ASC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	records, err := scanRecords(rows)
	return records, total, err
}

func (s *Store) DailyCloseStocks(ctx context.Context, tradeDate string, stockCodes []string) ([]graymarket.RankRecord, error) {
	if len(stockCodes) == 0 {
		return []graymarket.RankRecord{}, nil
	}
	unique := make([]string, 0, len(stockCodes))
	seen := make(map[string]struct{}, len(stockCodes))
	for _, code := range stockCodes {
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		unique = append(unique, code)
	}
	result := make([]graymarket.RankRecord, 0, len(unique))
	const batchSize = 500
	for start := 0; start < len(unique); start += batchSize {
		end := min(start+batchSize, len(unique))
		batch := unique[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch)+1)
		args = append(args, tradeDate)
		for _, code := range batch {
			args = append(args, code)
		}
		rows, err := s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock' AND code IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		records, err := scanRecords(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, records...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Rank < result[j].Rank })
	return result, nil
}

func (s *Store) DailyCloseRecords(ctx context.Context, tradeDate string) ([]graymarket.RankRecord, error) {
	rows, err := s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE trade_date=? AND snapshot_kind='daily_close'
ORDER BY CASE rank_type WHEN 'industry' THEN 1 WHEN 'concept' THEN 2 ELSE 3 END,rank`, tradeDate)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

func (s *Store) DailyCloseRevisionRecords(ctx context.Context, revisionID string) ([]graymarket.RankRecord, error) {
	tradeDate, err := archiveTradeDateByRevision(ctx, s.db, revisionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.readDB().QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE trade_date=? AND snapshot_kind='daily_close'
ORDER BY CASE rank_type WHEN 'industry' THEN 1 WHEN 'concept' THEN 2 ELSE 3 END,rank`, tradeDate)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

func (s *Store) DailyCloseTradeDates(ctx context.Context, asOf string, limit int) ([]string, error) {
	if limit < 1 {
		return []string{}, nil
	}
	rows, err := s.readDB().QueryContext(ctx, `SELECT trade_date FROM rank_snapshot
WHERE trade_date<=? AND snapshot_kind='daily_close' AND rank_type IN ('concept','stock')
GROUP BY trade_date
HAVING sum(rank_type='concept')>0
   AND sum(rank_type='concept' AND quote_available=1)=sum(rank_type='concept')
   AND sum(rank_type='stock')>0
ORDER BY trade_date DESC LIMIT ?`, asOf, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dates := make([]string, 0, limit)
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return nil, err
		}
		dates = append(dates, date)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(dates)-1; left < right; left, right = left+1, right-1 {
		dates[left], dates[right] = dates[right], dates[left]
	}
	return dates, nil
}

func (s *Store) HasDailyClose(ctx context.Context, tradeDate string) (bool, error) {
	var exists int
	err := s.readDB().QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock'
)`, tradeDate).Scan(&exists)
	return exists == 1, err
}

func (s *Store) HasEndOfDayArchive(ctx context.Context, tradeDate string) (bool, error) {
	var stockClose, boardCloses, curveTypes, stockMoney int
	if err := s.readDB().QueryRowContext(ctx, `SELECT
(SELECT EXISTS(SELECT 1 FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock')),
(SELECT count(DISTINCT rank_type) FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type IN ('industry','concept')),
(SELECT count(*) FROM (SELECT rank_type FROM board_money_5m WHERE trade_date=? GROUP BY rank_type HAVING count(DISTINCT snapshot_at)=48)),
(SELECT EXISTS(SELECT 1 FROM stock_archive_quality AS quality WHERE trade_date=?
AND daily_close_rows=expected_stocks AND daily_kline_rows=expected_kline_stocks
AND (SELECT coalesce(sum(money_available),0) FROM stock_research_5m WHERE trade_date=quality.trade_date)=expected_kline_stocks*expected_points))`,
		tradeDate, tradeDate, tradeDate, tradeDate).Scan(&stockClose, &boardCloses, &curveTypes, &stockMoney); err != nil {
		return false, err
	}
	return stockClose == 1 && boardCloses == 2 && curveTypes == 2 && stockMoney == 1, nil
}

func (s *Store) HasStockKlineArchive(ctx context.Context, tradeDate string) (bool, error) {
	var exists int
	err := s.readDB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM stock_archive_quality WHERE trade_date=?
AND kline_rows=expected_kline_stocks*expected_points)`, tradeDate).Scan(&exists)
	return exists == 1, err
}

func (s *Store) MissingStockKlineCodes(ctx context.Context, tradeDate string) ([]string, error) {
	var expectedStocks, expectedPoints, closeStocks int
	err := s.readDB().QueryRowContext(ctx, `SELECT expected_kline_stocks,expected_points,
(SELECT count(*) FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock' AND quote_available=1)
FROM stock_archive_quality WHERE trade_date=?`, tradeDate, tradeDate).Scan(&expectedStocks, &expectedPoints, &closeStocks)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: stock archive is unavailable for %s", graymarket.ErrNoData, tradeDate)
	}
	if err != nil {
		return nil, err
	}
	if expectedPoints != 48 || closeStocks != expectedStocks {
		return nil, fmt.Errorf("inconsistent stock archive quality for %s: expected_stocks=%d close_stocks=%d expected_points=%d", tradeDate, expectedStocks, closeStocks, expectedPoints)
	}
	rows, err := s.readDB().QueryContext(ctx, `SELECT close.code FROM rank_snapshot AS close
LEFT JOIN (
	SELECT market,code,sum(kline_available) AS kline_rows
	FROM stock_research_5m WHERE trade_date=? GROUP BY market,code
) AS archived ON archived.market=close.market AND archived.code=close.code
WHERE close.trade_date=? AND close.snapshot_kind='daily_close' AND close.rank_type='stock'
AND close.quote_available=1 AND coalesce(archived.kline_rows,0)<? ORDER BY close.rank`, tradeDate, tradeDate, expectedPoints)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0, expectedStocks)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		result = append(result, code)
	}
	return result, rows.Err()
}

type scanner interface {
	Scan(...any) error
}

func scanRecord(row scanner) (graymarket.RankRecord, error) {
	var record graymarket.RankRecord
	var snapshotAt, fetchedAt string
	var rankType string
	var descending, quoteAvailable, moneyAvailable int
	err := row.Scan(
		&snapshotAt, &record.TradeDate, &rankType, &record.Rank, &record.Market, &record.Code, &record.Name, &record.QuoteTime,
		&record.LatestPriceRaw, &record.OpenPrice, &record.HighPrice, &record.LowPrice, &record.ClosePrice, &record.PreviousClose,
		&record.ChangeValue, &record.ChangePct, &record.Volume, &record.Turnover, &record.TurnoverRate, &record.Amplitude,
		&quoteAvailable, &moneyAvailable,
		&record.DarkMoney, &record.RegularMoney, &record.MainMoneyInflow,
		&record.DarkActivity, &record.DarkInflowRatio, &record.UpCount, &record.FlatCount, &record.DownCount,
		&record.LeaderName, &record.LeaderCode, &record.SourceVersion, &record.SourceSortFlag, &descending, &fetchedAt,
	)
	if err != nil {
		return record, err
	}
	record.RankType = graymarket.RankType(rankType)
	record.SourceDescending = descending != 0
	record.QuoteAvailable = quoteAvailable != 0
	record.MoneyAvailable = moneyAvailable != 0
	record.SnapshotAt, err = time.Parse(timestampLayout, snapshotAt)
	if err != nil {
		return record, fmt.Errorf("parse snapshot_at: %w", err)
	}
	record.FetchedAt, err = time.Parse(timestampLayout, fetchedAt)
	if err != nil {
		return record, fmt.Errorf("parse fetched_at: %w", err)
	}
	return record, nil
}

func scanRecords(rows *sql.Rows) ([]graymarket.RankRecord, error) {
	defer rows.Close()
	result := make([]graymarket.RankRecord, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) Quality(ctx context.Context, tradeDate string) ([]repository.QualitySummary, error) {
	rows, err := s.readDB().QueryContext(ctx, `SELECT trade_date,rank_type,expected_minutes,collected_minutes,
expected_research,collected_research,expected_daily_close,collected_daily_close,
missing_minutes_json,missing_research_json,missing_daily_close_json,compacted_at
FROM research_quality WHERE trade_date=? ORDER BY rank_type`, tradeDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]repository.QualitySummary, 0, 2)
	for rows.Next() {
		var summary repository.QualitySummary
		var rankType, missingMinutes, missingResearch, missingDailyClose, compactedAt string
		if err := rows.Scan(&summary.TradeDate, &rankType, &summary.ExpectedMinutes, &summary.CollectedMinutes,
			&summary.ExpectedResearch, &summary.CollectedResearch, &summary.ExpectedDailyClose, &summary.CollectedDailyClose,
			&missingMinutes, &missingResearch, &missingDailyClose, &compactedAt); err != nil {
			return nil, err
		}
		summary.RankType = graymarket.RankType(rankType)
		_ = json.Unmarshal([]byte(missingMinutes), &summary.MissingMinutes)
		_ = json.Unmarshal([]byte(missingResearch), &summary.MissingResearch)
		_ = json.Unmarshal([]byte(missingDailyClose), &summary.MissingDailyClose)
		parsed, err := time.Parse(timestampLayout, compactedAt)
		if err != nil {
			return nil, err
		}
		summary.CompactedAt = &parsed
		result = append(result, summary)
	}
	return result, rows.Err()
}

func (s *Store) StockArchiveQuality(ctx context.Context, tradeDate string) (repository.StockArchiveQuality, error) {
	quality := repository.StockArchiveQuality{TradeDate: tradeDate, ExpectedPoints: 48}
	var moneyArchivedAt, klineArchivedAt sql.NullString
	err := s.readDB().QueryRowContext(ctx, `SELECT expected_stocks,expected_points,expected_kline_stocks,money_rows,kline_rows,
daily_close_rows,daily_kline_rows,money_archived_at,kline_archived_at
FROM stock_archive_quality WHERE trade_date=?`, tradeDate).Scan(&quality.ExpectedStocks, &quality.ExpectedPoints,
		&quality.ExpectedKlineStocks, &quality.MoneyRows, &quality.KlineRows, &quality.DailyCloseRows,
		&quality.DailyKlineRows, &moneyArchivedAt, &klineArchivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return quality, nil
	}
	if err != nil {
		return quality, err
	}
	if moneyArchivedAt.Valid {
		parsed, parseErr := time.Parse(timestampLayout, moneyArchivedAt.String)
		if parseErr != nil {
			return quality, parseErr
		}
		quality.MoneyArchivedAt = &parsed
	}
	if klineArchivedAt.Valid {
		parsed, parseErr := time.Parse(timestampLayout, klineArchivedAt.String)
		if parseErr != nil {
			return quality, parseErr
		}
		quality.KlineArchivedAt = &parsed
	}
	return quality, nil
}

func (s *Store) StartRun(ctx context.Context, run repository.CollectionRun) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO collection_run
(run_id,snapshot_at,snapshot_kind,rank_type,status,requested_date,actual_trade_date,expected_total,fetched_total,page_count,
attempt_count,started_at,finished_at,duration_ms,error_code,error_message)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, run.RunID, run.SnapshotAt.Format(timestampLayout), string(run.SnapshotKind),
		string(run.RankType), string(run.Status), run.RequestedDate, run.ActualTradeDate, run.ExpectedTotal, run.FetchedTotal,
		run.PageCount, run.AttemptCount, run.StartedAt.Format(timestampLayout), nil, run.DurationMS, run.ErrorCode, run.ErrorMessage)
	return err
}

func (s *Store) FinishRun(ctx context.Context, run repository.CollectionRun) error {
	var finished any
	if run.FinishedAt != nil {
		finished = run.FinishedAt.Format(timestampLayout)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE collection_run SET status=?,actual_trade_date=?,expected_total=?,fetched_total=?,
page_count=?,attempt_count=?,finished_at=?,duration_ms=?,error_code=?,error_message=? WHERE run_id=?`, string(run.Status),
		run.ActualTradeDate, run.ExpectedTotal, run.FetchedTotal, run.PageCount, run.AttemptCount, finished, run.DurationMS,
		run.ErrorCode, run.ErrorMessage, run.RunID)
	return err
}

func (s *Store) RecentRuns(ctx context.Context, tradeDate string, limit int) ([]repository.CollectionRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.readDB().QueryContext(ctx, `SELECT run_id,snapshot_at,snapshot_kind,rank_type,status,requested_date,
actual_trade_date,expected_total,fetched_total,page_count,attempt_count,started_at,finished_at,duration_ms,error_code,error_message
FROM collection_run WHERE requested_date=? ORDER BY snapshot_at DESC LIMIT ?`, tradeDate, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]repository.CollectionRun, 0)
	for rows.Next() {
		var run repository.CollectionRun
		var snapshotAt, startedAt, kind, rankType, status string
		var finishedAt sql.NullString
		if err := rows.Scan(&run.RunID, &snapshotAt, &kind, &rankType, &status, &run.RequestedDate,
			&run.ActualTradeDate, &run.ExpectedTotal, &run.FetchedTotal, &run.PageCount, &run.AttemptCount,
			&startedAt, &finishedAt, &run.DurationMS, &run.ErrorCode, &run.ErrorMessage); err != nil {
			return nil, err
		}
		run.SnapshotAt, _ = time.Parse(timestampLayout, snapshotAt)
		run.StartedAt, _ = time.Parse(timestampLayout, startedAt)
		run.SnapshotKind = graymarket.SnapshotKind(kind)
		run.RankType = graymarket.RankType(rankType)
		run.Status = repository.RunStatus(status)
		if finishedAt.Valid {
			parsed, _ := time.Parse(timestampLayout, finishedAt.String)
			run.FinishedAt = &parsed
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func (s *Store) OperationalMetrics(ctx context.Context) (repository.OperationalMetrics, error) {
	var result repository.OperationalMetrics

	rows, err := s.readDB().QueryContext(ctx, `SELECT rank_type,status,sum(run_count) FROM (
SELECT rank_type,status,count(*) AS run_count FROM collection_run GROUP BY rank_type,status
UNION ALL
SELECT rank_type,status,run_count FROM collection_run_rollup
) GROUP BY rank_type,status ORDER BY rank_type,status`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var rankType, status string
		var value int64
		if err := rows.Scan(&rankType, &status, &value); err != nil {
			rows.Close()
			return result, err
		}
		result.RunCounts = append(result.RunCounts, repository.MetricCount{RankType: graymarket.RankType(rankType), Status: repository.RunStatus(status), Value: value})
	}
	if err := rows.Close(); err != nil {
		return result, err
	}

	rows, err = s.readDB().QueryContext(ctx, `SELECT rank_type,coalesce(sum(record_count),0),
coalesce(sum(duration_ms),0)/1000.0,coalesce(sum(run_count),0) FROM (
SELECT rank_type,sum(fetched_total) AS record_count,sum(duration_ms) AS duration_ms,count(*) AS run_count
FROM collection_run WHERE status='success' GROUP BY rank_type
UNION ALL
SELECT rank_type,record_count,duration_ms,run_count FROM collection_run_rollup WHERE status='success'
) GROUP BY rank_type ORDER BY rank_type`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var rankType string
		var records, duration, count float64
		if err := rows.Scan(&rankType, &records, &duration, &count); err != nil {
			rows.Close()
			return result, err
		}
		typed := graymarket.RankType(rankType)
		result.RecordCounts = append(result.RecordCounts, repository.MetricValue{RankType: typed, Value: records})
		result.DurationSecondsSum = append(result.DurationSecondsSum, repository.MetricValue{RankType: typed, Value: duration})
		result.DurationCounts = append(result.DurationCounts, repository.MetricValue{RankType: typed, Value: count})
	}
	if err := rows.Close(); err != nil {
		return result, err
	}

	rows, err = s.readDB().QueryContext(ctx, `SELECT rank_type,max(finished_at) FROM (
SELECT rank_type,finished_at FROM collection_run WHERE status='success' AND finished_at IS NOT NULL
UNION ALL
SELECT rank_type,latest_success_at AS finished_at FROM collection_run_rollup
WHERE status='success' AND latest_success_at IS NOT NULL
) GROUP BY rank_type ORDER BY rank_type`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var rankType, value string
		if err := rows.Scan(&rankType, &value); err != nil {
			rows.Close()
			return result, err
		}
		parsed, err := time.Parse(timestampLayout, value)
		if err != nil {
			rows.Close()
			return result, err
		}
		result.LastSuccess = append(result.LastSuccess, repository.MetricTime{RankType: graymarket.RankType(rankType), Value: parsed})
	}
	if err := rows.Close(); err != nil {
		return result, err
	}

	rows, err = s.readDB().QueryContext(ctx, `SELECT rank_type,max(snapshot_at) FROM (
SELECT rank_type,snapshot_at FROM rank_intraday_work
UNION ALL
SELECT rank_type,snapshot_at FROM rank_snapshot WHERE snapshot_kind IN ('research_5m','daily_close')
) WHERE rank_type IN ('industry','concept') GROUP BY rank_type ORDER BY rank_type`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var rankType, value string
		if err := rows.Scan(&rankType, &value); err != nil {
			rows.Close()
			return result, err
		}
		parsed, err := time.Parse(timestampLayout, value)
		if err != nil {
			rows.Close()
			return result, err
		}
		result.LatestIntradaySnapshot = append(result.LatestIntradaySnapshot, repository.MetricTime{RankType: graymarket.RankType(rankType), Value: parsed})
	}
	if err := rows.Close(); err != nil {
		return result, err
	}

	if err := s.readDB().QueryRowContext(ctx, `SELECT count(DISTINCT trade_date) FROM research_quality`).Scan(&result.ResearchCompactionRuns); err != nil {
		return result, err
	}
	rows, err = s.readDB().QueryContext(ctx, `SELECT rank_type,COALESCE(sum(expected_research-collected_research),0)
FROM research_quality GROUP BY rank_type ORDER BY rank_type`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var rankType string
		var value float64
		if err := rows.Scan(&rankType, &value); err != nil {
			return result, err
		}
		result.ResearchMissingSnapshot = append(result.ResearchMissingSnapshot, repository.MetricValue{RankType: graymarket.RankType(rankType), Value: value})
	}
	return result, rows.Err()
}
