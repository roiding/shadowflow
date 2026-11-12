package collector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/limitup"
	"github.com/roiding/shadowflow/internal/repository"
	"github.com/roiding/shadowflow/internal/thsfocus"
)

type ThsFocusSource interface {
	FetchDay(context.Context, string, string) (thsfocus.Snapshot, error)
}

type thsFocusStore interface {
	SaveThsFocus(context.Context, thsfocus.Snapshot) error
	ReadThsFocus(context.Context, string, string) (*thsfocus.Snapshot, error)
}

func (service *Service) WithThsFocusSource(source ThsFocusSource, scope string) *Service {
	service.thsFocusSource = source
	service.thsFocusScope = scope
	if scope == "" {
		service.thsFocusScope = limitup.ScopeDefault
	}
	return service
}

func (service *Service) HasThsFocusArchive(ctx context.Context, tradeDate string) (bool, error) {
	store, available := service.store.(thsFocusStore)
	if !available || !limitup.ValidScope(service.thsFocusScope) {
		return false, fmt.Errorf("THS focus archive is not configured")
	}
	snapshot, err := store.ReadThsFocus(ctx, tradeDate, service.thsFocusScope)
	return snapshot != nil, err
}

func (service *Service) CollectThsFocus(ctx context.Context, runAt time.Time) error {
	store, available := service.store.(thsFocusStore)
	if !available || service.thsFocusSource == nil || !limitup.ValidScope(service.thsFocusScope) {
		return fmt.Errorf("THS focus archive is not configured")
	}
	tradeDate := runAt.In(limitup.Shanghai()).Format("2006-01-02")
	startedAt := time.Now().UTC()
	run := repository.CollectionRun{
		RunID: newRunID(), SnapshotAt: runAt, SnapshotKind: graymarket.SnapshotKind("ths_focus"),
		RankType: graymarket.RankStock, RequestedDate: tradeDate, Status: repository.RunRunning,
		StartedAt: startedAt, AttemptCount: 1,
	}
	if err := service.store.StartRun(ctx, run); err != nil {
		return err
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
		return errors.Join(resultErr, service.store.FinishRun(finishCtx, run))
	}
	snapshot, err := service.thsFocusSource.FetchDay(ctx, tradeDate, service.thsFocusScope)
	if err != nil {
		return finish(err)
	}
	if snapshot.TradeDate != tradeDate || snapshot.Scope != service.thsFocusScope {
		return finish(fmt.Errorf("THS focus identity mismatch"))
	}
	if err := store.SaveThsFocus(ctx, snapshot); err != nil {
		return finish(err)
	}
	run.ActualTradeDate, run.PageCount = tradeDate, len(snapshot.RawPages)
	if snapshot.Tabs != nil {
		run.ExpectedTotal, run.FetchedTotal = len(snapshot.Tabs), len(snapshot.Tabs)
	} else {
		run.ExpectedTotal, run.FetchedTotal = len(snapshot.Blocks), len(snapshot.Blocks)
	}
	return finish(nil)
}
