package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/roiding/shadowflow/internal/tradingcalendar"
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

// collectThsFocusRange backfills the limit-up chart categories for trading
// days in a bounded range. It is resumable: days that already contain the
// tab_list/all_tab_data snapshot are skipped. A failed day does not stop the
// remaining range, and the command exits non-zero after reporting all failures.
func collectThsFocusRange(cfg config.Config, from, to, scope string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Minute)
	defer cancel()
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	fromDate, err := time.ParseInLocation("2006-01-02", from, location)
	if err != nil || fromDate.Format("2006-01-02") != from {
		return fmt.Errorf("from must use YYYY-MM-DD")
	}
	toDate, err := time.ParseInLocation("2006-01-02", to, location)
	if err != nil || toDate.Format("2006-01-02") != to {
		return fmt.Errorf("to must use YYYY-MM-DD")
	}
	if toDate.Before(fromDate) {
		return fmt.Errorf("to must not precede from")
	}
	if toDate.Sub(fromDate) > 366*24*time.Hour {
		return fmt.Errorf("THS focus backfill range must be within 366 days")
	}
	calendarPath := os.Getenv("SHADOWFLOW_CALENDAR_PATH")
	if calendarPath == "" {
		calendarPath = "./config/trading_calendar.json"
	}
	calendar, err := tradingcalendar.LoadRequired(calendarPath)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	store, err := sqlite.OpenWithReadConns(cfg.DatabasePath, cfg.SQLiteReadConns)
	if err != nil {
		return err
	}
	defer store.Close()
	service := collector.New(nil, store, slog.New(slog.NewJSONHandler(os.Stderr, nil))).
		WithThsFocusSource(thsfocus.NewClient("", client), scope)

	var failures []error
	for day := fromDate; !day.After(toDate); day = day.AddDate(0, 0, 1) {
		if !calendar.IsTradingDay(day) {
			continue
		}
		date := day.Format("2006-01-02")
		existing, readError := store.ReadThsFocus(ctx, date, scope)
		if readError != nil {
			failures = append(failures, fmt.Errorf("%s read existing snapshot: %w", date, readError))
			continue
		}
		if existing != nil && existing.Tabs != nil {
			fmt.Fprintf(os.Stderr, "ths-focus-range skip %s: tab archive already present\n", date)
			continue
		}
		runAt := time.Date(day.Year(), day.Month(), day.Day(), 16, 0, 0, 0, location)
		if collectError := service.CollectThsFocus(ctx, runAt); collectError != nil {
			failures = append(failures, fmt.Errorf("%s: %w", date, collectError))
			fmt.Fprintf(os.Stderr, "ths-focus-range failed %s: %v\n", date, collectError)
			continue
		}
		snapshot, readError := store.ReadThsFocus(ctx, date, scope)
		if readError != nil {
			failures = append(failures, fmt.Errorf("%s read back: %w", date, readError))
			continue
		}
		if snapshot == nil {
			failures = append(failures, fmt.Errorf("%s: archive missing after save", date))
			continue
		}
		stockCount := 0
		for _, tab := range snapshot.Tabs {
			stockCount += len(tab.Stocks)
		}
		fmt.Fprintf(os.Stdout, "{\"trade_date\":%q,\"tabs\":%d,\"tab_stock_rows\":%d,\"tab_date_verified\":%t}\n",
			date, len(snapshot.Tabs), stockCount, snapshot.TabDateVerified)
	}
	return errors.Join(failures...)
}
