package collector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

type dayRangeState struct {
	date    time.Time
	pending map[string]graymarket.RankRecord
	buffer  []graymarket.StockKlinePoint
	written int
}

// stockKlineRangeSource fetches one stock's 5-minute bars over an inclusive
// date range in a single upstream request (the operator-run backfill path).
type stockKlineRangeSource interface {
	FetchStockKlines5mRange(ctx context.Context, from, to time.Time,
		stock graymarket.RankRecord, maxBars int, emit func(tradeDate string, points []graymarket.StockKlinePoint)) error
}

// RangePatchReport summarizes one operator-run range patch.
type RangePatchReport struct {
	Dates           []string `json:"dates"`
	Stocks          int      `json:"stocks"`
	RowsWritten     int      `json:"rows_written"`
	FailedStocks    int      `json:"failed_stocks"`
	IncompleteDates []string `json:"incomplete_dates"`
}

// CollectStockKlineRangePatch backfills every missing 5-minute kline in
// [from,to] by fetching each stock ONCE for the whole range and writing the
// returned bars straight into the archive. Bars arrive marked
// stock_kline_5m_backfill and skip the per-day daily-bar cross-check, because
// the upstream's own BSE series disagrees with its daily bar on auction and
// after-hours prints; per-date completeness (48 slots) is still enforced and
// incomplete dates are reported instead of written.
func (s *Service) CollectStockKlineRangePatch(ctx context.Context, from, to time.Time) (RangePatchReport, error) {
	rangeSource, ok := s.source.(stockKlineRangeSource)
	if !ok {
		return RangePatchReport{}, errors.New("stock kline range source is unavailable")
	}
	if to.Before(from) {
		return RangePatchReport{}, errors.New("range patch: to date precedes from date")
	}
	report := RangePatchReport{}
	days := map[string]*dayRangeState{}
	var dayOrder []string
	missingTotal := 0
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		missing, err := s.store.MissingStockKlineCodes(ctx, date)
		if err != nil {
			// Days without an archive (weekends, holidays, pre-history) have
			// nothing to patch.
			continue
		}
		if len(missing) == 0 {
			continue
		}
		records, err := s.store.DailyCloseStocks(ctx, date, missing)
		if err != nil {
			return report, fmt.Errorf("range patch %s: resolve daily-close candidates: %w", date, err)
		}
		if len(records) != len(missing) {
			return report, fmt.Errorf("range patch %s: incomplete persisted stock close candidates: expected %d rows, got %d", date, len(missing), len(records))
		}
		state := &dayRangeState{date: day, pending: make(map[string]graymarket.RankRecord, len(records))}
		for _, record := range records {
			if !record.QuoteAvailable {
				return report, fmt.Errorf("range patch %s: persisted stock close candidate %s has no daily bar", date, record.Code)
			}
			state.pending[record.Code] = record
		}
		days[date] = state
		dayOrder = append(dayOrder, date)
		missingTotal += len(records)
		report.Dates = append(report.Dates, date)
	}
	if missingTotal == 0 {
		return report, nil
	}
	// Union of per-day missing records: each stock is fetched exactly once for
	// the whole range. Keyed by code; a code present on several days keeps the
	// first record (market is identical across days).
	union := make(map[string]graymarket.RankRecord, missingTotal)
	for _, state := range days {
		for code, record := range state.pending {
			if _, seen := union[code]; !seen {
				union[code] = record
			}
		}
	}
	report.Stocks = len(union)
	runID := newRunID()
	startedAt := time.Now().UTC()
	run := repository.CollectionRun{RunID: runID,
		SnapshotAt: from, SnapshotKind: graymarket.SnapshotStockKline, RankType: graymarket.RankStock,
		Status: repository.RunRunning, RequestedDate: from.Format("2006-01-02"), AttemptCount: 1, StartedAt: startedAt}
	if err := s.store.StartRun(ctx, run); err != nil {
		return report, err
	}
	flush := func(state *dayRangeState) error {
		if len(state.buffer) == 0 {
			return nil
		}
		if err := s.store.SaveStockKlines(ctx, runID, state.buffer); err != nil {
			return fmt.Errorf("range patch %s: persist batch: %w", state.date.Format("2006-01-02"), err)
		}
		report.RowsWritten += len(state.buffer)
		state.written += len(state.buffer)
		state.buffer = state.buffer[:0]
		return nil
	}
	flushDay := func(date string) error {
		state := days[date]
		if state == nil {
			return nil
		}
		if len(state.buffer) >= stockKlinePersistBatchStocks*48 {
			return flush(state)
		}
		return nil
	}
	if err := s.PrepareUpstreamRequests(ctx, "stock-kline", len(union), min(8, len(union))); err != nil {
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "proxy_prewarm_failed", err.Error()
		return report, finishRange(ctx, s, run, &report, days, dayOrder)
	}
	spanDays := int(to.Sub(from).Hours()/24) + 1
	maxBars := spanDays*48 + 64
	if maxBars > 3000 {
		err := fmt.Errorf("range patch span %s..%s exceeds one request budget; split the range",
			from.Format("2006-01-02"), to.Format("2006-01-02"))
		run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "invalid_request", err.Error()
		return report, finishRange(ctx, s, run, &report, days, dayOrder)
	}
	var failedStocks int
	for code, record := range union {
		if ctx.Err() != nil {
			break
		}
		err := rangeSource.FetchStockKlines5mRange(ctx, from, to, record, maxBars,
			func(tradeDate string, points []graymarket.StockKlinePoint) {
				state := days[tradeDate]
				if state == nil {
					return
				}
				if _, needed := state.pending[points[0].Code]; !needed {
					return
				}
				state.buffer = append(state.buffer, points...)
				delete(state.pending, points[0].Code)
				if flushErr := flushDay(tradeDate); flushErr != nil {
					s.logger.Warn("range patch flush failed", "date", tradeDate, "error", flushErr)
				}
			})
		if err != nil {
			failedStocks++
			s.logger.Warn("range patch stock fetch failed", "code", code, "error", err)
		}
	}
	report.FailedStocks = failedStocks
	for _, date := range dayOrder {
		state := days[date]
		if err := flush(state); err != nil {
			run.Status, run.ErrorCode, run.ErrorMessage = repository.RunFailed, "storage_error", err.Error()
			return report, finishRange(ctx, s, run, &report, days, dayOrder)
		}
		if len(state.pending) > 0 {
			report.IncompleteDates = append(report.IncompleteDates, date)
		}
	}
	return report, finishRange(ctx, s, run, &report, days, dayOrder)
}

// finishRange settles the audit run. Every touched day's manifest was
// already refreshed inside SaveStockKlines; the per-date revision seal is the
// daily task's job and is skipped here because the range run spans many dates
// and a run's revision belongs to a single requested date.
func finishRange(ctx context.Context, s *Service, run repository.CollectionRun,
	report *RangePatchReport, days map[string]*dayRangeState, dayOrder []string) error {
	var combined error
	if combined == nil && report.FailedStocks == 0 && len(report.IncompleteDates) == 0 {
		run.Status = repository.RunSuccess
	} else if run.Status == repository.RunRunning {
		run.Status = repository.RunPartial
	}
	finishedAt := time.Now().UTC()
	run.FinishedAt, run.DurationMS = &finishedAt, finishedAt.Sub(run.StartedAt).Milliseconds()
	run.FetchedTotal = report.RowsWritten
	if err := s.store.FinishRun(context.WithoutCancel(ctx), run); err != nil {
		combined = errors.Join(combined, err)
	}
	return combined
}
