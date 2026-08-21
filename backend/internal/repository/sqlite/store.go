package sqlite

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

const timestampLayout = time.RFC3339Nano

func quoteAvailableRecords(records []graymarket.RankRecord) []graymarket.RankRecord {
	result := make([]graymarket.RankRecord, 0, len(records))
	for _, record := range records {
		if record.QuoteAvailable {
			result = append(result, record)
		}
	}
	return result
}

type Store struct {
	db     *sql.DB
	reader *sql.DB
}

func Open(path string) (*Store, error) {
	return OpenWithReadConns(path, 4)
}

func OpenWithReadConns(path string, readConns int) (*Store, error) {
	if readConns <= 0 {
		readConns = 4
	}
	if readConns > 32 {
		readConns = 32
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	writerDSN := path
	if path != ":memory:" {
		writerDSN = sqliteDSN(path, "_pragma=busy_timeout(30000)", "_pragma=foreign_keys(ON)",
			"_pragma=synchronous(NORMAL)", "_pragma=journal_mode(WAL)")
	}
	db, err := sql.Open("sqlite", writerDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	success := false
	defer func() {
		if !success {
			_ = store.Close()
		}
	}()
	if err := configureSQLite(db, path); err != nil {
		return nil, fmt.Errorf("configure sqlite: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if path == ":memory:" {
		store.reader = db
	} else {
		reader, readerErr := sql.Open("sqlite", sqliteDSN(path, "_pragma=busy_timeout(5000)", "_pragma=foreign_keys(ON)", "_pragma=query_only(ON)"))
		if readerErr != nil {
			return nil, fmt.Errorf("open sqlite reader: %w", readerErr)
		}
		reader.SetMaxOpenConns(readConns)
		reader.SetMaxIdleConns(readConns)
		store.reader = reader
		if err := configureReaderSQLite(reader); err != nil {
			return nil, fmt.Errorf("configure sqlite reader: %w", err)
		}
	}
	if err := migrateDailyQuoteColumns(db); err != nil {
		return nil, fmt.Errorf("migrate daily quote columns: %w", err)
	}
	if err := migrateStockArchiveQuality(db); err != nil {
		return nil, fmt.Errorf("migrate stock archive quality: %w", err)
	}
	if err := migrateStockResearchUniverse(store); err != nil {
		return nil, fmt.Errorf("migrate stock research universe: %w", err)
	}
	if err := migrateResearchCloseModel(db); err != nil {
		return nil, fmt.Errorf("migrate 15:00 close snapshots: %w", err)
	}
	if err := migrateLegacyBoardMoney(store); err != nil {
		return nil, fmt.Errorf("migrate legacy board money: %w", err)
	}
	if err := migrateBoardMoneyAvailability(store); err != nil {
		return nil, fmt.Errorf("migrate board money availability: %w", err)
	}
	if err := migrateArchivePlaceholders(store); err != nil {
		return nil, fmt.Errorf("migrate archive placeholders: %w", err)
	}
	if err := migrateArchiveMetadata(db); err != nil {
		return nil, fmt.Errorf("migrate archive metadata: %w", err)
	}
	if err := migrateArchiveRevisions(store); err != nil {
		return nil, fmt.Errorf("migrate archive revisions: %w", err)
	}
	if err := migrateLightweightArchiveStorage(store); err != nil {
		return nil, fmt.Errorf("migrate lightweight archive storage: %w", err)
	}
	if err := migrateRevisionMetadata(store); err != nil {
		return nil, fmt.Errorf("migrate revision metadata: %w", err)
	}
	if err := migrateAnalytics(store); err != nil {
		return nil, fmt.Errorf("migrate analytics: %w", err)
	}
	if err := recordSchemaMigration(store); err != nil {
		return nil, fmt.Errorf("record schema migration: %w", err)
	}
	if _, err := db.Exec(`UPDATE collection_run
SET status='failed', finished_at=COALESCE(finished_at, ?), error_code='interrupted',
error_message='process stopped before collection completed'
WHERE status='running'`, time.Now().UTC().Format(timestampLayout)); err != nil {
		return nil, fmt.Errorf("recover interrupted runs: %w", err)
	}
	if _, err := db.Exec(`UPDATE relation_sync_run
SET status='failed', finished_at=COALESCE(finished_at, ?), error_code='interrupted',
error_message='process stopped before relation synchronization completed'
WHERE status='running'`, time.Now().UTC().Format(timestampLayout)); err != nil {
		return nil, fmt.Errorf("recover interrupted relation syncs: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM stock_board_relation_stage`); err != nil {
		return nil, fmt.Errorf("cleanup interrupted relation stage: %w", err)
	}
	success = true
	return store, nil
}

// configureSQLite applies connection-local pragmas explicitly. The schema also
// contains these pragmas for fresh databases, but existing databases and
// in-memory test databases must receive the same settings on every open.
func configureSQLite(db *sql.DB, path string) error {
	for _, statement := range []string{
		`PRAGMA busy_timeout=30000`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	if path != ":memory:" {
		if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
			return err
		}
	}
	return nil
}

// migrateLegacyBoardMoney folds the pre-board_money_5m board funding rows into
// the current table once. The old rank_snapshot research rows are then removed
// so there is one authoritative storage path for historical funding data.
func migrateLegacyBoardMoney(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance
WHERE name='legacy_board_money_v1')`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 1 {
		return nil
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO board_money_5m
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,dark_money,regular_money,main_money_inflow,money_available,source_time,fetched_at)
SELECT run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,dark_money,regular_money,main_money_inflow,
CASE WHEN money_available=1 OR dark_money<>0 OR regular_money<>0 OR main_money_inflow<>0 THEN 1 ELSE 0 END,
CAST(CASE WHEN quote_time GLOB '[0-9]*' THEN quote_time ELSE '0' END AS INTEGER),fetched_at
FROM rank_snapshot
WHERE snapshot_kind='research_5m' AND rank_type IN ('industry','concept')`); err != nil {
		return err
	}
	var legacyRows, migratedRows int
	if err := tx.QueryRow(`SELECT count(*) FROM rank_snapshot
WHERE snapshot_kind='research_5m' AND rank_type IN ('industry','concept')`).Scan(&legacyRows); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT count(*) FROM board_money_5m AS money
WHERE EXISTS (SELECT 1 FROM rank_snapshot AS legacy
WHERE legacy.snapshot_kind='research_5m' AND legacy.rank_type IN ('industry','concept')
AND legacy.trade_date=money.trade_date AND legacy.snapshot_at=money.snapshot_at
AND legacy.rank_type=money.rank_type AND legacy.market=money.market AND legacy.code=money.code)`).Scan(&migratedRows); err != nil {
		return err
	}
	if legacyRows != migratedRows {
		return fmt.Errorf("legacy board money migration incomplete: expected %d rows, migrated %d", legacyRows, migratedRows)
	}
	if _, err := tx.Exec(`DELETE FROM rank_snapshot
WHERE snapshot_kind='research_5m' AND rank_type IN ('industry','concept')`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM raw_response
WHERE snapshot_kind='research_5m' AND rank_type IN ('industry','concept')`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(timestampLayout)
	if _, err := tx.Exec(`INSERT INTO database_maintenance(name,completed_at)
VALUES ('legacy_board_money_v1',?)`, now); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateBoardMoneyAvailability repairs historical rows written before the
// explicit availability bit was populated. The old API returned funding
// values even when the bit was left at its zero default; non-zero funding is
// therefore authoritative for those legacy rows.
func migrateBoardMoneyAvailability(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance WHERE name='board_money_available_v2')`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 1 {
		return nil
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE board_money_5m SET money_available=1
WHERE money_available=0 AND (dark_money<>0 OR regular_money<>0 OR main_money_inflow<>0)`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(timestampLayout)
	if _, err := tx.Exec(`INSERT INTO database_maintenance(name,completed_at) VALUES ('board_money_available_v2',?)`, now); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateArchivePlaceholders removes rows that represented unavailable money
// rather than an observed point. Stock rows carrying a real five-minute K-line
// are retained because that table is the current join target for both series.
func migrateArchivePlaceholders(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance WHERE name='archive_placeholders_v2')`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 1 {
		return nil
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT trade_date FROM board_money_5m WHERE money_available=0
UNION SELECT trade_date FROM stock_research_5m WHERE money_available=0 AND kline_available=0`)
	if err != nil {
		return err
	}
	var affectedDates []string
	for rows.Next() {
		var tradeDate string
		if err := rows.Scan(&tradeDate); err != nil {
			rows.Close()
			return err
		}
		affectedDates = append(affectedDates, tradeDate)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM board_money_5m WHERE money_available=0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM stock_research_5m WHERE money_available=0 AND kline_available=0`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(timestampLayout)
	if _, err := tx.Exec(`UPDATE stock_archive_quality SET
money_rows=(SELECT coalesce(sum(money_available),0) FROM stock_research_5m AS research WHERE research.trade_date=stock_archive_quality.trade_date),
kline_rows=(SELECT coalesce(sum(kline_available),0) FROM stock_research_5m AS research WHERE research.trade_date=stock_archive_quality.trade_date),
updated_at=?`, now); err != nil {
		return err
	}
	for _, tradeDate := range affectedDates {
		if err := refreshArchiveManifest(context.Background(), tx, tradeDate); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO database_maintenance(name,completed_at) VALUES ('archive_placeholders_v2',?)`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func readerClose(store *Store) error {
	if store.reader != nil && store.reader != store.db {
		return store.reader.Close()
	}
	return nil
}

func recordSchemaMigration(store *Store) error {
	started := time.Now()
	_, err := store.db.Exec(`INSERT INTO schema_migration(version,applied_at,duration_ms,checksum)
VALUES (1,?,?,?)
ON CONFLICT(version) DO UPDATE SET checksum=excluded.checksum`,
		time.Now().UTC().Format(timestampLayout), time.Since(started).Milliseconds(), fmt.Sprintf("%x", sha256.Sum256([]byte(schema))))
	return err
}

func (s *Store) Close() error {
	var closeErr error
	if s.reader != nil && s.reader != s.db {
		if err := s.reader.Close(); err != nil {
			closeErr = err
		}
	}
	if err := s.db.Close(); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	return closeErr
}

func migrateStockArchiveQuality(db *sql.DB) error {
	for _, statement := range []string{
		`ALTER TABLE rank_snapshot ADD COLUMN money_available INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE board_money_5m ADD COLUMN money_available INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE stock_research_5m ADD COLUMN money_available INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(statement); err != nil && !strings.Contains(err.Error(), "duplicate column") && !strings.Contains(err.Error(), "no such table") {
			return err
		}
	}
	columns := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(stock_archive_quality)`)
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
	for _, migration := range []struct{ name, definition string }{
		{"expected_kline_stocks", "INTEGER NOT NULL DEFAULT 0"},
		{"daily_kline_rows", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if !columns[migration.name] {
			if _, err := db.Exec(`ALTER TABLE stock_archive_quality ADD COLUMN ` + migration.name + ` ` + migration.definition); err != nil {
				return err
			}
		}
	}
	// Existing archives predate explicit daily-K accounting. Quote-available
	// close rows are the authoritative daily bars for those dates.
	_, err = db.Exec(`UPDATE stock_archive_quality SET
expected_kline_stocks=(SELECT count(*) FROM rank_snapshot WHERE trade_date=stock_archive_quality.trade_date
AND snapshot_kind='daily_close' AND rank_type='stock' AND quote_available=1),
daily_kline_rows=(SELECT count(*) FROM rank_snapshot WHERE trade_date=stock_archive_quality.trade_date
AND snapshot_kind='daily_close' AND rank_type='stock' AND quote_available=1)
WHERE expected_kline_stocks=0 AND daily_kline_rows=0`)
	return err
}

// migrateStockResearchUniverse removes placeholder five-minute rows for
// suspended or otherwise unavailable daily quotes. Daily identity snapshots
// remain in rank_snapshot; stock_research_5m contains only archive-eligible
// securities with a usable daily bar.
func migrateStockResearchUniverse(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance
WHERE name='stock_research_universe_v1')`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 1 {
		return nil
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT trade_date,market,code
FROM rank_snapshot WHERE snapshot_kind='daily_close' AND rank_type='stock'
AND quote_available=0 ORDER BY trade_date,market,code`)
	if err != nil {
		return err
	}
	type unavailableStock struct {
		tradeDate string
		market    int64
		code      string
	}
	var stocks []unavailableStock
	dates := make(map[string]struct{})
	for rows.Next() {
		var stock unavailableStock
		if err := rows.Scan(&stock.tradeDate, &stock.market, &stock.code); err != nil {
			rows.Close()
			return err
		}
		stocks = append(stocks, stock)
		dates[stock.tradeDate] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, stock := range stocks {
		if _, err := tx.Exec(`DELETE FROM stock_research_5m
WHERE trade_date=? AND market=? AND code=?`, stock.tradeDate, stock.market, stock.code); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(timestampLayout)
	for date := range dates {
		var moneyRows, klineRows int
		if err := tx.QueryRow(`SELECT coalesce(sum(money_available),0),coalesce(sum(kline_available),0)
FROM stock_research_5m WHERE trade_date=?`, date).Scan(&moneyRows, &klineRows); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE stock_archive_quality SET money_rows=?,kline_rows=?,
money_archived_at=CASE WHEN ?>0 THEN coalesce(money_archived_at,?) ELSE NULL END,
kline_archived_at=CASE WHEN ?=expected_kline_stocks*expected_points THEN coalesce(kline_archived_at,?) ELSE NULL END,
updated_at=? WHERE trade_date=?`, moneyRows, klineRows, moneyRows, now, klineRows, now, now, date); err != nil {
			return err
		}
		if err := refreshArchiveManifest(context.Background(), tx, date); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO database_maintenance(name,completed_at)
VALUES ('stock_research_universe_v1',?)`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateDailyQuoteColumns(db *sql.DB) error {
	migrations := []struct{ name, definition string }{
		{"open_price", "REAL NOT NULL DEFAULT 0"},
		{"high_price", "REAL NOT NULL DEFAULT 0"},
		{"low_price", "REAL NOT NULL DEFAULT 0"},
		{"close_price", "REAL NOT NULL DEFAULT 0"},
		{"previous_close", "REAL NOT NULL DEFAULT 0"},
		{"change_value", "REAL NOT NULL DEFAULT 0"},
		{"volume", "INTEGER NOT NULL DEFAULT 0"},
		{"turnover", "INTEGER NOT NULL DEFAULT 0"},
		{"turnover_rate", "REAL NOT NULL DEFAULT 0"},
		{"amplitude", "REAL NOT NULL DEFAULT 0"},
		{"quote_available", "INTEGER NOT NULL DEFAULT 0"},
		{"money_available", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, table := range []string{"rank_intraday_work", "rank_snapshot"} {
		columns := map[string]bool{}
		rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
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
		for _, migration := range migrations {
			if !columns[migration.name] {
				if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + migration.name + ` ` + migration.definition); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

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
latest_price_raw,change_pct,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at,created_at)
SELECT run_id,snapshot_at,trade_date,requested_date,'daily_close',rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
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
		researchMinutes, err := snapshotMinutes(context.Background(), tx, "board_money_5m", key.tradeDate, graymarket.RankType(key.rankType), "")
		if err != nil {
			return err
		}
		if len(researchMinutes) == 0 {
			researchMinutes, err = snapshotMinutes(context.Background(), tx, "rank_snapshot", key.tradeDate, graymarket.RankType(key.rankType), string(graymarket.SnapshotResearch5m))
			if err != nil {
				return err
			}
		}
		closeMinutes, err := snapshotMinutes(context.Background(), tx, "rank_snapshot", key.tradeDate, graymarket.RankType(key.rankType), string(graymarket.SnapshotDailyClose))
		if err != nil {
			return err
		}
		missingResearch, _ := json.Marshal(missing(expectedResearchTimes(), researchMinutes))
		missingClose, _ := json.Marshal(missing(expectedDailyCloseTimes(), closeMinutes))
		if _, err := tx.Exec(`UPDATE research_quality SET expected_research=48,collected_research=?,
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
	records := snapshot.Records
	if snapshot.RankType == graymarket.RankStock {
		records = quoteAvailableRecords(snapshot.Records)
	}
	if len(records) == 0 {
		return fmt.Errorf("daily close snapshot has no eligible records for %s", snapshot.RankType)
	}
	for _, record := range records {
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

func (s *Store) SaveBoardArchive(ctx context.Context, runID string, snapshot graymarket.RankSnapshot, points []graymarket.MoneyPoint) error {
	if snapshot.RankType != graymarket.RankIndustry && snapshot.RankType != graymarket.RankConcept {
		return fmt.Errorf("board archive cannot contain rank type %s", snapshot.RankType)
	}
	if snapshot.SnapshotAt.Format("15:04") != "15:00" {
		return fmt.Errorf("board close snapshot_at must be 15:00, got %s", snapshot.SnapshotAt.Format("15:04"))
	}
	if len(snapshot.Records) == 0 {
		return fmt.Errorf("incomplete %s board archive: records=%d points=%d", snapshot.RankType, len(snapshot.Records), len(points))
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM board_money_5m WHERE trade_date=? AND rank_type=?`, snapshot.TradeDate, string(snapshot.RankType)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type=?`, snapshot.TradeDate, string(snapshot.RankType)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM raw_response WHERE substr(snapshot_at,1,10)=? AND snapshot_kind='daily_close' AND rank_type=?`, snapshot.TradeDate, string(snapshot.RankType)); err != nil {
		return err
	}
	for _, record := range snapshot.Records {
		if err := insertRecord(ctx, tx, "rank_snapshot", runID, snapshot.TradeDate, string(graymarket.SnapshotDailyClose), record); err != nil {
			return err
		}
	}
	for _, page := range snapshot.RawPages {
		if err := insertRawPage(ctx, tx, runID, snapshot.SnapshotAt, graymarket.SnapshotDailyClose, snapshot.RankType, page); err != nil {
			return err
		}
	}
	for _, point := range points {
		clock := point.SnapshotAt.Format("15:04")
		if point.TradeDate != snapshot.TradeDate || point.RankType != snapshot.RankType || !isResearchMinute(clock) && clock != "15:00" {
			return fmt.Errorf("invalid %s board money point %s %s", snapshot.RankType, point.Code, point.SnapshotAt)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO board_money_5m
(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,dark_money,regular_money,main_money_inflow,money_available,source_time,fetched_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, runID, point.SnapshotAt.Format(timestampLayout), point.TradeDate, string(point.RankType), point.Rank,
			point.Market, point.Code, point.Name, point.DarkMoney, point.RegularMoney, point.MainMoneyInflow, 1, point.SourceTime, point.FetchedAt.Format(timestampLayout))
		if err != nil {
			return err
		}
	}
	minutes, err := snapshotMinutes(ctx, tx, "rank_intraday_work", snapshot.TradeDate, snapshot.RankType, "")
	if err != nil {
		return err
	}
	researchMinutes, err := snapshotMinutes(ctx, tx, "board_money_5m", snapshot.TradeDate, snapshot.RankType, "")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	summary := repository.QualitySummary{
		TradeDate: snapshot.TradeDate, RankType: snapshot.RankType,
		ExpectedMinutes: 240, CollectedMinutes: len(minutes),
		ExpectedResearch: 48, CollectedResearch: len(researchMinutes),
		ExpectedDailyClose: 1, CollectedDailyClose: 1,
		MissingMinutes:    missing(expectedMinuteTimes(), minutes),
		MissingResearch:   missing(expectedResearchTimes(), researchMinutes),
		MissingDailyClose: []string{}, CompactedAt: &now,
	}
	if err := upsertQuality(ctx, tx, summary); err != nil {
		return err
	}
	if err := refreshArchiveManifest(ctx, tx, snapshot.TradeDate); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveStockArchive(ctx context.Context, runID string, snapshot graymarket.RankSnapshot, points []graymarket.MoneyPoint) error {
	if snapshot.RankType != graymarket.RankStock || len(snapshot.Records) == 0 {
		return fmt.Errorf("incomplete stock archive: records=%d points=%d", len(snapshot.Records), len(points))
	}
	if snapshot.SnapshotAt.Format("15:04") != "15:00" {
		return fmt.Errorf("stock close snapshot_at must be 15:00, got %s", snapshot.SnapshotAt.Format("15:04"))
	}
	records := quoteAvailableRecords(snapshot.Records)
	if len(records) == 0 {
		return fmt.Errorf("incomplete stock archive: no eligible records from %d identities", len(snapshot.Records))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A rerun starts a fresh attempt. Only securities with a valid daily quote
	// belong to either the daily identity snapshot or the five-minute archive.
	if _, err := tx.ExecContext(ctx, `DELETE FROM stock_research_5m WHERE trade_date=?`, snapshot.TradeDate); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stock_kline_source WHERE trade_date=?`, snapshot.TradeDate); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE stock_research_5m SET money_rank=-1 WHERE trade_date=?`, snapshot.TradeDate); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock'`, snapshot.TradeDate); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM raw_response WHERE substr(snapshot_at,1,10)=? AND snapshot_kind='daily_close' AND rank_type='stock'`, snapshot.TradeDate); err != nil {
		return err
	}
	expectedKlineStocks := len(records)
	type stockKey struct {
		market int64
		code   string
	}
	eligible := make(map[stockKey]struct{}, len(snapshot.Records))
	for _, record := range records {
		eligible[stockKey{market: record.Market, code: record.Code}] = struct{}{}
		if err := insertRecord(ctx, tx, "rank_snapshot", runID, snapshot.TradeDate, string(graymarket.SnapshotDailyClose), record); err != nil {
			return err
		}
	}
	for _, page := range snapshot.RawPages {
		if err := insertRawPage(ctx, tx, runID, snapshot.SnapshotAt, graymarket.SnapshotDailyClose, snapshot.RankType, page); err != nil {
			return err
		}
	}
	for _, point := range points {
		minuteIndex, ok := researchMinuteIndex(point.SnapshotAt)
		if !ok || point.TradeDate != snapshot.TradeDate || point.RankType != graymarket.RankStock {
			return fmt.Errorf("invalid stock money point %s %s", point.Code, point.SnapshotAt)
		}
		if _, ok := eligible[stockKey{market: point.Market, code: point.Code}]; !ok {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO stock_research_5m
(trade_date,minute_index,market,code,money_rank,dark_money,regular_money,main_money_inflow,money_available)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(trade_date,minute_index,market,code) DO UPDATE SET
money_rank=excluded.money_rank,dark_money=excluded.dark_money,
	regular_money=excluded.regular_money,main_money_inflow=excluded.main_money_inflow,money_available=1`, point.TradeDate, minuteIndex, point.Market, point.Code, point.Rank,
			point.DarkMoney, point.RegularMoney, point.MainMoneyInflow, 1)
		if err != nil {
			return err
		}
	}
	// Do not materialize unavailable funding points. A row is now an observed
	// money point; SaveStockKlines may add a row later when only the K-line was
	// available for that minute.
	var klineRows, moneyRows int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(money_available),0) FROM stock_research_5m WHERE trade_date=?`, snapshot.TradeDate).Scan(&moneyRows); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM stock_research_5m WHERE trade_date=? AND kline_available=1`, snapshot.TradeDate).Scan(&klineRows); err != nil {
		return err
	}
	now := time.Now().UTC().Format(timestampLayout)
	_, err = tx.ExecContext(ctx, `INSERT INTO stock_archive_quality
(trade_date,expected_stocks,expected_points,expected_kline_stocks,money_rows,kline_rows,daily_close_rows,daily_kline_rows,money_archived_at,updated_at)
VALUES (?,?,48,?,?,?,?,?,?,?)
ON CONFLICT(trade_date) DO UPDATE SET expected_stocks=excluded.expected_stocks,expected_points=48,
expected_kline_stocks=excluded.expected_kline_stocks,money_rows=excluded.money_rows,kline_rows=excluded.kline_rows,
daily_close_rows=excluded.daily_close_rows,daily_kline_rows=excluded.daily_kline_rows,
money_archived_at=excluded.money_archived_at,
kline_archived_at=CASE WHEN excluded.kline_rows=excluded.expected_kline_stocks*excluded.expected_points
THEN coalesce(stock_archive_quality.kline_archived_at,excluded.updated_at) ELSE NULL END,
updated_at=excluded.updated_at`,
		snapshot.TradeDate, len(records), expectedKlineStocks, moneyRows, klineRows, len(records), expectedKlineStocks, now, now)
	if err != nil {
		return err
	}
	if err := refreshArchiveManifest(ctx, tx, snapshot.TradeDate); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveStockKlines(ctx context.Context, runID string, points []graymarket.StockKlinePoint) error {
	if len(points) == 0 {
		return graymarket.ErrNoData
	}
	tradeDate := points[0].TradeDate
	type stockKey struct {
		market int64
		code   string
	}
	pointIndexes := make(map[stockKey]map[int]struct{})
	sources := make(map[stockKey]string)
	fetchedAt := make(map[stockKey]time.Time)
	for _, point := range points {
		minuteIndex, ok := researchMinuteIndex(point.SnapshotAt)
		if !ok || point.TradeDate != tradeDate || point.Code == "" {
			return fmt.Errorf("invalid stock kline point %s %s", point.Code, point.SnapshotAt)
		}
		key := stockKey{market: point.Market, code: point.Code}
		if pointIndexes[key] == nil {
			pointIndexes[key] = make(map[int]struct{}, 48)
		}
		if _, duplicate := pointIndexes[key][minuteIndex]; duplicate {
			return fmt.Errorf("duplicate stock kline point %s at %s", point.Code, point.SnapshotAt.Format("15:04"))
		}
		pointIndexes[key][minuteIndex] = struct{}{}
		source := point.Source
		if source == "" {
			source = graymarket.KlineSourceUnknown
		}
		if previous := sources[key]; previous != "" && previous != source {
			return fmt.Errorf("stock %s kline batch mixes sources %s and %s", point.Code, previous, source)
		}
		sources[key] = source
		if point.FetchedAt.After(fetchedAt[key]) {
			fetchedAt[key] = point.FetchedAt
		}
	}
	for key, indexes := range pointIndexes {
		if len(indexes) != 48 {
			return fmt.Errorf("incomplete stock kline batch for %s: expected 48 rows, got %d", key.code, len(indexes))
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key := range pointIndexes {
		var eligible int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM rank_snapshot
WHERE trade_date=? AND snapshot_kind='daily_close' AND rank_type='stock' AND market=? AND code=? AND quote_available=1)`,
			tradeDate, key.market, key.code).Scan(&eligible); err != nil {
			return err
		}
		if eligible != 1 {
			return fmt.Errorf("stock %s is not an eligible daily-close kline candidate", key.code)
		}
	}
	for _, point := range points {
		minuteIndex, _ := researchMinuteIndex(point.SnapshotAt)
		_, err := tx.ExecContext(ctx, `INSERT INTO stock_research_5m
(trade_date,minute_index,market,code,money_rank,dark_money,regular_money,main_money_inflow,money_available,
open_price_e4,high_price_e4,low_price_e4,close_price_e4,volume,turnover,amplitude_ppm,change_pct_ppm,
change_value_e4,turnover_rate_ppm,kline_available)
VALUES (?,?,?,?,0,0,0,0,0,?,?,?,?,?,?,?,?,?,?,1)
ON CONFLICT(trade_date,minute_index,market,code) DO UPDATE SET
open_price_e4=excluded.open_price_e4,high_price_e4=excluded.high_price_e4,
low_price_e4=excluded.low_price_e4,close_price_e4=excluded.close_price_e4,
volume=excluded.volume,turnover=excluded.turnover,amplitude_ppm=excluded.amplitude_ppm,
change_pct_ppm=excluded.change_pct_ppm,change_value_e4=excluded.change_value_e4,
turnover_rate_ppm=excluded.turnover_rate_ppm,kline_available=1`,
			tradeDate, minuteIndex, point.Market, point.Code, scaleE4(point.OpenPrice), scaleE4(point.HighPrice),
			scaleE4(point.LowPrice), scaleE4(point.ClosePrice), point.Volume, point.Turnover, scalePPM(point.Amplitude),
			scalePPM(point.ChangePct), scaleE4(point.ChangeValue), scalePPM(point.TurnoverRate))
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	for key, indexes := range pointIndexes {
		at := fetchedAt[key]
		if at.IsZero() {
			at = now
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO stock_kline_source
(trade_date,market,code,source,point_count,parser_version,fetched_at,run_id)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(trade_date,market,code) DO UPDATE SET source=excluded.source,
point_count=excluded.point_count,parser_version=excluded.parser_version,
fetched_at=excluded.fetched_at,run_id=excluded.run_id`,
			tradeDate, key.market, key.code, sources[key], len(indexes), archiveParserVersion,
			at.Format(timestampLayout), runID); err != nil {
			return err
		}
	}
	var expectedKlineStocks, expectedPoints, klineRows int
	if err := tx.QueryRowContext(ctx, `SELECT expected_kline_stocks,expected_points FROM stock_archive_quality WHERE trade_date=?`, tradeDate).Scan(&expectedKlineStocks, &expectedPoints); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM stock_research_5m WHERE trade_date=? AND kline_available=1`, tradeDate).Scan(&klineRows); err != nil {
		return err
	}
	nowText := now.Format(timestampLayout)
	var archivedAt any
	if klineRows == expectedKlineStocks*expectedPoints {
		archivedAt = nowText
	}
	if _, err := tx.ExecContext(ctx, `UPDATE stock_archive_quality SET kline_rows=?,kline_archived_at=?,updated_at=? WHERE trade_date=?`, klineRows, archivedAt, nowText, tradeDate); err != nil {
		return err
	}
	if err := refreshArchiveManifest(ctx, tx, tradeDate); err != nil {
		return err
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
		record.LatestPriceRaw, record.OpenPrice, record.HighPrice, record.LowPrice, record.ClosePrice, record.PreviousClose,
		record.ChangeValue, record.ChangePct, record.Volume, record.Turnover, record.TurnoverRate, record.Amplitude,
		boolInt(record.QuoteAvailable), boolInt(record.MoneyAvailable),
		record.DarkMoney, record.RegularMoney, record.MainMoneyInflow,
		record.DarkActivity, record.DarkInflowRatio, record.UpCount, record.FlatCount, record.DownCount,
		record.LeaderName, record.LeaderCode, record.SourceVersion, record.SourceSortFlag, boolInt(record.SourceDescending),
		record.FetchedAt.Format(timestampLayout),
	)
	columns := `run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,quote_time,
	latest_price_raw,open_price,high_price,low_price,close_price,previous_close,change_value,change_pct,
	volume,turnover,turnover_rate,amplitude,quote_available,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at`
	if table == "rank_snapshot" {
		columns = `run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,
	latest_price_raw,open_price,high_price,low_price,close_price,previous_close,change_value,change_pct,
	volume,turnover,turnover_rate,amplitude,quote_available,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
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

		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO board_money_5m (
run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,dark_money,regular_money,main_money_inflow,
money_available,source_time,fetched_at)
SELECT run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,dark_money,regular_money,main_money_inflow,
money_available,CAST(CASE WHEN quote_time GLOB '[0-9]*' THEN quote_time ELSE '0' END AS INTEGER),fetched_at
FROM rank_intraday_work
WHERE trade_date=? AND rank_type=? AND substr(snapshot_at,15,2) IN ('00','05','10','15','20','25','30','35','40','45','50','55')
AND ((substr(snapshot_at,12,5) BETWEEN '09:35' AND '11:30')
OR (substr(snapshot_at,12,5) BETWEEN '13:05' AND '15:00'))`, tradeDate, string(rankType))
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", rankType, err)
		}

		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO rank_snapshot (
run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at)
SELECT run_id,snapshot_at,trade_date,trade_date,'daily_close',rank_type,rank,market,code,name,quote_time,
latest_price_raw,change_pct,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,
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
			ExpectedResearch:    48,
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

func (s *Store) CleanupArchivedIntraday(ctx context.Context, beforeDate string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	complete := `date_value<?
AND (SELECT count(*) FROM research_quality quality WHERE quality.trade_date=date_value
AND quality.collected_research=quality.expected_research
AND quality.collected_daily_close=quality.expected_daily_close)=2
	AND EXISTS (SELECT 1 FROM stock_archive_quality stock WHERE stock.trade_date=date_value
	AND stock.money_rows=stock.expected_kline_stocks*stock.expected_points
	AND stock.kline_rows=stock.expected_kline_stocks*stock.expected_points
	AND stock.daily_close_rows=stock.expected_stocks
	AND stock.daily_kline_rows=stock.expected_kline_stocks)`
	if _, err := tx.ExecContext(ctx, `DELETE FROM rank_intraday_work AS work WHERE `+strings.ReplaceAll(complete, "date_value", "work.trade_date"), beforeDate); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM raw_response AS raw WHERE snapshot_kind='research_5m' AND `+
		strings.ReplaceAll(complete, "date_value", "substr(raw.snapshot_at,1,10)"), beforeDate); err != nil {
		return err
	}
	return tx.Commit()
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
	if len(value) != 5 || !((value >= "09:35" && value <= "11:30") || (value >= "13:05" && value <= "15:00")) {
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
	return timeRange(9, 35, 11, 30, 5, timeRange(13, 5, 15, 0, 5, nil))
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

func researchMinuteIndex(value time.Time) (int, bool) {
	minutes := value.Hour()*60 + value.Minute()
	switch {
	case minutes >= 9*60+35 && minutes <= 11*60+30 && (minutes-(9*60+35))%5 == 0:
		return (minutes - (9*60 + 35)) / 5, true
	case minutes >= 13*60+5 && minutes <= 15*60 && (minutes-(13*60+5))%5 == 0:
		return 24 + (minutes-(13*60+5))/5, true
	default:
		return 0, false
	}
}

func scaleE4(value float64) int64    { return int64(math.Round(value * 1e4)) }
func scalePPM(value float64) int64   { return int64(math.Round(value * 1e6)) }
func unscaleE4(value int64) float64  { return float64(value) / 1e4 }
func unscalePPM(value int64) float64 { return float64(value) / 1e6 }

var _ repository.Store = (*Store)(nil)

func decompress(body []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func (s *Store) writeDB() *sql.DB { return s.db }

func (s *Store) readDB() *sql.DB {
	if s.reader != nil {
		return s.reader
	}
	return s.db
}

func sqliteDSN(path string, pragmas ...string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: strings.Join(pragmas, "&")}).String()
}

func configureReaderSQLite(db *sql.DB) error {
	for _, statement := range []string{
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA query_only=ON`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}
