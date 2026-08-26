package collector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

type Source interface {
	FetchAll(context.Context, graymarket.RankType, string, time.Time) (graymarket.RankSnapshot, error)
}

type RelationSource interface {
	FetchBoardCatalog(context.Context, graymarket.BoardType) ([]graymarket.Board, error)
	FetchBoardConstituents(context.Context, graymarket.Board) ([]graymarket.StockBoardRelation, error)
}

type StockDailyQuoteSource interface {
	FetchStockQuotes(context.Context, []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error)
}

type BoardDailyQuoteSource interface {
	FetchBoardQuotes(context.Context, graymarket.RankType) ([]graymarket.BoardQuote, error)
}

type BoardMoneySource interface {
	FetchMoney5m(context.Context, graymarket.RankSnapshot, bool) ([]graymarket.MoneyPoint, error)
}

// IncrementalMoneySource emits one complete money curve at a time. The
// collector batches those curves before handing them to the store, so a large
// archive does not have to remain in memory until every upstream request ends.
type IncrementalMoneySource interface {
	FetchMoney5mIncremental(context.Context, graymarket.RankSnapshot, bool, func([]graymarket.MoneyPoint) error) (int, error)
}

type StockKlineSource interface {
	FetchStockKlines5m(context.Context, graymarket.RankSnapshot) ([]graymarket.StockKlinePoint, error)
}

// IncrementalStockKlineSource emits each complete stock curve as soon as it
// is fetched.  This keeps a large archive rerun observable and resumable.
type IncrementalStockKlineSource interface {
	FetchStockKlines5mIncremental(context.Context, graymarket.RankSnapshot, func([]graymarket.StockKlinePoint) error) (int, error)
}

type IncrementalArchiveStore interface {
	SaveBoardArchiveBatch(context.Context, string, graymarket.RankSnapshot, []graymarket.MoneyPoint, bool, bool) error
	SaveStockArchiveBatch(context.Context, string, graymarket.RankSnapshot, []graymarket.MoneyPoint, bool, bool) error
}

type ArchivePartStore interface {
	HasBoardArchive(context.Context, string, graymarket.RankType) (bool, error)
	HasStockMoneyArchive(context.Context, string) (bool, error)
}

const stockKlinePersistBatchStocks = 500
const moneyPersistBatchStocks = 500

type store interface {
	SaveIntraday(context.Context, string, graymarket.RankSnapshot, bool) error
	SaveDailyClose(context.Context, string, graymarket.RankSnapshot) error
	SaveBoardArchive(context.Context, string, graymarket.RankSnapshot, []graymarket.MoneyPoint) error
	SaveStockArchive(context.Context, string, graymarket.RankSnapshot, []graymarket.MoneyPoint) error
	SaveStockKlines(context.Context, string, []graymarket.StockKlinePoint) error
	MissingStockKlineCodes(context.Context, string) ([]string, error)
	DailyCloseStocks(context.Context, string, []string) ([]graymarket.RankRecord, error)
	CompactResearch(context.Context, string) ([]repository.QualitySummary, error)
	CleanupArchivedIntraday(context.Context, string) error
	HasDailyClose(context.Context, string) (bool, error)
	HasEndOfDayArchive(context.Context, string) (bool, error)
	HasStockKlineArchive(context.Context, string) (bool, error)
	SealArchiveRevision(context.Context, string, string) (repository.ArchiveRevision, error)
	Maintain(context.Context, time.Time, int, int) (repository.MaintenanceResult, error)
	StartRun(context.Context, repository.CollectionRun) error
	FinishRun(context.Context, repository.CollectionRun) error
}

type relationStore interface {
	StartRelationSync(context.Context, repository.RelationSyncRun) error
	StageRelations(context.Context, string, []graymarket.StockBoardRelation) error
	ApplyRelationScan(context.Context, string, string, time.Time) (repository.RelationApplyResult, error)
	FailRelationSync(context.Context, repository.RelationSyncRun) error
	HasSuccessfulRelationSync(context.Context, string) (bool, error)
}

type Service struct {
	source         Source
	store          store
	relationSource RelationSource
	relationStore  relationStore
	logger         *slog.Logger
	retries        int
	retryGap       time.Duration
}

func New(source Source, store store, logger *slog.Logger) *Service {
	relationSource, _ := source.(RelationSource)
	relationPersistence, _ := store.(relationStore)
	return &Service{source: source, store: store, relationSource: relationSource, relationStore: relationPersistence,
		logger: logger, retries: 2, retryGap: 400 * time.Millisecond}
}

func (s *Service) CollectBoards(ctx context.Context, snapshotAt time.Time) error {
	date := snapshotAt.Format("20060102")
	errCh := make(chan error, 2)
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		rankType := rankType
		go func() { errCh <- s.collect(ctx, rankType, graymarket.SnapshotMinuteWork, date, snapshotAt) }()
	}
	var combined error
	for range 2 {
		combined = errors.Join(combined, <-errCh)
	}
	return combined
}

func (s *Service) CollectDailyClose(ctx context.Context, snapshotAt time.Time) error {
	return s.collect(ctx, graymarket.RankStock, graymarket.SnapshotDailyClose, snapshotAt.Format("20060102"), snapshotAt)
}

func (s *Service) CollectEndOfDay(ctx context.Context, runAt time.Time) error {
	closeAt := time.Date(runAt.Year(), runAt.Month(), runAt.Day(), 15, 0, 0, 0, runAt.Location())
	date := runAt.Format("20060102")
	tradeDate := runAt.Format("2006-01-02")
	revisionID := "eod-" + newRunID()
	var combined error
	// These scans share the same upstream endpoint and SQLite writer. Running
	// all three at once amplifies throttling and lock contention, so keep the
	// manual/full-service path sequential while still attempting every part.
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept, graymarket.RankStock} {
		combined = errors.Join(combined, s.collectMoneyArchive(ctx, rankType, date, closeAt, runAt))
	}
	if combined == nil {
		klineComplete, err := s.store.HasStockKlineArchive(ctx, tradeDate)
		if err != nil {
			return err
		}
		if klineComplete {
			_, err = s.store.SealArchiveRevision(ctx, tradeDate, revisionID)
			combined = errors.Join(combined, err)
		}
	}
	return combined
}

func (s *Service) CollectEndOfDayPart(ctx context.Context, runAt time.Time, rankType graymarket.RankType) error {
	closeAt := time.Date(runAt.Year(), runAt.Month(), runAt.Day(), 15, 0, 0, 0, runAt.Location())
	return s.collectMoneyArchive(ctx, rankType, runAt.Format("20060102"), closeAt, runAt)
}

func (s *Service) HasEndOfDayPart(ctx context.Context, tradeDate string, rankType graymarket.RankType) (bool, error) {
	partStore, ok := s.store.(ArchivePartStore)
	if !ok {
		return false, fmt.Errorf("archive part status store is unavailable")
	}
	if rankType == graymarket.RankStock {
		return partStore.HasStockMoneyArchive(ctx, tradeDate)
	}
	return partStore.HasBoardArchive(ctx, tradeDate, rankType)
}

func (s *Service) collectMoneyArchive(ctx context.Context, rankType graymarket.RankType, requestedDate string, closeAt, runAt time.Time) error {
	if incremental, ok := s.source.(IncrementalMoneySource); ok {
		moneyStore, storeOK := s.store.(IncrementalArchiveStore)
		if !storeOK {
			return fmt.Errorf("incremental archive store is unavailable")
		}
		return s.collectMoneyArchiveIncremental(ctx, rankType, requestedDate, closeAt, runAt, incremental, moneyStore)
	}
	if rankType == graymarket.RankStock {
		return s.collectStockArchive(ctx, requestedDate, closeAt, runAt)
	}
	return s.collectBoardArchive(ctx, rankType, requestedDate, closeAt, runAt)
}

func (s *Service) collectMoneyArchiveIncremental(ctx context.Context, rankType graymarket.RankType, requestedDate string, closeAt, runAt time.Time, source IncrementalMoneySource, store IncrementalArchiveStore) error {
	runID := newRunID()
	startedAt := time.Now().UTC()
	run := repository.CollectionRun{RunID: runID, SnapshotAt: runAt, SnapshotKind: graymarket.SnapshotResearch5m, RankType: rankType, Status: repository.RunRunning, RequestedDate: formatDate(requestedDate), AttemptCount: 1, StartedAt: startedAt}
	if err := s.store.StartRun(ctx, run); err != nil {
		return err
	}
	finish := func(resultErr error) error {
		finishedAt := time.Now().UTC()
		run.FinishedAt = &finishedAt
		run.DurationMS = finishedAt.Sub(startedAt).Milliseconds()
		if err := s.store.FinishRun(context.WithoutCancel(ctx), run); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
		return resultErr
	}
	snapshot, err := s.source.FetchAll(ctx, rankType, requestedDate, closeAt)
	if err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, errorCode(err), err.Error()
		return finish(err)
	}
	if snapshot.TradeDate != formatDate(requestedDate) {
		err = fmt.Errorf("upstream trade date %s does not match requested date %s", snapshot.TradeDate, formatDate(requestedDate))
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "date_mismatch", err.Error()
		return finish(err)
	}
	if rankType == graymarket.RankIndustry || rankType == graymarket.RankConcept {
		if err = s.enrichBoardDailyClose(ctx, &snapshot); err != nil {
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "quote_enrichment", err.Error()
			return finish(err)
		}
	} else {
		if err = s.enrichStockDailyClose(ctx, &snapshot); err != nil {
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "quote_enrichment", err.Error()
			return finish(err)
		}
		snapshot.Records = eligibleStockRecords(snapshot.Records)
	}
	if len(snapshot.Records) == 0 {
		err = fmt.Errorf("%s archive has no eligible records", rankType)
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "no_eligible_records", err.Error()
		return finish(err)
	}
	run.ExpectedTotal = len(snapshot.Records) * 48
	run.PageCount = len(snapshot.RawPages) + len(snapshot.Records)
	pending := make([]graymarket.MoneyPoint, 0, moneyPersistBatchStocks*48)
	pendingCurves := 0
	first := true
	flush := func(final bool) error {
		if pendingCurves == 0 && !final {
			return nil
		}
		var saveErr error
		if rankType == graymarket.RankStock {
			saveErr = store.SaveStockArchiveBatch(ctx, runID, snapshot, pending, first, final)
		} else {
			saveErr = store.SaveBoardArchiveBatch(ctx, runID, snapshot, pending, first, final)
		}
		if saveErr == nil {
			run.FetchedTotal += len(pending)
			first = false
			pending = pending[:0]
			pendingCurves = 0
		}
		return saveErr
	}
	completed, fetchErr := source.FetchMoney5mIncremental(ctx, snapshot, true, func(curve []graymarket.MoneyPoint) error {
		pending = append(pending, curve...)
		pendingCurves++
		if pendingCurves >= moneyPersistBatchStocks {
			return flush(false)
		}
		return nil
	})
	_ = completed
	if fetchErr != nil {
		// Preserve the last partial batch, but do not seal quality/manifest as
		// complete; the next independent retry will continue from the durable
		// rows.
		if err = flush(false); err != nil {
			fetchErr = errors.Join(fetchErr, err)
		}
	} else if run.FetchedTotal+len(pending) != run.ExpectedTotal {
		// Upstream reported success but delivered fewer points than expected.
		// Persist the durable rows without sealing quality/manifest, otherwise
		// the cleanup gate would treat this run as a complete archive and could
		// delete the intraday source data.
		if err = flush(false); err != nil {
			fetchErr = errors.Join(fetchErr, err)
		}
		err = fmt.Errorf("incomplete %s money archive: expected %d rows, got %d", rankType, run.ExpectedTotal, run.FetchedTotal)
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "incomplete", err.Error()
		return finish(errors.Join(err, fetchErr))
	} else if err = flush(true); err != nil {
		fetchErr = errors.Join(fetchErr, err)
	}
	if fetchErr != nil {
		run.Status = repository.RunPartial
		if run.FetchedTotal == 0 {
			run.Status = repository.RunFailed
		}
		run.ErrorCode, run.ErrorMessage = errorCode(fetchErr), fetchErr.Error()
		return finish(fetchErr)
	}
	run.Status = repository.RunSuccess
	return finish(nil)
}

func (s *Service) CollectStockKlines(ctx context.Context, runAt time.Time) error {
	tradeDate := runAt.Format("2006-01-02")
	klineSource, ok := s.source.(StockKlineSource)
	if !ok {
		return errors.New("stock kline source is unavailable")
	}
	runID := newRunID()
	startedAt := time.Now().UTC()
	run := repository.CollectionRun{RunID: runID, SnapshotAt: runAt, SnapshotKind: graymarket.SnapshotStockKline, RankType: graymarket.RankStock,
		Status: repository.RunRunning, RequestedDate: tradeDate, ActualTradeDate: tradeDate, AttemptCount: 1, StartedAt: startedAt}
	if err := s.store.StartRun(ctx, run); err != nil {
		return err
	}
	finish := func(resultErr error) error {
		finishedAt := time.Now().UTC()
		run.FinishedAt, run.DurationMS = &finishedAt, finishedAt.Sub(startedAt).Milliseconds()
		if err := s.store.FinishRun(context.WithoutCancel(ctx), run); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
		return resultErr
	}
	missingCodes, err := s.store.MissingStockKlineCodes(ctx, tradeDate)
	if err != nil {
		errorCode := "storage_error"
		if errors.Is(err, graymarket.ErrNoData) {
			errorCode = "no_data"
		}
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, errorCode, err.Error()
		return finish(err)
	}
	if len(missingCodes) == 0 {
		if _, err := s.store.SealArchiveRevision(ctx, tradeDate, "kline-"+runID); err != nil {
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
			return finish(err)
		}
		run.Status = repository.RunSuccess
		return finish(nil)
	}
	records, err := s.store.DailyCloseStocks(ctx, tradeDate, missingCodes)
	if err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
		return finish(err)
	}
	if len(records) != len(missingCodes) {
		err = fmt.Errorf("incomplete persisted stock close candidates: expected %d rows, got %d", len(missingCodes), len(records))
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
		return finish(err)
	}
	for _, record := range records {
		if !record.QuoteAvailable {
			err = fmt.Errorf("persisted stock close candidate %s has no daily bar", record.Code)
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
			return finish(err)
		}
	}
	snapshot := graymarket.RankSnapshot{TradeDate: tradeDate, RankType: graymarket.RankStock,
		SnapshotAt: time.Date(runAt.Year(), runAt.Month(), runAt.Day(), 15, 0, 0, 0, runAt.Location()), Records: records}
	run.ExpectedTotal = len(snapshot.Records) * 48
	run.PageCount = len(snapshot.Records)
	if incremental, ok := s.source.(IncrementalStockKlineSource); ok {
		pending := make([]graymarket.StockKlinePoint, 0, stockKlinePersistBatchStocks*48)
		pendingStocks := 0
		flush := func() error {
			if pendingStocks == 0 {
				return nil
			}
			if err := s.store.SaveStockKlines(ctx, runID, pending); err != nil {
				return err
			}
			run.FetchedTotal += len(pending)
			pending = pending[:0]
			pendingStocks = 0
			return nil
		}
		_, fetchErr := incremental.FetchStockKlines5mIncremental(ctx, snapshot, func(points []graymarket.StockKlinePoint) error {
			pending = append(pending, points...)
			pendingStocks++
			if pendingStocks < stockKlinePersistBatchStocks {
				return nil
			}
			return flush()
		})
		// Keep the final partial batch durable even when the fetch ended with a
		// recoverable upstream error; the next run will request only its missing
		// stocks.
		persistErr := flush()
		if fetchErr == nil && persistErr != nil {
			fetchErr = fmt.Errorf("persist stock kline batch: %w", persistErr)
		} else if fetchErr != nil && persistErr != nil {
			fetchErr = errors.Join(fetchErr, fmt.Errorf("persist stock kline batch: %w", persistErr))
		}
		if fetchErr != nil {
			run.Status = repository.RunFailed
			if run.FetchedTotal > 0 {
				run.Status = repository.RunPartial
			}
			run.ErrorCode, run.ErrorMessage = errorCode(fetchErr), fetchErr.Error()
			return finish(fetchErr)
		}
		if run.FetchedTotal != run.ExpectedTotal {
			err = fmt.Errorf("incomplete stock kline fetch: expected %d rows, got %d", run.ExpectedTotal, run.FetchedTotal)
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
			return finish(err)
		}
	} else {
		points, fetchErr := klineSource.FetchStockKlines5m(ctx, snapshot)
		run.FetchedTotal = len(points)
		if len(points) > 0 {
			if err := s.store.SaveStockKlines(ctx, runID, points); err != nil {
				run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
				return finish(err)
			}
		}
		if fetchErr != nil {
			run.Status = repository.RunFailed
			if len(points) > 0 {
				run.Status = repository.RunPartial
			}
			run.ErrorCode, run.ErrorMessage = errorCode(fetchErr), fetchErr.Error()
			return finish(fetchErr)
		}
		if len(points) != run.ExpectedTotal {
			err = fmt.Errorf("incomplete stock kline fetch: expected %d rows, got %d", run.ExpectedTotal, len(points))
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
			return finish(err)
		}
	}
	complete, err := s.store.HasStockKlineArchive(ctx, tradeDate)
	if err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
		return finish(err)
	}
	if !complete {
		err = fmt.Errorf("stock kline archive remains incomplete after fetching all missing candidates")
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
		return finish(err)
	}
	if _, err := s.store.SealArchiveRevision(ctx, tradeDate, "kline-"+runID); err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
		return finish(err)
	}
	run.Status = repository.RunSuccess
	return finish(nil)
}

func (s *Service) HasStockKlineArchive(ctx context.Context, tradeDate string) (bool, error) {
	return s.store.HasStockKlineArchive(ctx, tradeDate)
}
func (s *Service) HasEndOfDayArchive(ctx context.Context, tradeDate string) (bool, error) {
	return s.store.HasEndOfDayArchive(ctx, tradeDate)
}
func (s *Service) CleanupArchivedIntraday(ctx context.Context, beforeDate string) error {
	return s.store.CleanupArchivedIntraday(ctx, beforeDate)
}

func (s *Service) Maintain(ctx context.Context, at time.Time, successRetentionDays, failureRetentionDays int) (repository.MaintenanceResult, error) {
	return s.store.Maintain(ctx, at, successRetentionDays, failureRetentionDays)
}

func (s *Service) HasDailyClose(ctx context.Context, tradeDate string) bool {
	exists, err := s.store.HasDailyClose(ctx, tradeDate)
	if err != nil {
		s.logger.Error("check daily close status", "trade_date", tradeDate, "error", err)
		return false
	}
	return exists
}

func (s *Service) CompactAndCleanup(ctx context.Context, tradeDate string) ([]repository.QualitySummary, error) {
	summaries, err := s.store.CompactResearch(ctx, tradeDate)
	if err != nil {
		return summaries, fmt.Errorf("compact research data: %w", err)
	}
	return summaries, nil
}

func (s *Service) collect(ctx context.Context, rankType graymarket.RankType, kind graymarket.SnapshotKind, requestedDate string, snapshotAt time.Time) error {
	runID := newRunID()
	startedAt := time.Now().UTC()
	run := repository.CollectionRun{RunID: runID, SnapshotAt: snapshotAt, SnapshotKind: kind, RankType: rankType,
		Status: repository.RunRunning, RequestedDate: formatDate(requestedDate), AttemptCount: 1, StartedAt: startedAt}
	if err := s.store.StartRun(ctx, run); err != nil {
		return fmt.Errorf("start collection run: %w", err)
	}

	finish := func(resultErr error) error {
		finishedAt := time.Now().UTC()
		run.FinishedAt = &finishedAt
		run.DurationMS = finishedAt.Sub(startedAt).Milliseconds()
		if err := s.store.FinishRun(context.WithoutCancel(ctx), run); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("finish collection run: %w", err))
		}
		return resultErr
	}

	var snapshot graymarket.RankSnapshot
	var fetchErr error
	for attempt := 1; attempt <= s.retries+1; attempt++ {
		run.AttemptCount = attempt
		snapshot, fetchErr = s.source.FetchAll(ctx, rankType, requestedDate, snapshotAt)
		if fetchErr == nil || errors.Is(fetchErr, graymarket.ErrNoData) || ctx.Err() != nil {
			break
		}
		if attempt <= s.retries {
			select {
			case <-ctx.Done():
				fetchErr = ctx.Err()
			case <-time.After(s.retryGap * time.Duration(attempt)):
			}
		}
	}

	if fetchErr != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, errorCode(fetchErr), fetchErr.Error()
		return finish(fetchErr)
	}
	run.ActualTradeDate, run.ExpectedTotal, run.FetchedTotal, run.PageCount = snapshot.TradeDate, snapshot.ExpectedTotal, len(snapshot.Records), len(snapshot.RawPages)
	if snapshot.TradeDate != formatDate(requestedDate) {
		mismatch := fmt.Errorf("upstream trade date %s does not match requested date %s", snapshot.TradeDate, formatDate(requestedDate))
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "date_mismatch", mismatch.Error()
		return finish(mismatch)
	}
	if kind == graymarket.SnapshotDailyClose && rankType == graymarket.RankStock {
		if err := s.enrichStockDailyClose(ctx, &snapshot); err != nil {
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "quote_enrichment", err.Error()
			return finish(err)
		}
	}

	var saveErr error
	if kind == graymarket.SnapshotMinuteWork {
		saveErr = s.store.SaveIntraday(ctx, runID, snapshot, false)
	} else {
		saveErr = s.store.SaveDailyClose(ctx, runID, snapshot)
	}
	if saveErr != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", saveErr.Error()
	} else {
		run.Status = repository.RunSuccess
	}
	resultErr := finish(saveErr)
	s.logger.Info("collection finished", "run_id", runID, "kind", kind, "rank_type", rankType,
		"records", len(snapshot.Records), "pages", len(snapshot.RawPages), "duration_ms", run.DurationMS, "error", resultErr)
	return resultErr
}

func (s *Service) collectBoardArchive(ctx context.Context, rankType graymarket.RankType, requestedDate string, closeAt, runAt time.Time) error {
	moneySource, ok := s.source.(BoardMoneySource)
	if !ok {
		return errors.New("board money curve source is unavailable")
	}
	runID := newRunID()
	startedAt := time.Now().UTC()
	run := repository.CollectionRun{RunID: runID, SnapshotAt: runAt, SnapshotKind: graymarket.SnapshotResearch5m, RankType: rankType,
		Status: repository.RunRunning, RequestedDate: formatDate(requestedDate), AttemptCount: 1, StartedAt: startedAt}
	if err := s.store.StartRun(ctx, run); err != nil {
		return fmt.Errorf("start board archive run: %w", err)
	}
	finish := func(resultErr error) error {
		finishedAt := time.Now().UTC()
		run.FinishedAt = &finishedAt
		run.DurationMS = finishedAt.Sub(startedAt).Milliseconds()
		if err := s.store.FinishRun(context.WithoutCancel(ctx), run); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("finish board archive run: %w", err))
		}
		return resultErr
	}

	var snapshot graymarket.RankSnapshot
	var err error
	for attempt := 1; attempt <= s.retries+1; attempt++ {
		run.AttemptCount = attempt
		snapshot, err = s.source.FetchAll(ctx, rankType, requestedDate, closeAt)
		if err == nil || ctx.Err() != nil {
			break
		}
		if attempt <= s.retries {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(s.retryGap * time.Duration(attempt)):
			}
		}
	}
	if err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, errorCode(err), err.Error()
		return finish(err)
	}
	// The end-of-day board archive uses the same board set returned by the
	// darktrade ranking endpoint. The separate full catalog is reserved for
	// stock-board relation maintenance; merging it here would silently turn a
	// ranked board snapshot into a different universe.
	run.ActualTradeDate = snapshot.TradeDate
	if snapshot.TradeDate != formatDate(requestedDate) {
		err = fmt.Errorf("upstream trade date %s does not match requested date %s", snapshot.TradeDate, formatDate(requestedDate))
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "date_mismatch", err.Error()
		return finish(err)
	}
	if rankType == graymarket.RankIndustry || rankType == graymarket.RankConcept {
		if err := s.enrichBoardDailyClose(ctx, &snapshot); err != nil {
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "quote_enrichment", err.Error()
			return finish(err)
		}
	}
	points, moneyErr := moneySource.FetchMoney5m(ctx, snapshot, true)
	run.ExpectedTotal = len(snapshot.Records) * 48
	run.FetchedTotal = len(points)
	run.PageCount = len(snapshot.RawPages) + len(snapshot.Records)
	if moneyErr != nil && len(points) == 0 {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, errorCode(moneyErr), moneyErr.Error()
		return finish(moneyErr)
	}
	mergeCloseMoney(&snapshot, points)
	if err := s.store.SaveBoardArchive(ctx, runID, snapshot, points); err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
		return finish(err)
	}
	run.Status = repository.RunSuccess
	if moneyErr != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunPartial, errorCode(moneyErr), moneyErr.Error()
	}
	return finish(moneyErr)
}

// mergeCloseMoney makes the 15:00 darktradetick values authoritative for the
// daily-close funding fields, including boards that were absent from the
// darktrade ranked response.
func mergeCloseMoney(snapshot *graymarket.RankSnapshot, points []graymarket.MoneyPoint) {
	close := make(map[string]graymarket.MoneyPoint, len(snapshot.Records))
	for _, point := range points {
		if point.SnapshotAt.Format("15:04") == "15:00" {
			close[point.Code] = point
		}
	}
	for index := range snapshot.Records {
		if point, ok := close[snapshot.Records[index].Code]; ok {
			snapshot.Records[index].MoneyAvailable = true
			snapshot.Records[index].DarkMoney = point.DarkMoney
			snapshot.Records[index].RegularMoney = point.RegularMoney
			snapshot.Records[index].MainMoneyInflow = point.MainMoneyInflow
		}
	}
}

func (s *Service) collectStockArchive(ctx context.Context, requestedDate string, closeAt, runAt time.Time) error {
	moneySource, ok := s.source.(BoardMoneySource)
	if !ok {
		return errors.New("stock money curve source is unavailable")
	}
	runID := newRunID()
	startedAt := time.Now().UTC()
	run := repository.CollectionRun{RunID: runID, SnapshotAt: runAt, SnapshotKind: graymarket.SnapshotResearch5m, RankType: graymarket.RankStock,
		Status: repository.RunRunning, RequestedDate: formatDate(requestedDate), AttemptCount: 1, StartedAt: startedAt}
	if err := s.store.StartRun(ctx, run); err != nil {
		return err
	}
	finish := func(resultErr error) error {
		finishedAt := time.Now().UTC()
		run.FinishedAt, run.DurationMS = &finishedAt, finishedAt.Sub(startedAt).Milliseconds()
		if err := s.store.FinishRun(context.WithoutCancel(ctx), run); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
		return resultErr
	}
	snapshot, err := s.source.FetchAll(ctx, graymarket.RankStock, requestedDate, closeAt)
	if err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, errorCode(err), err.Error()
		return finish(err)
	}
	run.ActualTradeDate = snapshot.TradeDate
	if snapshot.TradeDate != formatDate(requestedDate) {
		err = fmt.Errorf("upstream trade date %s does not match requested date %s", snapshot.TradeDate, formatDate(requestedDate))
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "date_mismatch", err.Error()
		return finish(err)
	}
	// The stock darktrade response is the authoritative end-of-day universe.
	// Enrichment below supplies the official daily quote fields; records without
	// a usable quote are excluded from the quantitative archive.
	if err := s.enrichStockDailyClose(ctx, &snapshot); err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "quote_enrichment", err.Error()
		return finish(err)
	}
	snapshot.Records = eligibleStockRecords(snapshot.Records)
	snapshot.ExpectedTotal = len(snapshot.Records)
	if len(snapshot.Records) == 0 {
		err = errors.New("darktrade stock archive has no eligible daily quotes")
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "no_eligible_stocks", err.Error()
		return finish(err)
	}
	points, err := moneySource.FetchMoney5m(ctx, snapshot, true)
	run.ExpectedTotal, run.FetchedTotal = len(snapshot.Records)*48, len(points)
	run.PageCount = len(snapshot.RawPages) + len(snapshot.Records)
	moneyErr := err
	mergeCloseMoney(&snapshot, points)
	if err := s.store.SaveStockArchive(ctx, runID, snapshot, points); err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
		return finish(err)
	}
	run.Status = repository.RunSuccess
	if moneyErr != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunPartial, errorCode(moneyErr), moneyErr.Error()
	}
	return finish(moneyErr)
}

func eligibleStockRecords(records []graymarket.RankRecord) []graymarket.RankRecord {
	eligible := make([]graymarket.RankRecord, 0, len(records))
	for _, record := range records {
		if record.QuoteAvailable {
			eligible = append(eligible, record)
		}
	}
	return eligible
}

func (s *Service) enrichStockDailyClose(ctx context.Context, snapshot *graymarket.RankSnapshot) error {
	return s.enrichStockDailyQuotes(ctx, snapshot)
}

func (s *Service) enrichStockDailyQuotes(ctx context.Context, snapshot *graymarket.RankSnapshot) error {
	source, ok := s.source.(StockDailyQuoteSource)
	if !ok {
		return errors.New("daily quote source is unavailable")
	}
	relations := make([]graymarket.StockBoardRelation, 0, len(snapshot.Records))
	for _, record := range snapshot.Records {
		relations = append(relations, graymarket.StockBoardRelation{
			StockCode: record.Code, StockMarket: record.Market, StockName: record.Name,
		})
	}
	quotes, err := source.FetchStockQuotes(ctx, relations)
	if err != nil {
		return fmt.Errorf("fetch %s daily quotes: %w", snapshot.RankType, err)
	}
	byCode := make(map[string]graymarket.StockQuote, len(quotes))
	wrongDate := 0
	noQuoteTime := 0
	for _, quote := range quotes {
		if quote.Available {
			switch quoteDate(quote.QuoteTime, snapshot.SnapshotAt.Location()) {
			case snapshot.TradeDate:
			case "":
				// f124 missing or unparseable (seen on suspended/special
				// instruments that still expose a price). Downgrade the quote
				// instead of failing the whole run: one such stock must not
				// block the entire daily-close archive.
				quote.Available = false
				noQuoteTime++
			default:
				wrongDate++
			}
		}
		byCode[quote.StockCode] = quote
	}
	if noQuoteTime > 0 {
		s.logger.Warn("stock daily quotes without a parseable quote time were marked unavailable",
			"trade_date", snapshot.TradeDate, "rank_type", snapshot.RankType, "count", noQuoteTime)
	}
	if wrongDate > 0 {
		return fmt.Errorf("%s daily quotes contain %d rows outside trade date %s", snapshot.RankType, wrongDate, snapshot.TradeDate)
	}
	unavailable := make([]string, 0)
	for index := range snapshot.Records {
		record := &snapshot.Records[index]
		quote := byCode[record.Code]
		record.OpenPrice = quote.OpenPrice
		record.HighPrice = quote.HighPrice
		record.LowPrice = quote.LowPrice
		record.ClosePrice = quote.LatestPrice
		record.PreviousClose = quote.PreviousClose
		record.ChangeValue = quote.ChangeValue
		record.Volume = quote.Volume
		record.Turnover = quote.Turnover
		record.TurnoverRate = quote.TurnoverRate
		record.Amplitude = quote.Amplitude
		record.QuoteAvailable = quote.Available
		if quote.Available {
			record.ChangePct = quote.ChangePct
		} else if quote.PreviousClose <= 0 {
			unavailable = append(unavailable, record.Code)
		}
	}
	if len(unavailable) > 0 {
		sample := unavailable
		if len(sample) > 20 {
			sample = sample[:20]
		}
		s.logger.Warn("some stock daily quotes are unavailable; retaining records without kline eligibility",
			"trade_date", snapshot.TradeDate, "rank_type", snapshot.RankType, "count", len(unavailable), "sample_codes", sample)
	}
	return nil
}

func (s *Service) enrichBoardDailyClose(ctx context.Context, snapshot *graymarket.RankSnapshot) error {
	source, ok := s.source.(BoardDailyQuoteSource)
	if !ok {
		return errors.New("board daily quote source is unavailable")
	}
	quotes, err := source.FetchBoardQuotes(ctx, snapshot.RankType)
	if err != nil {
		return fmt.Errorf("fetch %s board daily quotes: %w", snapshot.RankType, err)
	}
	byCode := make(map[string]graymarket.BoardQuote, len(quotes))
	for _, quote := range quotes {
		byCode[quote.BoardCode] = quote
	}
	for index := range snapshot.Records {
		record := &snapshot.Records[index]
		quote, exists := byCode[record.Code]
		if !exists || !quote.Available {
			return fmt.Errorf("missing %s board daily quote for %s", snapshot.RankType, record.Code)
		}
		if quoteDate(quote.QuoteTime, snapshot.SnapshotAt.Location()) != snapshot.TradeDate {
			return fmt.Errorf("%s board daily quote for %s is outside trade date %s", snapshot.RankType, record.Code, snapshot.TradeDate)
		}
		record.OpenPrice = quote.OpenPrice
		record.HighPrice = quote.HighPrice
		record.LowPrice = quote.LowPrice
		record.ClosePrice = quote.LatestPrice
		record.PreviousClose = quote.PreviousClose
		record.ChangeValue = quote.ChangeValue
		record.ChangePct = quote.ChangePct
		record.Volume = quote.Volume
		record.Turnover = quote.Turnover
		record.TurnoverRate = quote.TurnoverRate
		record.Amplitude = quote.Amplitude
		record.QuoteTime = quote.QuoteTime
		record.QuoteAvailable = true
	}
	return nil
}

func quoteDate(value string, location *time.Location) string {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.In(location).Format("2006-01-02")
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, location); err == nil {
		return parsed.Format("2006-01-02")
	}
	return ""
}

func newRunID() string {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer[:])
}

func formatDate(value string) string {
	if len(value) == 8 {
		return value[:4] + "-" + value[4:6] + "-" + value[6:]
	}
	return value
}

func errorCode(err error) string {
	if errors.Is(err, graymarket.ErrNoData) {
		return "no_data"
	}
	if errors.Is(err, graymarket.ErrDecode) {
		return "decode_error"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "upstream_error"
}
