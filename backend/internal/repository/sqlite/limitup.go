package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/limitup"
	"github.com/roiding/shadowflow/internal/repository"
)

const limitUpSchema = `
CREATE TABLE IF NOT EXISTS limit_up_day (
    source TEXT NOT NULL,
    scope TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    sealed_count INTEGER NOT NULL CHECK (sealed_count >= 0),
    broken_count INTEGER NOT NULL CHECK (broken_count >= 0),
    down_count INTEGER NOT NULL CHECK (down_count >= 0),
    touched_count INTEGER NOT NULL CHECK (touched_count = sealed_count + broken_count),
    content_hash TEXT NOT NULL,
    raw_pages BLOB NOT NULL,
    parser_version TEXT NOT NULL,
    PRIMARY KEY (source, scope, trade_date)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS limit_up_event (
    source TEXT NOT NULL,
    scope TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    code TEXT NOT NULL,
    pool TEXT NOT NULL CHECK (pool IN ('limit_up', 'open_limit', 'limit_down')),
    payload_json TEXT NOT NULL,
    raw_json TEXT NOT NULL,
    PRIMARY KEY (source, scope, trade_date, code, pool),
    FOREIGN KEY (source, scope, trade_date) REFERENCES limit_up_day (source, scope, trade_date) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_limit_up_stock_dates ON limit_up_event (source, scope, code, trade_date);

CREATE TABLE IF NOT EXISTS limit_up_run_scope (
    run_id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    scope TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES collection_run (run_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_limit_up_run_date ON limit_up_run_scope (source, scope, trade_date, run_id);
`

func (store *Store) StartLimitUpRun(ctx context.Context, run repository.CollectionRun, scope string) error {
	parsedDate, err := time.Parse("2006-01-02", run.RequestedDate)
	if err != nil || parsedDate.Format("2006-01-02") != run.RequestedDate || !limitup.ValidScope(scope) ||
		run.SnapshotKind != graymarket.SnapshotLimitUpEvents || run.Status != repository.RunRunning {
		return fmt.Errorf("%w: invalid event archive run", limitup.ErrInvalid)
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `INSERT INTO collection_run
(run_id,snapshot_at,snapshot_kind,rank_type,status,requested_date,actual_trade_date,expected_total,fetched_total,page_count,
attempt_count,started_at,finished_at,duration_ms,error_code,error_message,lease_until)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, run.RunID, formatTimestamp(run.SnapshotAt), string(run.SnapshotKind),
		string(run.RankType), string(run.Status), run.RequestedDate, run.ActualTradeDate, run.ExpectedTotal, run.FetchedTotal,
		run.PageCount, run.AttemptCount, formatTimestamp(run.StartedAt), nil, run.DurationMS, run.ErrorCode, run.ErrorMessage, runLeaseUntil(ctx)); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO limit_up_run_scope (run_id,source,scope,trade_date) VALUES(?,?,?,?)`,
		run.RunID, limitup.SourceTHS, scope, run.RequestedDate); err != nil {
		return err
	}
	return transaction.Commit()
}

func (store *Store) HasLimitUpDay(ctx context.Context, tradeDate, scope string) (bool, error) {
	parsed, err := time.Parse("2006-01-02", tradeDate)
	if err != nil || parsed.Format("2006-01-02") != tradeDate || !limitup.ValidScope(scope) {
		return false, fmt.Errorf("%w: invalid archive date or scope", limitup.ErrInvalid)
	}
	var exists bool
	err = store.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM limit_up_day WHERE source=? AND scope=? AND trade_date=?)`,
		limitup.SourceTHS, scope, tradeDate).Scan(&exists)
	return exists, err
}

func (store *Store) LimitUpArchiveQuality(ctx context.Context, tradeDate, scope string) (limitup.ArchiveQuality, error) {
	quality := limitup.ArchiveQuality{TradeDate: tradeDate, Source: limitup.SourceTHS, Scope: scope, Status: limitup.ArchiveStatusNotCollected}
	parsed, err := time.Parse("2006-01-02", tradeDate)
	if err != nil || parsed.Format("2006-01-02") != tradeDate || !limitup.ValidScope(scope) {
		return quality, fmt.Errorf("%w: invalid archive date or scope", limitup.ErrInvalid)
	}
	transaction, err := store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return quality, err
	}
	defer transaction.Rollback()
	var day limitup.Day
	var fetched string
	err = transaction.QueryRowContext(ctx, `SELECT trade_date,fetched_at,sealed_count,broken_count,down_count,touched_count,content_hash
FROM limit_up_day WHERE source=? AND scope=? AND trade_date=?`, limitup.SourceTHS, scope, tradeDate).
		Scan(&day.TradeDate, &fetched, &day.SealedCount, &day.BrokenCount, &day.DownCount, &day.TouchedCount, &day.ContentHash)
	if err == nil {
		day.FetchedAt, err = time.Parse(time.RFC3339Nano, fetched)
		if err != nil {
			return quality, err
		}
		quality.Day, quality.Complete, quality.Status = &day, true, limitup.ArchiveStatusComplete
	} else if !errors.Is(err, sql.ErrNoRows) {
		return quality, err
	}
	var latest sql.NullString
	if err := transaction.QueryRowContext(ctx, `SELECT max(trade_date) FROM limit_up_day WHERE source=? AND scope=? AND trade_date<=?`,
		limitup.SourceTHS, scope, tradeDate).Scan(&latest); err != nil {
		return quality, err
	}
	quality.LatestTradeDate = latest.String
	var run limitup.ArchiveRun
	var started string
	var finished sql.NullString
	err = transaction.QueryRowContext(ctx, `SELECT run.run_id,run.status,run.started_at,run.finished_at,run.fetched_total,
run.page_count,run.error_code,run.error_message FROM collection_run run
JOIN limit_up_run_scope scope ON run.run_id=scope.run_id
WHERE scope.source=? AND scope.scope=? AND scope.trade_date=? ORDER BY run.started_at DESC,run.run_id DESC LIMIT 1`,
		limitup.SourceTHS, scope, tradeDate).Scan(&run.RunID, &run.Status, &started, &finished, &run.FetchedTotal, &run.PageCount, &run.ErrorCode, &run.ErrorMessage)
	if err == nil {
		run.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return quality, err
		}
		if finished.Valid {
			finishedAt, err := time.Parse(time.RFC3339Nano, finished.String)
			if err != nil {
				return quality, err
			}
			run.FinishedAt = &finishedAt
		}
		quality.LastRun = &run
		if !quality.Complete && (run.Status == limitup.ArchiveStatusRunning || run.Status == limitup.ArchiveStatusFailed) {
			quality.Status = run.Status
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return quality, err
	}
	return quality, transaction.Commit()
}

func (store *Store) SaveLimitUpSnapshot(ctx context.Context, snapshot limitup.Snapshot) error {
	if err := limitup.ValidateSnapshot(snapshot); err != nil {
		return err
	}
	events := append([]limitup.Event{}, snapshot.Events...)
	sort.Slice(events, func(left, right int) bool {
		if events[left].Code != events[right].Code {
			return events[left].Code < events[right].Code
		}
		return events[left].Pool < events[right].Pool
	})
	content, err := json.Marshal(struct {
		Source    string          `json:"source"`
		Scope     string          `json:"scope"`
		TradeDate string          `json:"trade_date"`
		Events    []limitup.Event `json:"events"`
	}{snapshot.Source, snapshot.Scope, snapshot.TradeDate, events})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(content)
	rawPages, err := json.Marshal(snapshot.RawPages)
	if err != nil {
		return err
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	var previousFetch string
	err = transaction.QueryRowContext(ctx, `SELECT fetched_at FROM limit_up_day WHERE source=? AND scope=? AND trade_date=?`,
		snapshot.Source, snapshot.Scope, snapshot.TradeDate).Scan(&previousFetch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		previousTime, parseErr := time.Parse(time.RFC3339Nano, previousFetch)
		if parseErr != nil {
			return fmt.Errorf("read previous limit-up collection time: %w", parseErr)
		}
		if snapshot.FetchedAt.Before(previousTime) {
			return fmt.Errorf("%w: cannot replace a newer limit-up snapshot", limitup.ErrInvalid)
		}
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM limit_up_day WHERE source=? AND scope=? AND trade_date=?`,
		snapshot.Source, snapshot.Scope, snapshot.TradeDate); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO limit_up_day
(source,scope,trade_date,fetched_at,sealed_count,broken_count,down_count,touched_count,content_hash,raw_pages,parser_version)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, snapshot.Source, snapshot.Scope, snapshot.TradeDate, formatTimestamp(snapshot.FetchedAt),
		snapshot.SealedCount, snapshot.BrokenCount, snapshot.DownCount, snapshot.TouchedCount, hex.EncodeToString(digest[:]), rawPages, "ths-limit-events-v1"); err != nil {
		return err
	}
	statement, err := transaction.PrepareContext(ctx, `INSERT INTO limit_up_event
(source,scope,trade_date,code,pool,payload_json,raw_json) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		raw := event.Raw
		if len(raw) == 0 {
			raw = json.RawMessage(`null`)
		}
		if _, err := statement.ExecContext(ctx, snapshot.Source, snapshot.Scope, snapshot.TradeDate, event.Code,
			event.Pool, string(payload), string(raw)); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (store *Store) ReadLimitUpWindow(ctx context.Context, fromDate, asOf, scope string) (limitup.Dataset, error) {
	dataset := limitup.Dataset{Days: []limitup.Day{}, Events: []limitup.Event{}}
	from, fromErr := time.Parse("2006-01-02", fromDate)
	until, untilErr := time.Parse("2006-01-02", asOf)
	if fromErr != nil || untilErr != nil || fromDate != from.Format("2006-01-02") || asOf != until.Format("2006-01-02") ||
		from.After(until) || until.Sub(from) > 365*24*time.Hour || !limitup.ValidScope(scope) {
		return dataset, fmt.Errorf("%w: invalid bounded event window", limitup.ErrInvalid)
	}
	transaction, err := store.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return dataset, err
	}
	defer transaction.Rollback()
	rows, err := transaction.QueryContext(ctx, `SELECT trade_date,fetched_at,sealed_count,broken_count,down_count,touched_count,content_hash
FROM limit_up_day WHERE source=? AND scope=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date`,
		limitup.SourceTHS, scope, fromDate, asOf)
	if err != nil {
		return dataset, err
	}
	windowEvents := 0
	for rows.Next() {
		var day limitup.Day
		var fetched string
		if err := rows.Scan(&day.TradeDate, &fetched, &day.SealedCount, &day.BrokenCount, &day.DownCount, &day.TouchedCount, &day.ContentHash); err != nil {
			rows.Close()
			return dataset, err
		}
		day.FetchedAt, err = time.Parse(time.RFC3339Nano, fetched)
		if err != nil {
			rows.Close()
			return dataset, err
		}
		windowEvents += day.TouchedCount + day.DownCount
		if windowEvents > limitup.MaxWindowEvents {
			rows.Close()
			return dataset, fmt.Errorf("%w: too many event records; reduce the date range", limitup.ErrInvalid)
		}
		dataset.Days = append(dataset.Days, day)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil || closeErr != nil {
		return dataset, errors.Join(readErr, closeErr)
	}
	rows, err = transaction.QueryContext(ctx, `SELECT payload_json FROM limit_up_event
WHERE source=? AND scope=? AND trade_date>=? AND trade_date<=? ORDER BY trade_date,code,pool`,
		limitup.SourceTHS, scope, fromDate, asOf)
	if err != nil {
		return dataset, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(dataset.Events) >= limitup.MaxWindowEvents {
			return dataset, fmt.Errorf("%w: too many event records; reduce the date range", limitup.ErrInvalid)
		}
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return dataset, err
		}
		var event limitup.Event
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return dataset, fmt.Errorf("decode stored limit-up event: %w", err)
		}
		dataset.Events = append(dataset.Events, event)
	}
	if err := rows.Err(); err != nil {
		return dataset, err
	}
	if err := rows.Close(); err != nil {
		return dataset, err
	}
	return dataset, transaction.Commit()
}
