package collector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/limitup"
	"github.com/roiding/shadowflow/internal/repository"
)

type LimitUpSource interface {
	FetchDay(context.Context, string, string) (limitup.Snapshot, error)
}

type limitUpArchiveStore interface {
	SaveLimitUpSnapshot(context.Context, limitup.Snapshot) error
	HasLimitUpDay(context.Context, string, string) (bool, error)
	StartLimitUpRun(context.Context, repository.CollectionRun, string) error
}

func (service *Service) WithLimitUpSource(source LimitUpSource, scope string) *Service {
	service.limitUpSource = source
	service.limitUpScope = scope
	if service.limitUpScope == "" {
		service.limitUpScope = limitup.ScopeDefault
	}
	return service
}

func (service *Service) HasLimitUpArchive(ctx context.Context, tradeDate string) (bool, error) {
	store, available := service.store.(limitUpArchiveStore)
	if !available {
		return false, fmt.Errorf("limit-up event archive is not configured")
	}
	exists, err := store.HasLimitUpDay(ctx, tradeDate, service.limitUpScope)
	if err != nil || exists {
		return exists, err
	}
	if service.limitUpSource == nil {
		return false, fmt.Errorf("limit-up event archive is not configured")
	}
	return false, nil
}

func (service *Service) CollectLimitUpEvents(ctx context.Context, runAt time.Time) error {
	store, available := service.store.(limitUpArchiveStore)
	if !available || service.limitUpSource == nil || !limitup.ValidScope(service.limitUpScope) {
		return fmt.Errorf("limit-up event archive is not configured")
	}
	tradeDate := runAt.In(limitup.Shanghai()).Format("2006-01-02")
	startedAt := time.Now().UTC()
	run := repository.CollectionRun{
		RunID: newRunID(), SnapshotAt: runAt, SnapshotKind: graymarket.SnapshotLimitUpEvents,
		RankType: graymarket.RankStock, RequestedDate: tradeDate, Status: repository.RunRunning,
		StartedAt: startedAt, AttemptCount: 1,
	}
	if err := store.StartLimitUpRun(ctx, run, service.limitUpScope); err != nil {
		return fmt.Errorf("start limit-up event archive: %w", err)
	}
	finish := func(resultErr error) error {
		finishedAt := time.Now().UTC()
		run.FinishedAt = &finishedAt
		run.DurationMS = finishedAt.Sub(startedAt).Milliseconds()
		run.Status = repository.RunSuccess
		if resultErr != nil {
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, errorCode(resultErr), resultErr.Error()
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := service.store.FinishRun(finishCtx, run); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("finish limit-up event archive: %w", err))
		}
		return resultErr
	}
	snapshot, err := service.limitUpSource.FetchDay(ctx, tradeDate, service.limitUpScope)
	if err != nil {
		return finish(err)
	}
	if snapshot.TradeDate != tradeDate || snapshot.Scope != service.limitUpScope || snapshot.Source != limitup.SourceTHS {
		return finish(fmt.Errorf("limit-up event snapshot identity does not match requested date, source or scope"))
	}
	if err := limitup.ValidateSnapshot(snapshot); err != nil {
		return finish(err)
	}
	run.ActualTradeDate = snapshot.TradeDate
	run.ExpectedTotal, run.PageCount = len(snapshot.Events), len(snapshot.RawPages)
	if err := store.SaveLimitUpSnapshot(ctx, snapshot); err != nil {
		return finish(err)
	}
	run.FetchedTotal = len(snapshot.Events)
	return finish(nil)
}
