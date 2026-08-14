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
	"github.com/roiding/shadowflow/internal/repository/sqlite"
)

func main() {
	var task, date, at string
	flag.StringVar(&task, "task", "", "task: boards, compact, daily-close, relations, or cleanup")
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
	store, err := sqlite.Open(cfg.DatabasePath)
	if err != nil {
		fatal(err)
	}
	defer store.Close()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	client := eastmoney.NewClient(cfg.UpstreamBaseURL, &http.Client{Timeout: cfg.RequestTimeout}, cfg.PageSize).
		WithQuoteBaseURLs(cfg.QuoteBaseURLs)
	service := collector.New(client, store, logger)
	timeout := 10 * time.Minute
	// A full industry/concept relationship scan visits roughly a thousand
	// boards. Keep the manual command aligned with the scheduled-job budget.
	if task == "relations" {
		timeout = 45 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	switch task {
	case "boards":
		if err := service.CollectBoards(ctx, snapshotAt); err != nil {
			fatal(err)
		}
	case "compact":
		summaries, err := service.CompactAndCleanup(ctx, date)
		if err != nil {
			fatal(err)
		}
		for _, summary := range summaries {
			logger.Info("compaction completed", "rank_type", summary.RankType, "minutes", summary.CollectedMinutes,
				"research_points", summary.CollectedResearch, "daily_close_points", summary.CollectedDailyClose)
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
		if err := store.CleanupIntraday(ctx, date); err != nil {
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
