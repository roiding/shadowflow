package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/roiding/shadowflow/internal/collector"
	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/datasource/eastmoney"
	"github.com/roiding/shadowflow/internal/datasource/upstream"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
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
	var task, date, at, scope, codes, from, to string
	flag.StringVar(&task, "task", "", "task: boards, end-of-day, stock-kline, stock-kline-range, daily-close, relations, limit-up, ths-focus, ths-login, ths-auth-status, ths-account-data, cleanup, maintenance, analytics, or is-trading-day")
	flag.StringVar(&from, "from", "", "range start date YYYY-MM-DD for stock-kline-range")
	flag.StringVar(&to, "to", "", "range end date YYYY-MM-DD for stock-kline-range")
	flag.StringVar(&date, "date", "", "trade date in YYYY-MM-DD")
	flag.StringVar(&at, "at", "15:00", "snapshot time in HH:MM for boards")
	flag.StringVar(&scope, "scope", "HS,GEM2STAR", "market scope for limit-up: HS,GEM2STAR or HS,GEM2STAR,ST,NEW")
	flag.StringVar(&codes, "codes", "", "comma-separated THS market:code identities for ths-account-data (maximum 20)")
	flag.Parse()
	if task == "ths-login" || task == "ths-auth-status" || task == "ths-account-data" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		var store *sqlite.Store
		if cfg.THSSessionFile == "" || task == "ths-account-data" {
			store, err = sqlite.OpenWithReadConns(cfg.DatabasePath, cfg.SQLiteReadConns)
			if err != nil {
				return err
			}
			defer store.Close()
		}
		return collectTHSAccount(cfg, store, task, date, codes)
	}
	if task == "stock-kline-range" {
		if from == "" || to == "" {
			flag.Usage()
			os.Exit(2)
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return collectStockKlineRange(cfg, from, to)
	}
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
	if task == "is-trading-day" {
		path := os.Getenv("SHADOWFLOW_CALENDAR_PATH")
		if path == "" {
			path = "./config/trading_calendar.json"
		}
		calendar, err := tradingcalendar.LoadRequired(path)
		if err != nil {
			return err
		}
		fmt.Println(calendar.IsTradingDay(tradeDate))
		return nil
	}
	snapshotAt, err := time.ParseInLocation("2006-01-02 15:04", date+" "+at, location)
	if err != nil {
		return fmt.Errorf("at must use HH:MM")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if task == "limit-up" {
		return collectLimitUp(cfg, date, scope)
	}
	if task == "ths-focus" {
		return collectThsFocus(cfg, date, scope)
	}
	store, err := sqlite.OpenWithReadConns(cfg.DatabasePath, cfg.SQLiteReadConns)
	if err != nil {
		return err
	}
	defer store.Close()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), cfg.ProxyPoolRequestTimeout)
	upstreamResult, err := upstream.NewClient(bootstrapCtx, cfg, logger)
	bootstrapCancel()
	if err != nil {
		return err
	}
	if upstreamResult.Pool != nil {
		defer upstreamResult.Pool.Stop()
	}
	upstreamClient := upstreamResult.Client
	client := eastmoney.NewClient(cfg.UpstreamBaseURL, upstreamClient, cfg.PageSize).
		WithQuoteBaseURLs(cfg.QuoteBaseURLs).
		WithUpstreamGuard(upstreamResult.Guard)
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
	} else if task == "stock-kline" || task == "analytics" || task == "stock-kline-range" {
		// Historical analysis repairs may rebuild up to sixty daily windows.
		timeout = 90 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	switch task {
	case "boards":
		return service.CollectBoards(ctx, snapshotAt)
	case "end-of-day":
		runAt := time.Date(tradeDate.Year(), tradeDate.Month(), tradeDate.Day(), 16, 30, 0, 0, location)
		return service.CollectEndOfDay(ctx, runAt)
	case "stock-kline":
		runAt := time.Date(tradeDate.Year(), tradeDate.Month(), tradeDate.Day(), 16, 45, 0, 0, location)
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
