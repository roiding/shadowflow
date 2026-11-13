package marketdata

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type testQuoteProvider struct {
	mu     sync.Mutex
	calls  int
	quotes []graymarket.StockQuote
	err    error
}

func (p *testQuoteProvider) FetchStockQuotes(context.Context, []graymarket.StockBoardRelation) ([]graymarket.StockQuote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.quotes, p.err
}

type testKlineProvider struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (p *testKlineProvider) FetchStockKline5m(_ context.Context, stock graymarket.RankRecord) ([]graymarket.StockKlinePoint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	points := make([]graymarket.StockKlinePoint, 48)
	for index := range points {
		points[index] = graymarket.StockKlinePoint{TradeDate: stock.TradeDate, Code: stock.Code, Market: stock.Market,
			SnapshotAt: stock.SnapshotAt, Source: graymarket.KlineSourceFiveMinute}
	}
	return points, nil
}

type testEastmoneyProvider struct {
	*testQuoteProvider
	*testKlineProvider
	darkCalls    int
	preparations int
}

func (p *testEastmoneyProvider) PrepareUpstreamRequests(context.Context, string, int, int) error {
	p.preparations++
	return nil
}

type testBackupProvider struct {
	*testQuoteProvider
	*testKlineProvider
}

func (p *testEastmoneyProvider) FetchLatestMoney5m(context.Context, string, []graymarket.RankRecord, time.Time) ([]graymarket.MoneyPoint, error) {
	p.darkCalls++
	return []graymarket.MoneyPoint{{Code: "600001"}}, nil
}

func (p *testEastmoneyProvider) FetchBoardQuotes(context.Context, graymarket.RankType) ([]graymarket.BoardQuote, error) {
	return nil, nil
}

func (p *testEastmoneyProvider) FetchStockKlines5mRange(context.Context, time.Time, time.Time,
	graymarket.RankRecord, int, func(string, []graymarket.StockKlinePoint)) error {
	return nil
}

func TestQuoteServiceSwitchesToBackupAndCoolsPrimary(t *testing.T) {
	primary := &testQuoteProvider{err: errors.New("blocked")}
	backup := &testQuoteProvider{quotes: []graymarket.StockQuote{{StockCode: "000001", LatestPrice: 10, Available: true}}}
	set, err := NewProviderSet(&testEastmoneyProvider{testQuoteProvider: primary, testKlineProvider: &testKlineProvider{}},
		&testBackupProvider{testQuoteProvider: backup, testKlineProvider: &testKlineProvider{}},
		Options{Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	relations := []graymarket.StockBoardRelation{{StockCode: "000001", StockMarket: 0}}
	for range 2 {
		quotes, err := set.Quotes.FetchStockQuotes(context.Background(), relations)
		if err != nil || len(quotes) != 1 || quotes[0].LatestPrice != 10 {
			t.Fatalf("backup quote failed: %+v %v", quotes, err)
		}
	}
	if primary.calls != 1 || backup.calls != 2 {
		t.Fatalf("primary was not cooled: primary=%d backup=%d", primary.calls, backup.calls)
	}
}

func TestStockKlineServiceFallsBackPerArchiveAfterPrimaryCooling(t *testing.T) {
	primary := &testKlineProvider{err: errors.New("blocked")}
	backup := &testKlineProvider{}
	set, err := NewProviderSet(&testEastmoneyProvider{testQuoteProvider: &testQuoteProvider{}, testKlineProvider: primary},
		&testBackupProvider{testQuoteProvider: &testQuoteProvider{}, testKlineProvider: backup},
		Options{Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-11-13", RankType: graymarket.RankStock,
		SnapshotAt: time.Date(2026, 11, 13, 15, 0, 0, 0, time.Local)}
	for index := 0; index < 8; index++ {
		snapshot.Records = append(snapshot.Records, graymarket.RankRecord{
			TradeDate: snapshot.TradeDate, Market: 0, Code: fmt.Sprintf("%06d", index+1),
		})
	}
	count, err := set.Klines.FetchStockKlines5mIncremental(context.Background(), snapshot, func([]graymarket.StockKlinePoint) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 8 || primary.calls != 1 || backup.calls != 8 {
		t.Fatalf("unexpected fallback counts: completed=%d primary=%d backup=%d", count, primary.calls, backup.calls)
	}
}

func TestDailyCloseQuotesDoNotUseUndatedBackup(t *testing.T) {
	primary := &testQuoteProvider{err: errors.New("eastmoney blocked")}
	backup := &testQuoteProvider{quotes: []graymarket.StockQuote{{StockCode: "000001", LatestPrice: 10, Available: true}}}
	set, err := NewProviderSet(&testEastmoneyProvider{testQuoteProvider: primary, testKlineProvider: &testKlineProvider{}},
		&testBackupProvider{testQuoteProvider: backup, testKlineProvider: &testKlineProvider{}},
		Options{Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	relations := []graymarket.StockBoardRelation{{StockCode: "000001", StockMarket: 0}}
	if _, err := set.DailyCloseQuotes.FetchStockQuotes(context.Background(), relations); err == nil {
		t.Fatal("daily close archive must not accept a provider without a verified quote date")
	}
	if backup.calls != 0 {
		t.Fatalf("undated backup was used for the daily close: %d", backup.calls)
	}
}

func TestPrepareUpstreamSkipsCoolingPrimary(t *testing.T) {
	primary := &testEastmoneyProvider{testQuoteProvider: &testQuoteProvider{err: errors.New("blocked")},
		testKlineProvider: &testKlineProvider{err: errors.New("blocked")}}
	set, err := NewProviderSet(primary,
		&testBackupProvider{testQuoteProvider: &testQuoteProvider{}, testKlineProvider: &testKlineProvider{}},
		Options{Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.PrepareUpstreamRequests(context.Background(), "stock-kline", 10, 4); err != nil {
		t.Fatal(err)
	}
	if primary.preparations != 1 {
		t.Fatalf("healthy primary should be prepared: %d", primary.preparations)
	}
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-11-13", RankType: graymarket.RankStock,
		SnapshotAt: time.Date(2026, 11, 13, 15, 0, 0, 0, time.Local),
		Records:    []graymarket.RankRecord{{TradeDate: "2026-11-13", Code: "000001"}}}
	if _, err := set.Klines.FetchStockKlines5mIncremental(context.Background(), snapshot, func([]graymarket.StockKlinePoint) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := set.PrepareUpstreamRequests(context.Background(), "stock-kline", 10, 4); err != nil {
		t.Fatal(err)
	}
	if primary.preparations != 1 {
		t.Fatalf("cooling primary should not be prepared: %d", primary.preparations)
	}
}

func TestInstrumentUnavailableDoesNotCoolProvider(t *testing.T) {
	primary := &testKlineProvider{err: fmt.Errorf("%w: suspended stock", graymarket.ErrInstrumentUnavailable)}
	set, err := NewProviderSet(&testEastmoneyProvider{testQuoteProvider: &testQuoteProvider{}, testKlineProvider: primary},
		&testBackupProvider{testQuoteProvider: &testQuoteProvider{}, testKlineProvider: &testKlineProvider{}},
		Options{Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-11-13", RankType: graymarket.RankStock,
		SnapshotAt: time.Date(2026, 11, 13, 15, 0, 0, 0, time.Local)}
	for index := 0; index < 2; index++ {
		snapshot.Records = append(snapshot.Records, graymarket.RankRecord{
			TradeDate: snapshot.TradeDate, Market: 0, Code: fmt.Sprintf("%06d", index+1),
		})
	}
	count, err := set.Klines.FetchStockKlines5mIncremental(context.Background(), snapshot, func([]graymarket.StockKlinePoint) error {
		return nil
	})
	if err != nil || count != 2 {
		t.Fatalf("backup should complete instrument-level failures: count=%d err=%v", count, err)
	}
	if primary.calls != 2 || !set.Klines.primaryHealthy() {
		t.Fatalf("instrument-level error cooled the supplier: calls=%d healthy=%v", primary.calls, set.Klines.primaryHealthy())
	}
}
