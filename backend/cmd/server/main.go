package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	// Embed tzdata: the binary is built with CGO_ENABLED=0 and must not
	// depend on the host zoneinfo database for Asia/Shanghai.
	_ "time/tzdata"

	"github.com/roiding/shadowflow/internal/api"
	"github.com/roiding/shadowflow/internal/collector"
	"github.com/roiding/shadowflow/internal/config"
	"github.com/roiding/shadowflow/internal/datasource/eastmoney"
	"github.com/roiding/shadowflow/internal/datasource/ths"
	"github.com/roiding/shadowflow/internal/datasource/thsdataq"
	"github.com/roiding/shadowflow/internal/datasource/thsfocus"
	"github.com/roiding/shadowflow/internal/datasource/upstream"
	"github.com/roiding/shadowflow/internal/quote"
	"github.com/roiding/shadowflow/internal/repository/sqlite"
	"github.com/roiding/shadowflow/internal/scheduler"
	"github.com/roiding/shadowflow/internal/thsauth"
	"github.com/roiding/shadowflow/internal/thsheadless"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	if err := validateListenSecurity(cfg.ListenAddr, cfg.APIToken); err != nil {
		logger.Error("invalid security configuration", "error", err)
		os.Exit(1)
	}
	if err := validateStaticDir(cfg.StaticDir); err != nil {
		logger.Error("invalid static directory", "error", err)
		os.Exit(1)
	}
	store, err := sqlite.OpenWithCold(cfg.DatabasePath, cfg.ColdDatabasePath, cfg.SQLiteReadConns)
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
	// A missing or expired calendar silently degrades IsTradingDay to plain
	// weekday logic, which makes the scheduler collect on exchange holidays.
	if coverage := calendar.Coverage(time.Now()); coverage.Expired {
		logger.Warn("trading calendar is missing or expired; falling back to weekday-only trading detection", "coverage", coverage)
	}
	bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), cfg.ProxyPoolRequestTimeout)
	upstreamResult, err := upstream.NewClient(bootstrapCtx, cfg, logger)
	bootstrapCancel()
	if err != nil {
		logger.Error("create upstream client", "error", err)
		os.Exit(1)
	}
	upstreamClient, guard, proxyPool := upstreamResult.Client, upstreamResult.Guard, upstreamResult.Pool
	if proxyPool != nil {
		defer proxyPool.Stop()
	}
	client := eastmoney.NewClient(cfg.UpstreamBaseURL, upstreamClient, cfg.PageSize).
		WithQuoteBaseURLs(cfg.QuoteBaseURLs).WithUpstreamGuard(guard)
	thsClient := ths.NewClient(ths.DefaultBaseURL, &http.Client{Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }})
	collectorService := collector.New(client, store, logger).WithAllowMissingQuotes(cfg.AllowMissingQuotes).
		WithLimitUpSource(thsClient, cfg.LimitUpScope).
		WithThsFocusSource(thsfocus.NewClient("", &http.Client{Timeout: 20 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}), cfg.LimitUpScope)
	// The restricted THS dataset lane needs an authenticated account session;
	// it is wired only when headless THS login is enabled and the archive lane
	// it depends on is scheduled. The manager refreshes the session (including
	// a headless browser login) on demand when the stored state is rejected.
	if cfg.THSAutoLogin && (cfg.THSUsername != "" || cfg.THSCredentialsFile != "") && cfg.THSHeadlessBrowser == "" {
		logger.Error("THS auto-login requires SHADOWFLOW_THS_HEADLESS_BROWSER when credentials are configured")
		os.Exit(1)
	}
	if cfg.THSAutoLogin && cfg.THSUsername == "" && cfg.THSCredentialsFile == "" {
		logger.Warn("THS auto-login has no credentials configured; only a stored session can be used until it expires")
	}
	// The lane requires the full login capability: credentials plus a headless
	// browser. Session-only deployments (database reuse without credentials)
	// still serve the collect CLI but cannot run the scheduled restricted lane.
	thsAccountDataEnabled := false
	if cfg.THSAutoLogin && cfg.LimitUpArchiveEnabled && cfg.THSHeadlessBrowser != "" &&
		(cfg.THSUsername != "" || cfg.THSCredentialsFile != "") {
		manager, managerErr := newTHSAccountManager(cfg, store, logger)
		if managerErr != nil {
			logger.Error("create THS account manager", "error", managerErr)
			os.Exit(1)
		}
		defer manager.Close()
		collectorService = collectorService.WithThsAccountSource(thsdataq.NewClient(manager))
		thsAccountDataEnabled = true
	}
	disabledKinds := make(map[string]struct{}, len(cfg.SchedulerDisabledKinds))
	for _, kind := range cfg.SchedulerDisabledKinds {
		if kind = strings.TrimSpace(kind); kind != "" {
			disabledKinds[kind] = struct{}{}
		}
	}
	schedulerService, err := scheduler.New(collectorService, calendar, logger, scheduler.Options{
		SuccessRunRetentionDays: cfg.SuccessRunRetentionDays,
		FailureRunRetentionDays: cfg.FailureRunRetentionDays,
		Jobs:                    store,
		DisabledKinds:           disabledKinds,
		ProxyPrewarmEnabled:     proxyPool != nil,
		LimitUpArchiveEnabled:   cfg.LimitUpArchiveEnabled,
		THSAccountDataEnabled:   thsAccountDataEnabled,
	})
	if err != nil {
		logger.Error("create scheduler", "error", err)
		os.Exit(1)
	}
	if len(disabledKinds) > 0 {
		kinds := make([]string, 0, len(disabledKinds))
		for kind := range disabledKinds {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		logger.Info("scheduler job kinds disabled", "kinds", kinds)
	}
	_, eventDisabled := disabledKinds["end-of-day-events"]
	_, postCloseDisabled := disabledKinds["end-of-day"]
	eventsScheduled := cfg.SchedulerEnabled && cfg.LimitUpArchiveEnabled && !eventDisabled && !postCloseDisabled
	apiServer, err := api.New(store, calendar, logger, api.Options{StaticDir: cfg.StaticDir, QuoteSource: quote.NewCache(client, logger), APIToken: cfg.APIToken, NormalRatePerMinute: cfg.NormalRatePerMinute, ExportRatePerMinute: cfg.ExportRatePerMinute, ScanRatePerMinute: cfg.ScanRatePerMinute, TrustedProxyCIDR: cfg.TrustedProxyCIDR, ProxyPool: proxyPool, LimitUpArchiveScheduled: eventsScheduled, LimitUpScope: cfg.LimitUpScope, LimitUpIntradaySource: thsClient})
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
	var schedulerDone chan struct{}
	if cfg.SchedulerEnabled {
		schedulerDone = make(chan struct{})
		go func() {
			defer close(schedulerDone)
			schedulerService.Run(ctx)
		}()
	} else {
		logger.Info("scheduler disabled")
	}

	server := &http.Server{
		Addr: cfg.ListenAddr, Handler: apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		// Bounds how long a client may dribble a request body (the focus/scan
		// POST); http.TimeoutHandler only bounds handler execution, not reads.
		ReadTimeout:    30 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 64 << 10,
		// No WriteTimeout: CSV exports stream for up to 120s; slow readers are
		// bounded by the per-request context checks in the export loops.
	}
	listenFailed := make(chan struct{}, 1)
	go func() {
		logger.Info("server started", "addr", cfg.ListenAddr, "database", cfg.DatabasePath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			listenFailed <- struct{}{}
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown", "error", err)
	}
	// Let in-flight scheduler jobs persist their final status before the
	// deferred store.Close() runs; Run waits on its own job WaitGroup.
	if schedulerDone != nil {
		select {
		case <-schedulerDone:
		case <-time.After(30 * time.Second):
			logger.Warn("scheduler did not stop in time; closing store anyway")
		}
	}
	select {
	case <-listenFailed:
		store.Close()
		os.Exit(1)
	default:
	}
}

// newTHSAccountManager builds the long-lived THS login manager backed by the
// database session store and the headless browser risk-token provider.
func newTHSAccountManager(cfg config.Config, store *sqlite.Store, logger *slog.Logger) (*thsauth.Manager, error) {
	var source thsauth.CredentialSource
	if cfg.THSUsername != "" {
		source = thsauth.EnvCredentials(cfg.THSUsername, cfg.THSPassword)
	} else {
		source = thsauth.FileCredentials(cfg.THSCredentialsFile)
	}
	browser, err := thsheadless.New(thsheadless.Options{
		Executable: cfg.THSHeadlessBrowser,
		Timeout:    30 * time.Second,
		ChromeArgs: strings.Fields(cfg.THSChromeArgs),
	})
	if err != nil {
		return nil, err
	}
	options := thsauth.Options{
		SessionFile:    cfg.THSSessionFile,
		AutoLogin:      true,
		Login:          thsauth.PasswordLoginWithRiskProviderSource(source, browser),
		RequestTimeout: 20 * time.Second,
		RefreshTimeout: 120 * time.Second,
		Cooldown:       5 * time.Minute,
	}
	if cfg.THSSessionFile == "" {
		options.Session = store.THSSessionStore()
	}
	manager, err := thsauth.New(options)
	if err != nil {
		return nil, err
	}
	logger.Info("THS account lane enabled",
		"credentials", map[bool]string{true: "environment", false: "file"}[cfg.THSUsername != ""],
		"browser", cfg.THSHeadlessBrowser)
	return manager, nil
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

// validateStaticDir refuses obviously dangerous static roots: the containment
// check in mountStatic is relative to this directory, so serving "/" would
// expose the whole filesystem.
func validateStaticDir(directory string) error {
	if directory == "" {
		return nil
	}
	cleaned := filepath.Clean(directory)
	if cleaned == "/" || cleaned == filepath.VolumeName(cleaned)+string(os.PathSeparator) {
		return fmt.Errorf("SHADOWFLOW_STATIC_DIR must not be the filesystem root")
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		return fmt.Errorf("SHADOWFLOW_STATIC_DIR: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("SHADOWFLOW_STATIC_DIR %s is not a directory", cleaned)
	}
	return nil
}

func validateListenSecurity(addr, token string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse listen address: %w", err)
	}
	if host == "" {
		return fmt.Errorf("listen host must be explicit; use 127.0.0.1 for local access")
	}
	if token == "" {
		return nil
	}
	if len(token) < 16 {
		return fmt.Errorf("SHADOWFLOW_API_TOKEN must contain at least 16 characters when authentication is enabled")
	}
	return nil
}
