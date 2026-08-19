package collector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

func (s *Service) CollectStockBoardRelations(ctx context.Context, tradeDate string) error {
	if _, err := time.Parse("2006-01-02", tradeDate); err != nil {
		return fmt.Errorf("trade date must use YYYY-MM-DD: %w", err)
	}
	if s.relationSource == nil || s.relationStore == nil {
		return errors.New("stock-board relation synchronization is not configured")
	}
	runID := newRunID()
	startedAt := time.Now().UTC()
	run := repository.RelationSyncRun{RunID: runID, TradeDate: tradeDate, Status: repository.RunRunning, StartedAt: startedAt}
	if err := s.relationStore.StartRelationSync(ctx, run); err != nil {
		return fmt.Errorf("start relation sync: %w", err)
	}

	fail := func(syncErr error) error {
		finishedAt := time.Now().UTC()
		run.Status = repository.RunFailed
		run.FinishedAt = &finishedAt
		run.DurationMS = finishedAt.Sub(startedAt).Milliseconds()
		run.ErrorCode = errorCode(syncErr)
		run.ErrorMessage = syncErr.Error()
		if err := s.relationStore.FailRelationSync(context.WithoutCancel(ctx), run); err != nil {
			syncErr = errors.Join(syncErr, fmt.Errorf("finish relation sync: %w", err))
		}
		return syncErr
	}

	seenBoards := make(map[string]graymarket.BoardType, 1024)
	catalogs := make(map[graymarket.BoardType][]graymarket.Board, 2)
	for _, boardType := range []graymarket.BoardType{graymarket.BoardIndustry, graymarket.BoardConcept} {
		boards, err := s.fetchBoardCatalog(ctx, boardType)
		if err != nil {
			return fail(err)
		}
		catalogs[boardType] = append([]graymarket.Board(nil), boards...)
		for _, board := range boards {
			if previous, duplicate := seenBoards[board.Code]; duplicate {
				return fail(fmt.Errorf("board code %s appears in both %s and %s catalogs", board.Code, previous, board.Type))
			}
			seenBoards[board.Code] = board.Type
			relations, err := s.fetchBoardConstituents(ctx, board)
			if err != nil {
				return fail(err)
			}
			if err := s.relationStore.StageRelations(ctx, runID, relations); err != nil {
				return fail(fmt.Errorf("stage relations for %s %s: %w", board.Type, board.Code, err))
			}
			run.BoardCount++
			run.RelationCount += len(relations)
		}
	}

	if catalogStore, ok := s.relationStore.(interface {
		SaveBoardCatalogSnapshots(context.Context, string, map[graymarket.BoardType][]graymarket.Board) error
	}); ok {
		if err := catalogStore.SaveBoardCatalogSnapshots(ctx, tradeDate, catalogs); err != nil {
			return fail(fmt.Errorf("save board catalog snapshots: %w", err))
		}
	}
	result, err := s.relationStore.ApplyRelationScan(ctx, runID, tradeDate, startedAt)
	if err != nil {
		return fail(fmt.Errorf("apply relation scan: %w", err))
	}
	run.RelationCount = result.RelationCount
	run.AddedCount = result.AddedCount
	run.RemovedCount = result.RemovedCount
	run.BaselineBuilt = result.BaselineBuilt
	s.logger.Info("stock-board relation sync finished", "run_id", runID, "trade_date", tradeDate,
		"boards", run.BoardCount, "relations", run.RelationCount, "added", run.AddedCount,
		"removed", run.RemovedCount, "baseline_built", run.BaselineBuilt)
	return nil
}

func (s *Service) HasStockBoardRelations(ctx context.Context, tradeDate string) bool {
	if s.relationStore == nil {
		return false
	}
	exists, err := s.relationStore.HasSuccessfulRelationSync(ctx, tradeDate)
	if err != nil {
		s.logger.Error("check stock-board relation sync", "trade_date", tradeDate, "error", err)
		return false
	}
	return exists
}

func (s *Service) fetchBoardCatalog(ctx context.Context, boardType graymarket.BoardType) ([]graymarket.Board, error) {
	var boards []graymarket.Board
	var err error
	for attempt := 1; attempt <= s.retries+1; attempt++ {
		boards, err = s.relationSource.FetchBoardCatalog(ctx, boardType)
		if err == nil || ctx.Err() != nil {
			break
		}
		if attempt <= s.retries && !waitForRetry(ctx, s.retryGap*time.Duration(attempt)) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("fetch %s board catalog: %w", boardType, err)
	}
	return boards, nil
}

func (s *Service) fetchBoardConstituents(ctx context.Context, board graymarket.Board) ([]graymarket.StockBoardRelation, error) {
	var relations []graymarket.StockBoardRelation
	var err error
	for attempt := 1; attempt <= s.retries+1; attempt++ {
		relations, err = s.relationSource.FetchBoardConstituents(ctx, board)
		if err == nil || ctx.Err() != nil {
			break
		}
		if attempt <= s.retries && !waitForRetry(ctx, s.retryGap*time.Duration(attempt)) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("fetch constituents for %s %s: %w", board.Type, board.Code, err)
	}
	return relations, nil
}

func waitForRetry(ctx context.Context, duration time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(duration):
		return true
	}
}
