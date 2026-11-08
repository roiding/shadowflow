package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/roiding/shadowflow/internal/collector"
	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/datasource/thsfocus"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
)

func collectThsFocus(cfg config.Config, date, scope string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	store, err := sqlite.OpenWithReadConns(cfg.DatabasePath, cfg.SQLiteReadConns)
	if err != nil {
		return err
	}
	defer store.Close()
	service := collector.New(nil, store, slog.New(slog.NewJSONHandler(os.Stderr, nil))).
		WithThsFocusSource(thsfocus.NewClient("", client), scope)
	runAt, err := time.ParseInLocation("2006-01-02 15:04", date+" 16:00", time.FixedZone("Asia/Shanghai", 8*60*60))
	if err != nil {
		return err
	}
	if err := service.CollectThsFocus(ctx, runAt); err != nil {
		return err
	}
	snapshot, err := store.ReadThsFocus(ctx, date, scope)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(snapshot)
}
