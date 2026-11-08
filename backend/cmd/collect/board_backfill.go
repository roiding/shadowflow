package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/roiding/shadowflow/internal/collector"
	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/datasource/eastmoney"
	"github.com/roiding/shadowflow/internal/datasource/upstream"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
)

// collectBoardDailyCloseBackfill reconstructs missing industry and concept
// daily-close identity snapshots by fetching each board's daily kline history
// once (operator-run backfill for days the end-of-day lane missed).
func collectBoardDailyCloseBackfill(cfg config.Config, from, to string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	fromDate, err := time.ParseInLocation("2006-01-02", from, location)
	if err != nil || fromDate.Format("2006-01-02") != from {
		return fmt.Errorf("from must use YYYY-MM-DD")
	}
	toDate, err := time.ParseInLocation("2006-01-02", to, location)
	if err != nil || toDate.Format("2006-01-02") != to {
		return fmt.Errorf("to must use YYYY-MM-DD")
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
	client := eastmoney.NewClient(cfg.UpstreamBaseURL, upstreamResult.Client, cfg.PageSize).
		WithQuoteBaseURLs(cfg.QuoteBaseURLs).WithUpstreamGuard(upstreamResult.Guard)
	service := collector.New(client, store, logger)
	report, err := service.CollectBoardDailyCloseBackfill(ctx, fromDate, toDate)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
