package collector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/roiding/shadowflow/internal/datasource/eastmoney"
	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

// shanghaiZone is the archive location for board snapshot timestamps.
var shanghaiZone = time.FixedZone("Asia/Shanghai", 8*3600)

// boardDailyCloseSource fetches one board's daily kline bars over an
// inclusive date range in a single upstream request (the operator-run
// backfill path for board identity snapshots).
type boardDailyCloseSource interface {
	FetchBoardDailyKlinesRange(ctx context.Context, from, to time.Time,
		board graymarket.Board, emit func(tradeDate string, bar eastmoney.BoardDailyBar)) error
}

// BoardBackfillReport summarizes one operator-run board identity patch.
type BoardBackfillReport struct {
	Days          int      `json:"days"`
	Boards        int      `json:"boards"`
	RowsWritten   int      `json:"rows_written"`
	FailedBoards  int      `json:"failed_boards"`
	RemainingGaps []string `json:"remaining_gaps"`
}

// CollectBoardDailyCloseBackfill reconstructs missing industry and concept
// daily-close identity snapshots in [from,to] by fetching each board's daily
// kline history ONCE. The identity rows carry the board's real close, open,
// high, low and change from the upstream daily bars. The 15:00 money
// dimension (dark_money, rank) is zeroed: the upstream no longer serves
// historical board money ticks, so backfilled days report the money gap
// honestly instead of fabricating it.
func (s *Service) CollectBoardDailyCloseBackfill(ctx context.Context, from, to time.Time) (BoardBackfillReport, error) {
	report := BoardBackfillReport{}
	source, ok := s.source.(boardDailyCloseSource)
	if !ok {
		return report, errors.New("board daily close backfill source is unavailable")
	}
	if s.relationSource == nil {
		return report, errors.New("board catalog source is unavailable")
	}
	if to.Before(from) {
		return report, errors.New("board backfill: to date precedes from date")
	}
	type dayTypeKey struct {
		date     string
		rankType graymarket.RankType
	}
	pending := map[dayTypeKey]bool{}
	var dates []time.Time
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		present, err := s.store.CountBoardDailyCloseRows(ctx, date)
		if err != nil {
			return report, fmt.Errorf("board backfill %s: %w", date, err)
		}
		dates = append(dates, day)
		for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
			if present[rankType] == 0 {
				pending[dayTypeKey{date, rankType}] = true
			}
		}
	}
	if len(pending) == 0 {
		return report, nil
	}
	report.Days = len(dates)
	catalog := map[graymarket.RankType][]graymarket.Board{}
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		boards, err := s.relationSource.FetchBoardCatalog(ctx, boardCatalogType(rankType))
		if err != nil {
			return report, fmt.Errorf("board backfill: fetch %s catalog: %w", rankType, err)
		}
		catalog[rankType] = boards
		report.Boards += len(boards)
	}
	runID := newRunID()
	startedAt := time.Now().UTC()
	run := repository.CollectionRun{RunID: runID,
		SnapshotAt: from, SnapshotKind: graymarket.SnapshotDailyClose, RankType: graymarket.RankIndustry,
		Status: repository.RunRunning, RequestedDate: from.Format("2006-01-02"), AttemptCount: 1, StartedAt: startedAt}
	if err := s.store.StartRun(ctx, run); err != nil {
		return report, err
	}
	settle := func(resultErr error) error {
		finishedAt := time.Now().UTC()
		run.FinishedAt, run.DurationMS = &finishedAt, finishedAt.Sub(startedAt).Milliseconds()
		if resultErr == nil && report.FailedBoards == 0 {
			run.Status = repository.RunSuccess
		} else if run.Status == repository.RunRunning {
			run.Status = repository.RunPartial
		}
		if err := s.store.FinishRun(context.WithoutCancel(ctx), run); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
		return resultErr
	}
	type bufferKey struct {
		date     string
		rankType graymarket.RankType
	}
	buffers := map[bufferKey][]graymarket.RankRecord{}
	var failedBoards int
	fetchOne := func(rankType graymarket.RankType, board graymarket.Board) {
		if ctx.Err() != nil {
			return
		}
		needed := false
		for _, day := range dates {
			if pending[dayTypeKey{day.Format("2006-01-02"), rankType}] {
				needed = true
				break
			}
		}
		if !needed {
			return
		}
		err := source.FetchBoardDailyKlinesRange(ctx, from, to, board,
			func(tradeDate string, bar eastmoney.BoardDailyBar) {
				key := dayTypeKey{tradeDate, rankType}
				if !pending[key] {
					return
				}
				at := barTime(tradeDate)
				record := graymarket.RankRecord{
					TradeDate: tradeDate, SnapshotAt: at, RankType: rankType,
					Rank: int64(board.SourceRank + 1), Market: 90, Code: board.Code, Name: board.Name,
					QuoteTime:      tradeDate + " 15:00:00",
					LatestPriceRaw: int64(bar.Close * 10000),
					OpenPrice:      bar.Open, HighPrice: bar.High, LowPrice: bar.Low, ClosePrice: bar.Close,
					PreviousClose: bar.Close - bar.ChangeValue, ChangeValue: bar.ChangeValue, ChangePct: bar.ChangePct,
					Volume: int64(bar.Volume), Turnover: int64(bar.Amount), TurnoverRate: bar.TurnoverRate,
					Amplitude: bar.Amplitude, QuoteAvailable: true, SourceVersion: 1,
					FetchedAt: time.Now().UTC(),
				}
				buffers[bufferKey{tradeDate, rankType}] = append(buffers[bufferKey{tradeDate, rankType}], record)
			})
		if err != nil {
			failedBoards++
			s.logger.Warn("board backfill fetch failed", "board", board.Code, "error", err)
		}
	}
	for _, board := range catalog[graymarket.RankIndustry] {
		fetchOne(graymarket.RankIndustry, board)
	}
	for _, board := range catalog[graymarket.RankConcept] {
		fetchOne(graymarket.RankConcept, board)
	}
	for _, day := range dates {
		date := day.Format("2006-01-02")
		snapshotAt := time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, shanghaiZone)
		for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
			key := bufferKey{date, rankType}
			if !pending[dayTypeKey{date, rankType}] {
				continue
			}
			records := buffers[key]
			if len(records) == 0 {
				report.RemainingGaps = append(report.RemainingGaps, date+" "+string(rankType)+": upstream returned no bars")
				continue
			}
			snapshot := graymarket.RankSnapshot{TradeDate: date, RankType: rankType,
				SnapshotAt: snapshotAt, Records: records}
			if err := s.store.SaveDailyClose(ctx, runID, snapshot); err != nil {
				return report, settle(fmt.Errorf("board backfill %s %s: persist: %w", date, rankType, err))
			}
			report.RowsWritten += len(records)
			delete(pending, dayTypeKey{date, rankType})
			if err := s.store.RefreshArchiveManifestDay(ctx, date); err != nil {
				return report, settle(fmt.Errorf("board backfill %s: refresh manifest: %w", date, err))
			}
		}
	}
	report.FailedBoards = failedBoards
	for key := range pending {
		report.RemainingGaps = append(report.RemainingGaps, key.date+" "+string(key.rankType)+": fetch failed or no bars")
	}
	return report, settle(nil)
}

func boardCatalogType(rankType graymarket.RankType) graymarket.BoardType {
	if rankType == graymarket.RankConcept {
		return graymarket.BoardConcept
	}
	return graymarket.BoardIndustry
}

func barTime(tradeDate string) time.Time {
	at, _ := time.ParseInLocation("2006-01-02 15:04", tradeDate+" 15:00", shanghaiZone)
	return at
}
