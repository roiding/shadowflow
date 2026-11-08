package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/datasource/thsdataq"
	"github.com/roiding/shadowflow/internal/limitup"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
)

func TestThsIdentitiesMapping(t *testing.T) {
	events := []limitup.Event{
		{Code: "002578", MarketID: 33},
		{Code: "600519", MarketID: 17},
		{Code: "300750", MarketID: 0},   // market id missing: prefix decides
		{Code: "688981", MarketID: 33},  // inconsistent id: prefix wins
		{Code: "830799", MarketID: 36},  // BSE: skipped
		{Code: "002578", MarketID: 33},  // duplicate: skipped
		{Code: "", MarketID: 33},        // empty code: skipped
		{Code: "0025781", MarketID: 33}, // malformed length: skipped
	}
	identities := thsIdentities(events)
	expected := []string{"17:600519", "17:688981", "33:002578", "33:300750"}
	if strings.Join(identities, ",") != strings.Join(expected, ",") {
		t.Fatalf("unexpected identities: %v (expected %v)", identities, expected)
	}
}

func TestThsAccountDataCollectorEndToEnd(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := seedLimitUpEvents(t, store, "2026-11-07",
		limitup.Event{Code: "002578", MarketID: 33, Name: "测试A"},
		limitup.Event{Code: "600519", MarketID: 17, Name: "测试B"}); err != nil {
		t.Fatal(err)
	}
	source := &fakeTHSAccountSource{}
	service := New(nil, store, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithLimitUpSource(nil, limitup.ScopeDefault).
		WithThsAccountSource(source)
	if err := service.CollectThsAccountData(context.Background(),
		time.Date(2026, 11, 7, 16, 50, 0, 0, limitup.Shanghai())); err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 1 {
		t.Fatalf("expected one batch call, got %d", len(source.calls))
	}
	if strings.Join(source.calls[0], ",") != "17:600519,33:002578" {
		t.Fatalf("unexpected batch identities: %v", source.calls[0])
	}
	count, err := store.ThsAccountDataCount(context.Background(), "2026-11-07")
	if err != nil || count != 2 {
		t.Fatalf("expected two stored records, count=%d err=%v", count, err)
	}

	// A complete archive is skipped so replay slots stay idempotent.
	if err := service.CollectThsAccountData(context.Background(),
		time.Date(2026, 11, 7, 17, 10, 0, 0, limitup.Shanghai())); err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 1 {
		t.Fatalf("complete archive was refetched: %d calls", len(source.calls))
	}
}

func TestThsAccountDataSkipsWhenEventsMissing(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := &fakeTHSAccountSource{}
	service := New(nil, store, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithLimitUpSource(nil, limitup.ScopeDefault).
		WithThsAccountSource(source)
	if err := service.CollectThsAccountData(context.Background(),
		time.Date(2026, 11, 7, 16, 50, 0, 0, limitup.Shanghai())); err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 0 {
		t.Fatalf("missing event archive must skip the fetch: %v", source.calls)
	}
}

func TestThsAccountDataBatchesOverTwentyIdentities(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events := make([]limitup.Event, 0, 45)
	for index := 0; index < 45; index++ {
		events = append(events, limitup.Event{Code: fmt.Sprintf("%06d", 300000+index),
			MarketID: 33, Name: "批量" + fmt.Sprint(index)})
	}
	if err := seedLimitUpEvents(t, store, "2026-11-07", events...); err != nil {
		t.Fatal(err)
	}
	source := &fakeTHSAccountSource{}
	service := New(nil, store, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithLimitUpSource(nil, limitup.ScopeDefault).
		WithThsAccountSource(source)
	if err := service.CollectThsAccountData(context.Background(),
		time.Date(2026, 11, 7, 16, 50, 0, 0, limitup.Shanghai())); err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 3 {
		t.Fatalf("expected three batches, got %d", len(source.calls))
	}
	for index, batch := range source.calls {
		if len(batch) > thsAccountBatchSize {
			t.Fatalf("batch %d exceeds the size limit: %d", index, len(batch))
		}
	}
	if len(source.calls[0]) != 20 || len(source.calls[1]) != 20 || len(source.calls[2]) != 5 {
		t.Fatalf("unexpected batch sizes: %d/%d/%d", len(source.calls[0]), len(source.calls[1]), len(source.calls[2]))
	}
	count, err := store.ThsAccountDataCount(context.Background(), "2026-11-07")
	if err != nil || count != 45 {
		t.Fatalf("expected 45 stored records, count=%d err=%v", count, err)
	}
}

func TestThsAccountDataRejectsNoAuthWithoutStoringRows(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := seedLimitUpEvents(t, store, "2026-11-07",
		limitup.Event{Code: "002578", MarketID: 33, Name: "测试A"}); err != nil {
		t.Fatal(err)
	}
	source := &fakeTHSAccountSource{permission: "NO_AUTH"}
	service := New(nil, store, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithLimitUpSource(nil, limitup.ScopeDefault).
		WithThsAccountSource(source)
	collectError := service.CollectThsAccountData(context.Background(),
		time.Date(2026, 11, 7, 16, 50, 0, 0, limitup.Shanghai()))
	if !errors.Is(collectError, thsdataq.ErrPermissionDenied) {
		t.Fatalf("expected permission denial, got %v", collectError)
	}
	count, countError := store.ThsAccountDataCount(context.Background(), "2026-11-07")
	if countError != nil || count != 0 {
		t.Fatalf("permission denial stored rows: count=%d err=%v", count, countError)
	}
}

func TestThsAccountDataRefetchesLegacyRecordAndKeepsOtherRows(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := seedLimitUpEvents(t, store, "2026-11-07",
		limitup.Event{Code: "002578", MarketID: 33, Name: "测试A"},
		limitup.Event{Code: "600519", MarketID: 17, Name: "测试B"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	legacy := `{"code":"33:002578","values":[]}`
	if err := store.SaveThsAccountData(ctx, "2026-11-07", "33:002578", legacy,
		time.Date(2026, 11, 7, 8, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	source := &fakeTHSAccountSource{}
	service := New(nil, store, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithLimitUpSource(nil, limitup.ScopeDefault).
		WithThsAccountSource(source)
	if err := service.CollectThsAccountData(ctx,
		time.Date(2026, 11, 7, 16, 50, 0, 0, limitup.Shanghai())); err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 1 || strings.Join(source.calls[0], ",") != "17:600519,33:002578" {
		t.Fatalf("expected only current identities, got %v", source.calls)
	}
	stored, err := store.ThsAccountDataPayloads(ctx, "2026-11-07", []string{"33:002578", "17:600519"})
	if err != nil || !thsdataq.RecordComplete(stored["33:002578"], "2026-11-07", "33:002578") {
		t.Fatalf("legacy record was not replaced: %#v %v", stored, err)
	}
}

func TestThsAccountDataReplacesIdentitySetWithEqualCount(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	at := time.Date(2026, 11, 7, 16, 50, 0, 0, limitup.Shanghai())
	if err := seedLimitUpEvents(t, store, "2026-11-07",
		limitup.Event{Code: "002578", MarketID: 33, Name: "测试A"},
		limitup.Event{Code: "600519", MarketID: 17, Name: "测试B"}); err != nil {
		t.Fatal(err)
	}
	source := &fakeTHSAccountSource{}
	service := New(nil, store, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithLimitUpSource(nil, limitup.ScopeDefault).
		WithThsAccountSource(source)
	if err := service.CollectThsAccountData(ctx, at); err != nil {
		t.Fatal(err)
	}
	if err := seedLimitUpEvents(t, store, "2026-11-07",
		limitup.Event{Code: "002578", MarketID: 33, Name: "测试A"},
		limitup.Event{Code: "300750", MarketID: 33, Name: "测试C"}); err != nil {
		t.Fatal(err)
	}
	if err := service.CollectThsAccountData(ctx, at.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 2 || strings.Join(source.calls[1], ",") != "33:300750" {
		t.Fatalf("expected only the missing identity, got %v", source.calls)
	}
	stored, err := store.ThsAccountDataPayloads(ctx, "2026-11-07", []string{"33:002578", "33:300750"})
	if err != nil || len(stored) != 2 {
		t.Fatalf("unexpected stored records: %#v %v", stored, err)
	}
	if !thsdataq.RecordComplete(stored["33:300750"], "2026-11-07", "33:300750") {
		t.Fatal("new identity was not archived")
	}
}

func TestThsAccountDataResumesAfterPartialBatchFailure(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	events := make([]limitup.Event, 0, 45)
	for index := 0; index < 45; index++ {
		events = append(events, limitup.Event{Code: fmt.Sprintf("%06d", 300000+index),
			MarketID: 33, Name: "批量" + fmt.Sprint(index)})
	}
	if err := seedLimitUpEvents(t, store, "2026-11-07", events...); err != nil {
		t.Fatal(err)
	}
	source := &fakeTHSAccountSource{failAtCall: 2}
	service := New(nil, store, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithLimitUpSource(nil, limitup.ScopeDefault).
		WithThsAccountSource(source)
	at := time.Date(2026, 11, 7, 16, 50, 0, 0, limitup.Shanghai())
	if err := service.CollectThsAccountData(ctx, at); err == nil {
		t.Fatal("expected partial batch failure")
	}
	count, err := store.ThsAccountDataCount(ctx, "2026-11-07")
	if err != nil || count != 20 {
		t.Fatalf("expected first batch preserved, count=%d err=%v", count, err)
	}
	source.failAtCall = 0
	if err := service.CollectThsAccountData(ctx, at.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	count, err = store.ThsAccountDataCount(ctx, "2026-11-07")
	if err != nil || count != 45 {
		t.Fatalf("failed to resume missing identities, count=%d err=%v", count, err)
	}
}

// seedLimitUpEvents archives a minimal valid limit-up snapshot so the account
// lane can resolve the day's stock identities.
func seedLimitUpEvents(t *testing.T, store *sqlite.Store, tradeDate string, events ...limitup.Event) error {
	t.Helper()
	fetchedAt := time.Date(2026, 11, 7, 16, 30, 0, 0, limitup.Shanghai())
	for index := range events {
		events[index].TradeDate = tradeDate
		events[index].MarketType = "A股"
		events[index].Pool = limitup.PoolSealed
		events[index].FirstLimitAt = &fetchedAt
		events[index].LastLimitAt = &fetchedAt
	}
	snapshot := limitup.Snapshot{
		TradeDate: tradeDate, Source: limitup.SourceTHS, Scope: limitup.ScopeDefault,
		FetchedAt: fetchedAt, Events: events,
		SealedCount: len(events), TouchedCount: len(events),
		RawPages: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
	return store.SaveLimitUpSnapshot(context.Background(), snapshot)
}

type fakeTHSAccountSource struct {
	calls      [][]string
	failAtCall int
	permission string
	nullValues bool
}

func (fake *fakeTHSAccountSource) Fetch(ctx context.Context, tradeDate string, identities []string) (json.RawMessage, error) {
	fake.calls = append(fake.calls, append([]string(nil), identities...))
	if fake.failAtCall > 0 && len(fake.calls) == fake.failAtCall {
		return nil, fmt.Errorf("synthetic THS account source failure")
	}
	parsedDate, err := time.ParseInLocation("2006-01-02", tradeDate, limitup.Shanghai())
	if err != nil {
		return nil, err
	}
	timestamp := strconv.FormatInt(parsedDate.Unix(), 10)
	indexes := make([]map[string]any, 0, len(thsdataq.MetricIndexIDs()))
	for _, indexID := range thsdataq.MetricIndexIDs() {
		index := map[string]any{"index_id": indexID, "time_type": "TRADE_DAILY", "timestamp": timestamp}
		if fake.permission != "" {
			index["attribute"] = map[string]any{"permission_flag": fake.permission}
		}
		indexes = append(indexes, index)
	}
	rows := make([]map[string]any, 0, len(identities))
	for _, identity := range identities {
		values := make([]map[string]any, 0, len(thsdataq.MetricIndexIDs()))
		for index, indexID := range thsdataq.MetricIndexIDs() {
			value := any("value-" + indexID)
			if fake.nullValues {
				value = nil
			}
			values = append(values, map[string]any{"idx": index, "value": value})
		}
		rows = append(rows, map[string]any{"code": identity, "values": values})
	}
	return json.Marshal(map[string]any{
		"status_code": 0,
		"data":        map[string]any{"indexes": indexes, "data": rows, "total": len(identities)},
	})
}
