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
