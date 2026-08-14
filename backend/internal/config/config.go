package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr       string
	DatabasePath     string
	CalendarPath     string
	UpstreamBaseURL  string
	QuoteBaseURLs    []string
	PageSize         int
	RequestTimeout   time.Duration
	StaticDir        string
	SchedulerEnabled bool
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
	if pageSize < 1 || pageSize > 100 {
		return Config{}, fmt.Errorf("SHADOWFLOW_PAGE_SIZE must be between 1 and 100")
	}

	return Config{
		ListenAddr:       env("SHADOWFLOW_LISTEN_ADDR", ":8080"),
		DatabasePath:     env("SHADOWFLOW_DATABASE_PATH", "./data/shadowflow.db"),
		CalendarPath:     env("SHADOWFLOW_CALENDAR_PATH", "./config/trading_calendar.json"),
		UpstreamBaseURL:  env("SHADOWFLOW_UPSTREAM_URL", "https://quotederivates.eastmoney.com/datacenter/darktrade"),
		QuoteBaseURLs:    envList("SHADOWFLOW_QUOTE_BASE_URLS", []string{"https://push2.eastmoney.com", "https://push2delay.eastmoney.com"}),
		PageSize:         pageSize,
		RequestTimeout:   time.Duration(timeoutSeconds) * time.Second,
		StaticDir:        os.Getenv("SHADOWFLOW_STATIC_DIR"),
		SchedulerEnabled: schedulerEnabled,
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
