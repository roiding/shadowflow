package sqlite

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestThsSessionRoundTrip(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	_, found, err := store.ReadThsSession(ctx)
	if err != nil || found {
		t.Fatalf("expected absent session, found=%v err=%v", found, err)
	}
	if err := store.StoreThsSession(ctx, `{"version":1,"cookies":[]}`); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreThsSession(ctx, `{"version":1,"cookies":[{"name":"v","value":"token"}]}`); err != nil {
		t.Fatal(err)
	}
	payload, found, err := store.ReadThsSession(ctx)
	if err != nil || !found {
		t.Fatalf("expected stored session, found=%v err=%v", found, err)
	}
	if payload != `{"version":1,"cookies":[{"name":"v","value":"token"}]}` {
		t.Fatalf("unexpected payload: %s", payload)
	}
	if err := store.StoreThsSession(ctx, "   "); err == nil {
		t.Fatal("empty payload was accepted")
	}
}

func TestThsSessionStoreAdapter(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	sessionStore := store.THSSessionStore()
	if _, found, err := sessionStore.LoadSession(ctx); err != nil || found {
		t.Fatalf("expected absent session, found=%v err=%v", found, err)
	}
	fixed := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	cookies := []*http.Cookie{{Name: "v", Value: "token", Domain: ".10jqka.com.cn", Path: "/",
		Secure: true, HttpOnly: true, Expires: fixed}}
	if err := sessionStore.SaveSession(ctx, cookies); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := sessionStore.LoadSession(ctx)
	if err != nil || !found || len(loaded) != 1 {
		t.Fatalf("unexpected load: found=%v len=%d err=%v", found, len(loaded), err)
	}
	if loaded[0].Name != "v" || loaded[0].Value != "token" || !loaded[0].HttpOnly || loaded[0].Expires.IsZero() {
		t.Fatalf("cookie metadata was lost: %+v", loaded[0])
	}
}

func TestThsAccountDataSaveAndCount(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	count, err := store.ThsAccountDataCount(ctx, "2026-11-07")
	if err != nil || count != 0 {
		t.Fatalf("expected zero rows, count=%d err=%v", count, err)
	}
	fetchedAt := time.Date(2026, 11, 7, 8, 50, 0, 0, time.UTC)
	if err := store.SaveThsAccountData(ctx, "2026-11-07", "33:002578", `{"code":"33:002578","values":[]}`, fetchedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveThsAccountData(ctx, "2026-11-07", "17:600519", `{"code":"17:600519","values":[]}`, fetchedAt); err != nil {
		t.Fatal(err)
	}
	count, err = store.ThsAccountDataCount(ctx, "2026-11-07")
	if err != nil || count != 2 {
		t.Fatalf("expected two rows, count=%d err=%v", count, err)
	}
	if err := store.SaveThsAccountData(ctx, "2026-11-07", "33:002578", `{"code":"33:002578","values":[1]}`, fetchedAt); err != nil {
		t.Fatal(err)
	}
	count, err = store.ThsAccountDataCount(ctx, "2026-11-07")
	if err != nil || count != 2 {
		t.Fatalf("upsert should stay at two rows, count=%d err=%v", count, err)
	}
}

func TestThsAccountDataRejectsInvalidInput(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	fetchedAt := time.Now().UTC()
	invalid := []struct {
		date     string
		identity string
	}{
		{"2026-11-07", "36:830799"}, // unsupported market
		{"2026-11-07", "33002578"},  // missing separator
		{"2026-11-07", "33:00257"},  // short code
		{"2026-11-07", "33:00257a"}, // non-numeric code
		{"20261107", "33:002578"},   // malformed date
		{"2026-11-07", ""},          // empty identity
	}
	for _, item := range invalid {
		if err := store.SaveThsAccountData(ctx, item.date, item.identity, `{}`, fetchedAt); !errors.Is(err, ErrInvalidThsState) {
			t.Fatalf("expected ErrInvalidThsState for %+v, got %v", item, err)
		}
	}
	if err := store.SaveThsAccountData(ctx, "2026-11-07", "33:002578", "  ", fetchedAt); !errors.Is(err, ErrInvalidThsState) {
		t.Fatalf("expected ErrInvalidThsState for empty payload, got %v", err)
	}
}

func TestMigrateStockUniverseHSPurgesBSE(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Reopen the migration window: the marker was set during Open.
	if _, err := store.db.Exec(`DELETE FROM database_maintenance WHERE name='stock_universe_hs_v1'`); err != nil {
		t.Fatal(err)
	}
	seed := func(date string) {
		if _, err := store.db.Exec(`INSERT INTO rank_snapshot(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,volume,turnover,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at,quote_available)
		VALUES('r','2026-11-06T15:00:00Z',?,?,'daily_close','stock',1,0,'920718','合肥高科','09:25:00',93800,0.0,0,0,0,0,0,0.0,0.0,0,0,0,'','',1,0,0,'2026-11-06T15:00:00Z',1)`, date, date); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO rank_snapshot(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,volume,turnover,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at,quote_available)
		VALUES('r','2026-11-06T15:00:00Z',?,?,'daily_close','stock',1,1,'600519','贵州茅台','15:00:00',97700,0.0,0,0,0,0,0,0.0,0.0,0,0,0,'','',1,0,0,'2026-11-06T15:00:00Z',1)`, date, date); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 48; index++ {
			if _, err := store.db.Exec(`INSERT INTO stock_research_5m(trade_date,minute_index,market,code,money_rank,dark_money,regular_money,main_money_inflow,money_available,kline_available)
			VALUES(?,?,0,'600519',1,0,0,0,1,1)`, date, index); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.db.Exec(`INSERT INTO stock_kline_source(trade_date,market,code,source,point_count,parser_version,fetched_at,run_id)
		VALUES(?,0,'600519','stock_kline_5m',48,'v','2026-11-06T15:00:00Z','r')`, date); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO stock_archive_quality(trade_date,expected_stocks,expected_points,expected_kline_stocks,money_rows,kline_rows,daily_close_rows,daily_kline_rows,updated_at)
		VALUES(?,1,48,1,48,48,1,1,?)`, date, "2026-11-06T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO daily_archive_manifest(trade_date,status,industry_close_rows,industry_money_rows,concept_close_rows,concept_money_rows,
		stock_close_rows,stock_money_rows,stock_kline_rows,stock_daily_kline_rows,expected_stock_rows,expected_stock_kline_rows,code_count,code_set_sha256,
		kline_source_counts_json,darktrade_contract,darktradetick_contract,stock_kline_contract,parser_version,validation_errors_json,updated_at)
		VALUES(?,'incomplete',0,0,0,0,2,96,96,2,2,96,2,'x','{}','a','b','c','v','[]',?)`, date, "2026-11-06T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	seed("2026-11-06")
	if err := migrateStockUniverseHS(store); err != nil {
		t.Fatal(err)
	}
	var bseIdentity int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM rank_snapshot WHERE code LIKE '92%'`).Scan(&bseIdentity); err != nil {
		t.Fatal(err)
	}
	if bseIdentity != 0 {
		t.Fatalf("BSE identity rows survived: %d", bseIdentity)
	}
	var hsStocks int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM rank_snapshot WHERE code='600519'`).Scan(&hsStocks); err != nil {
		t.Fatal(err)
	}
	if hsStocks != 1 {
		t.Fatal("HS stock was removed by the purge")
	}
	var expectedKline, klineRows int
	if err := store.db.QueryRow(`SELECT expected_kline_stocks, kline_rows FROM stock_archive_quality WHERE trade_date='2026-11-06'`).Scan(&expectedKline, &klineRows); err != nil {
		t.Fatal(err)
	}
	if expectedKline != 1 || klineRows != 48 {
		t.Fatalf("unexpected recomputed accounting: expected_kline_stocks=%d kline_rows=%d", expectedKline, klineRows)
	}
	// The manifest must carry the recomputed kline expectation and no stock
	// lane validation errors. (Industry/concept lanes are out of scope here.)
	var manifestErrors string
	if err := store.db.QueryRow(`SELECT validation_errors_json FROM daily_archive_manifest WHERE trade_date='2026-11-06'`).Scan(&manifestErrors); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(manifestErrors, "stock ") {
		t.Fatalf("stock lane validation errors remained after the purge: %s", manifestErrors)
	}
	var expectedStockKlineRows int
	if err := store.db.QueryRow(`SELECT expected_stock_kline_rows FROM daily_archive_manifest WHERE trade_date='2026-11-06'`).Scan(&expectedStockKlineRows); err != nil {
		t.Fatal(err)
	}
	if expectedStockKlineRows != 48 {
		t.Fatalf("manifest kline expectation was not recomputed: %d", expectedStockKlineRows)
	}
}
