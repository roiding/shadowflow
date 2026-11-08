package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/thsauth"
)

// ErrInvalidThsState rejects malformed THS session payloads, trade dates or
// stock identities before they reach the database.
var ErrInvalidThsState = errors.New("invalid THS auth state")

const thsAuthSchema = `
CREATE TABLE IF NOT EXISTS ths_session (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    cookies_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ths_account_data (
    trade_date TEXT NOT NULL,
    identity TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    PRIMARY KEY (trade_date, identity)
) WITHOUT ROWID;
`

// StoreThsSession persists the THS login-state cookie payload (the JSON
// encoding produced by thsauth's session encoding) as the single session row.
func (store *Store) StoreThsSession(ctx context.Context, payload string) error {
	if strings.TrimSpace(payload) == "" {
		return fmt.Errorf("%w: empty THS session payload", ErrInvalidThsState)
	}
	if len(payload) > maxThsSessionBytes {
		return fmt.Errorf("%w: THS session payload exceeds size limit", ErrInvalidThsState)
	}
	if _, err := store.writeDB().ExecContext(ctx, `INSERT INTO ths_session(id,cookies_json,updated_at) VALUES(1,?,?)
ON CONFLICT(id) DO UPDATE SET cookies_json=excluded.cookies_json,updated_at=excluded.updated_at`,
		payload, formatTimestamp(time.Now().UTC())); err != nil {
		return fmt.Errorf("store THS session: %w", err)
	}
	return nil
}

// ReadThsSession returns the stored THS session payload; found is false when no
// session has been persisted yet.
func (store *Store) ReadThsSession(ctx context.Context) (string, bool, error) {
	var payload string
	err := store.readDB().QueryRowContext(ctx, `SELECT cookies_json FROM ths_session WHERE id=1`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read THS session: %w", err)
	}
	return payload, true, nil
}

// SaveThsAccountData upserts one restricted THS stock record for a trade date.
func (store *Store) SaveThsAccountData(ctx context.Context, tradeDate, identity, payload string, fetchedAt time.Time) error {
	if !validTradeDate(tradeDate) {
		return fmt.Errorf("%w: invalid THS account data trade date", ErrInvalidThsState)
	}
	if !validThsIdentity(identity) {
		return fmt.Errorf("%w: invalid THS account data identity", ErrInvalidThsState)
	}
	if strings.TrimSpace(payload) == "" {
		return fmt.Errorf("%w: empty THS account data payload", ErrInvalidThsState)
	}
	if _, err := store.writeDB().ExecContext(ctx, `INSERT INTO ths_account_data(trade_date,identity,fetched_at,payload_json) VALUES(?,?,?,?)
ON CONFLICT(trade_date,identity) DO UPDATE SET fetched_at=excluded.fetched_at,payload_json=excluded.payload_json`,
		tradeDate, identity, formatTimestamp(fetchedAt), payload); err != nil {
		return fmt.Errorf("store THS account data %s %s: %w", tradeDate, identity, err)
	}
	return nil
}

// ThsAccountDataCount returns how many restricted THS records exist for a date,
// so collectors can detect partially archived days.
func (store *Store) ThsAccountDataCount(ctx context.Context, tradeDate string) (int, error) {
	if !validTradeDate(tradeDate) {
		return 0, fmt.Errorf("%w: invalid THS account data trade date", ErrInvalidThsState)
	}
	var count int
	if err := store.readDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM ths_account_data WHERE trade_date=?`, tradeDate).Scan(&count); err != nil {
		return 0, fmt.Errorf("count THS account data %s: %w", tradeDate, err)
	}
	return count, nil
}

func (store *Store) ThsAccountDataPayloads(ctx context.Context, tradeDate string, identities []string) (map[string]string, error) {
	if !validTradeDate(tradeDate) {
		return nil, fmt.Errorf("%w: invalid THS account data trade date", ErrInvalidThsState)
	}
	payloads := make(map[string]string, len(identities))
	if len(identities) == 0 {
		return payloads, nil
	}
	seen := make(map[string]bool, len(identities))
	arguments := make([]any, 0, len(identities)+1)
	arguments = append(arguments, tradeDate)
	placeholders := make([]string, 0, len(identities))
	for _, identity := range identities {
		if !validThsIdentity(identity) {
			return nil, fmt.Errorf("%w: invalid THS account data identity", ErrInvalidThsState)
		}
		if seen[identity] {
			return nil, fmt.Errorf("%w: duplicate THS account data identity", ErrInvalidThsState)
		}
		seen[identity] = true
		arguments = append(arguments, identity)
		placeholders = append(placeholders, "?")
	}
	rows, queryError := store.readDB().QueryContext(ctx, `SELECT identity,payload_json FROM ths_account_data
WHERE trade_date=? AND identity IN (`+strings.Join(placeholders, ",")+`)`, arguments...)
	if queryError != nil {
		return nil, fmt.Errorf("read THS account data %s: %w", tradeDate, queryError)
	}
	defer rows.Close()
	for rows.Next() {
		var identity, payload string
		if scanError := rows.Scan(&identity, &payload); scanError != nil {
			return nil, fmt.Errorf("read THS account data %s: %w", tradeDate, scanError)
		}
		payloads[identity] = payload
	}
	if rowsError := rows.Err(); rowsError != nil {
		return nil, fmt.Errorf("read THS account data %s: %w", tradeDate, rowsError)
	}
	return payloads, nil
}

func validThsIdentity(identity string) bool {
	market, code, found := strings.Cut(identity, ":")
	if !found {
		return false
	}
	if (market != "17" && market != "33") || len(code) != 6 {
		return false
	}
	for _, character := range code {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validTradeDate(tradeDate string) bool {
	parsed, err := time.Parse("2006-01-02", tradeDate)
	return err == nil && parsed.Format("2006-01-02") == tradeDate
}

// THSSessionStore exposes the session row as a thsauth.SessionStore, so both
// the server and the collect CLI persist the THS login state in the database
// without a mounted session file.
func (store *Store) THSSessionStore() thsauth.SessionStore {
	return thsSessionStoreAdapter{store: store}
}

type thsSessionStoreAdapter struct{ store *Store }

func (adapter thsSessionStoreAdapter) LoadSession(ctx context.Context) ([]*http.Cookie, bool, error) {
	payload, found, err := adapter.store.ReadThsSession(ctx)
	if err != nil || !found {
		return nil, false, err
	}
	cookies, err := thsauth.DecodeSessionCookies(payload)
	if err != nil {
		return nil, false, err
	}
	return cookies, true, nil
}

func (adapter thsSessionStoreAdapter) SaveSession(ctx context.Context, cookies []*http.Cookie) error {
	payload, err := thsauth.EncodeSessionCookies(cookies)
	if err != nil {
		return err
	}
	return adapter.store.StoreThsSession(ctx, payload)
}

// thsAccountRecord mirrors the per-stock rows of a thsdataq batch response.
type thsAccountRecord struct {
	Code   string            `json:"code"`
	Values []json.RawMessage `json:"values"`
}

// maxThsSessionBytes mirrors thsauth's file size bound for the cookie payload.
const maxThsSessionBytes = 1 << 20
