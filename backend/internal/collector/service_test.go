package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

type sourceResult struct {
	snapshot graymarket.RankSnapshot
	err      error
}

type fakeSource struct {
	mu               sync.Mutex
	results          []sourceResult
	calls            int
	quoteErr         error
	unavailableQuote map[string]bool
	klineErr         error
	klineLimit       int
	klineBatches     [][]string
}

func (s *fakeSource) FetchMoney5m(_ context.Context, snapshot graymarket.RankSnapshot, includeClose bool) ([]graymarket.MoneyPoint, error) {
	pointsPerCode := 47
	if includeClose {
		pointsPerCode = 48
	}
	points := make([]graymarket.MoneyPoint, 0, len(snapshot.Records)*pointsPerCode)
	for _, record := range snapshot.Records {
		for _, session := range []struct{ start, end int }{{9*60 + 35, 11*60 + 30}, {13*60 + 5, 14*60 + 55}} {
			for minute := session.start; minute <= session.end; minute += 5 {
				at := time.Date(snapshot.SnapshotAt.Year(), snapshot.SnapshotAt.Month(), snapshot.SnapshotAt.Day(), minute/60, minute%60, 0, 0, snapshot.SnapshotAt.Location())
				points = append(points, graymarket.MoneyPoint{TradeDate: snapshot.TradeDate, SnapshotAt: at, RankType: record.RankType,
					Rank: record.Rank, Market: record.Market, Code: record.Code, Name: record.Name, DarkMoney: record.DarkMoney,
					RegularMoney: record.RegularMoney, MainMoneyInflow: record.MainMoneyInflow, FetchedAt: at})
			}
		}
		if includeClose {
			at := time.Date(snapshot.SnapshotAt.Year(), snapshot.SnapshotAt.Month(), snapshot.SnapshotAt.Day(), 15, 0, 0, 0, snapshot.SnapshotAt.Location())
			points = append(points, graymarket.MoneyPoint{TradeDate: snapshot.TradeDate, SnapshotAt: at, RankType: record.RankType,
				Rank: record.Rank, Market: record.Market, Code: record.Code, Name: record.Name, DarkMoney: record.DarkMoney,
				RegularMoney: record.RegularMoney, MainMoneyInflow: record.MainMoneyInflow, FetchedAt: at})
		}
	}
	return points, nil
}

func (s *fakeSource) FetchStockKlines5m(_ context.Context, snapshot graymarket.RankSnapshot) ([]graymarket.StockKlinePoint, error) {
	s.mu.Lock()
	codes := make([]string, 0, len(snapshot.Records))
	for _, record := range snapshot.Records {
		codes = append(codes, record.Code)
	}
	s.klineBatches = append(s.klineBatches, codes)
	limit := len(snapshot.Records)
	if s.klineLimit > 0 && s.klineLimit < limit {
		limit = s.klineLimit
	}
	err := s.klineErr
	s.mu.Unlock()
	points := make([]graymarket.StockKlinePoint, 0, limit*48)
	for _, record := range snapshot.Records[:limit] {
		for _, session := range []struct{ start, end int }{{9*60 + 35, 11*60 + 30}, {13*60 + 5, 15 * 60}} {
			for minute := session.start; minute <= session.end; minute += 5 {
				at := time.Date(snapshot.SnapshotAt.Year(), snapshot.SnapshotAt.Month(), snapshot.SnapshotAt.Day(), minute/60, minute%60, 0, 0, snapshot.SnapshotAt.Location())
				points = append(points, graymarket.StockKlinePoint{TradeDate: snapshot.TradeDate, SnapshotAt: at, Market: record.Market, Code: record.Code, ClosePrice: 10, FetchedAt: at})
			}
		}
	}
	return points, err
}

func (s *fakeSource) FetchAll(context.Context, graymarket.RankType, string, time.Time) (graymarket.RankSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return result.snapshot, result.err
}

func (s *fakeSource) FetchStockQuotes(_ context.Context, relations []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	if s.quoteErr != nil {
		return nil, s.quoteErr
	}
	quotes := make([]graymarket.StockQuote, 0, len(relations))
	for _, relation := range relations {
		available := !s.unavailableQuote[relation.StockCode]
		quotes = append(quotes, graymarket.StockQuote{StockCode: relation.StockCode, StockMarket: relation.StockMarket,
			StockName: relation.StockName, LatestPrice: 10, OpenPrice: 9.5, HighPrice: 10.5, LowPrice: 9.25,
			PreviousClose: 9, ChangePct: 0.1111, ChangeValue: 1, Volume: 100, Turnover: 1000,
			TurnoverRate: 0.02, Amplitude: 0.1, QuoteTime: "2026-08-14T07:00:00Z", Available: available})
	}
	return quotes, nil
}

func (s *fakeSource) FetchBoardQuotes(_ context.Context, rankType graymarket.RankType) ([]graymarket.BoardQuote, error) {
	code := "BK001"
	return []graymarket.BoardQuote{{BoardCode: code, BoardMarket: 90, BoardName: string(rankType), LatestPrice: 10,
		OpenPrice: 9.5, HighPrice: 10.5, LowPrice: 9.25, PreviousClose: 9, ChangePct: 0.1111,
		ChangeValue: 1, Volume: 100, Turnover: 1000, TurnoverRate: 0.02, Amplitude: 0.1,
		QuoteTime: "2026-08-14T07:00:00Z", Available: true}}, nil
}

type fakeStore struct {
	mu              sync.Mutex
	started         []repository.CollectionRun
	finished        []repository.CollectionRun
	savedIntraday   int
	savedDailyClose int
	savedArchives   int
	savedKlineRows  int
	lastDailyClose  graymarket.RankSnapshot
	lastBoardClose  graymarket.RankSnapshot
	startErr        error
	finishErr       error
	saveErr         error
	hasDailyClose   bool
	hasCloseErr     error
	quality         []repository.QualitySummary
	compactErr      error
	missingKlines   []string
	dailyCloseRows  []graymarket.RankRecord
}

func (s *fakeStore) SaveIntraday(_ context.Context, _ string, _ graymarket.RankSnapshot, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedIntraday++
	return s.saveErr
}

func (s *fakeStore) SaveDailyClose(_ context.Context, _ string, snapshot graymarket.RankSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedDailyClose++
	s.lastDailyClose = snapshot
	return s.saveErr
}

func (s *fakeStore) SaveBoardArchive(_ context.Context, _ string, snapshot graymarket.RankSnapshot, _ []graymarket.MoneyPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedArchives++
	s.lastBoardClose = snapshot
	return s.saveErr
}

func (s *fakeStore) SaveStockArchive(_ context.Context, _ string, _ graymarket.RankSnapshot, _ []graymarket.MoneyPoint) error {
	s.savedArchives++
	return s.saveErr
}

func (s *fakeStore) SaveStockKlines(_ context.Context, _ string, points []graymarket.StockKlinePoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.savedKlineRows += len(points)
	savedCodes := make(map[string]struct{})
	for _, point := range points {
		savedCodes[point.Code] = struct{}{}
	}
	remaining := s.missingKlines[:0]
	for _, code := range s.missingKlines {
		if _, saved := savedCodes[code]; !saved {
			remaining = append(remaining, code)
		}
	}
	s.missingKlines = remaining
	return nil
}

func (s *fakeStore) MissingStockKlineCodes(context.Context, string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.missingKlines...), nil
}

func (s *fakeStore) DailyCloseStocks(_ context.Context, _ string, codes []string) ([]graymarket.RankRecord, error) {
	wanted := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		wanted[code] = struct{}{}
	}
	result := make([]graymarket.RankRecord, 0, len(codes))
	for _, record := range s.dailyCloseRows {
		if _, ok := wanted[record.Code]; ok {
			result = append(result, record)
		}
	}
	return result, nil
}

func (s *fakeStore) CompactResearch(context.Context, string) ([]repository.QualitySummary, error) {
	return s.quality, s.compactErr
}

func (s *fakeStore) HasDailyClose(context.Context, string) (bool, error) {
	return s.hasDailyClose, s.hasCloseErr
}

func (s *fakeStore) HasEndOfDayArchive(context.Context, string) (bool, error) {
	return s.hasDailyClose, s.hasCloseErr
}

func (s *fakeStore) HasStockKlineArchive(context.Context, string) (bool, error) {
	return len(s.missingKlines) == 0, s.hasCloseErr
}

func (s *fakeStore) CleanupArchivedIntraday(context.Context, string) error { return nil }

func (s *fakeStore) Maintain(context.Context, time.Time, int, int) (repository.MaintenanceResult, error) {
	return repository.MaintenanceResult{}, nil
}

func (s *fakeStore) SealArchiveRevision(_ context.Context, tradeDate, revisionID string) (repository.ArchiveRevision, error) {
	return repository.ArchiveRevision{TradeDate: tradeDate, RevisionID: revisionID}, nil
}

func (s *fakeStore) StartRun(_ context.Context, run repository.CollectionRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, run)
	return s.startErr
}

func (s *fakeStore) FinishRun(_ context.Context, run repository.CollectionRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, run)
	return s.finishErr
}

func newTestService(source Source, store store) *Service {
	service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.retryGap = 0
	return service
}

func successfulSnapshot(at time.Time) graymarket.RankSnapshot {
	tradeDate := at.Format("2006-01-02")
	return graymarket.RankSnapshot{
		TradeDate:     tradeDate,
		RankType:      graymarket.RankIndustry,
		SnapshotAt:    at,
		ExpectedTotal: 1,
		Records: []graymarket.RankRecord{{
			TradeDate:    tradeDate,
			SnapshotAt:   at,
			RankType:     graymarket.RankIndustry,
			Rank:         1,
			Code:         "BK001",
			Name:         "board",
			DarkMoney:    123,
			DarkActivity: 0.123,
		}},
		RawPages: []graymarket.RawPage{{Page: 1}},
	}
}

func TestCollectRetriesThenPersistsSuccessfulRun(t *testing.T) {
	at := time.Date(2026, 8, 14, 10, 5, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	upstreamErr := errors.New("temporary upstream failure")
	source := &fakeSource{results: []sourceResult{{err: upstreamErr}, {snapshot: successfulSnapshot(at)}}}
	store := &fakeStore{}
	service := newTestService(source, store)

	if err := service.collect(context.Background(), graymarket.RankIndustry, graymarket.SnapshotMinuteWork, "20260814", at); err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 {
		t.Fatalf("expected 2 fetch attempts, got %d", source.calls)
	}
	if store.savedIntraday != 1 || store.savedDailyClose != 0 {
		t.Fatalf("unexpected saves: intraday=%d daily_close=%d", store.savedIntraday, store.savedDailyClose)
	}
	if len(store.finished) != 1 {
		t.Fatalf("expected one finished run, got %d", len(store.finished))
	}
	run := store.finished[0]
	if run.Status != repository.RunSuccess || run.AttemptCount != 2 || run.ActualTradeDate != "2026-08-14" {
		t.Fatalf("unexpected finished run: %+v", run)
	}
	if run.ExpectedTotal != 1 || run.FetchedTotal != 1 || run.PageCount != 1 || run.FinishedAt == nil {
		t.Fatalf("missing run metrics: %+v", run)
	}
}

func TestCollectDoesNotRetryNoData(t *testing.T) {
	at := time.Date(2026, 8, 14, 10, 5, 0, 0, time.UTC)
	source := &fakeSource{results: []sourceResult{{err: graymarket.ErrNoData}}}
	store := &fakeStore{}
	service := newTestService(source, store)

	err := service.collect(context.Background(), graymarket.RankConcept, graymarket.SnapshotMinuteWork, "20260814", at)
	if !errors.Is(err, graymarket.ErrNoData) {
		t.Fatalf("expected ErrNoData, got %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("no-data response should not retry, got %d calls", source.calls)
	}
	if len(store.finished) != 1 || store.finished[0].Status != repository.RunFailed || store.finished[0].ErrorCode != "no_data" {
		t.Fatalf("unexpected failed run: %+v", store.finished)
	}
}

func TestCollectRejectsDateMismatchBeforeSaving(t *testing.T) {
	at := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	snapshot := successfulSnapshot(at)
	snapshot.TradeDate = "2026-08-13"
	source := &fakeSource{results: []sourceResult{{snapshot: snapshot}}}
	store := &fakeStore{}
	service := newTestService(source, store)

	err := service.collect(context.Background(), graymarket.RankStock, graymarket.SnapshotDailyClose, "20260814", at)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected date mismatch, got %v", err)
	}
	if store.savedIntraday != 0 || store.savedDailyClose != 0 {
		t.Fatalf("mismatched snapshot must not be saved")
	}
	if len(store.finished) != 1 || store.finished[0].ErrorCode != "date_mismatch" || store.finished[0].ActualTradeDate != "2026-08-13" {
		t.Fatalf("unexpected failed run: %+v", store.finished)
	}
}

func TestCollectPropagatesFinishRunFailure(t *testing.T) {
	at := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	finishErr := errors.New("database is locked")
	source := &fakeSource{results: []sourceResult{{snapshot: successfulSnapshot(at)}}}
	store := &fakeStore{finishErr: finishErr}
	service := newTestService(source, store)

	err := service.collect(context.Background(), graymarket.RankStock, graymarket.SnapshotDailyClose, "20260814", at)
	if !errors.Is(err, finishErr) || !strings.Contains(err.Error(), "finish collection run") {
		t.Fatalf("expected finish error to be returned, got %v", err)
	}
	if store.savedDailyClose != 1 {
		t.Fatalf("snapshot should be saved before finish failure, got %d saves", store.savedDailyClose)
	}
	if len(store.lastDailyClose.Records) != 1 {
		t.Fatalf("daily close snapshot was not retained by test store: %+v", store.lastDailyClose)
	}
	record := store.lastDailyClose.Records[0]
	if record.OpenPrice != 9.5 || record.HighPrice != 10.5 || record.LowPrice != 9.25 || record.ClosePrice != 10 || record.PreviousClose != 9 || record.Turnover != 1000 || record.TurnoverRate != 0.02 || !record.QuoteAvailable {
		t.Fatalf("daily OHLC quote fields were not enriched: %+v", record)
	}
	if record.DarkActivity != 0.123 {
		t.Fatalf("original dark activity must be preserved, got %f", record.DarkActivity)
	}
	if len(store.finished) != 1 || store.finished[0].Status != repository.RunSuccess {
		t.Fatalf("unexpected finished run: %+v", store.finished)
	}
}

func TestCollectDailyCloseFailsWhenQuoteEnrichmentFails(t *testing.T) {
	at := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	quoteErr := errors.New("quote unavailable")
	source := &fakeSource{results: []sourceResult{{snapshot: successfulSnapshot(at)}}, quoteErr: quoteErr}
	store := &fakeStore{}

	err := newTestService(source, store).collect(context.Background(), graymarket.RankStock, graymarket.SnapshotDailyClose, "20260814", at)
	if !errors.Is(err, quoteErr) || store.savedDailyClose != 0 {
		t.Fatalf("incomplete OHLC snapshot must not be saved: err=%v saves=%d", err, store.savedDailyClose)
	}
	if len(store.finished) != 1 || store.finished[0].ErrorCode != "quote_enrichment" {
		t.Fatalf("unexpected failed run: %+v", store.finished)
	}
}

func TestCollectBoardArchivesPersistTurnoverAndTurnoverRate(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, rankType := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept} {
		t.Run(string(rankType), func(t *testing.T) {
			closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
			runAt := time.Date(2026, 8, 14, 16, 0, 0, 0, location)
			snapshot := successfulSnapshot(closeAt)
			snapshot.RankType = rankType
			snapshot.Records[0].RankType = rankType
			snapshot.Records[0].Market = 90
			snapshot.Records[0].Code = "BK001"
			source := &fakeSource{results: []sourceResult{{snapshot: snapshot}}}
			store := &fakeStore{}

			if err := newTestService(source, store).collectBoardArchive(context.Background(), rankType, "20260814", closeAt, runAt); err != nil {
				t.Fatal(err)
			}
			if store.savedArchives != 1 || len(store.lastBoardClose.Records) != 1 {
				t.Fatalf("board archive was not saved: %+v", store.lastBoardClose)
			}
			record := store.lastBoardClose.Records[0]
			if record.Turnover != 1000 || record.TurnoverRate != 0.02 || !record.QuoteAvailable || record.ClosePrice != 10 {
				t.Fatalf("board close quote was not enriched: %+v", record)
			}
		})
	}
}

func TestMergeBoardArchiveUniverseUsesFullCatalog(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	at := time.Date(2026, 8, 18, 15, 0, 0, 0, location)
	dark := graymarket.RankSnapshot{
		TradeDate: "2026-08-18", RankType: graymarket.RankConcept, SnapshotAt: at,
		Records: []graymarket.RankRecord{{TradeDate: "2026-08-18", SnapshotAt: at, RankType: graymarket.RankConcept,
			Rank: 1, Market: 90, Code: "BK0001", Name: "榜内概念", DarkMoney: 11}},
	}
	merged, err := mergeBoardArchiveUniverse(dark, []graymarket.Board{
		{Code: "BK0001", Name: "榜内概念", Type: graymarket.BoardConcept},
		{Code: "BK1013", Name: "华为欧拉", Type: graymarket.BoardConcept},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Records) != 2 {
		t.Fatalf("expected full catalog records, got %d", len(merged.Records))
	}
	if merged.Records[1].Code != "BK1013" || merged.Records[1].Name != "华为欧拉" {
		t.Fatalf("catalog-only board was not retained: %+v", merged.Records[1])
	}
	if merged.Records[1].DarkMoney != 0 || merged.Records[1].Market != 90 {
		t.Fatalf("catalog-only board should retain no fabricated dark rank fields: %+v", merged.Records[1])
	}
}

func TestMergeStockArchiveUniverseUsesFullMarketQuotes(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	at := time.Date(2026, 8, 18, 15, 0, 0, 0, location)
	dark := graymarket.RankSnapshot{TradeDate: "2026-08-18", RankType: graymarket.RankStock, SnapshotAt: at,
		Records: []graymarket.RankRecord{{TradeDate: "2026-08-18", SnapshotAt: at, RankType: graymarket.RankStock, Rank: 1, Market: 1, Code: "600001", Name: "榜内", DarkMoney: 8}}}
	merged, err := mergeStockArchiveUniverse(dark, []graymarket.StockQuote{{StockCode: "600001", StockMarket: 1, StockName: "榜内"}, {StockCode: "688836", StockMarket: 1, StockName: "榜外"}}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Records) != 2 || merged.Records[1].Code != "688836" || merged.Records[1].Name != "榜外" {
		t.Fatalf("full-market stock was not retained: %+v", merged.Records)
	}
}

func TestCollectStockKlinesUsesPersistedEligibleStocks(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	runAt := time.Date(2026, 8, 14, 16, 15, 0, 0, location)
	records := []graymarket.RankRecord{
		{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 0, Code: "000001", Name: "交易股", QuoteAvailable: true},
		{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: "600001", Name: "停牌股"},
	}
	source := &fakeSource{}
	store := &fakeStore{missingKlines: []string{"000001"}, dailyCloseRows: records}
	if err := newTestService(source, store).CollectStockKlines(context.Background(), runAt); err != nil {
		t.Fatal(err)
	}
	if store.savedKlineRows != 48 || len(store.finished) != 1 {
		t.Fatalf("unexpected kline save: rows=%d runs=%+v", store.savedKlineRows, store.finished)
	}
	run := store.finished[0]
	if run.Status != repository.RunSuccess || run.ExpectedTotal != 48 || run.FetchedTotal != 48 || run.PageCount != 1 {
		t.Fatalf("kline run counted suspended stock: %+v", run)
	}
}

func TestCollectStockKlinesPersistsPartialBatchAndRetriesOnlyMissingStocks(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	closeAt := time.Date(2026, 8, 14, 15, 0, 0, 0, location)
	runAt := time.Date(2026, 8, 14, 16, 15, 0, 0, location)
	records := []graymarket.RankRecord{
		{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 0, Code: "000001", QuoteAvailable: true},
		{TradeDate: "2026-08-14", SnapshotAt: closeAt, RankType: graymarket.RankStock, Market: 1, Code: "600001", QuoteAvailable: true},
	}
	fetchErr := errors.New("temporary kline failure")
	source := &fakeSource{klineErr: fetchErr, klineLimit: 1}
	store := &fakeStore{missingKlines: []string{"000001", "600001"}, dailyCloseRows: records}
	service := newTestService(source, store)

	if err := service.CollectStockKlines(context.Background(), runAt); !errors.Is(err, fetchErr) {
		t.Fatalf("expected partial fetch error, got %v", err)
	}
	if store.savedKlineRows != 48 || len(store.missingKlines) != 1 || store.missingKlines[0] != "600001" {
		t.Fatalf("partial batch was not retained: rows=%d missing=%v", store.savedKlineRows, store.missingKlines)
	}
	if len(store.finished) != 1 || store.finished[0].Status != repository.RunPartial {
		t.Fatalf("partial run was not recorded: %+v", store.finished)
	}

	source.klineErr = nil
	source.klineLimit = 0
	if err := service.CollectStockKlines(context.Background(), runAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if store.savedKlineRows != 96 || len(store.missingKlines) != 0 {
		t.Fatalf("retry did not complete archive: rows=%d missing=%v", store.savedKlineRows, store.missingKlines)
	}
	if len(source.klineBatches) != 2 || len(source.klineBatches[1]) != 1 || source.klineBatches[1][0] != "600001" {
		t.Fatalf("retry fetched already archived stocks: batches=%v", source.klineBatches)
	}
}
