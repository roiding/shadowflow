package config

import (
	"testing"
	"time"
)

func TestLoadColdDatabasePath(t *testing.T) {
	t.Setenv("SHADOWFLOW_COLD_DATABASE_PATH", " /data/history.db ")
	cfg, err := Load()
	if err != nil || cfg.ColdDatabasePath != "/data/history.db" {
		t.Fatalf("cold path=%q, error=%v", cfg.ColdDatabasePath, err)
	}
	t.Setenv("SHADOWFLOW_COLD_DATABASE_PATH", "")
	cfg, err = Load()
	if err != nil || cfg.ColdDatabasePath != "" {
		t.Fatalf("cold storage should be opt-in: %+v, %v", cfg, err)
	}
}

func TestLoadSchedulerEnabledByDefault(t *testing.T) {
	t.Setenv("SHADOWFLOW_SCHEDULER_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.SchedulerEnabled {
		t.Fatal("SchedulerEnabled = false, want true")
	}
}

func TestLoadLimitUpArchiveDisabledByDefault(t *testing.T) {
	t.Setenv("SHADOWFLOW_LIMIT_UP_ARCHIVE_ENABLED", "")
	t.Setenv("SHADOWFLOW_LIMIT_UP_SCOPE", "")
	t.Setenv("SHADOWFLOW_SCHEDULER_DISABLED_KINDS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LimitUpArchiveEnabled {
		t.Fatal("LimitUpArchiveEnabled = true, want false")
	}
	if cfg.LimitUpScope != "HS,GEM2STAR" {
		t.Fatalf("LimitUpScope = %q, want default scope", cfg.LimitUpScope)
	}
	if len(cfg.SchedulerDisabledKinds) != 0 {
		t.Fatalf("existing scheduler disabled kinds changed: %#v", cfg.SchedulerDisabledKinds)
	}
}

func TestLoadSQLiteReadConnsDefaultsToOne(t *testing.T) {
	t.Setenv("SHADOWFLOW_SQLITE_READ_CONNS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SQLiteReadConns != 1 {
		t.Fatalf("SQLiteReadConns=%d, want 1", cfg.SQLiteReadConns)
	}
}

func TestLoadSchedulerEnabled(t *testing.T) {
	t.Setenv("SHADOWFLOW_SCHEDULER_ENABLED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SchedulerEnabled {
		t.Fatal("SchedulerEnabled = true, want false")
	}
}

func TestLoadRejectsInvalidSchedulerEnabled(t *testing.T) {
	t.Setenv("SHADOWFLOW_SCHEDULER_ENABLED", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid boolean error")
	}
}

func TestLoadQuoteBaseURLs(t *testing.T) {
	t.Setenv("SHADOWFLOW_QUOTE_BASE_URLS", " https://primary.example ,https://delay.example ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.QuoteBaseURLs) != 2 || cfg.QuoteBaseURLs[0] != "https://primary.example" || cfg.QuoteBaseURLs[1] != "https://delay.example" {
		t.Fatalf("unexpected quote base URLs: %#v", cfg.QuoteBaseURLs)
	}
}

func TestLoadRunRetentionDefaultsAndValidation(t *testing.T) {
	t.Setenv("SHADOWFLOW_SUCCESS_RUN_RETENTION_DAYS", "")
	t.Setenv("SHADOWFLOW_FAILURE_RUN_RETENTION_DAYS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SuccessRunRetentionDays != 30 || cfg.FailureRunRetentionDays != 180 {
		t.Fatalf("unexpected retention defaults: %+v", cfg)
	}
	t.Setenv("SHADOWFLOW_SUCCESS_RUN_RETENTION_DAYS", "180")
	t.Setenv("SHADOWFLOW_FAILURE_RUN_RETENTION_DAYS", "30")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid retention order to be rejected")
	}
}

func TestLoadCalendarUpdateDefaultsAndValidation(t *testing.T) {
	t.Setenv("SHADOWFLOW_CALENDAR_AUTO_UPDATE", "")
	t.Setenv("SHADOWFLOW_CALENDAR_REFRESH_LEAD_DAYS", "")
	t.Setenv("SHADOWFLOW_CALENDAR_SOURCE_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CalendarAutoUpdate || cfg.CalendarRefreshLeadDays != 45 || cfg.CalendarSourceURL == "" {
		t.Fatalf("unexpected calendar update defaults: %+v", cfg)
	}
	t.Setenv("SHADOWFLOW_CALENDAR_REFRESH_LEAD_DAYS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid calendar refresh lead days")
	}
}

func TestLoadPropagatesSecurityAndConcurrencyOptions(t *testing.T) {
	t.Setenv("SHADOWFLOW_API_TOKEN", " 0123456789abcdef ")
	t.Setenv("SHADOWFLOW_RATE_LIMIT_PER_MINUTE", "321")
	t.Setenv("SHADOWFLOW_EXPORT_RATE_LIMIT_PER_MINUTE", "17")
	t.Setenv("SHADOWFLOW_SCAN_RATE_LIMIT_PER_MINUTE", "43")
	t.Setenv("SHADOWFLOW_UPSTREAM_MAX_CONCURRENCY", "7")
	t.Setenv("SHADOWFLOW_UPSTREAM_RATE_PER_SECOND", "12.5")
	t.Setenv("SHADOWFLOW_SQLITE_READ_CONNS", "9")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIToken != "0123456789abcdef" || cfg.NormalRatePerMinute != 321 ||
		cfg.ExportRatePerMinute != 17 || cfg.ScanRatePerMinute != 43 ||
		cfg.UpstreamMaxConcurrency != 7 || cfg.UpstreamRatePerSecond != 12.5 || cfg.SQLiteReadConns != 9 {
		t.Fatalf("security/concurrency options were not propagated: %+v", cfg)
	}
}

func TestLoadProxyPoolOptions(t *testing.T) {
	t.Setenv("SHADOWFLOW_PROXY_POOL_URL", " https://pool.example/proxies ")
	t.Setenv("SHADOWFLOW_PROXY_POOL_TOKEN", " token ")
	t.Setenv("SHADOWFLOW_PROXY_POOL_REFRESH_SECONDS", "120")
	t.Setenv("SHADOWFLOW_PROXY_POOL_REQUEST_TIMEOUT_SECONDS", "4")
	t.Setenv("SHADOWFLOW_PROXY_POOL_MAX_ATTEMPTS", "5")
	t.Setenv("SHADOWFLOW_PROXY_POOL_FAILURE_LIMIT", "3")
	t.Setenv("SHADOWFLOW_PROXY_POOL_COOLDOWN_SECONDS", "45")
	t.Setenv("SHADOWFLOW_PROXY_POOL_DIRECT_FALLBACK", "true")
	t.Setenv("SHADOWFLOW_PROXY_POOL_PROTOCOLS", " http,socks5 ")
	t.Setenv("SHADOWFLOW_PROXY_POOL_STRATEGY", "random")
	t.Setenv("SHADOWFLOW_PROXY_POOL_MAX_PROXIES", "20")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxyPoolURL != "https://pool.example/proxies" || cfg.ProxyPoolToken != "token" ||
		cfg.ProxyPoolRefreshEvery != 2*time.Minute || cfg.ProxyPoolRequestTimeout != 4*time.Second ||
		cfg.ProxyPoolMaxAttempts != 5 || cfg.ProxyPoolFailureLimit != 3 ||
		cfg.ProxyPoolCooldown != 45*time.Second || !cfg.ProxyPoolDirectFallback ||
		len(cfg.ProxyPoolProtocols) != 2 || cfg.ProxyPoolStrategy != "random" || cfg.ProxyPoolMaxProxies != 20 {
		t.Fatalf("unexpected proxy options: %+v", cfg)
	}

	t.Setenv("SHADOWFLOW_PROXY_POOL_REFRESH_SECONDS", "1")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid refresh interval to be rejected")
	}
	t.Setenv("SHADOWFLOW_PROXY_POOL_REFRESH_SECONDS", "120")
	t.Setenv("SHADOWFLOW_PROXY_POOL_STRATEGY", "least-recently-used")
	if _, err := Load(); err == nil {
		t.Fatal("expected unsupported strategy to be rejected")
	}
}

func TestLoadAllowsEmptyTokenByDefault(t *testing.T) {
	t.Setenv("SHADOWFLOW_API_TOKEN", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIToken != "" {
		t.Fatalf("unexpected API token: %q", cfg.APIToken)
	}
}

func TestLoadProxyPrewarmDefaultsAndValidation(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxyPoolPrewarmCount != 8 || cfg.ProxyPoolPrewarmTimeout != 120*time.Second ||
		cfg.ProxyPoolAcquireTimeout != 30*time.Second || cfg.ProxyPoolPollEvery != 2*time.Second || cfg.ProxyPoolCooldown != 300*time.Second {
		t.Fatal("unexpected managed proxy defaults")
	}
	t.Setenv("SHADOWFLOW_PROXY_POOL_SOURCE_IDS", "public-one, public-two")
	cfg, err = Load()
	if err != nil || len(cfg.ProxyPoolSourceIDs) != 2 || cfg.ProxyPoolSourceIDs[1] != "public-two" {
		t.Fatalf("source scope was not loaded: %v", err)
	}
	for _, entry := range []struct{ key, value string }{
		{"SHADOWFLOW_PROXY_POOL_PREWARM_COUNT", "0"},
		{"SHADOWFLOW_PROXY_POOL_PREWARM_COUNT", "101"},
		{"SHADOWFLOW_PROXY_POOL_PREWARM_TIMEOUT_SECONDS", "601"},
		{"SHADOWFLOW_PROXY_POOL_POLL_SECONDS", "0"},
		{"SHADOWFLOW_PROXY_POOL_ACQUIRE_TIMEOUT_SECONDS", "121"},
	} {
		t.Run(entry.key+entry.value, func(test *testing.T) {
			test.Setenv(entry.key, entry.value)
			if _, err := Load(); err == nil {
				test.Fatal("invalid prewarm configuration accepted")
			}
		})
	}
}

func TestLoadRejectsInvalidRequestTimeout(t *testing.T) {
	t.Setenv("SHADOWFLOW_REQUEST_TIMEOUT_SECONDS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected zero request timeout to be rejected")
	}
	t.Setenv("SHADOWFLOW_REQUEST_TIMEOUT_SECONDS", "301")
	if _, err := Load(); err == nil {
		t.Fatal("expected excessive request timeout to be rejected")
	}
}
