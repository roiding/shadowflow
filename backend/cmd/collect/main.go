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

func main() {
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
		fatal(err)
	}
	tradeDate, err := time.ParseInLocation("2006-01-02", date, location)
	if err != nil || tradeDate.Format("2006-01-02") != date {
		fatal(fmt.Errorf("date must use YYYY-MM-DD"))
	}
	snapshotAt, err := time.ParseInLocation("2006-01-02 15:04", date+" "+at, location)
	if err != nil {
		fatal(fmt.Errorf("at must use HH:MM"))
	}

	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	store, err := sqlite.OpenWithReadConns(cfg.DatabasePath, cfg.SQLiteReadConns)
	if err != nil {
		fatal(err)
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
	} else if task == "stock-kline" {
		timeout = 90 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	switch task {
	case "boards":
		if err := service.CollectBoards(ctx, snapshotAt); err != nil {
			fatal(err)
		}
	case "end-of-day":
		runAt := time.Date(tradeDate.Year(), tradeDate.Month(), tradeDate.Day(), 16, 0, 0, 0, location)
		if err := service.CollectEndOfDay(ctx, runAt); err != nil {
			fatal(err)
		}
	case "stock-kline":
		runAt := time.Date(tradeDate.Year(), tradeDate.Month(), tradeDate.Day(), 16, 15, 0, 0, location)
		if err := service.CollectStockKlines(ctx, runAt); err != nil {
			fatal(err)
		}
	case "daily-close":
		closeAt := time.Date(tradeDate.Year(), tradeDate.Month(), tradeDate.Day(), 15, 0, 0, 0, location)
		if err := service.CollectDailyClose(ctx, closeAt); err != nil {
			fatal(err)
		}
	case "relations":
		if err := service.CollectStockBoardRelations(ctx, date); err != nil {
			fatal(err)
		}
	case "cleanup":
		if err := service.CleanupArchivedIntraday(ctx, date); err != nil {
			fatal(err)
		}
	case "maintenance":
		if _, err := service.Maintain(ctx, tradeDate, cfg.SuccessRunRetentionDays, cfg.FailureRunRetentionDays); err != nil {
			fatal(err)
		}
	case "analytics":
		manifest, err := store.ArchiveManifest(ctx, date)
		if err != nil {
			fatal(err)
		}
		if manifest.CurrentRevisionID == "" {
			fatal(fmt.Errorf("no current complete archive revision for %s", date))
		}
		if err := store.RebuildAnalytics(ctx, manifest.CurrentRevisionID); err != nil {
			fatal(err)
		}
	default:
		fatal(fmt.Errorf("unknown task %q", task))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
