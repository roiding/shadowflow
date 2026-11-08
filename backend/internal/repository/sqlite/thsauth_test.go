package sqlite

import (
	"context"
	"errors"
	"net/http"
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
	if loaded[0].Name != "v" || loaded[0].Value != "token" || loaded[0].HttpOnly != true || loaded[0].Expires.IsZero() {
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

func TestThsAccountDataPayloadsUsesCurrentIdentitySet(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	fetchedAt := time.Date(2026, 11, 7, 8, 50, 0, 0, time.UTC)
	if err := store.SaveThsAccountData(ctx, "2026-11-07", "33:002578",
		`{"code":"33:002578","values":[]}`, fetchedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveThsAccountData(ctx, "2026-11-07", "17:600519",
		`{"code":"17:600519","values":[]}`, fetchedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveThsAccountData(ctx, "2026-11-06", "33:300750",
		`{"code":"33:300750","values":[]}`, fetchedAt); err != nil {
		t.Fatal(err)
	}
	payloads, err := store.ThsAccountDataPayloads(ctx, "2026-11-07", []string{"33:002578", "33:300750"})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || payloads["33:002578"] != `{"code":"33:002578","values":[]}` {
		t.Fatalf("payload lookup leaked another date or identity: %#v", payloads)
	}
	if _, err := store.ThsAccountDataPayloads(ctx, "2026-11-07", []string{"33:002578", "33:002578"}); !errors.Is(err, ErrInvalidThsState) {
		t.Fatalf("duplicate identity was accepted: %v", err)
	}
	if _, err := store.ThsAccountDataPayloads(ctx, "2026-11-07", []string{"36:830799"}); !errors.Is(err, ErrInvalidThsState) {
		t.Fatalf("unsupported identity was accepted: %v", err)
	}
}
