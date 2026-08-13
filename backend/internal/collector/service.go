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

type Service struct {
	source   Source
	store    repository.Store
	logger   *slog.Logger
	retries  int
	retryGap time.Duration
}

func New(source Source, store repository.Store, logger *slog.Logger) *Service {
	return &Service{source: source, store: store, logger: logger, retries: 2, retryGap: 400 * time.Millisecond}
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

	finishedAt := time.Now().UTC()
	run.FinishedAt = &finishedAt
	run.DurationMS = finishedAt.Sub(startedAt).Milliseconds()
	if fetchErr != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, errorCode(fetchErr), fetchErr.Error()
		_ = s.store.FinishRun(context.WithoutCancel(ctx), run)
		return fetchErr
	}
	if snapshot.TradeDate != formatDate(requestedDate) {
		mismatch := fmt.Errorf("upstream trade date %s does not match requested date %s", snapshot.TradeDate, formatDate(requestedDate))
		run.Status, run.ErrorCode, run.ErrorMessage, run.ActualTradeDate = repository.RunFailed, "date_mismatch", mismatch.Error(), snapshot.TradeDate
		_ = s.store.FinishRun(context.WithoutCancel(ctx), run)
		return mismatch
	}

	var saveErr error
	if kind == graymarket.SnapshotMinuteWork {
		saveErr = s.store.SaveIntraday(ctx, runID, snapshot, isLongTermBoundary(snapshotAt))
	} else {
		saveErr = s.store.SaveDailyClose(ctx, runID, snapshot)
	}
	if saveErr != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", saveErr.Error()
	} else {
		run.Status = repository.RunSuccess
	}
	run.ActualTradeDate, run.ExpectedTotal, run.FetchedTotal, run.PageCount = snapshot.TradeDate, snapshot.ExpectedTotal, len(snapshot.Records), len(snapshot.RawPages)
	_ = s.store.FinishRun(context.WithoutCancel(ctx), run)
	s.logger.Info("collection finished", "run_id", runID, "kind", kind, "rank_type", rankType,
		"records", len(snapshot.Records), "pages", len(snapshot.RawPages), "duration_ms", run.DurationMS, "error", saveErr)
	return saveErr
}

func isLongTermBoundary(value time.Time) bool {
	if value.Minute()%5 != 0 {
		return false
	}
	hourMinute := value.Format("15:04")
	return hourMinute >= "09:35" && hourMinute <= "11:30" || hourMinute >= "13:05" && hourMinute <= "15:00"
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
