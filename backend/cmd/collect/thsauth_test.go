package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/thsauth"
)

func TestCollectTHSSessionStorageSelection(testingT *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer store.Close()
	databaseExpiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	fileExpiry := databaseExpiry.Add(time.Hour)
	encode := func(expiry time.Time) string {
		payload, err := thsauth.EncodeSessionCookies([]*http.Cookie{{Name: "ticket", Value: "fixture-session",
			Domain: ".10jqka.com.cn", Path: "/", Secure: true, Expires: expiry}})
		if err != nil {
			testingT.Fatal(err)
		}
		return payload
	}
	if err := store.StoreThsSession(context.Background(), encode(databaseExpiry)); err != nil {
		testingT.Fatal(err)
	}
	directory := testingT.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		testingT.Fatal(err)
	}
	sessionPath := filepath.Join(directory, "session.json")
	if err := os.WriteFile(sessionPath, []byte(encode(fileExpiry)), 0o600); err != nil {
		testingT.Fatal(err)
	}
	for _, scenario := range []struct {
		name   string
		path   string
		store  *sqlite.Store
		expiry time.Time
		state  string
	}{
		{name: "database default", store: store, expiry: databaseExpiry, state: thsauth.StateReady},
		{name: "file overrides database", path: sessionPath, store: store, expiry: fileExpiry, state: thsauth.StateReady},
		{name: "file without database", path: sessionPath, expiry: fileExpiry, state: thsauth.StateReady},
		{name: "missing explicit file does not use database", path: filepath.Join(directory, "missing.json"), store: store, state: thsauth.StateUnconfigured},
	} {
		testingT.Run(scenario.name, func(testingT *testing.T) {
			manager, err := newTHSAccountManager(config.Config{THSSessionFile: scenario.path}, scenario.store)
			if err != nil {
				testingT.Fatal(err)
			}
			defer manager.Close()
			status := manager.Status()
			if status.State != scenario.state {
				testingT.Fatalf("unexpected session state: %s", status.State)
			}
			if !scenario.expiry.IsZero() && (status.ExpiresAt == nil || !status.ExpiresAt.Equal(scenario.expiry)) {
				testingT.Fatal("session was loaded from the wrong storage backend")
			}
		})
	}
}
