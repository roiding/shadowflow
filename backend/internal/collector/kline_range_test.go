package collector

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

type fakeRangeSource struct {
	calls int
	codes []string
}

func (fake *fakeRangeSource) FetchAll(context.Context, graymarket.RankType, string, time.Time) (graymarket.RankSnapshot, error) {
	return graymarket.RankSnapshot{}, nil
}

func (fake *fakeRangeSource) FetchStockKlines5mRange(ctx context.Context, from, to time.Time,
	stock graymarket.RankRecord, maxBars int, emit func(tradeDate string, points []graymarket.StockKlinePoint)) error {
	fake.calls++
	fake.codes = append(fake.codes, stock.Code)
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		tradeDate := day.Format("2006-01-02")
		points := make([]graymarket.StockKlinePoint, 0, 48)
		for index := 0; index < 48; index++ {
			points = append(points, graymarket.StockKlinePoint{
				TradeDate: tradeDate, SnapshotAt: day.Add(time.Duration(39+index*5) * time.Minute),
				Market: stock.Market, Code: stock.Code,
				Source:    graymarket.KlineSourceFiveMinuteBackfill,
				OpenPrice: 10, ClosePrice: 10, HighPrice: 10, LowPrice: 10,
			})
		}
		emit(tradeDate, points)
	}
	return nil
}

func TestCollectStockKlineRangePatchFetchesEachStockOnce(testingT *testing.T) {
	source := &fakeRangeSource{}
	store := &fakeStore{hasDailyClose: true, missingKlines: []string{"300413", "920718"},
		dailyCloseRows: []graymarket.RankRecord{
			{Market: 0, Code: "300413", Name: "甲", QuoteAvailable: true},
			{Market: 0, Code: "920718", Name: "乙", QuoteAvailable: true},
		}}
	service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	report, err := service.CollectStockKlineRangePatch(context.Background(),
		time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		testingT.Fatal(err)
	}
	if source.calls != 2 {
		testingT.Fatalf("expected one fetch per stock, got %d calls", source.calls)
	}
	if report.Stocks != 2 || report.RowsWritten != 192 || len(report.IncompleteDates) != 0 {
		testingT.Fatalf("unexpected report: %+v", report)
	}
	if store.savedKlineRows != 192 {
		testingT.Fatalf("expected 192 persisted rows, got %d", store.savedKlineRows)
	}
	if len(store.finished) != 1 || store.finished[0].Status != repository.RunSuccess {
		testingT.Fatalf("range run was not settled as success: %+v", store.finished)
	}
}

func (s *fakeStore) CountBoardDailyCloseRows(ctx context.Context, tradeDate string) (map[graymarket.RankType]int, error) {
	return map[graymarket.RankType]int{graymarket.RankIndustry: 0, graymarket.RankConcept: 0}, nil
}

func (s *fakeStore) RefreshArchiveManifestDay(ctx context.Context, tradeDate string) error {
	return nil
}

type fakeBoardKlineSource struct {
	calls  int
	boards []string
}

func (fake *fakeBoardKlineSource) FetchBoardCatalog(ctx context.Context, boardType graymarket.BoardType) ([]graymarket.Board, error) {
	return []graymarket.Board{{Code: "BK0433", Name: "农林牧渔", Type: boardType, SourceRank: 0}}, nil
}

func (fake *fakeBoardKlineSource) FetchBoardConstituents(ctx context.Context, board graymarket.Board) ([]graymarket.StockBoardRelation, error) {
	return nil, nil
}

func (fake *fakeBoardKlineSource) FetchBoardDailyKlinesRange(ctx context.Context, from, to time.Time,
	board graymarket.Board, emit func(tradeDate string, bar BoardDailyBar)) error {
	fake.calls++
	fake.boards = append(fake.boards, string(board.Type)+":"+board.Code)
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		tradeDate := day.Format("2006-01-02")
		emit(tradeDate, BoardDailyBar{TradeDate: tradeDate, Open: 10, Close: 11, High: 12, Low: 9,
			Volume: 1000, Amount: 100000, ChangeValue: 1, ChangePct: 10, Amplitude: 30, TurnoverRate: 1})
	}
	return nil
}

type boardSourceAdapter struct {
	*fakeRangeSource
	*fakeBoardKlineSource
}

func TestCollectBoardDailyCloseBackfillFillsMissingDimensions(t *testing.T) {
	store := &fakeStore{hasDailyClose: true, missingKlines: []string{}}
	source := &boardSourceAdapter{fakeRangeSource: &fakeRangeSource{}, fakeBoardKlineSource: &fakeBoardKlineSource{}}
	service := New(source, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	report, err := service.CollectBoardDailyCloseBackfill(context.Background(),
		time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if source.fakeBoardKlineSource.calls != 2 {
		t.Fatalf("expected two board fetches (industry+concept), got %d", source.fakeBoardKlineSource.calls)
	}
	if report.RowsWritten != 2 || len(report.RemainingGaps) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(store.finished) != 1 || store.finished[0].Status != repository.RunSuccess {
		t.Fatalf("run was not settled as success: %+v", store.finished)
	}
}
