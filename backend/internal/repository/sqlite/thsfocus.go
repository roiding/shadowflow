package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
	"github.com/roiding/shadowflow/internal/thsfocus"
)

const thsFocusSchema = `
CREATE TABLE IF NOT EXISTS ths_focus_day (
    source TEXT NOT NULL,
    scope TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    raw_pages BLOB NOT NULL,
    PRIMARY KEY (source, scope, trade_date)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS ths_theme_stock_day (
    source TEXT NOT NULL,
    scope TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    stock_code TEXT NOT NULL,
    stock_name TEXT NOT NULL,
    theme_code TEXT NOT NULL,
    theme_name TEXT NOT NULL,
    theme_rank INTEGER NOT NULL,
    theme_limit_up_count INTEGER NOT NULL,
    day_sealed_count INTEGER NOT NULL,
    PRIMARY KEY (source, scope, trade_date, stock_code, theme_code)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_ths_theme_stock_code
    ON ths_theme_stock_day (scope, stock_code, trade_date);

CREATE INDEX IF NOT EXISTS idx_ths_theme_stock_theme
    ON ths_theme_stock_day (scope, theme_code, trade_date, stock_code);

CREATE TABLE IF NOT EXISTS ths_theme_tab_day (
    source TEXT NOT NULL,
    scope TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    tab_name TEXT NOT NULL,
    tab_rank INTEGER NOT NULL,
    stock_count INTEGER NOT NULL,
    usable INTEGER NOT NULL,
    fetched_at TEXT NOT NULL,
    PRIMARY KEY (source, scope, trade_date, tab_name)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_ths_theme_tab_day_date
    ON ths_theme_tab_day (scope, trade_date, tab_rank);

CREATE TABLE IF NOT EXISTS ths_theme_tab_stock (
    source TEXT NOT NULL,
    scope TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    tab_name TEXT NOT NULL,
    tab_rank INTEGER NOT NULL,
    stock_code TEXT NOT NULL,
    stock_name TEXT NOT NULL,
    market_id INTEGER NOT NULL,
    reason TEXT NOT NULL,
    abnormal_reason TEXT NOT NULL,
    detail_reason TEXT NOT NULL,
    usable INTEGER NOT NULL,
    PRIMARY KEY (source, scope, trade_date, tab_name, stock_code)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_ths_theme_tab_stock_code
    ON ths_theme_tab_stock (scope, stock_code, trade_date);

CREATE INDEX IF NOT EXISTS idx_ths_theme_tab_stock_tab
    ON ths_theme_tab_stock (scope, tab_name, trade_date, stock_code);
`

func (store *Store) SaveThsFocus(ctx context.Context, snapshot thsfocus.Snapshot) error {
	if err := thsfocus.ValidateSnapshot(snapshot); err != nil {
		return err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	rawPages, err := json.Marshal(snapshot.RawPages)
	if err != nil {
		return err
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	var previous string
	err = transaction.QueryRowContext(ctx, `SELECT fetched_at FROM ths_focus_day WHERE source=? AND scope=? AND trade_date=?`, snapshot.Source, snapshot.Scope, snapshot.TradeDate).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		previousTime, parseErr := time.Parse(time.RFC3339Nano, previous)
		if parseErr != nil {
			return parseErr
		}
		if snapshot.FetchedAt.Before(previousTime) {
			return fmt.Errorf("cannot replace a newer THS focus snapshot")
		}
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO ths_focus_day(source,scope,trade_date,fetched_at,payload_json,raw_pages)
VALUES(?,?,?,?,?,?) ON CONFLICT(source,scope,trade_date) DO UPDATE SET fetched_at=excluded.fetched_at,payload_json=excluded.payload_json,raw_pages=excluded.raw_pages`,
		snapshot.Source, snapshot.Scope, snapshot.TradeDate, formatTimestamp(snapshot.FetchedAt), string(payload), rawPages); err != nil {
		return err
	}
	if err := replaceThsTabRows(ctx, transaction, snapshot); err != nil {
		return err
	}
	return transaction.Commit()
}

func replaceThsTabRows(ctx context.Context, transaction *sql.Tx, snapshot thsfocus.Snapshot) error {
	if snapshot.Tabs == nil {
		// Older snapshots only contain block_top data. Leave the tab-derived
		// tables untouched rather than treating a broad Top20 block as the
		// source's limit-up chart classification.
		return nil
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM ths_theme_tab_day
WHERE source=? AND scope=? AND trade_date=?`, snapshot.Source, snapshot.Scope, snapshot.TradeDate); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM ths_theme_tab_stock
WHERE source=? AND scope=? AND trade_date=?`, snapshot.Source, snapshot.Scope, snapshot.TradeDate); err != nil {
		return err
	}
	fetchedAt := formatTimestamp(snapshot.FetchedAt)
	for _, tab := range snapshot.Tabs {
		usable := thsfocus.IsUsableThemeTab(tab)
		usableValue := 0
		if usable {
			usableValue = 1
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO ths_theme_tab_day
(source,scope,trade_date,tab_name,tab_rank,stock_count,usable,fetched_at)
VALUES(?,?,?,?,?,?,?,?)`, snapshot.Source, snapshot.Scope, snapshot.TradeDate,
			tab.Name, tab.Rank, len(tab.Stocks), usableValue, fetchedAt); err != nil {
			return err
		}
		for _, stock := range tab.Stocks {
			if _, err := transaction.ExecContext(ctx, `INSERT INTO ths_theme_tab_stock
(source,scope,trade_date,tab_name,tab_rank,stock_code,stock_name,market_id,reason,abnormal_reason,detail_reason,usable)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, snapshot.Source, snapshot.Scope, snapshot.TradeDate,
				tab.Name, tab.Rank, stock.Code, stock.Name, stock.MarketID,
				stock.Reason, stock.AbnormalReason, stock.DetailReason, usableValue); err != nil {
				return err
			}
		}
	}
	return nil
}

func replaceThsThemeRows(ctx context.Context, transaction *sql.Tx, snapshot thsfocus.Snapshot) error {
	if _, err := transaction.ExecContext(ctx, `DELETE FROM ths_theme_stock_day
WHERE source=? AND scope=? AND trade_date=?`, snapshot.Source, snapshot.Scope, snapshot.TradeDate); err != nil {
		return err
	}
	daySealedCount := 0
	seenDayStocks := make(map[string]struct{})
	for _, block := range snapshot.Blocks {
		for _, stock := range block.Stocks {
			seenDayStocks[stock.Code] = struct{}{}
		}
	}
	daySealedCount = len(seenDayStocks)
	for index, block := range snapshot.Blocks {
		if !thsfocus.IsUsableThemeBlock(block) {
			continue
		}
		seenStocks := make(map[string]struct{}, len(block.Stocks))
		for _, stock := range block.Stocks {
			if _, duplicate := seenStocks[stock.Code]; duplicate {
				continue
			}
			seenStocks[stock.Code] = struct{}{}
			if _, err := transaction.ExecContext(ctx, `INSERT INTO ths_theme_stock_day
(source,scope,trade_date,stock_code,stock_name,theme_code,theme_name,theme_rank,theme_limit_up_count,day_sealed_count)
VALUES(?,?,?,?,?,?,?,?,?,?)`, snapshot.Source, snapshot.Scope, snapshot.TradeDate,
				stock.Code, stock.Name, block.Code, block.Name, index+1, block.LimitUpCount, daySealedCount); err != nil {
				return err
			}
		}
	}
	return nil
}

func (store *Store) ReadThsFocus(ctx context.Context, date, scope string) (*thsfocus.Snapshot, error) {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.Format("2006-01-02") != date || !limitup.ValidScope(scope) {
		return nil, fmt.Errorf("%w: invalid THS focus date or scope", limitup.ErrInvalid)
	}
	var payload string
	err = store.reader.QueryRowContext(ctx, `SELECT payload_json FROM ths_focus_day WHERE source=? AND scope=? AND trade_date=?`, "ths_focus", scope, date).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot thsfocus.Snapshot
	if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
		return nil, fmt.Errorf("decode THS focus snapshot: %w", err)
	}
	if snapshot.TradeDate != date || snapshot.Scope != scope || snapshot.Source != "ths_focus" {
		return nil, fmt.Errorf("stored THS focus identity mismatch")
	}
	return &snapshot, nil
}

// migrateThsThemeStock builds the derived stock-theme-day index from the
// already archived focus payloads. It is a one-time, restart-safe migration;
// the source table remains the evidence of record.
func migrateThsThemeStock(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance
WHERE name='ths_theme_stock_v1')`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 1 {
		return nil
	}
	type storedFocus struct {
		source    string
		scope     string
		tradeDate string
		payload   string
	}
	rows, err := store.db.Query(`SELECT source,scope,trade_date,payload_json
FROM ths_focus_day ORDER BY trade_date,scope`)
	if err != nil {
		return err
	}
	stored := make([]storedFocus, 0)
	for rows.Next() {
		var item storedFocus
		if err := rows.Scan(&item.source, &item.scope, &item.tradeDate, &item.payload); err != nil {
			rows.Close()
			return err
		}
		stored = append(stored, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	transaction, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.Exec(`DELETE FROM ths_theme_stock_day`); err != nil {
		return err
	}
	for _, item := range stored {
		var snapshot thsfocus.Snapshot
		if err := json.Unmarshal([]byte(item.payload), &snapshot); err != nil {
			return fmt.Errorf("decode THS focus %s/%s: %w", item.tradeDate, item.scope, err)
		}
		if snapshot.Source != item.source || snapshot.Scope != item.scope || snapshot.TradeDate != item.tradeDate {
			return fmt.Errorf("stored THS focus identity mismatch for %s/%s", item.tradeDate, item.scope)
		}
		if err := replaceThsThemeRows(context.Background(), transaction, snapshot); err != nil {
			return err
		}
	}
	if _, err := transaction.Exec(`INSERT INTO database_maintenance(name,completed_at)
VALUES('ths_theme_stock_v1',?)`, formatTimestamp(time.Now())); err != nil {
		return err
	}
	return transaction.Commit()
}

// migrateThsThemeTabs builds the limit-up chart category index from snapshots
// that contain the tab_list/all_tab_data payload. Snapshots from the older
// block_top-only collector are intentionally skipped: a broad Top20 block is
// not the same classification as the source's daily limit-up chart.
func migrateThsThemeTabs(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance
WHERE name='ths_theme_tabs_v1')`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 1 {
		return nil
	}
	type storedFocus struct {
		source    string
		scope     string
		tradeDate string
		payload   string
	}
	rows, err := store.db.Query(`SELECT source,scope,trade_date,payload_json
FROM ths_focus_day ORDER BY trade_date,scope`)
	if err != nil {
		return err
	}
	stored := make([]storedFocus, 0)
	for rows.Next() {
		var item storedFocus
		if err := rows.Scan(&item.source, &item.scope, &item.tradeDate, &item.payload); err != nil {
			rows.Close()
			return err
		}
		stored = append(stored, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	transaction, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.Exec(`DELETE FROM ths_theme_tab_day`); err != nil {
		return err
	}
	if _, err := transaction.Exec(`DELETE FROM ths_theme_tab_stock`); err != nil {
		return err
	}
	for _, item := range stored {
		var snapshot thsfocus.Snapshot
		if err := json.Unmarshal([]byte(item.payload), &snapshot); err != nil {
			return fmt.Errorf("decode THS focus %s/%s: %w", item.tradeDate, item.scope, err)
		}
		if snapshot.Source != item.source || snapshot.Scope != item.scope || snapshot.TradeDate != item.tradeDate {
			return fmt.Errorf("stored THS focus identity mismatch for %s/%s", item.tradeDate, item.scope)
		}
		if err := replaceThsTabRows(context.Background(), transaction, snapshot); err != nil {
			return err
		}
	}
	if _, err := transaction.Exec(`INSERT INTO database_maintenance(name,completed_at)
VALUES('ths_theme_tabs_v1',?)`, formatTimestamp(time.Now())); err != nil {
		return err
	}
	return transaction.Commit()
}
