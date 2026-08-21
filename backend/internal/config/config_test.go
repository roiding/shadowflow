package config

import "testing"

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
	t.Setenv("SHADOWFLOW_AUTH_ENABLED", "true")
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
	if !cfg.AuthEnabled || cfg.APIToken != "0123456789abcdef" || cfg.NormalRatePerMinute != 321 ||
		cfg.ExportRatePerMinute != 17 || cfg.ScanRatePerMinute != 43 ||
		cfg.UpstreamMaxConcurrency != 7 || cfg.UpstreamRatePerSecond != 12.5 || cfg.SQLiteReadConns != 9 {
		t.Fatalf("security/concurrency options were not propagated: %+v", cfg)
	}
}

func TestLoadDisablesAuthenticationByDefault(t *testing.T) {
	t.Setenv("SHADOWFLOW_AUTH_ENABLED", "")
	t.Setenv("SHADOWFLOW_API_TOKEN", "unused-token")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthEnabled {
		t.Fatal("authentication should be disabled by default")
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
