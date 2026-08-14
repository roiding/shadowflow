package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/roiding/shadowflow/internal/api"
	"github.com/roiding/shadowflow/internal/collector"
	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/datasource/eastmoney"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/scheduler"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	store, err := sqlite.Open(cfg.DatabasePath)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	calendar, err := tradingcalendar.Load(cfg.CalendarPath)
	if err != nil {
		logger.Error("load calendar", "error", err)
		os.Exit(1)
	}
	client := eastmoney.NewClient(cfg.UpstreamBaseURL, &http.Client{Timeout: cfg.RequestTimeout}, cfg.PageSize).
		WithQuoteBaseURLs(cfg.QuoteBaseURLs)
	collectorService := collector.New(client, store, logger)
	schedulerService, err := scheduler.New(collectorService, calendar, logger)
	if err != nil {
		logger.Error("create scheduler", "error", err)
		os.Exit(1)
	}
	apiServer, err := api.New(store, calendar, logger, api.Options{StaticDir: cfg.StaticDir})
	if err != nil {
		logger.Error("create API", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.SchedulerEnabled {
		go schedulerService.Run(ctx)
	} else {
		logger.Info("scheduler disabled")
	}

	server := &http.Server{Addr: cfg.ListenAddr, Handler: apiServer.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		logger.Info("server started", "addr", cfg.ListenAddr, "database", cfg.DatabasePath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown", "error", err)
	}
}
