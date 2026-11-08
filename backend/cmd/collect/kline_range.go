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

// collectStockKlineRange patches every missing 5-minute kline between two
// dates by fetching each stock ONCE for the whole range (operator-run backfill).
func collectStockKlineRange(cfg config.Config, from, to string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Hour)
	defer cancel()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	fromDate, err := time.ParseInLocation("2006-01-02", from, location)
	if err != nil || fromDate.Format("2006-01-02") != from {
		return fmt.Errorf("from must use YYYY-MM-DD")
	}
	toDate, err := time.ParseInLocation("2006-01-02 15:04", to+" 15:04", location)
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
	report, err := service.CollectStockKlineRangePatch(ctx, fromDate, toDate)
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
