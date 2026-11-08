package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/thsauth"
)

func TestServerTHSSessionStorageSelection(testingT *testing.T) {
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
	executable, err := os.Executable()
	if err != nil {
		testingT.Fatal(err)
	}
	for _, scenario := range []struct {
		name   string
		path   string
		expiry time.Time
		state  string
	}{
		{name: "database default", expiry: databaseExpiry, state: thsauth.StateReady},
		{name: "explicit file", path: sessionPath, expiry: fileExpiry, state: thsauth.StateReady},
		{name: "missing file", path: filepath.Join(directory, "missing.json"), state: thsauth.StateUnconfigured},
	} {
		testingT.Run(scenario.name, func(testingT *testing.T) {
			manager, err := newTHSAccountManager(config.Config{THSSessionFile: scenario.path, THSHeadlessBrowser: executable,
				THSUsername: "fixture-user", THSPassword: "fixture-secret"}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				testingT.Fatal(err)
			}
			defer manager.Close()
			status := manager.Status()
			if status.State != scenario.state {
				testingT.Fatalf("unexpected session state: %s", status.State)
			}
			if !scenario.expiry.IsZero() && (status.ExpiresAt == nil || !status.ExpiresAt.Equal(scenario.expiry)) {
				testingT.Fatal("server ignored its selected session storage")
			}
		})
	}
}
