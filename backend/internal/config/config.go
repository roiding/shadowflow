package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr              string
	DatabasePath            string
	ColdDatabasePath        string
	CalendarPath            string
	CalendarAutoUpdate      bool
	CalendarSourceURL       string
	CalendarRefreshLeadDays int
	UpstreamBaseURL         string
	QuoteBaseURLs           []string
	PageSize                int
	RequestTimeout          time.Duration
	StaticDir               string
	SchedulerEnabled        bool
	LimitUpArchiveEnabled   bool
	LimitUpScope            string
	// SchedulerDisabledKinds turns individual scheduled job kinds off while
	// the scheduler itself stays enabled, so a blocked upstream no longer
	// drags unrelated collection lanes down with it.
	SchedulerDisabledKinds  []string
	AllowMissingQuotes      bool
	SuccessRunRetentionDays int
	FailureRunRetentionDays int
	APIToken                string
	NormalRatePerMinute     int
	ExportRatePerMinute     int
	ScanRatePerMinute       int
	TrustedProxyCIDR        string
	UpstreamMaxConcurrency  int
	UpstreamRatePerSecond   float64
	ProxyPoolURL            string
	ProxyPoolToken          string
	ProxyPoolRefreshEvery   time.Duration
	ProxyPoolRequestTimeout time.Duration
	ProxyPoolMaxAttempts    int
	ProxyPoolFailureLimit   int
	ProxyPoolCooldown       time.Duration
	ProxyPoolDirectFallback bool
	ProxyPoolProtocols      []string
	ProxyPoolStrategy       string
	ProxyPoolMaxProxies     int
	ProxyPoolPrewarmCount   int
	ProxyPoolPrewarmTimeout time.Duration
	ProxyPoolPollEvery      time.Duration
	ProxyPoolAcquireTimeout time.Duration
	ProxyPoolSourceIDs      []string
	SQLiteReadConns         int
}

func Load() (Config, error) {
	pageSize, err := envInt("SHADOWFLOW_PAGE_SIZE", 100)
	if err != nil {
		return Config{}, err
	}
	timeoutSeconds, err := envInt("SHADOWFLOW_REQUEST_TIMEOUT_SECONDS", 5)
	if err != nil {
		return Config{}, err
	}
	schedulerEnabled, err := envBool("SHADOWFLOW_SCHEDULER_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	limitUpArchiveEnabled, err := envBool("SHADOWFLOW_LIMIT_UP_ARCHIVE_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	limitUpScope := env("SHADOWFLOW_LIMIT_UP_SCOPE", "HS,GEM2STAR")
	if limitUpScope != "HS,GEM2STAR" && limitUpScope != "HS,GEM2STAR,ST,NEW" {
		return Config{}, fmt.Errorf("SHADOWFLOW_LIMIT_UP_SCOPE must be HS,GEM2STAR or HS,GEM2STAR,ST,NEW")
	}
	allowMissingQuotes, err := envBool("SHADOWFLOW_ALLOW_MISSING_QUOTES", false)
	if err != nil {
		return Config{}, err
	}
	calendarAutoUpdate, err := envBool("SHADOWFLOW_CALENDAR_AUTO_UPDATE", true)
	if err != nil {
		return Config{}, err
	}
	calendarRefreshLeadDays, err := envInt("SHADOWFLOW_CALENDAR_REFRESH_LEAD_DAYS", 45)
	if err != nil {
		return Config{}, err
	}
	successRetentionDays, err := envInt("SHADOWFLOW_SUCCESS_RUN_RETENTION_DAYS", 30)
	if err != nil {
		return Config{}, err
	}
	failureRetentionDays, err := envInt("SHADOWFLOW_FAILURE_RUN_RETENTION_DAYS", 180)
	if err != nil {
		return Config{}, err
	}
	normalRate, err := envInt("SHADOWFLOW_RATE_LIMIT_PER_MINUTE", 120)
	if err != nil {
		return Config{}, err
	}
	exportRate, err := envInt("SHADOWFLOW_EXPORT_RATE_LIMIT_PER_MINUTE", 10)
	if err != nil {
		return Config{}, err
	}
	scanRate, err := envInt("SHADOWFLOW_SCAN_RATE_LIMIT_PER_MINUTE", 30)
	if err != nil {
		return Config{}, err
	}
	upstreamConcurrency, err := envInt("SHADOWFLOW_UPSTREAM_MAX_CONCURRENCY", 4)
	if err != nil {
		return Config{}, err
	}
	upstreamRate, err := envFloat("SHADOWFLOW_UPSTREAM_RATE_PER_SECOND", 8)
	if err != nil {
		return Config{}, err
	}
	proxyPoolRefreshSeconds, err := envInt("SHADOWFLOW_PROXY_POOL_REFRESH_SECONDS", 300)
	if err != nil {
		return Config{}, err
	}
	proxyPoolRequestTimeoutSeconds, err := envInt("SHADOWFLOW_PROXY_POOL_REQUEST_TIMEOUT_SECONDS", 5)
	if err != nil {
		return Config{}, err
	}
	proxyPoolMaxAttempts, err := envInt("SHADOWFLOW_PROXY_POOL_MAX_ATTEMPTS", 3)
	if err != nil {
		return Config{}, err
	}
	proxyPoolFailureLimit, err := envInt("SHADOWFLOW_PROXY_POOL_FAILURE_LIMIT", 2)
	if err != nil {
		return Config{}, err
	}
	proxyPoolCooldownSeconds, err := envInt("SHADOWFLOW_PROXY_POOL_COOLDOWN_SECONDS", 300)
	if err != nil {
		return Config{}, err
	}
	proxyPoolDirectFallback, err := envBool("SHADOWFLOW_PROXY_POOL_DIRECT_FALLBACK", false)
	if err != nil {
		return Config{}, err
	}
	proxyPoolMaxProxies, err := envInt("SHADOWFLOW_PROXY_POOL_MAX_PROXIES", 500)
	if err != nil {
		return Config{}, err
	}
	proxyPoolStrategy := env("SHADOWFLOW_PROXY_POOL_STRATEGY", "round_robin")
	proxyPoolProtocols := envList("SHADOWFLOW_PROXY_POOL_PROTOCOLS", []string{"http", "socks5"})
	prewarmCount, err := envInt("SHADOWFLOW_PROXY_POOL_PREWARM_COUNT", 8)
	if err != nil {
		return Config{}, err
	}
	prewarmSeconds, err := envInt("SHADOWFLOW_PROXY_POOL_PREWARM_TIMEOUT_SECONDS", 120)
	if err != nil {
		return Config{}, err
	}
	pollSeconds, err := envInt("SHADOWFLOW_PROXY_POOL_POLL_SECONDS", 2)
	if err != nil {
		return Config{}, err
	}
	acquireSeconds, err := envInt("SHADOWFLOW_PROXY_POOL_ACQUIRE_TIMEOUT_SECONDS", 30)
	if err != nil {
		return Config{}, err
	}
	if prewarmCount < 1 || prewarmCount > 100 || prewarmSeconds < 1 || prewarmSeconds > 600 ||
		pollSeconds < 1 || pollSeconds > 30 || acquireSeconds < 1 || acquireSeconds > 120 {
		return Config{}, fmt.Errorf("proxy prewarm count, timeout, polling, or acquire timeout is out of range")
	}
	// SQLite has one writer and this service does not need a reader fan-out;
	// keep a single read connection by default to avoid cache/IO contention.
	readConns, err := envInt("SHADOWFLOW_SQLITE_READ_CONNS", 1)
	if err != nil {
		return Config{}, err
	}
	if pageSize < 1 || pageSize > 100 {
		return Config{}, fmt.Errorf("SHADOWFLOW_PAGE_SIZE must be between 1 and 100")
	}
	if timeoutSeconds < 1 || timeoutSeconds > 300 {
		return Config{}, fmt.Errorf("SHADOWFLOW_REQUEST_TIMEOUT_SECONDS must be between 1 and 300")
	}
	if successRetentionDays < 1 || failureRetentionDays < successRetentionDays {
		return Config{}, fmt.Errorf("run retention must satisfy 1 <= success days <= failure days")
	}
	if normalRate < 1 || exportRate < 1 || scanRate < 1 {
		return Config{}, fmt.Errorf("rate limits must be at least one request per minute")
	}
	if upstreamConcurrency < 1 || upstreamRate <= 0 || readConns < 1 || readConns > 32 {
		return Config{}, fmt.Errorf("upstream and SQLite concurrency settings are out of range")
	}
	if proxyPoolRefreshSeconds < 30 || proxyPoolRefreshSeconds > 86400 ||
		proxyPoolRequestTimeoutSeconds < 1 || proxyPoolRequestTimeoutSeconds > 60 ||
		proxyPoolMaxAttempts < 1 || proxyPoolMaxAttempts > 10 ||
		proxyPoolFailureLimit < 1 || proxyPoolFailureLimit > 20 ||
		proxyPoolCooldownSeconds < 1 || proxyPoolCooldownSeconds > 86400 ||
		proxyPoolMaxProxies < 1 || proxyPoolMaxProxies > 10000 {
		return Config{}, fmt.Errorf("proxy pool refresh, timeout, retry, cooldown, or size setting is out of range")
	}
	if proxyPoolStrategy != "round_robin" && proxyPoolStrategy != "random" {
		return Config{}, fmt.Errorf("SHADOWFLOW_PROXY_POOL_STRATEGY must be round_robin or random")
	}
	if calendarRefreshLeadDays < 1 || calendarRefreshLeadDays > 180 {
		return Config{}, fmt.Errorf("SHADOWFLOW_CALENDAR_REFRESH_LEAD_DAYS must be between 1 and 180")
	}

	return Config{
		ListenAddr:              env("SHADOWFLOW_LISTEN_ADDR", "127.0.0.1:8080"),
		DatabasePath:            env("SHADOWFLOW_DATABASE_PATH", "./data/shadowflow.db"),
		ColdDatabasePath:        strings.TrimSpace(os.Getenv("SHADOWFLOW_COLD_DATABASE_PATH")),
		CalendarPath:            env("SHADOWFLOW_CALENDAR_PATH", "./config/trading_calendar.json"),
		CalendarAutoUpdate:      calendarAutoUpdate,
		CalendarSourceURL:       env("SHADOWFLOW_CALENDAR_SOURCE_URL", "https://www.sse.com.cn/disclosure/dealinstruc/closed/"),
		CalendarRefreshLeadDays: calendarRefreshLeadDays,
		UpstreamBaseURL:         env("SHADOWFLOW_UPSTREAM_URL", "https://quotederivates.eastmoney.com/datacenter/darktrade"),
		QuoteBaseURLs:           envList("SHADOWFLOW_QUOTE_BASE_URLS", []string{"https://push2.eastmoney.com", "https://push2delay.eastmoney.com"}),
		PageSize:                pageSize,
		RequestTimeout:          time.Duration(timeoutSeconds) * time.Second,
		StaticDir:               os.Getenv("SHADOWFLOW_STATIC_DIR"),
		SchedulerEnabled:        schedulerEnabled,
		LimitUpArchiveEnabled:   limitUpArchiveEnabled,
		LimitUpScope:            limitUpScope,
		SchedulerDisabledKinds:  envList("SHADOWFLOW_SCHEDULER_DISABLED_KINDS", nil),
		AllowMissingQuotes:      allowMissingQuotes,
		SuccessRunRetentionDays: successRetentionDays,
		FailureRunRetentionDays: failureRetentionDays,
		APIToken:                strings.TrimSpace(os.Getenv("SHADOWFLOW_API_TOKEN")),
		NormalRatePerMinute:     normalRate,
		ExportRatePerMinute:     exportRate,
		ScanRatePerMinute:       scanRate,
		TrustedProxyCIDR:        strings.TrimSpace(os.Getenv("SHADOWFLOW_TRUSTED_PROXY_CIDR")),
		UpstreamMaxConcurrency:  upstreamConcurrency,
		UpstreamRatePerSecond:   upstreamRate,
		ProxyPoolURL:            strings.TrimSpace(os.Getenv("SHADOWFLOW_PROXY_POOL_URL")),
		ProxyPoolToken:          strings.TrimSpace(os.Getenv("SHADOWFLOW_PROXY_POOL_TOKEN")),
		ProxyPoolRefreshEvery:   time.Duration(proxyPoolRefreshSeconds) * time.Second,
		ProxyPoolRequestTimeout: time.Duration(proxyPoolRequestTimeoutSeconds) * time.Second,
		ProxyPoolMaxAttempts:    proxyPoolMaxAttempts,
		ProxyPoolFailureLimit:   proxyPoolFailureLimit,
		ProxyPoolCooldown:       time.Duration(proxyPoolCooldownSeconds) * time.Second,
		ProxyPoolDirectFallback: proxyPoolDirectFallback,
		ProxyPoolProtocols:      proxyPoolProtocols,
		ProxyPoolStrategy:       proxyPoolStrategy,
		ProxyPoolMaxProxies:     proxyPoolMaxProxies,
		ProxyPoolPrewarmCount:   prewarmCount,
		ProxyPoolPrewarmTimeout: time.Duration(prewarmSeconds) * time.Second,
		ProxyPoolPollEvery:      time.Duration(pollSeconds) * time.Second,
		ProxyPoolAcquireTimeout: time.Duration(acquireSeconds) * time.Second,
		ProxyPoolSourceIDs:      envList("SHADOWFLOW_PROXY_POOL_SOURCE_IDS", nil),
		SQLiteReadConns:         readConns,
	}, nil
}

func envList(key string, fallback []string) []string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	return parsed, nil
}

func envBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}
