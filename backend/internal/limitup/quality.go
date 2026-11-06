package limitup

import (
	"context"
	"time"
)

const (
	ArchiveStatusDisabled     = "disabled"
	ArchiveStatusNotCollected = "not_collected"
	ArchiveStatusRunning      = "running"
	ArchiveStatusFailed       = "failed"
	ArchiveStatusComplete     = "complete"
)

type ArchiveRun struct {
	RunID        string     `json:"run_id"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	FetchedTotal int        `json:"fetched_total"`
	PageCount    int        `json:"page_count"`
	ErrorCode    string     `json:"error_code"`
	ErrorMessage string     `json:"error_message"`
}

type ArchiveQuality struct {
	TradeDate       string      `json:"trade_date"`
	Source          string      `json:"source"`
	Scope           string      `json:"scope"`
	Status          string      `json:"status"`
	Scheduled       bool        `json:"scheduled"`
	Complete        bool        `json:"complete"`
	Day             *Day        `json:"day"`
	LastRun         *ArchiveRun `json:"last_run"`
	LatestTradeDate string      `json:"latest_trade_date"`
}

type QualityReader interface {
	LimitUpArchiveQuality(context.Context, string, string) (ArchiveQuality, error)
}
