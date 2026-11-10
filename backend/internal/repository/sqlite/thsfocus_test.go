package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/thsfocus"
)

func focusTestSnapshot() thsfocus.Snapshot {
	return thsfocus.Snapshot{
		TradeDate: "2026-11-06", Source: thsfocus.Source, Scope: thsfocus.ScopeDefault, MarketScope: thsfocus.MarketScope,
		FetchedAt: time.Date(2026, 11, 6, 16, 0, 0, 0, thsfocus.Shanghai()),
		Blocks: []thsfocus.Block{
			{Code: "885001", Name: "题材", LimitUpCount: 2, Stocks: []thsfocus.Stock{
				{Code: "000001", Name: "样本", MarketID: 33, Reason: "明确原因"},
				{Code: "000002", Name: "同题材样本", MarketID: 33, Reason: "明确原因"},
			}},
			{Code: "885999", Name: "其他概念", LimitUpCount: 1, Stocks: []thsfocus.Stock{
				{Code: "000003", Name: "未分类样本", MarketID: 33, Reason: "未分类"},
			}},
		},
		Ladder: []thsfocus.Level{}, Market: json.RawMessage(`{"rise_fall":{"rise":100,"fall":200,"deuce":10,"limit_up":10,"limit_down":2}}`),
		Comparison:  json.RawMessage(`{"today_date":"2026-11-06","yesterday_date":"2026-11-05","board_list":[],"one_board":[]}`),
		Limitations: []string{"题材Top列表，部分接口未回显日期"},
		RawPages:    []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
}

func TestThsFocusScopeIsolationAtomicReplacementAndProvenance(testingT *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	snapshot := focusTestSnapshot()
	if err := store.SaveThsFocus(ctx, snapshot); err != nil {
		testingT.Fatal(err)
	}
	read, err := store.ReadThsFocus(ctx, snapshot.TradeDate, snapshot.Scope)
	if err != nil || read == nil || read.SourceDateVerified || read.Blocks[0].Stocks[0].Code != "000001" || read.MarketScope != thsfocus.MarketScope {
		testingT.Fatalf("bad persisted focus: %+v %v", read, err)
	}
	var themeRows int
	if err := store.reader.QueryRow(`SELECT count(*) FROM ths_theme_stock_day`).Scan(&themeRows); err != nil || themeRows != 2 {
		testingT.Fatalf("unclassified theme block leaked into index: rows=%d err=%v", themeRows, err)
	}
	profiles, err := store.ReadThemeProfiles(ctx, "2025-11-10", "2026-11-10", thsfocus.ScopeDefault, nil)
	if err != nil || len(profiles) != 2 || len(profiles[0].Tags) != 1 || profiles[0].Tags[0].Name != "题材" {
		testingT.Fatalf("bad theme profiles: %+v %v", profiles, err)
	}
	cooccurrences, err := store.ReadThemeCooccurrences(ctx, "2025-11-10", "2026-11-10", thsfocus.ScopeDefault, "000001")
	if err != nil || len(cooccurrences) != 1 || cooccurrences[0].PeerCode != "000002" {
		testingT.Fatalf("bad theme cooccurrences: %+v %v", cooccurrences, err)
	}
	other, err := store.ReadThsFocus(ctx, snapshot.TradeDate, thsfocus.ScopeExpanded)
	if err != nil || other != nil {
		testingT.Fatal("scope leaked")
	}
	snapshot.FetchedAt = snapshot.FetchedAt.Add(time.Hour)
	snapshot.Blocks[0].Name = "修订题材"
	if _, err := store.db.Exec(`CREATE TRIGGER fail_focus BEFORE UPDATE ON ths_focus_day BEGIN SELECT RAISE(ABORT,'test rollback'); END`); err != nil {
		testingT.Fatal(err)
	}
	if err := store.SaveThsFocus(ctx, snapshot); err == nil {
		testingT.Fatal("expected interrupted publication")
	}
	read, err = store.ReadThsFocus(ctx, snapshot.TradeDate, snapshot.Scope)
	if err != nil || read.Blocks[0].Name != "题材" {
		testingT.Fatal("failed replacement destroyed good snapshot")
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_focus`); err != nil {
		testingT.Fatal(err)
	}
	if err := store.SaveThsFocus(ctx, snapshot); err != nil {
		testingT.Fatal(err)
	}
	if err := store.SaveThsFocus(ctx, focusTestSnapshot()); err == nil {
		testingT.Fatal("older data replaced newer revision")
	}
	var pages []byte
	if err := store.reader.QueryRow(`SELECT raw_pages FROM ths_focus_day`).Scan(&pages); err != nil || string(pages) != "[{},{},{},{}]" {
		testingT.Fatal("raw evidence missing")
	}
}

func TestThsThemeStockMigrationBackfillsExistingSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shadowflow.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveThsFocus(context.Background(), focusTestSnapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM ths_theme_stock_day`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM database_maintenance WHERE name='ths_theme_stock_v1'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var rows int
	if err := reopened.reader.QueryRow(`SELECT count(*) FROM ths_theme_stock_day`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("expected migration to backfill two usable rows, got %d", rows)
	}
}
