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
