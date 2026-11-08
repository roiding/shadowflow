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
	return transaction.Commit()
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
