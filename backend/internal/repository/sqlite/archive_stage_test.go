package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

func completeArchiveFixture(t *testing.T, store *Store, date string, stocks int, seal bool) graymarket.RankSnapshot {
	t.Helper()
	ctx := context.Background()
	location := time.FixedZone("Asia/Shanghai", 8*3600)
	at, err := time.ParseInLocation("2006-01-02 15:04", date+" 15:00", location)
	if err != nil {
		t.Fatal(err)
	}
	var stockSnapshot graymarket.RankSnapshot
	for _, kind := range []graymarket.RankType{graymarket.RankIndustry, graymarket.RankConcept, graymarket.RankStock} {
		n := 1
		if kind == graymarket.RankStock {
			n = stocks
		}
		snapshot := graymarket.RankSnapshot{TradeDate: date, SnapshotAt: at, RankType: kind, RawPages: []graymarket.RawPage{{Page: 1, ContentEncoding: "utf-8", Body: []byte(`{"fixture":"original"}`), FetchedAt: at}}}
		for index := range n {
			code, market := "BK0001", int64(90)
			if kind == graymarket.RankConcept {
				code = "BK1001"
			}
			if kind == graymarket.RankStock {
				code, market = fmt.Sprintf("%06d", index+1), 0
			}
			snapshot.Records = append(snapshot.Records, graymarket.RankRecord{TradeDate: date, SnapshotAt: at, RankType: kind, Market: market, Code: code, Name: "original", Rank: int64(index + 1),
				QuoteAvailable: true, MoneyAvailable: true, OpenPrice: 10, HighPrice: 11, LowPrice: 9, ClosePrice: 10.5, PreviousClose: 10, Volume: 48_000, Turnover: 96_000,
				DarkMoney: 100, RegularMoney: 50, MainMoneyInflow: 150, FetchedAt: at})
		}
		if kind == graymarket.RankStock {
			stockSnapshot = snapshot
			err = store.SaveStockArchive(ctx, "fixture-stock", snapshot, testMoneyPoints(snapshot))
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range snapshot.Records {
				if err := store.SaveStockKlines(ctx, "fixture-kline", testStockKlines(date, location, record)); err != nil {
					t.Fatal(err)
				}
			}
		} else {
			err = store.SaveBoardArchive(ctx, "fixture-board", snapshot, testMoneyPoints(snapshot))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if seal {
		if _, err := store.SealArchiveRevision(ctx, date, "fixture-"+date); err != nil {
			t.Fatal(err)
		}
	}
	return stockSnapshot
}

func TestStockArchiveReplacementPublishesOnlyCompleteStage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	snapshot := completeArchiveFixture(t, store, "2026-09-04", 2, true)
	if err := store.StartRun(ctx, repository.CollectionRun{RunID: "replacement", RequestedDate: snapshot.TradeDate, SnapshotAt: snapshot.SnapshotAt, RankType: graymarket.RankStock, SnapshotKind: graymarket.SnapshotResearch5m, StartedAt: time.Now(), Status: repository.RunRunning}); err != nil {
		t.Fatal(err)
	}
	snapshot.Records[0].Name = "replacement"
	points := testMoneyPoints(snapshot)
	points[0].DarkMoney = 900
	if err := store.SaveStockArchiveBatch(ctx, "replacement", snapshot, points[:48], true, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertOriginal := func() {
		t.Helper()
		rows, err := store.DailyCloseStocks(ctx, snapshot.TradeDate, []string{"000001"})
		if err != nil || len(rows) != 1 || rows[0].Name != "original" {
			t.Fatalf("old close changed before publication: rows=%v err=%v", rows, err)
		}
		var money, kline int
		if err := store.db.QueryRow(`SELECT sum(money_available),sum(kline_available) FROM stock_research_5m WHERE trade_date=?`, snapshot.TradeDate).Scan(&money, &kline); err != nil || money != 96 || kline != 96 {
			t.Fatalf("old curves damaged: money=%d kline=%d err=%v", money, kline, err)
		}
		manifest, err := store.ArchiveManifest(ctx, snapshot.TradeDate)
		if err != nil || manifest.Status != archiveManifestCompleted {
			t.Fatalf("old manifest changed: %v %v", manifest, err)
		}
	}
	assertOriginal()
	if err := store.SaveStockArchiveBatch(ctx, "replacement", snapshot, nil, false, true); !errors.Is(err, repository.ErrArchiveIncomplete) {
		t.Fatalf("partial stage published: %v", err)
	}
	assertOriginal()
	if _, err := store.db.Exec(`CREATE TRIGGER fail_publication BEFORE INSERT ON stock_research_5m BEGIN SELECT RAISE(ABORT,'publication failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStockArchiveBatch(ctx, "replacement", snapshot, points[48:], false, true); err == nil {
		t.Fatal("expected publication failure")
	}
	assertOriginal()
	if _, err := store.db.Exec(`DROP TRIGGER fail_publication`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStockArchiveBatch(ctx, "replacement", snapshot, points[48:], false, true); err != nil {
		t.Fatal(err)
	}
	quality, err := store.StockArchiveQuality(ctx, snapshot.TradeDate)
	if err != nil || quality.MoneyRows != 96 || quality.KlineRows != 0 || quality.KlineArchivedAt != nil {
		t.Fatalf("replacement quality is stale: %v %v", quality, err)
	}
	manifest, err := store.ArchiveManifest(ctx, snapshot.TradeDate)
	if err != nil || manifest.Status != archiveManifestPending {
		t.Fatalf("replacement requires new klines: %v %v", manifest, err)
	}
	series, err := store.StockResearchSeries(ctx, "000001", snapshot.TradeDate)
	if err != nil || series[0].DarkMoney != 900 {
		t.Fatalf("replacement was not published: %v %v", series, err)
	}
	var staged int
	if err := store.db.QueryRow(`SELECT count(*) FROM archive_money_stage`).Scan(&staged); err != nil || staged != 0 {
		t.Fatalf("published stage retained: count=%d err=%v", staged, err)
	}
}

func TestBoardStageIsolationAndIncompleteFirstArchive(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	at := time.Date(2026, 9, 4, 15, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	snapshot := graymarket.RankSnapshot{TradeDate: "2026-09-04", SnapshotAt: at, RankType: graymarket.RankConcept}
	for i := range 2 {
		snapshot.Records = append(snapshot.Records, graymarket.RankRecord{TradeDate: snapshot.TradeDate, SnapshotAt: at, FetchedAt: at, RankType: snapshot.RankType, Market: 90, Code: fmt.Sprintf("BK%04d", i+1), Rank: int64(i + 1)})
	}
	points := testMoneyPoints(snapshot)
	if err := store.SaveBoardArchiveBatch(ctx, "one", snapshot, points[:48], true, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBoardArchiveBatch(ctx, "two", snapshot, points[48:], true, true); !errors.Is(err, repository.ErrArchiveIncomplete) {
		t.Fatalf("stages mixed across runs: %v", err)
	}
	if complete, err := store.HasBoardArchive(ctx, snapshot.TradeDate, snapshot.RankType); err != nil || complete {
		t.Fatalf("incomplete archive exposed: complete=%v err=%v", complete, err)
	}
	if err := store.SaveBoardArchiveBatch(ctx, "one", snapshot, points[48:], false, true); err != nil {
		t.Fatal(err)
	}
	if complete, err := store.HasBoardArchive(ctx, snapshot.TradeDate, snapshot.RankType); err != nil || !complete {
		t.Fatalf("complete archive missing: complete=%v err=%v", complete, err)
	}
}
