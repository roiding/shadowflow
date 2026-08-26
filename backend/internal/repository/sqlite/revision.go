package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/repository"
)

func (s *Store) SealArchiveRevision(ctx context.Context, tradeDate, revisionID string) (repository.ArchiveRevision, error) {
	if tradeDate == "" || strings.TrimSpace(revisionID) == "" {
		return repository.ArchiveRevision{}, errors.New("trade date and revision id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return repository.ArchiveRevision{}, err
	}
	defer tx.Rollback()

	if existing, found, err := archiveRevisionByID(ctx, tx, revisionID); err != nil {
		return repository.ArchiveRevision{}, err
	} else if found {
		return existing, nil
	}

	manifest, err := scanArchiveManifest(tx.QueryRowContext(ctx, `SELECT trade_date,status,
industry_close_rows,industry_money_rows,concept_close_rows,concept_money_rows,
stock_close_rows,stock_money_rows,stock_kline_rows,stock_daily_kline_rows,
expected_stock_rows,expected_stock_kline_rows,code_count,code_set_sha256,
kline_source_counts_json,darktrade_contract,darktradetick_contract,stock_kline_contract,
parser_version,validation_errors_json,completed_at,updated_at,'',0
FROM daily_archive_manifest WHERE trade_date=?`, tradeDate))
	if errors.Is(err, sql.ErrNoRows) {
		return repository.ArchiveRevision{}, fmt.Errorf("%w: %s", repository.ErrArchiveIncomplete, tradeDate)
	}
	if err != nil {
		// A scan failure (corrupt JSON column, bad timestamp) is a real error;
		// reporting it as ErrArchiveIncomplete would hide the root cause from
		// the collector logs and make the sealing failure undiagnosable.
		return repository.ArchiveRevision{}, err
	}
	if manifest.Status != archiveManifestCompleted {
		return repository.ArchiveRevision{}, fmt.Errorf("%w: %s", repository.ErrArchiveIncomplete, tradeDate)
	}

	var previous sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT revision_id FROM daily_archive_current WHERE trade_date=?`, tradeDate).Scan(&previous); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return repository.ArchiveRevision{}, err
	}
	revisionNo := 1
	if previous.Valid {
		revisionID = previous.String
		if err := tx.QueryRowContext(ctx, `SELECT revision_no FROM daily_archive_revision WHERE revision_id=?`, revisionID).Scan(&revisionNo); err != nil {
			return repository.ArchiveRevision{}, err
		}
	}

	contentSHA256, err := hashArchiveData(ctx, tx, tradeDate)
	if err != nil {
		return repository.ArchiveRevision{}, err
	}
	createdAt := time.Now().UTC()
	manifest.CurrentRevisionID = revisionID
	manifest.RevisionNo = revisionNo
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return repository.ArchiveRevision{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO daily_archive_revision
(revision_id,trade_date,revision_no,previous_revision_id,content_sha256,manifest_json,created_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(revision_id) DO UPDATE SET
content_sha256=excluded.content_sha256,manifest_json=excluded.manifest_json,created_at=excluded.created_at`,
		revisionID, tradeDate, revisionNo, nil, contentSHA256,
		string(manifestJSON), createdAt.Format(timestampLayout)); err != nil {
		return repository.ArchiveRevision{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO daily_archive_current(trade_date,revision_id,updated_at)
VALUES (?,?,?) ON CONFLICT(trade_date) DO UPDATE SET
revision_id=excluded.revision_id,updated_at=excluded.updated_at`,
		tradeDate, revisionID, createdAt.Format(timestampLayout)); err != nil {
		return repository.ArchiveRevision{}, err
	}
	if err := buildAnalyticsForRevision(ctx, tx, revisionID, tradeDate); err != nil {
		return repository.ArchiveRevision{}, err
	}
	if err := tx.Commit(); err != nil {
		return repository.ArchiveRevision{}, err
	}
	result := repository.ArchiveRevision{
		RevisionID: revisionID, TradeDate: tradeDate, RevisionNo: revisionNo,
		ContentSHA256: contentSHA256, CreatedAt: createdAt,
	}
	return result, nil
}

func (s *Store) ArchiveRevisions(ctx context.Context, tradeDate string) ([]repository.ArchiveRevision, error) {
	rows, err := s.readDB().QueryContext(ctx, `SELECT revision_id,trade_date,revision_no,
coalesce(previous_revision_id,''),content_sha256,created_at
FROM daily_archive_revision WHERE trade_date=? ORDER BY revision_no DESC`, tradeDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]repository.ArchiveRevision, 0)
	for rows.Next() {
		var item repository.ArchiveRevision
		var createdAt string
		if err := rows.Scan(&item.RevisionID, &item.TradeDate, &item.RevisionNo,
			&item.PreviousRevision, &item.ContentSHA256, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt, err = time.Parse(timestampLayout, createdAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func archiveRevisionByID(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, revisionID string) (repository.ArchiveRevision, bool, error) {
	var item repository.ArchiveRevision
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT revision_id,trade_date,revision_no,
coalesce(previous_revision_id,''),content_sha256,created_at
FROM daily_archive_revision WHERE revision_id=?`, revisionID).
		Scan(&item.RevisionID, &item.TradeDate, &item.RevisionNo,
			&item.PreviousRevision, &item.ContentSHA256, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	item.CreatedAt, err = time.Parse(timestampLayout, createdAt)
	return item, err == nil, err
}

func archiveTradeDateByRevision(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, revisionID string) (string, error) {
	var tradeDate string
	if err := queryer.QueryRowContext(ctx, `SELECT trade_date FROM daily_archive_revision WHERE revision_id=?`, revisionID).Scan(&tradeDate); err != nil {
		return "", err
	}
	return tradeDate, nil
}

func hashArchiveData(ctx context.Context, tx *sql.Tx, tradeDate string) (string, error) {
	digest := sha256.New()
	queries := []struct {
		name  string
		query string
	}{
		{"rank_snapshot", `SELECT snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,
quote_time,latest_price_raw,open_price,high_price,low_price,close_price,previous_close,change_value,
change_pct,volume,turnover,turnover_rate,amplitude,quote_available,money_available,dark_money,regular_money,
main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,
leader_code,source_version,source_sort_flag,source_descending
FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close' ORDER BY rank_type,market,code`},
		{"board_money", `SELECT snapshot_at,trade_date,rank_type,rank,market,code,name,
dark_money,regular_money,main_money_inflow,money_available,source_time
FROM board_money_5m WHERE trade_date=? ORDER BY rank_type,snapshot_at,market,code`},
		{"stock_research", `SELECT trade_date,minute_index,market,code,money_rank,dark_money,
regular_money,main_money_inflow,money_available,open_price_e4,high_price_e4,low_price_e4,close_price_e4,
volume,turnover,amplitude_ppm,change_pct_ppm,change_value_e4,turnover_rate_ppm,kline_available
FROM stock_research_5m WHERE trade_date=? ORDER BY market,code,minute_index`},
		{"stock_kline_source", `SELECT trade_date,market,code,source,point_count,parser_version
FROM stock_kline_source WHERE trade_date=? ORDER BY market,code`},
		{"raw_response", `SELECT snapshot_at,snapshot_kind,rank_type,page,content_encoding,
compression,body FROM raw_response WHERE date(snapshot_at,'+8 hours')=? AND snapshot_kind='daily_close'
ORDER BY snapshot_kind,snapshot_at,rank_type,page`},
	}
	for _, item := range queries {
		if err := hashRows(ctx, tx, digest, item.name, item.query, tradeDate); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func hashRows(ctx context.Context, tx *sql.Tx, digest hash.Hash, name, query string, args ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	fmt.Fprintf(digest, "table:%s\n", name)
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return err
		}
		for index, value := range values {
			fmt.Fprintf(digest, "%s=", columns[index])
			switch typed := value.(type) {
			case []byte:
				fmt.Fprintf(digest, "b:%d:", len(typed))
				_, _ = digest.Write(typed)
			case string:
				fmt.Fprintf(digest, "s:%d:%s", len(typed), typed)
			default:
				fmt.Fprintf(digest, "%T:%v", value, value)
			}
			_, _ = digest.Write([]byte{0})
		}
		_, _ = digest.Write([]byte{'\n'})
	}
	return rows.Err()
}

func migrateArchiveRevisions(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance
WHERE name='archive_revisions_v1')`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 1 {
		return nil
	}
	rows, err := store.db.Query(`SELECT manifest.trade_date
FROM daily_archive_manifest AS manifest
LEFT JOIN daily_archive_current AS current ON current.trade_date=manifest.trade_date
WHERE manifest.status='complete' AND current.revision_id IS NULL ORDER BY manifest.trade_date`)
	if err != nil {
		return err
	}
	var dates []string
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			rows.Close()
			return err
		}
		dates = append(dates, date)
	}
	// The done marker below is permanent; a truncated date list must fail the
	// migration instead of being recorded as complete.
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, date := range dates {
		revisionID := "legacy-" + strings.ReplaceAll(date, "-", "")
		if _, err := store.SealArchiveRevision(context.Background(), date, revisionID); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(timestampLayout)
	_, err = store.db.Exec(`INSERT INTO database_maintenance(name,completed_at)
VALUES ('archive_revisions_v1',?) ON CONFLICT(name) DO UPDATE SET completed_at=excluded.completed_at`, now)
	return err
}

func migrateLightweightArchiveStorage(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance
WHERE name='lightweight_archive_storage_v1')`).Scan(&migrated); err != nil {
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
	for _, table := range []string{
		"rank_snapshot_revision",
		"board_money_revision",
		"stock_research_revision",
		"stock_kline_source_revision",
		"raw_response_revision",
		"board_catalog_snapshot",
	} {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(timestampLayout)
	if _, err := tx.Exec(`INSERT INTO database_maintenance(name,completed_at)
VALUES ('lightweight_archive_storage_v1',?)`, now); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateRevisionMetadataV2 keeps only revision metadata that still backs the
// current archive or a computed feature/label set. Revisions are batch IDs,
// not archive copies; orphan rows have no data path and only add catalog
// noise. The physical *_revision archive tables were already removed by the
// lightweight migration above.
func migrateRevisionMetadata(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance WHERE name='revision_metadata_v2')`).Scan(&migrated); err != nil {
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
	if _, err := tx.Exec(`DELETE FROM daily_archive_revision AS revision
WHERE NOT EXISTS (SELECT 1 FROM daily_archive_current AS current WHERE current.revision_id=revision.revision_id)
  AND NOT EXISTS (SELECT 1 FROM daily_feature_set AS feature_set WHERE feature_set.revision_id=revision.revision_id)
  AND NOT EXISTS (SELECT 1 FROM daily_feature AS feature WHERE feature.revision_id=revision.revision_id)
  AND NOT EXISTS (SELECT 1 FROM future_return_label AS label WHERE label.signal_revision_id=revision.revision_id OR label.target_revision_id=revision.revision_id)`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(timestampLayout)
	if _, err := tx.Exec(`INSERT INTO database_maintenance(name,completed_at) VALUES ('revision_metadata_v2',?)`, now); err != nil {
		return err
	}
	return tx.Commit()
}
