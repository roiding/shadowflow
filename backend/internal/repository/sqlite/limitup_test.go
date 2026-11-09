package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/limitup"
)

func limitUpTestSnapshot() limitup.Snapshot {
	return limitup.Snapshot{
		TradeDate: "2026-08-03", Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt:   time.Date(2026, 8, 3, 16, 0, 0, 0, limitup.Shanghai()),
		SealedCount: 1, BrokenCount: 1, TouchedCount: 2,
		Events: []limitup.Event{
			{TradeDate: "2026-08-03", Code: "000001", Name: "甲", MarketID: 33, MarketType: "HS", Pool: limitup.PoolSealed},
			{TradeDate: "2026-08-03", Code: "000002", Name: "乙", MarketID: 33, MarketType: "HS", Pool: limitup.PoolBroken},
		},
		RawPages: []json.RawMessage{json.RawMessage(`{"status_code":0,"pool":"limit_up"}`), json.RawMessage(`{"status_code":0,"pool":"open_limit"}`), json.RawMessage(`{"status_code":0,"pool":"limit_down"}`)},
	}
}

func TestLimitUpStoreKeepsBrokenAndDownSameDay(testingT *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	snapshot := limitUpTestSnapshot()
	snapshot.Events[0].Pool = limitup.PoolBroken
	snapshot.SealedCount, snapshot.BrokenCount, snapshot.DownCount = 0, 2, 1
	down := snapshot.Events[0]
	down.Pool = limitup.PoolDown
	snapshot.Events = append(snapshot.Events, down)
	if err := store.SaveLimitUpSnapshot(context.Background(), snapshot); err != nil {
		testingT.Fatalf("valid touched-up then closed-down record rejected: %v", err)
	}
	dataset, err := store.ReadLimitUpWindow(context.Background(), "2026-08-03", "2026-08-03", limitup.ScopeDefault)
	if err != nil || len(dataset.Events) != 3 || dataset.Days[0].TouchedCount != 2 || dataset.Days[0].DownCount != 1 {
		testingT.Fatalf("dual-pool stock lost: %+v %v", dataset, err)
	}
	snapshot.Events[0].Pool = limitup.PoolSealed
	snapshot.SealedCount, snapshot.BrokenCount = 1, 1
	if err := store.SaveLimitUpSnapshot(context.Background(), snapshot); err == nil {
		testingT.Fatal("simultaneously closing at up and down limits must be rejected")
	}
}

func TestLimitUpAtomicReplaceAndScopeIsolation(testingT *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	snapshot := limitUpTestSnapshot()
	for attempt := 0; attempt < 2; attempt++ {
		if err := store.SaveLimitUpSnapshot(ctx, snapshot); err != nil {
			testingT.Fatal(err)
		}
	}
	dataset, err := store.ReadLimitUpWindow(ctx, "2026-08-01", "2026-08-04", limitup.ScopeDefault)
	if err != nil || len(dataset.Days) != 1 || len(dataset.Events) != 2 || dataset.Events[0].OpenCount != nil || len(dataset.Days[0].ContentHash) != 64 {
		testingT.Fatalf("idempotence/null/provenance failure: %+v, %v", dataset, err)
	}
	originalHash := dataset.Days[0].ContentHash
	expanded := snapshot
	expanded.Scope = limitup.ScopeExpanded
	expanded.Events = nil
	expanded.SealedCount, expanded.BrokenCount, expanded.TouchedCount = 0, 0, 0
	if err := store.SaveLimitUpSnapshot(ctx, expanded); err != nil {
		testingT.Fatal(err)
	}
	other, err := store.ReadLimitUpWindow(ctx, "2026-08-03", "2026-08-03", limitup.ScopeExpanded)
	if err != nil || len(other.Days) != 1 || len(other.Events) != 0 {
		testingT.Fatalf("verified empty day must survive independently: %+v, %v", other, err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_limit_event BEFORE INSERT ON limit_up_event
WHEN NEW.code='000002' BEGIN SELECT RAISE(ABORT, 'simulated write error'); END`); err != nil {
		testingT.Fatal(err)
	}
	snapshot.Events[0].Name = "修订名称"
	snapshot.FetchedAt = snapshot.FetchedAt.Add(time.Hour)
	if err := store.SaveLimitUpSnapshot(ctx, snapshot); err == nil {
		testingT.Fatal("expected publication failure")
	}
	dataset, err = store.ReadLimitUpWindow(ctx, "2026-08-03", "2026-08-03", limitup.ScopeDefault)
	if err != nil || len(dataset.Events) != 2 || dataset.Events[0].Name != "甲" || dataset.Days[0].ContentHash != originalHash {
		testingT.Fatalf("failed replacement destroyed previous complete snapshot: %+v, %v", dataset, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_limit_event`); err != nil {
		testingT.Fatal(err)
	}
	if err := store.SaveLimitUpSnapshot(ctx, snapshot); err != nil {
		testingT.Fatal(err)
	}
	dataset, err = store.ReadLimitUpWindow(ctx, "2026-08-03", "2026-08-03", limitup.ScopeDefault)
	if err != nil || dataset.Events[0].Name != "修订名称" || dataset.Days[0].ContentHash == originalHash {
		testingT.Fatalf("valid revision did not replace day: %+v, %v", dataset, err)
	}
	if err := store.SaveLimitUpSnapshot(ctx, limitUpTestSnapshot()); err == nil {
		testingT.Fatal("older fetch must not overwrite newer snapshot")
	}
}

func TestLatestLimitUpDayIsStrictlyBeforeRequestedDate(testingT *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, tradeDate := range []string{"2026-11-05", "2026-11-06", "2026-11-09"} {
		snapshot := limitUpTestSnapshot()
		snapshot.TradeDate = tradeDate
		parsed, err := time.ParseInLocation("2006-01-02", tradeDate, limitup.Shanghai())
		if err != nil {
			testingT.Fatal(err)
		}
		snapshot.FetchedAt = parsed.Add(16 * time.Hour)
		for index := range snapshot.Events {
			snapshot.Events[index].TradeDate = tradeDate
		}
		if err := store.SaveLimitUpSnapshot(ctx, snapshot); err != nil {
			testingT.Fatal(err)
		}
	}
	latest, err := store.LatestLimitUpDay(ctx, "2026-11-09", limitup.ScopeDefault)
	if err != nil || latest != "2026-11-06" {
		testingT.Fatalf("today's archive must be excluded: %q %v", latest, err)
	}
	latest, err = store.LatestLimitUpDay(ctx, "2026-11-07", limitup.ScopeDefault)
	if err != nil || latest != "2026-11-06" {
		testingT.Fatalf("non-trading requested date should use the newest earlier archive: %q %v", latest, err)
	}
	latest, err = store.LatestLimitUpDay(ctx, "2026-11-05", limitup.ScopeDefault)
	if err != nil || latest != "" {
		testingT.Fatalf("no earlier archive must be explicit: %q %v", latest, err)
	}
	if _, err := store.LatestLimitUpDay(ctx, "2026-11-09", "unsupported"); err == nil {
		testingT.Fatal("unsupported scope must be rejected")
	}
}

func TestLimitUpIncompleteAndWrongDatesNeverPublish(testingT *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	cases := map[string]func(*limitup.Snapshot){
		"missing page":   func(snapshot *limitup.Snapshot) { snapshot.RawPages = snapshot.RawPages[:1] },
		"missing record": func(snapshot *limitup.Snapshot) { snapshot.Events = snapshot.Events[:1] },
		"pool overlap":   func(snapshot *limitup.Snapshot) { snapshot.Events[1].Code = snapshot.Events[0].Code },
		"wrong date":     func(snapshot *limitup.Snapshot) { snapshot.Events[1].TradeDate = "2026-08-04" },
		"not final":      func(snapshot *limitup.Snapshot) { snapshot.FetchedAt = snapshot.FetchedAt.Add(-time.Hour) },
		"bad pool total": func(snapshot *limitup.Snapshot) { snapshot.SealedCount, snapshot.BrokenCount = 2, 0 },
	}
	for name, mutate := range cases {
		testingT.Run(name, func(testingT *testing.T) {
			snapshot := limitUpTestSnapshot()
			mutate(&snapshot)
			if err := store.SaveLimitUpSnapshot(context.Background(), snapshot); err == nil {
				testingT.Fatal("invalid snapshot accepted")
			}
		})
	}
	dataset, err := store.ReadLimitUpWindow(context.Background(), "2026-08-01", "2026-08-05", limitup.ScopeDefault)
	if err != nil || len(dataset.Days) != 0 || len(dataset.Events) != 0 {
		testingT.Fatalf("invalid snapshot left visible data: %+v %v", dataset, err)
	}
}
