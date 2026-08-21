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
	SuccessRunRetentionDays int
	FailureRunRetentionDays int
	AuthEnabled             bool
	APIToken                string
	NormalRatePerMinute     int
	ExportRatePerMinute     int
	ScanRatePerMinute       int
	UpstreamMaxConcurrency  int
	UpstreamRatePerSecond   float64
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
	authEnabled, err := envBool("SHADOWFLOW_AUTH_ENABLED", false)
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
	readConns, err := envInt("SHADOWFLOW_SQLITE_READ_CONNS", 4)
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
	if calendarRefreshLeadDays < 1 || calendarRefreshLeadDays > 180 {
		return Config{}, fmt.Errorf("SHADOWFLOW_CALENDAR_REFRESH_LEAD_DAYS must be between 1 and 180")
	}

	return Config{
		ListenAddr:              env("SHADOWFLOW_LISTEN_ADDR", "127.0.0.1:8080"),
		DatabasePath:            env("SHADOWFLOW_DATABASE_PATH", "./data/shadowflow.db"),
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
		SuccessRunRetentionDays: successRetentionDays,
		FailureRunRetentionDays: failureRetentionDays,
		AuthEnabled:             authEnabled,
		APIToken:                strings.TrimSpace(os.Getenv("SHADOWFLOW_API_TOKEN")),
		NormalRatePerMinute:     normalRate,
		ExportRatePerMinute:     exportRate,
		ScanRatePerMinute:       scanRate,
		UpstreamMaxConcurrency:  upstreamConcurrency,
		UpstreamRatePerSecond:   upstreamRate,
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
