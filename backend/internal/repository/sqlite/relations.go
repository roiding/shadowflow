package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

func (s *Store) StartRelationSync(ctx context.Context, run repository.RelationSyncRun) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO relation_sync_run
(run_id,trade_date,status,board_count,relation_count,added_count,removed_count,baseline_built,
started_at,finished_at,duration_ms,error_code,error_message)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, run.RunID, run.TradeDate, string(run.Status), run.BoardCount, run.RelationCount,
		run.AddedCount, run.RemovedCount, boolInt(run.BaselineBuilt), run.StartedAt.Format(timestampLayout), nil,
		run.DurationMS, run.ErrorCode, run.ErrorMessage)
	return err
}

func (s *Store) StageRelations(ctx context.Context, runID string, relations []graymarket.StockBoardRelation) error {
	if len(relations) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO stock_board_relation_stage
(run_id,stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
relation_source,relation_scope,detected_at,raw_data) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, relation := range relations {
		if relation.StockCode == "" || relation.BoardCode == "" {
			return fmt.Errorf("relation contains an empty stock or board code")
		}
		if relation.BoardType != graymarket.BoardIndustry && relation.BoardType != graymarket.BoardConcept {
			return fmt.Errorf("unsupported board type %q", relation.BoardType)
		}
		if _, err := statement.ExecContext(ctx, runID, relation.StockCode, relation.StockMarket, relation.StockName,
			relation.BoardCode, relation.BoardName, string(relation.BoardType), relation.SourceOrder,
			relation.RelationSource, relation.RelationScope, relation.DetectedAt.Format(timestampLayout), relation.RawData); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ApplyRelationScan(ctx context.Context, runID, tradeDate string, detectedAt time.Time) (repository.RelationApplyResult, error) {
	var result repository.RelationApplyResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM stock_board_relation_stage WHERE run_id=?`, runID).Scan(&result.RelationCount); err != nil {
		return result, err
	}
	if result.RelationCount == 0 {
		return result, fmt.Errorf("relation scan produced no records")
	}
	var boardCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(DISTINCT board_type || ':' || board_code)
FROM stock_board_relation_stage WHERE run_id=?`, runID).Scan(&boardCount); err != nil {
		return result, err
	}
	var baselineCount, currentCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM stock_board_relation_baseline`).Scan(&baselineCount); err != nil {
		return result, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM stock_board_relation_current`).Scan(&currentCount); err != nil {
		return result, err
	}

	if baselineCount == 0 {
		result.BaselineBuilt = true
		_, err = tx.ExecContext(ctx, `INSERT INTO stock_board_relation_baseline
(baseline_date,stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
relation_source,relation_scope,detected_at,raw_data)
SELECT ?,stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
relation_source,relation_scope,detected_at,raw_data FROM stock_board_relation_stage WHERE run_id=?`, tradeDate, runID)
		if err != nil {
			return result, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO stock_board_relation_current
(stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
relation_source,relation_scope,since_date,detected_at,raw_data)
SELECT stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
relation_source,relation_scope,?,detected_at,raw_data FROM stock_board_relation_stage WHERE run_id=?`, tradeDate, runID)
		if err != nil {
			return result, err
		}
	} else {
		if currentCount == 0 {
			return result, fmt.Errorf("relation baseline exists but current state is empty")
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM stock_board_relation_stage stage
LEFT JOIN stock_board_relation_current current ON current.stock_code=stage.stock_code
AND current.board_code=stage.board_code AND current.relation_source=stage.relation_source
AND current.relation_scope=stage.relation_scope
WHERE stage.run_id=? AND current.stock_code IS NULL`, runID).Scan(&result.AddedCount); err != nil {
			return result, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM stock_board_relation_current current
LEFT JOIN stock_board_relation_stage stage ON stage.run_id=? AND stage.stock_code=current.stock_code
AND stage.board_code=current.board_code AND stage.relation_source=current.relation_source
AND stage.relation_scope=current.relation_scope WHERE stage.stock_code IS NULL`, runID).Scan(&result.RemovedCount); err != nil {
			return result, err
		}

		_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO stock_board_relation_change
(effective_date,change_type,stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
relation_source,relation_scope,detected_at,run_id,raw_data)
SELECT ?,'added',stage.stock_code,stage.stock_market,stage.stock_name,stage.board_code,stage.board_name,
stage.board_type,stage.source_order,stage.relation_source,stage.relation_scope,?,stage.run_id,stage.raw_data
FROM stock_board_relation_stage stage
LEFT JOIN stock_board_relation_current current ON current.stock_code=stage.stock_code
AND current.board_code=stage.board_code AND current.relation_source=stage.relation_source
AND current.relation_scope=stage.relation_scope
WHERE stage.run_id=? AND current.stock_code IS NULL`, tradeDate, detectedAt.Format(timestampLayout), runID)
		if err != nil {
			return result, err
		}
		_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO stock_board_relation_change
(effective_date,change_type,stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
relation_source,relation_scope,detected_at,run_id,raw_data)
SELECT ?,'removed',current.stock_code,current.stock_market,current.stock_name,current.board_code,current.board_name,
current.board_type,current.source_order,current.relation_source,current.relation_scope,?,?,current.raw_data
FROM stock_board_relation_current current
LEFT JOIN stock_board_relation_stage stage ON stage.run_id=? AND stage.stock_code=current.stock_code
AND stage.board_code=current.board_code AND stage.relation_source=current.relation_source
AND stage.relation_scope=current.relation_scope WHERE stage.stock_code IS NULL`, tradeDate, detectedAt.Format(timestampLayout), runID, runID)
		if err != nil {
			return result, err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM stock_board_relation_current AS current
WHERE NOT EXISTS (SELECT 1 FROM stock_board_relation_stage stage WHERE stage.run_id=?
AND stage.stock_code=current.stock_code AND stage.board_code=current.board_code
AND stage.relation_source=current.relation_source AND stage.relation_scope=current.relation_scope)`, runID)
		if err != nil {
			return result, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO stock_board_relation_current
(stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
relation_source,relation_scope,since_date,detected_at,raw_data)
SELECT stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
relation_source,relation_scope,?,detected_at,raw_data FROM stock_board_relation_stage WHERE run_id=? AND true
ON CONFLICT(stock_code,board_code,relation_source,relation_scope) DO UPDATE SET
stock_market=excluded.stock_market,stock_name=excluded.stock_name,board_name=excluded.board_name,
board_type=excluded.board_type,source_order=excluded.source_order,detected_at=excluded.detected_at,raw_data=excluded.raw_data`, tradeDate, runID)
		if err != nil {
			return result, err
		}
	}

	finishedAt := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE relation_sync_run SET status='success',board_count=?,relation_count=?,
added_count=?,removed_count=?,baseline_built=?,finished_at=?,duration_ms=?,error_code='',error_message=''
WHERE run_id=?`, boardCount, result.RelationCount, result.AddedCount, result.RemovedCount, boolInt(result.BaselineBuilt),
		finishedAt.Format(timestampLayout), finishedAt.Sub(detectedAt).Milliseconds(), runID); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stock_board_relation_stage WHERE run_id=?`, runID); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) FailRelationSync(ctx context.Context, run repository.RelationSyncRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM stock_board_relation_stage WHERE run_id=?`, run.RunID); err != nil {
		return err
	}
	var finished any
	if run.FinishedAt != nil {
		finished = run.FinishedAt.Format(timestampLayout)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE relation_sync_run SET status=?,board_count=?,relation_count=?,
added_count=?,removed_count=?,baseline_built=?,finished_at=?,duration_ms=?,error_code=?,error_message=? WHERE run_id=?`,
		string(run.Status), run.BoardCount, run.RelationCount, run.AddedCount, run.RemovedCount, boolInt(run.BaselineBuilt),
		finished, run.DurationMS, run.ErrorCode, run.ErrorMessage, run.RunID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) HasSuccessfulRelationSync(ctx context.Context, tradeDate string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM relation_sync_run
WHERE trade_date=? AND status='success')`, tradeDate).Scan(&exists)
	return exists == 1, err
}

func (s *Store) StockBoardRelations(ctx context.Context, stockCode, asOf string) ([]graymarket.StockBoardRelation, error) {
	return s.relationsAsOf(ctx, asOf, "stock_code=?", []any{stockCode}, "board_type,source_order,board_code")
}

func (s *Store) BoardStockRelations(ctx context.Context, boardType graymarket.BoardType, boardCode, asOf string) ([]graymarket.StockBoardRelation, error) {
	return s.relationsAsOf(ctx, asOf, "board_type=? AND board_code=?", []any{string(boardType), boardCode}, "stock_code")
}

func (s *Store) BoardStockRelationsBatch(ctx context.Context, boardType graymarket.BoardType, boardCodes []string, asOf string) ([]graymarket.StockBoardRelation, error) {
	unique := make([]string, 0, len(boardCodes))
	seen := make(map[string]struct{}, len(boardCodes))
	for _, code := range boardCodes {
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		unique = append(unique, code)
	}
	if len(unique) == 0 {
		return []graymarket.StockBoardRelation{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, 0, len(unique)+1)
	args = append(args, string(boardType))
	for _, code := range unique {
		args = append(args, code)
	}
	return s.relationsAsOf(ctx, asOf, "board_type=? AND board_code IN ("+placeholders+")",
		args, "board_code,stock_code")
}

func (s *Store) relationsAsOf(ctx context.Context, asOf, filter string, filterArgs []any, orderBy string) ([]graymarket.StockBoardRelation, error) {
	query := `WITH selected_baseline AS (
    SELECT max(baseline_date) AS baseline_date FROM stock_board_relation_baseline WHERE baseline_date<=?
), events AS (
    SELECT baseline_date AS effective_date,'added' AS change_type,stock_code,stock_market,stock_name,
           board_code,board_name,board_type,source_order,relation_source,relation_scope,detected_at,raw_data
    FROM stock_board_relation_baseline WHERE baseline_date=(SELECT baseline_date FROM selected_baseline) AND (` + filter + `)
    UNION ALL
    SELECT effective_date,change_type,stock_code,stock_market,stock_name,board_code,board_name,board_type,
           source_order,relation_source,relation_scope,detected_at,raw_data
    FROM stock_board_relation_change
    WHERE effective_date>=(SELECT baseline_date FROM selected_baseline) AND effective_date<=? AND (` + filter + `)
), latest AS (
    SELECT *,row_number() OVER (PARTITION BY stock_code,board_code,relation_source,relation_scope
                                ORDER BY effective_date DESC,detected_at DESC) AS event_rank
    FROM events
)
SELECT stock_code,stock_market,stock_name,board_code,board_name,board_type,source_order,
       relation_source,relation_scope,effective_date,detected_at,raw_data
FROM latest WHERE event_rank=1 AND change_type='added' ORDER BY ` + orderBy
	args := []any{asOf}
	args = append(args, filterArgs...)
	args = append(args, asOf)
	args = append(args, filterArgs...)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanRelations(rows)
}

func (s *Store) RelationChanges(ctx context.Context, tradeDate string, boardType graymarket.BoardType) ([]graymarket.StockBoardRelationChange, error) {
	where := "effective_date=?"
	args := []any{tradeDate}
	if boardType != "" {
		where += " AND board_type=?"
		args = append(args, string(boardType))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT stock_code,stock_market,stock_name,board_code,board_name,
board_type,source_order,relation_source,relation_scope,effective_date,detected_at,raw_data,change_type,run_id
FROM stock_board_relation_change WHERE `+where+` ORDER BY change_type,board_type,board_code,stock_code`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]graymarket.StockBoardRelationChange, 0)
	for rows.Next() {
		var item graymarket.StockBoardRelationChange
		var boardTypeValue, detectedAt, changeType string
		if err := rows.Scan(&item.StockCode, &item.StockMarket, &item.StockName, &item.BoardCode, &item.BoardName,
			&boardTypeValue, &item.SourceOrder, &item.RelationSource, &item.RelationScope, &item.EffectiveDate,
			&detectedAt, &item.RawData, &changeType, &item.RunID); err != nil {
			return nil, err
		}
		item.BoardType = graymarket.BoardType(boardTypeValue)
		item.ChangeType = graymarket.RelationChangeType(changeType)
		item.DetectedAt, _ = time.Parse(timestampLayout, detectedAt)
		result = append(result, item)
	}
	return result, rows.Err()
}

func scanRelations(rows *sql.Rows) ([]graymarket.StockBoardRelation, error) {
	defer rows.Close()
	result := make([]graymarket.StockBoardRelation, 0)
	for rows.Next() {
		var item graymarket.StockBoardRelation
		var boardType, detectedAt string
		if err := rows.Scan(&item.StockCode, &item.StockMarket, &item.StockName, &item.BoardCode, &item.BoardName,
			&boardType, &item.SourceOrder, &item.RelationSource, &item.RelationScope, &item.EffectiveDate,
			&detectedAt, &item.RawData); err != nil {
			return nil, err
		}
		item.BoardType = graymarket.BoardType(boardType)
		item.DetectedAt, _ = time.Parse(timestampLayout, detectedAt)
		result = append(result, item)
	}
	return result, rows.Err()
}
