package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
)

const sqliteCorrectionBrokenRaw = `{"code":"000004","name":"丁","market_id":33,"market_type":"HS","open_num":2,"first_limit_up_time":"1785721500","last_limit_up_time":"1785721800","change_rate":-5.0,"change_tag":"LIMIT_DOWN"}`

func sqliteCorrectionSnapshot() limitup.Snapshot {
	firstLimit := time.Date(2026, 8, 3, 9, 45, 0, 0, limitup.Shanghai())
	lastLimit := time.Date(2026, 8, 3, 9, 50, 0, 0, limitup.Shanghai())
	openCount := 2
	downOpenCount := 2
	changePercent := -5.0
	return limitup.Snapshot{
		TradeDate: "2026-08-03", Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt:   time.Date(2026, 8, 3, 16, 0, 0, 0, limitup.Shanghai()),
		SealedCount: 2, BrokenCount: 2, DownCount: 1, TouchedCount: 4,
		CountCorrection: &limitup.CountCorrection{
			Reason: limitup.CountCorrectionBrokenDownSummaryUndercount, SourceBrokenCount: 1,
			SourceTouchedCount: 3, Codes: []string{"000004"},
		},
		Events: []limitup.Event{
			{TradeDate: "2026-08-03", Code: "000001", Name: "甲", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{TradeDate: "2026-08-03", Code: "000002", Name: "乙", MarketID: 33, MarketType: "HS", Pool: limitup.PoolBroken},
			{TradeDate: "2026-08-03", Code: "000003", Name: "丙", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{
				TradeDate: "2026-08-03", Code: "000004", Name: "丁", MarketID: 33, MarketType: "HS",
				Pool: limitup.PoolBroken, OpenCount: &openCount, FirstLimitAt: &firstLimit, LastLimitAt: &lastLimit,
				ChangePercent: &changePercent, Raw: json.RawMessage(sqliteCorrectionBrokenRaw),
			},
			{
				TradeDate: "2026-08-03", Code: "000004", Name: "丁", MarketID: 33, MarketType: "HS",
				Pool: limitup.PoolDown, OpenCount: &downOpenCount, ChangePercent: &changePercent,
				Raw: json.RawMessage(`{"code":"000004","name":"丁","market_id":33,"market_type":"HS","open_num":2,"first_limit_up_time":null,"last_limit_up_time":null,"change_rate":-5.0,"change_tag":"LIMIT_DOWN"}`),
			},
		},
		RawPages: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
}

func sqliteNormalSnapshot() limitup.Snapshot {
	return limitup.Snapshot{
		TradeDate: "2026-08-03", Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt:   time.Date(2026, 8, 3, 17, 0, 0, 0, limitup.Shanghai()),
		SealedCount: 2, BrokenCount: 1, DownCount: 1, TouchedCount: 3,
		Events: []limitup.Event{
			{TradeDate: "2026-08-03", Code: "000001", Name: "甲", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{TradeDate: "2026-08-03", Code: "000002", Name: "乙", MarketID: 33, MarketType: "HS", Pool: limitup.PoolBroken},
			{TradeDate: "2026-08-03", Code: "000003", Name: "丙", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{TradeDate: "2026-08-03", Code: "000004", Name: "丁", MarketID: 33, MarketType: "HS", Pool: limitup.PoolDown},
		},
		RawPages: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
}

func assertStoredCorrection(testingT *testing.T, store *Store, expected bool) {
	testingT.Helper()
	ctx := context.Background()
	quality, qualityError := store.LimitUpArchiveQuality(ctx, "2026-08-03", limitup.ScopeDefault)
	if qualityError != nil || quality.Day == nil {
		testingT.Fatalf("read correction quality: day=%+v error=%v", quality.Day, qualityError)
	}
	dataset, readError := store.ReadLimitUpWindow(ctx, "2026-08-03", "2026-08-03", limitup.ScopeDefault)
	if readError != nil || len(dataset.Days) != 1 {
		testingT.Fatalf("read correction window: %+v %v", dataset, readError)
	}
	corrections := []*limitup.CountCorrection{quality.Day.CountCorrection, dataset.Days[0].CountCorrection}
	for _, correction := range corrections {
		if !expected {
			if correction != nil {
				testingT.Fatalf("stale correction audit survived replacement: %+v", correction)
			}
			continue
		}
		if correction == nil || correction.Reason != limitup.CountCorrectionBrokenDownSummaryUndercount ||
			correction.SourceBrokenCount != 1 || correction.SourceTouchedCount != 3 ||
			len(correction.Codes) != 1 || correction.Codes[0] != "000004" {
			testingT.Fatalf("correction audit was not persisted: %+v", correction)
		}
	}
}

func TestLimitUpCorrectionAuditPersistsAcrossReopenAndReplacesAtomically(testingT *testing.T) {
	databasePath := filepath.Join(testingT.TempDir(), "shadowflow.db")
	store, openError := Open(databasePath)
	if openError != nil {
		testingT.Fatal(openError)
	}
	ctx := context.Background()
	correctedSnapshot := sqliteCorrectionSnapshot()
	if saveError := store.SaveLimitUpSnapshot(ctx, correctedSnapshot); saveError != nil {
		testingT.Fatalf("save corrected snapshot: %v", saveError)
	}
	if closeError := store.Close(); closeError != nil {
		testingT.Fatal(closeError)
	}
	store, openError = Open(databasePath)
	if openError != nil {
		testingT.Fatal(openError)
	}
	assertStoredCorrection(testingT, store, true)
	normalSnapshot := sqliteNormalSnapshot()
	normalSnapshot.FetchedAt = correctedSnapshot.FetchedAt.Add(time.Hour)
	if saveError := store.SaveLimitUpSnapshot(ctx, normalSnapshot); saveError != nil {
		testingT.Fatalf("save normal replacement: %v", saveError)
	}
	assertStoredCorrection(testingT, store, false)
	correctedSnapshot.FetchedAt = normalSnapshot.FetchedAt.Add(time.Hour)
	if saveError := store.SaveLimitUpSnapshot(ctx, correctedSnapshot); saveError != nil {
		testingT.Fatalf("restore corrected snapshot: %v", saveError)
	}
	assertStoredCorrection(testingT, store, true)
	if _, triggerError := store.db.Exec(`CREATE TRIGGER fail_corrected_event BEFORE INSERT ON limit_up_event
WHEN NEW.code='000003' BEGIN SELECT RAISE(ABORT, 'simulated correction failure'); END`); triggerError != nil {
		testingT.Fatal(triggerError)
	}
	replacement := sqliteNormalSnapshot()
	replacement.FetchedAt = correctedSnapshot.FetchedAt.Add(time.Hour)
	if saveError := store.SaveLimitUpSnapshot(ctx, replacement); saveError == nil {
		testingT.Fatal("expected replacement failure after correction audit deletion")
	}
	assertStoredCorrection(testingT, store, true)
	if _, triggerError := store.db.Exec(`DROP TRIGGER fail_corrected_event`); triggerError != nil {
		testingT.Fatal(triggerError)
	}
	if closeError := store.Close(); closeError != nil {
		testingT.Fatal(closeError)
	}
}

func TestLimitUpCorrectionSchemaIsAdditiveForOldArchives(testingT *testing.T) {
	databasePath := filepath.Join(testingT.TempDir(), "legacy.db")
	store, openError := Open(databasePath)
	if openError != nil {
		testingT.Fatal(openError)
	}
	ctx := context.Background()
	if saveError := store.SaveLimitUpSnapshot(ctx, sqliteNormalSnapshot()); saveError != nil {
		testingT.Fatal(saveError)
	}
	if _, dropError := store.db.Exec(`DROP TABLE limit_up_day_correction`); dropError != nil {
		testingT.Fatal(dropError)
	}
	if closeError := store.Close(); closeError != nil {
		testingT.Fatal(closeError)
	}
	store, openError = Open(databasePath)
	if openError != nil {
		testingT.Fatalf("old archive without correction table cannot reopen: %v", openError)
	}
	defer store.Close()
	assertStoredCorrection(testingT, store, false)
	dataset, readError := store.ReadLimitUpWindow(ctx, "2026-08-03", "2026-08-03", limitup.ScopeDefault)
	if readError != nil || len(dataset.Events) != 4 || len(dataset.Days) != 1 {
		testingT.Fatalf("legacy archive lost data after additive migration: %+v %v", dataset, readError)
	}
}
