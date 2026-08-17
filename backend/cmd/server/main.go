package main

import (
	"context"
	"crypto/tls"
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
	schedulerService, err := scheduler.New(collectorService, calendar, logger, scheduler.Options{
		SuccessRunRetentionDays: cfg.SuccessRunRetentionDays,
		FailureRunRetentionDays: cfg.FailureRunRetentionDays,
	})
	if err != nil {
		logger.Error("create scheduler", "error", err)
		os.Exit(1)
	}
	apiServer, err := api.New(store, calendar, logger, api.Options{StaticDir: cfg.StaticDir, QuoteSource: client})
	if err != nil {
		logger.Error("create API", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.CalendarAutoUpdate {
		go runCalendarUpdater(ctx, calendar, cfg, logger)
	} else {
		logger.Info("trading calendar auto-update disabled")
	}
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

func runCalendarUpdater(ctx context.Context, calendar *tradingcalendar.Calendar, cfg config.Config, logger *slog.Logger) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 30 * time.Second
	transport.ForceAttemptHTTP2 = false
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport, Timeout: 45 * time.Second}
	refresh := func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		updated, err := calendar.RefreshIfNeeded(refreshCtx, client,
			cfg.CalendarPath, cfg.CalendarSourceURL, time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)),
			cfg.CalendarRefreshLeadDays)
		if err != nil {
			logger.Warn("refresh trading calendar", "error", err, "coverage", calendar.Coverage(time.Now()))
			return
		}
		if updated {
			logger.Info("trading calendar updated", "coverage", calendar.Coverage(time.Now()))
		}
	}
	refresh()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}
