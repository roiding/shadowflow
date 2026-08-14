package sqlite

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

const timestampLayout = time.RFC3339Nano

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err := migrateResearchCloseModel(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate 15:00 close snapshots: %w", err)
	}
	if _, err := db.Exec(`UPDATE collection_run
SET status='failed', finished_at=COALESCE(finished_at, ?), error_code='interrupted',
error_message='process stopped before collection completed'
WHERE status='running'`, time.Now().UTC().Format(timestampLayout)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("recover interrupted runs: %w", err)
	}
	if _, err := db.Exec(`UPDATE relation_sync_run
SET status='failed', finished_at=COALESCE(finished_at, ?), error_code='interrupted',
error_message='process stopped before relation synchronization completed'
WHERE status='running'`, time.Now().UTC().Format(timestampLayout)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("recover interrupted relation syncs: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM stock_board_relation_stage`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cleanup interrupted relation stage: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM rank_intraday_work WHERE trade_date IN (
SELECT trade_date FROM research_quality WHERE collected_daily_close=1
GROUP BY trade_date HAVING count(DISTINCT rank_type)=2
)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cleanup compacted intraday data: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func migrateResearchCloseModel(db *sql.DB) error {
	columns := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(research_quality)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, migration := range []struct{ name, sql string }{
		{"expected_daily_close", `ALTER TABLE research_quality ADD COLUMN expected_daily_close INTEGER NOT NULL DEFAULT 1`},
		{"collected_daily_close", `ALTER TABLE research_quality ADD COLUMN collected_daily_close INTEGER NOT NULL DEFAULT 0`},
		{"missing_daily_close_json", `ALTER TABLE research_quality ADD COLUMN missing_daily_close_json TEXT NOT NULL DEFAULT '["15:00"]'`},
	} {
		if !columns[migration.name] {
			if _, err := db.Exec(migration.sql); err != nil {
				return err
			}
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT OR IGNORE INTO rank_snapshot (
run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at,created_at)
SELECT run_id,snapshot_at,trade_date,requested_date,'daily_close',rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at,created_at
FROM rank_snapshot WHERE snapshot_kind='research_5m' AND rank_type IN ('industry','concept')
AND substr(snapshot_at,12,5)='15:00'`)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM rank_snapshot WHERE snapshot_kind='research_5m'
AND rank_type IN ('industry','concept') AND substr(snapshot_at,12,5)='15:00'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO raw_response
(run_id,snapshot_at,snapshot_kind,rank_type,page,content_encoding,compression,body,fetched_at)
SELECT run_id,snapshot_at,'daily_close',rank_type,page,content_encoding,compression,body,fetched_at
FROM raw_response WHERE snapshot_kind='research_5m' AND rank_type IN ('industry','concept')
AND substr(snapshot_at,12,5)='15:00'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM raw_response WHERE snapshot_kind='research_5m'
AND rank_type IN ('industry','concept') AND substr(snapshot_at,12,5)='15:00'`); err != nil {
		return err
	}

	qualityRows, err := tx.Query(`SELECT trade_date,rank_type FROM research_quality ORDER BY trade_date,rank_type`)
	if err != nil {
		return err
	}
	type qualityKey struct{ tradeDate, rankType string }
	var keys []qualityKey
	for qualityRows.Next() {
		var key qualityKey
		if err := qualityRows.Scan(&key.tradeDate, &key.rankType); err != nil {
			qualityRows.Close()
			return err
		}
		keys = append(keys, key)
	}
	if err := qualityRows.Close(); err != nil {
		return err
	}
	for _, key := range keys {
		researchMinutes, err := snapshotMinutes(context.Background(), tx, "rank_snapshot", key.tradeDate, graymarket.RankType(key.rankType), string(graymarket.SnapshotResearch5m))
		if err != nil {
			return err
		}
		closeMinutes, err := snapshotMinutes(context.Background(), tx, "rank_snapshot", key.tradeDate, graymarket.RankType(key.rankType), string(graymarket.SnapshotDailyClose))
		if err != nil {
			return err
		}
		missingResearch, _ := json.Marshal(missing(expectedResearchTimes(), researchMinutes))
		missingClose, _ := json.Marshal(missing(expectedDailyCloseTimes(), closeMinutes))
		if _, err := tx.Exec(`UPDATE research_quality SET expected_research=47,collected_research=?,
expected_daily_close=1,collected_daily_close=?,missing_research_json=?,missing_daily_close_json=?
WHERE trade_date=? AND rank_type=?`, len(researchMinutes), len(filterDailyCloseMinutes(closeMinutes)),
			string(missingResearch), string(missingClose), key.tradeDate, key.rankType); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SaveIntraday(ctx context.Context, runID string, snapshot graymarket.RankSnapshot, keepRaw bool) error {
	if snapshot.RankType != graymarket.RankIndustry && snapshot.RankType != graymarket.RankConcept {
		return fmt.Errorf("intraday snapshot cannot contain rank type %s", snapshot.RankType)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, record := range snapshot.Records {
		if err := insertRecord(ctx, tx, "rank_intraday_work", runID, "", "", record); err != nil {
			return err
		}
	}
	if keepRaw {
		rawKind := graymarket.SnapshotResearch5m
		if snapshot.SnapshotAt.Format("15:04") == "15:00" {
			rawKind = graymarket.SnapshotDailyClose
		}
		for _, page := range snapshot.RawPages {
			if err := insertRawPage(ctx, tx, runID, snapshot.SnapshotAt, rawKind, snapshot.RankType, page); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) SaveDailyClose(ctx context.Context, runID string, snapshot graymarket.RankSnapshot) error {
	if snapshot.RankType != graymarket.RankStock && snapshot.RankType != graymarket.RankIndustry && snapshot.RankType != graymarket.RankConcept {
		return fmt.Errorf("daily close snapshot cannot contain rank type %s", snapshot.RankType)
	}
	if snapshot.SnapshotAt.Format("15:04") != "15:00" {
		return fmt.Errorf("daily close snapshot_at must be 15:00, got %s", snapshot.SnapshotAt.Format("15:04"))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, record := range snapshot.Records {
		if err := insertRecord(ctx, tx, "rank_snapshot", runID, snapshot.RequestedDate, string(graymarket.SnapshotDailyClose), record); err != nil {
			return err
		}
	}
	for _, page := range snapshot.RawPages {
		if err := insertRawPage(ctx, tx, runID, snapshot.SnapshotAt, graymarket.SnapshotDailyClose, snapshot.RankType, page); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertRecord(ctx context.Context, tx *sql.Tx, table, runID, requestedDate, snapshotKind string, record graymarket.RankRecord) error {
	commonArgs := []any{
		runID, record.SnapshotAt.Format(timestampLayout), record.TradeDate,
	}
	if table == "rank_snapshot" {
		commonArgs = append(commonArgs, requestedDate, snapshotKind)
	}
	commonArgs = append(commonArgs,
		string(record.RankType), record.Rank, record.Market, record.Code, record.Name, record.QuoteTime,
		record.LatestPriceRaw, record.ChangePct, record.DarkMoney, record.RegularMoney, record.MainMoneyInflow,
		record.DarkActivity, record.DarkInflowRatio, record.UpCount, record.FlatCount, record.DownCount,
		record.LeaderName, record.LeaderCode, record.SourceVersion, record.SourceSortFlag, boolInt(record.SourceDescending),
		record.FetchedAt.Format(timestampLayout),
	)
	columns := `run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at`
	if table == "rank_snapshot" {
		columns = `run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at`
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(commonArgs)), ",")
	_, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO "+table+" ("+columns+") VALUES ("+placeholders+")", commonArgs...)
	return err
}

func insertRawPage(ctx context.Context, tx *sql.Tx, runID string, snapshotAt time.Time, kind graymarket.SnapshotKind, rankType graymarket.RankType, page graymarket.RawPage) error {
	compressed, err := compress(page.Body)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO raw_response
(run_id,snapshot_at,snapshot_kind,rank_type,page,content_encoding,compression,body,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?)`, runID, snapshotAt.Format(timestampLayout), string(kind), string(rankType), page.Page,
		page.ContentEncoding, "gzip", compressed, page.FetchedAt.Format(timestampLayout))
	return err
}

func compress(body []byte) ([]byte, error) {
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	if _, err := writer.Write(body); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (s *Store) CompactResearch(ctx context.Context, tradeDate string) ([]repository.QualitySummary, error) {
	var workRows int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM rank_intraday_work WHERE trade_date=?", tradeDate).Scan(&workRows); err != nil {
		return nil, err
	}
	if workRows == 0 {
		existing, err := s.Quality(ctx, tradeDate)
		if err != nil {
			return existing, err
		}
		if len(existing) == 2 && existing[0].CollectedDailyClose == 1 && existing[1].CollectedDailyClose == 1 && len(existing[0].MissingDailyClose) == 0 && len(existing[1].MissingDailyClose) == 0 {
			return existing, nil
		}
		return existing, fmt.Errorf("no intraday work is available to create the 15:00 daily close snapshot")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	summaries := make([]repository.QualitySummary, 0, 2)
	closeComplete := true
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		minutes, err := snapshotMinutes(ctx, tx, "rank_intraday_work", tradeDate, rankType, "")
		if err != nil {
			return nil, err
		}
		researchMinutes := filterResearchMinutes(minutes)
		closeMinutes := filterDailyCloseMinutes(minutes)
		missingMinutes := missing(expectedMinuteTimes(), minutes)
		missingResearch := missing(expectedResearchTimes(), researchMinutes)
		missingDailyClose := missing(expectedDailyCloseTimes(), closeMinutes)

		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO rank_snapshot (
run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
SELECT run_id,snapshot_at,trade_date,trade_date,'research_5m',rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at
FROM rank_intraday_work
WHERE trade_date=? AND rank_type=? AND substr(snapshot_at,15,2) IN ('00','05','10','15','20','25','30','35','40','45','50','55')
AND ((substr(snapshot_at,12,5) BETWEEN '09:35' AND '11:30')
OR (substr(snapshot_at,12,5) BETWEEN '13:05' AND '14:55'))`, tradeDate, string(rankType))
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", rankType, err)
		}

		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO rank_snapshot (
run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
SELECT run_id,snapshot_at,trade_date,trade_date,'daily_close',rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at
FROM rank_intraday_work
WHERE trade_date=? AND rank_type=? AND substr(snapshot_at,12,5)='15:00'`, tradeDate, string(rankType))
		if err != nil {
			return nil, fmt.Errorf("archive %s daily close: %w", rankType, err)
		}

		summary := repository.QualitySummary{
			TradeDate:           tradeDate,
			RankType:            rankType,
			ExpectedMinutes:     240,
			CollectedMinutes:    len(minutes),
			ExpectedResearch:    47,
			CollectedResearch:   len(researchMinutes),
			ExpectedDailyClose:  1,
			CollectedDailyClose: len(closeMinutes),
			MissingMinutes:      missingMinutes,
			MissingResearch:     missingResearch,
			MissingDailyClose:   missingDailyClose,
			CompactedAt:         &now,
		}
		if err := upsertQuality(ctx, tx, summary); err != nil {
			return nil, err
		}
		if len(missingDailyClose) > 0 {
			closeComplete = false
		}
		summaries = append(summaries, summary)
	}
	// Research points, the 15:00 board closes, quality summaries, and cleanup
	// are one atomic unit. A failed transaction therefore leaves work rows
	// available for a safe retry.
	if closeComplete {
		if _, err := tx.ExecContext(ctx, "DELETE FROM rank_intraday_work WHERE trade_date=?", tradeDate); err != nil {
			return nil, fmt.Errorf("cleanup compacted intraday data: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if !closeComplete {
		return summaries, fmt.Errorf("15:00 daily close snapshot is missing; intraday work retained for retry: %s", formatMissingDailyClose(summaries))
	}
	return summaries, nil
}

func formatMissingDailyClose(summaries []repository.QualitySummary) string {
	missingTypes := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		if len(summary.MissingDailyClose) > 0 {
			missingTypes = append(missingTypes, string(summary.RankType))
		}
	}
	return strings.Join(missingTypes, ",")
}

func (s *Store) CleanupIntraday(ctx context.Context, tradeDate string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM rank_intraday_work WHERE trade_date=?", tradeDate)
	return err
}

func snapshotMinutes(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, table, tradeDate string, rankType graymarket.RankType, kind string) ([]string, error) {
	query := "SELECT DISTINCT substr(snapshot_at,12,5) FROM " + table + " WHERE trade_date=? AND rank_type=?"
	args := []any{tradeDate, string(rankType)}
	if kind != "" {
		query += " AND snapshot_kind=?"
		args = append(args, kind)
	}
	query += " ORDER BY 1"
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func filterResearchMinutes(minutes []string) []string {
	var result []string
	for _, minute := range minutes {
		if isResearchMinute(minute) {
			result = append(result, minute)
		}
	}
	return result
}

func isResearchMinute(value string) bool {
	if len(value) != 5 || !((value >= "09:35" && value <= "11:30") || (value >= "13:05" && value <= "14:55")) {
		return false
	}
	minute := int((value[3]-'0')*10 + (value[4] - '0'))
	return minute%5 == 0
}

func filterDailyCloseMinutes(minutes []string) []string {
	for _, minute := range minutes {
		if minute == "15:00" {
			return []string{minute}
		}
	}
	return nil
}

func expectedMinuteTimes() []string {
	return timeRange(9, 31, 11, 30, 1, timeRange(13, 1, 15, 0, 1, nil))
}

func expectedResearchTimes() []string {
	return timeRange(9, 35, 11, 30, 5, timeRange(13, 5, 14, 55, 5, nil))
}

func expectedDailyCloseTimes() []string { return []string{"15:00"} }

func timeRange(startHour, startMinute, endHour, endMinute, step int, tail []string) []string {
	base := time.Date(2000, 1, 1, startHour, startMinute, 0, 0, time.UTC)
	end := time.Date(2000, 1, 1, endHour, endMinute, 0, 0, time.UTC)
	result := make([]string, 0, int(end.Sub(base)/time.Minute)+len(tail)+1)
	for current := base; !current.After(end); current = current.Add(time.Duration(step) * time.Minute) {
		result = append(result, current.Format("15:04"))
	}
	return append(result, tail...)
}

func missing(expected, actual []string) []string {
	seen := make(map[string]struct{}, len(actual))
	for _, value := range actual {
		seen[value] = struct{}{}
	}
	result := make([]string, 0)
	for _, value := range expected {
		if _, ok := seen[value]; !ok {
			result = append(result, value)
		}
	}
	return result
}

func upsertQuality(ctx context.Context, tx *sql.Tx, summary repository.QualitySummary) error {
	missingMinutes, _ := json.Marshal(summary.MissingMinutes)
	missingResearch, _ := json.Marshal(summary.MissingResearch)
	missingDailyClose, _ := json.Marshal(summary.MissingDailyClose)
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO research_quality
(trade_date,rank_type,expected_minutes,collected_minutes,expected_research,collected_research,
expected_daily_close,collected_daily_close,missing_minutes_json,missing_research_json,missing_daily_close_json,compacted_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, summary.TradeDate, string(summary.RankType), summary.ExpectedMinutes, summary.CollectedMinutes,
		summary.ExpectedResearch, summary.CollectedResearch, summary.ExpectedDailyClose, summary.CollectedDailyClose,
		string(missingMinutes), string(missingResearch), string(missingDailyClose), summary.CompactedAt.Format(timestampLayout))
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

var _ repository.Store = (*Store)(nil)

func decompress(body []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
