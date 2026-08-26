package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/roiding/shadowflow/internal/collector"
	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/datasource/eastmoney"
	"github.com/roiding/shadowflow/internal/datasource/upstream"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
)

// main only translates run's outcome into an exit code: calling os.Exit
// anywhere deeper would skip the deferred store.Close and leave the SQLite
// WAL uncheckpointed.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var task, date, at string
	flag.StringVar(&task, "task", "", "task: boards, end-of-day, stock-kline, daily-close, relations, cleanup, maintenance, or analytics")
	flag.StringVar(&date, "date", "", "trade date in YYYY-MM-DD")
	flag.StringVar(&at, "at", "15:00", "snapshot time in HH:MM for boards")
	flag.Parse()
	if task == "" || date == "" {
		flag.Usage()
		os.Exit(2)
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	tradeDate, err := time.ParseInLocation("2006-01-02", date, location)
	if err != nil || tradeDate.Format("2006-01-02") != date {
		return fmt.Errorf("date must use YYYY-MM-DD")
	}
	snapshotAt, err := time.ParseInLocation("2006-01-02 15:04", date+" "+at, location)
	if err != nil {
		return fmt.Errorf("at must use HH:MM")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	store, err := sqlite.OpenWithReadConns(cfg.DatabasePath, cfg.SQLiteReadConns)
	if err != nil {
		return err
	}
	defer store.Close()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = cfg.UpstreamMaxConcurrency * 4
	transport.MaxIdleConnsPerHost = cfg.UpstreamMaxConcurrency
	upstreamClient := &http.Client{Transport: transport, Timeout: cfg.RequestTimeout}
	client := eastmoney.NewClient(cfg.UpstreamBaseURL, upstreamClient, cfg.PageSize).
		WithQuoteBaseURLs(cfg.QuoteBaseURLs).
		WithUpstreamGuard(upstream.New(upstreamClient, upstream.Options{
			MaxConcurrency: cfg.UpstreamMaxConcurrency, RatePerSecond: cfg.UpstreamRatePerSecond,
		}))
	service := collector.New(client, store, logger)
	timeout := 10 * time.Minute
	// A full industry/concept relationship scan visits roughly a thousand
	// boards. Keep the manual command aligned with the scheduled-job budget.
	if task == "relations" {
		timeout = 45 * time.Minute
	} else if task == "end-of-day" {
		// The full archive is intentionally sequential to avoid saturating the
		// upstream guard and SQLite writer.
		timeout = 90 * time.Minute
	} else if task == "stock-kline" {
		timeout = 90 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	switch task {
	case "boards":
		return service.CollectBoards(ctx, snapshotAt)
	case "end-of-day":
		runAt := time.Date(tradeDate.Year(), tradeDate.Month(), tradeDate.Day(), 16, 0, 0, 0, location)
		return service.CollectEndOfDay(ctx, runAt)
	case "stock-kline":
		runAt := time.Date(tradeDate.Year(), tradeDate.Month(), tradeDate.Day(), 16, 15, 0, 0, location)
		return service.CollectStockKlines(ctx, runAt)
	case "daily-close":
		closeAt := time.Date(tradeDate.Year(), tradeDate.Month(), tradeDate.Day(), 15, 0, 0, 0, location)
		return service.CollectDailyClose(ctx, closeAt)
	case "relations":
		return service.CollectStockBoardRelations(ctx, date)
	case "cleanup":
		return service.CleanupArchivedIntraday(ctx, date)
	case "maintenance":
		_, err := service.Maintain(ctx, tradeDate, cfg.SuccessRunRetentionDays, cfg.FailureRunRetentionDays)
		return err
	case "analytics":
		manifest, err := store.ArchiveManifest(ctx, date)
		if err != nil {
			return err
		}
		if manifest.CurrentRevisionID == "" {
			return fmt.Errorf("no current complete archive revision for %s", date)
		}
		return store.RebuildAnalytics(ctx, manifest.CurrentRevisionID)
	default:
		return fmt.Errorf("unknown task %q", task)
	}
}
