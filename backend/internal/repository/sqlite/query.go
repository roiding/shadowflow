package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

const recordColumns = `snapshot_at,trade_date,rank_type,rank,market,code,name,quote_time,
	latest_price_raw,open_price,high_price,low_price,close_price,previous_close,change_value,change_pct,
	volume,turnover,turnover_rate,amplitude,quote_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at`

func (s *Store) LatestRank(ctx context.Context, rankType graymarket.RankType) ([]graymarket.RankRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_intraday_work
WHERE rank_type=? AND snapshot_at=(SELECT max(snapshot_at) FROM rank_intraday_work WHERE rank_type=?)
ORDER BY rank`, string(rankType), string(rankType))
	if err != nil {
		return nil, err
	}
	result, err := scanRecords(rows)
	if err != nil || len(result) > 0 {
		return result, err
	}
	// After compaction the board 15:00 close is authoritative. Research points
	// remain a fallback for older or incomplete dates.
	rows, err = s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE rank_type=? AND snapshot_kind IN ('daily_close','research_5m')
AND snapshot_at=(SELECT max(snapshot_at) FROM rank_snapshot
    WHERE rank_type=? AND snapshot_kind IN ('daily_close','research_5m'))
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_intraday_work
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
	}
	rows, err = s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE trade_date=? AND rank_type=? AND snapshot_at=? AND snapshot_kind=? ORDER BY rank`, tradeDate, string(rankType), at.Format(timestampLayout), string(kind))
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

func (s *Store) IntradaySeries(ctx context.Context, rankType graymarket.RankType, code, tradeDate string) ([]graymarket.RankRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_intraday_work
WHERE trade_date=? AND rank_type=? AND code=? ORDER BY snapshot_at`, tradeDate, string(rankType), code)
	if err != nil {
		return nil, err
	}
	result, err := scanRecords(rows)
	if err != nil || len(result) > 0 {
		return result, err
	}
	// The current-day work rows are removed after compaction. Return the 47
	// research points plus the separate 15:00 close for a continuous monitor.
	rows, err = s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE trade_date=? AND rank_type=? AND code=? AND snapshot_kind IN ('research_5m','daily_close')
ORDER BY snapshot_at`, tradeDate, string(rankType), code)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

func (s *Store) ResearchSeries(ctx context.Context, rankType graymarket.RankType, code string, from, to time.Time) ([]graymarket.RankRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE snapshot_kind='research_5m' AND rank_type=? AND code=? AND snapshot_at>=? AND snapshot_at<=?
ORDER BY snapshot_at`, string(rankType), code, from.Format(timestampLayout), to.Format(timestampLayout))
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
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
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM rank_snapshot WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot WHERE `+where+
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
		rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM rank_snapshot
WHERE trade_date=? AND snapshot_kind='daily_close'
ORDER BY CASE rank_type WHEN 'industry' THEN 1 WHEN 'concept' THEN 2 ELSE 3 END,rank`, tradeDate)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

func (s *Store) HasDailyClose(ctx context.Context, tradeDate string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock'
)`, tradeDate).Scan(&exists)
	return exists == 1, err
}

type scanner interface {
	Scan(...any) error
}

func scanRecord(row scanner) (graymarket.RankRecord, error) {
	var record graymarket.RankRecord
	var snapshotAt, fetchedAt string
	var rankType string
	var descending, quoteAvailable int
	err := row.Scan(
		&snapshotAt, &record.TradeDate, &rankType, &record.Rank, &record.Market, &record.Code, &record.Name, &record.QuoteTime,
		&record.LatestPriceRaw, &record.OpenPrice, &record.HighPrice, &record.LowPrice, &record.ClosePrice, &record.PreviousClose,
		&record.ChangeValue, &record.ChangePct, &record.Volume, &record.Turnover, &record.TurnoverRate, &record.Amplitude, &quoteAvailable,
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
	rows, err := s.db.QueryContext(ctx, `SELECT trade_date,rank_type,expected_minutes,collected_minutes,
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
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,snapshot_at,snapshot_kind,rank_type,status,requested_date,
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

	rows, err := s.db.QueryContext(ctx, `SELECT rank_type,status,count(*) FROM collection_run GROUP BY rank_type,status ORDER BY rank_type,status`)
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

	rows, err = s.db.QueryContext(ctx, `SELECT rank_type,COALESCE(sum(fetched_total),0),COALESCE(sum(duration_ms),0)/1000.0,count(*)
FROM collection_run WHERE status='success' GROUP BY rank_type ORDER BY rank_type`)
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

	rows, err = s.db.QueryContext(ctx, `SELECT rank_type,max(finished_at) FROM collection_run
WHERE status='success' AND finished_at IS NOT NULL GROUP BY rank_type ORDER BY rank_type`)
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

	rows, err = s.db.QueryContext(ctx, `SELECT rank_type,max(snapshot_at) FROM (
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

	if err := s.db.QueryRowContext(ctx, `SELECT count(DISTINCT trade_date) FROM research_quality`).Scan(&result.ResearchCompactionRuns); err != nil {
		return result, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT rank_type,COALESCE(sum(expected_research-collected_research),0)
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
