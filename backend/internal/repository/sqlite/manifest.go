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
	"github.com/roiding/shadowflow/internal/repository"
)

const (
	archiveParserVersion     = "shadowflow-archive-v1"
	darkTradeContract        = "darktrade:version=101,cver=100,sortflag=6,desc=1"
	darkTradeTickContract    = "darktradetick:version=100,cver=11.2.6,points=48"
	stockKlineContract       = "stock-kline:klt=5,fqt=0|trends2:241-to-48-v1"
	archiveManifestCompleted = "complete"
	archiveManifestPending   = "incomplete"
)

type manifestQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) ArchiveManifest(ctx context.Context, tradeDate string) (repository.DailyArchiveManifest, error) {
	manifest, err := scanArchiveManifest(s.readDB().QueryRowContext(ctx, `SELECT manifest.trade_date,manifest.status,
manifest.industry_close_rows,manifest.industry_money_rows,manifest.concept_close_rows,manifest.concept_money_rows,
manifest.stock_close_rows,manifest.stock_money_rows,manifest.stock_kline_rows,manifest.stock_daily_kline_rows,
manifest.expected_stock_rows,manifest.expected_stock_kline_rows,manifest.code_count,manifest.code_set_sha256,
manifest.kline_source_counts_json,manifest.darktrade_contract,manifest.darktradetick_contract,manifest.stock_kline_contract,
manifest.parser_version,manifest.validation_errors_json,manifest.completed_at,manifest.updated_at,
coalesce(current.revision_id,''),coalesce(revision.revision_no,0)
FROM daily_archive_manifest AS manifest
LEFT JOIN daily_archive_current AS current ON current.trade_date=manifest.trade_date
LEFT JOIN daily_archive_revision AS revision ON revision.revision_id=current.revision_id
WHERE manifest.trade_date=?`, tradeDate))
	if errors.Is(err, sql.ErrNoRows) {
		return repository.DailyArchiveManifest{
			TradeDate:             tradeDate,
			Status:                archiveManifestPending,
			KlineSourceCounts:     map[string]int{},
			ValidationErrors:      []string{},
			DarkTradeContract:     darkTradeContract,
			DarkTradeTickContract: darkTradeTickContract,
			StockKlineContract:    stockKlineContract,
			ParserVersion:         archiveParserVersion,
		}, nil
	}
	return manifest, err
}

func scanArchiveManifest(row scanner) (repository.DailyArchiveManifest, error) {
	var manifest repository.DailyArchiveManifest
	var sourceCountsJSON, validationErrorsJSON, updatedAt string
	var completedAt sql.NullString
	err := row.Scan(&manifest.TradeDate, &manifest.Status,
		&manifest.IndustryCloseRows, &manifest.IndustryMoneyRows,
		&manifest.ConceptCloseRows, &manifest.ConceptMoneyRows,
		&manifest.StockCloseRows, &manifest.StockMoneyRows,
		&manifest.StockKlineRows, &manifest.StockDailyKlineRows,
		&manifest.ExpectedStockRows, &manifest.ExpectedStockKlineRows,
		&manifest.CodeCount, &manifest.CodeSetSHA256, &sourceCountsJSON,
		&manifest.DarkTradeContract, &manifest.DarkTradeTickContract,
		&manifest.StockKlineContract, &manifest.ParserVersion,
		&validationErrorsJSON, &completedAt, &updatedAt,
		&manifest.CurrentRevisionID, &manifest.RevisionNo)
	if err != nil {
		return manifest, err
	}
	manifest.KlineSourceCounts = map[string]int{}
	if err := json.Unmarshal([]byte(sourceCountsJSON), &manifest.KlineSourceCounts); err != nil {
		return manifest, fmt.Errorf("parse kline source counts: %w", err)
	}
	if err := json.Unmarshal([]byte(validationErrorsJSON), &manifest.ValidationErrors); err != nil {
		return manifest, fmt.Errorf("parse manifest validation errors: %w", err)
	}
	parsedUpdatedAt, err := time.Parse(timestampLayout, updatedAt)
	if err != nil {
		return manifest, fmt.Errorf("parse manifest updated_at: %w", err)
	}
	manifest.UpdatedAt = &parsedUpdatedAt
	if completedAt.Valid {
		parsed, parseErr := time.Parse(timestampLayout, completedAt.String)
		if parseErr != nil {
			return manifest, fmt.Errorf("parse manifest completed_at: %w", parseErr)
		}
		manifest.CompletedAt = &parsed
	}
	return manifest, nil
}

func refreshArchiveManifest(ctx context.Context, queryer manifestQueryer, tradeDate string) error {
	manifest := repository.DailyArchiveManifest{
		TradeDate:             tradeDate,
		Status:                archiveManifestPending,
		KlineSourceCounts:     map[string]int{},
		DarkTradeContract:     darkTradeContract,
		DarkTradeTickContract: darkTradeTickContract,
		StockKlineContract:    stockKlineContract,
		ParserVersion:         archiveParserVersion,
		UpdatedAt:             func() *time.Time { value := time.Now().UTC(); return &value }(),
	}
	if err := queryer.QueryRowContext(ctx, `SELECT
coalesce(sum(CASE WHEN rank_type='industry' THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN rank_type='concept' THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN rank_type='stock' THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN rank_type='stock' AND quote_available=1 THEN 1 ELSE 0 END),0)
FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close'`, tradeDate).
		Scan(&manifest.IndustryCloseRows, &manifest.ConceptCloseRows,
			&manifest.StockCloseRows, &manifest.StockDailyKlineRows); err != nil {
		return err
	}
	if err := queryer.QueryRowContext(ctx, `SELECT
coalesce(sum(CASE WHEN rank_type='industry' THEN 1 ELSE 0 END),0),
coalesce(sum(CASE WHEN rank_type='concept' THEN 1 ELSE 0 END),0)
FROM board_money_5m WHERE trade_date=?`, tradeDate).
		Scan(&manifest.IndustryMoneyRows, &manifest.ConceptMoneyRows); err != nil {
		return err
	}
	if err := queryer.QueryRowContext(ctx, `SELECT coalesce(sum(money_available),0),coalesce(sum(kline_available),0)
FROM stock_research_5m WHERE trade_date=?`, tradeDate).
		Scan(&manifest.StockMoneyRows, &manifest.StockKlineRows); err != nil {
		return err
	}
	var expectedStockRows, expectedStockKlineStocks, expectedPoints int
	err := queryer.QueryRowContext(ctx, `SELECT expected_stocks,expected_kline_stocks,expected_points
FROM stock_archive_quality WHERE trade_date=?`, tradeDate).
		Scan(&expectedStockRows, &expectedStockKlineStocks, &expectedPoints)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	manifest.ExpectedStockRows = expectedStockRows
	manifest.ExpectedStockKlineRows = expectedStockKlineStocks * expectedPoints

	rows, err := queryer.QueryContext(ctx, `SELECT rank_type,market,code
FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close'
ORDER BY rank_type,market,code`, tradeDate)
	if err != nil {
		return err
	}
	digest := sha256.New()
	for rows.Next() {
		var rankType, code string
		var market int64
		if err := rows.Scan(&rankType, &market, &code); err != nil {
			rows.Close()
			return err
		}
		manifest.CodeCount++
		_, _ = fmt.Fprintf(digest, "%s|%d|%s\n", rankType, market, code)
	}
	// A truncated iteration here would produce a wrong-but-plausible code
	// digest and count, silently sealing an incomplete manifest.
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if manifest.CodeCount > 0 {
		manifest.CodeSetSHA256 = hex.EncodeToString(digest.Sum(nil))
	}

	rows, err = queryer.QueryContext(ctx, `SELECT source,sum(point_count)
FROM stock_kline_source WHERE trade_date=? GROUP BY source ORDER BY source`, tradeDate)
	if err != nil {
		return err
	}
	sourcePoints := 0
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			rows.Close()
			return err
		}
		manifest.KlineSourceCounts[source] = count / 48
		sourcePoints += count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	validateCount := func(label string, actual, expected int) {
		if actual != expected {
			manifest.ValidationErrors = append(manifest.ValidationErrors,
				fmt.Sprintf("%s: expected %d, got %d", label, expected, actual))
		}
	}
	if manifest.IndustryCloseRows == 0 {
		manifest.ValidationErrors = append(manifest.ValidationErrors, "industry daily close is missing")
	} else {
		validateCount("industry money rows", manifest.IndustryMoneyRows, manifest.IndustryCloseRows*48)
	}
	if manifest.ConceptCloseRows == 0 {
		manifest.ValidationErrors = append(manifest.ValidationErrors, "concept daily close is missing")
	} else {
		validateCount("concept money rows", manifest.ConceptMoneyRows, manifest.ConceptCloseRows*48)
	}
	if manifest.ExpectedStockRows == 0 {
		manifest.ValidationErrors = append(manifest.ValidationErrors, "stock archive quality is missing")
	} else {
		validateCount("stock daily close rows", manifest.StockCloseRows, manifest.ExpectedStockRows)
		validateCount("stock money rows", manifest.StockMoneyRows, expectedStockKlineStocks*48)
		validateCount("stock daily kline rows", manifest.StockDailyKlineRows, expectedStockKlineStocks)
		validateCount("stock five-minute kline rows", manifest.StockKlineRows, manifest.ExpectedStockKlineRows)
	}
	if sourcePoints != manifest.StockKlineRows {
		validateCount("stock kline provenance rows", sourcePoints, manifest.StockKlineRows)
	}
	sort.Strings(manifest.ValidationErrors)
	if len(manifest.ValidationErrors) == 0 {
		manifest.Status = archiveManifestCompleted
		manifest.CompletedAt = manifest.UpdatedAt
	}

	sourceCountsJSON, err := json.Marshal(manifest.KlineSourceCounts)
	if err != nil {
		return err
	}
	validationErrorsJSON, err := json.Marshal(manifest.ValidationErrors)
	if err != nil {
		return err
	}
	var completedAt any
	if manifest.CompletedAt != nil {
		completedAt = manifest.CompletedAt.Format(timestampLayout)
	}
	_, err = queryer.ExecContext(ctx, `INSERT INTO daily_archive_manifest
(trade_date,status,industry_close_rows,industry_money_rows,concept_close_rows,concept_money_rows,
stock_close_rows,stock_money_rows,stock_kline_rows,stock_daily_kline_rows,
expected_stock_rows,expected_stock_kline_rows,code_count,code_set_sha256,kline_source_counts_json,
darktrade_contract,darktradetick_contract,stock_kline_contract,parser_version,
validation_errors_json,completed_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(trade_date) DO UPDATE SET
status=excluded.status,industry_close_rows=excluded.industry_close_rows,
industry_money_rows=excluded.industry_money_rows,concept_close_rows=excluded.concept_close_rows,
concept_money_rows=excluded.concept_money_rows,stock_close_rows=excluded.stock_close_rows,
stock_money_rows=excluded.stock_money_rows,stock_kline_rows=excluded.stock_kline_rows,
stock_daily_kline_rows=excluded.stock_daily_kline_rows,expected_stock_rows=excluded.expected_stock_rows,
expected_stock_kline_rows=excluded.expected_stock_kline_rows,code_count=excluded.code_count,
code_set_sha256=excluded.code_set_sha256,kline_source_counts_json=excluded.kline_source_counts_json,
darktrade_contract=excluded.darktrade_contract,darktradetick_contract=excluded.darktradetick_contract,
stock_kline_contract=excluded.stock_kline_contract,parser_version=excluded.parser_version,
validation_errors_json=excluded.validation_errors_json,
completed_at=CASE WHEN excluded.status='complete'
THEN coalesce(daily_archive_manifest.completed_at,excluded.completed_at) ELSE NULL END,
updated_at=excluded.updated_at`,
		manifest.TradeDate, manifest.Status, manifest.IndustryCloseRows, manifest.IndustryMoneyRows,
		manifest.ConceptCloseRows, manifest.ConceptMoneyRows, manifest.StockCloseRows,
		manifest.StockMoneyRows, manifest.StockKlineRows, manifest.StockDailyKlineRows,
		manifest.ExpectedStockRows, manifest.ExpectedStockKlineRows, manifest.CodeCount,
		manifest.CodeSetSHA256, string(sourceCountsJSON), manifest.DarkTradeContract,
		manifest.DarkTradeTickContract, manifest.StockKlineContract, manifest.ParserVersion,
		string(validationErrorsJSON), completedAt, manifest.UpdatedAt.Format(timestampLayout))
	return err
}

func migrateArchiveMetadata(db *sql.DB) error {
	var migrated int
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance
WHERE name='archive_metadata_v1')`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 1 {
		return nil
	}
	now := time.Now().UTC().Format(timestampLayout)
	if _, err := db.Exec(`INSERT OR IGNORE INTO stock_kline_source
(trade_date,market,code,source,point_count,parser_version,fetched_at,run_id)
SELECT trade_date,market,code,?,count(*),?,?,'legacy'
FROM stock_research_5m WHERE kline_available=1 GROUP BY trade_date,market,code`,
		graymarket.KlineSourceUnknown, archiveParserVersion, now); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT DISTINCT trade_date FROM (
SELECT trade_date FROM rank_snapshot WHERE snapshot_kind='daily_close'
UNION SELECT trade_date FROM board_money_5m
UNION SELECT trade_date FROM stock_archive_quality
) ORDER BY trade_date`)
	if err != nil {
		return err
	}
	var tradeDates []string
	for rows.Next() {
		var tradeDate string
		if err := rows.Scan(&tradeDate); err != nil {
			rows.Close()
			return err
		}
		tradeDates = append(tradeDates, tradeDate)
	}
	// This migration records a done marker below; a truncated date list would
	// leave some manifests unrefreshed with no way to retry.
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, tradeDate := range tradeDates {
		if err := refreshArchiveManifest(context.Background(), db, tradeDate); err != nil {
			return err
		}
	}
	_, err = db.Exec(`INSERT INTO database_maintenance(name,completed_at) VALUES ('archive_metadata_v1',?)
ON CONFLICT(name) DO UPDATE SET completed_at=excluded.completed_at`, now)
	return err
}
