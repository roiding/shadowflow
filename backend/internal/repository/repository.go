package repository

import (
	"context"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type RunStatus string

const (
	RunRunning RunStatus = "running"
	RunSuccess RunStatus = "success"
	RunPartial RunStatus = "partial"
	RunFailed  RunStatus = "failed"
	RunSkipped RunStatus = "skipped"
)

type CollectionRun struct {
	RunID           string                  `json:"run_id"`
	SnapshotAt      time.Time               `json:"snapshot_at"`
	SnapshotKind    graymarket.SnapshotKind `json:"snapshot_kind"`
	RankType        graymarket.RankType     `json:"rank_type"`
	Status          RunStatus               `json:"status"`
	RequestedDate   string                  `json:"requested_date"`
	ActualTradeDate string                  `json:"actual_trade_date"`
	ExpectedTotal   int                     `json:"expected_total"`
	FetchedTotal    int                     `json:"fetched_total"`
	PageCount       int                     `json:"page_count"`
	AttemptCount    int                     `json:"attempt_count"`
	StartedAt       time.Time               `json:"started_at"`
	FinishedAt      *time.Time              `json:"finished_at,omitempty"`
	DurationMS      int64                   `json:"duration_ms"`
	ErrorCode       string                  `json:"error_code"`
	ErrorMessage    string                  `json:"error_message"`
}

type QualitySummary struct {
	TradeDate           string              `json:"trade_date"`
	RankType            graymarket.RankType `json:"rank_type"`
	ExpectedMinutes     int                 `json:"expected_minutes"`
	CollectedMinutes    int                 `json:"collected_minutes"`
	ExpectedResearch    int                 `json:"expected_research_snapshots"`
	CollectedResearch   int                 `json:"collected_research_snapshots"`
	ExpectedDailyClose  int                 `json:"expected_daily_close_snapshots"`
	CollectedDailyClose int                 `json:"collected_daily_close_snapshots"`
	MissingMinutes      []string            `json:"missing_minutes"`
	MissingResearch     []string            `json:"missing_research_snapshots"`
	MissingDailyClose   []string            `json:"missing_daily_close_snapshots"`
	CompactedAt         *time.Time          `json:"compacted_at,omitempty"`
}

type StockArchiveQuality struct {
	TradeDate           string     `json:"trade_date"`
	ExpectedStocks      int        `json:"expected_stocks"`
	ExpectedPoints      int        `json:"expected_points"`
	ExpectedKlineStocks int        `json:"expected_kline_stocks"`
	MoneyRows           int        `json:"money_rows"`
	KlineRows           int        `json:"kline_rows"`
	DailyCloseRows      int        `json:"daily_close_rows"`
	DailyKlineRows      int        `json:"daily_kline_rows"`
	MoneyArchivedAt     *time.Time `json:"money_archived_at,omitempty"`
	KlineArchivedAt     *time.Time `json:"kline_archived_at,omitempty"`
}

type MetricCount struct {
	RankType graymarket.RankType
	Status   RunStatus
	Value    int64
}

type MetricValue struct {
	RankType graymarket.RankType
	Value    float64
}

type MetricTime struct {
	RankType graymarket.RankType
	Value    time.Time
}

type OperationalMetrics struct {
	RunCounts               []MetricCount
	RecordCounts            []MetricValue
	DurationSecondsSum      []MetricValue
	DurationCounts          []MetricValue
	LastSuccess             []MetricTime
	LatestIntradaySnapshot  []MetricTime
	ResearchCompactionRuns  int64
	ResearchMissingSnapshot []MetricValue
}

type RelationSyncRun struct {
	RunID         string     `json:"run_id"`
	TradeDate     string     `json:"trade_date"`
	Status        RunStatus  `json:"status"`
	BoardCount    int        `json:"board_count"`
	RelationCount int        `json:"relation_count"`
	AddedCount    int        `json:"added_count"`
	RemovedCount  int        `json:"removed_count"`
	BaselineBuilt bool       `json:"baseline_built"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	DurationMS    int64      `json:"duration_ms"`
	ErrorCode     string     `json:"error_code"`
	ErrorMessage  string     `json:"error_message"`
}

type RelationApplyResult struct {
	RelationCount int
	AddedCount    int
	RemovedCount  int
	BaselineBuilt bool
}

type Store interface {
	Close() error
	SaveIntraday(context.Context, string, graymarket.RankSnapshot, bool) error
	SaveDailyClose(context.Context, string, graymarket.RankSnapshot) error
	SaveBoardArchive(context.Context, string, graymarket.RankSnapshot, []graymarket.MoneyPoint) error
	SaveStockArchive(context.Context, string, graymarket.RankSnapshot, []graymarket.MoneyPoint) error
	SaveStockKlines(context.Context, string, []graymarket.StockKlinePoint) error
	CompactResearch(context.Context, string) ([]QualitySummary, error)
	CleanupIntraday(context.Context, string) error
	CleanupArchivedIntraday(context.Context, string) error
	LatestRank(context.Context, graymarket.RankType) ([]graymarket.RankRecord, error)
	RankAt(context.Context, graymarket.RankType, string, time.Time) ([]graymarket.RankRecord, error)
	IntradaySeries(context.Context, graymarket.RankType, string, string) ([]graymarket.RankRecord, error)
	ResearchSeries(context.Context, graymarket.RankType, string, time.Time, time.Time) ([]graymarket.RankRecord, error)
	StockResearchSeries(context.Context, string, string) ([]graymarket.StockResearchPoint, error)
	DailyClosePage(context.Context, graymarket.RankType, string, string, string, bool, int, int) ([]graymarket.RankRecord, int, error)
	DailyCloseStocks(context.Context, string, []string) ([]graymarket.RankRecord, error)
	DailyCloseRecords(context.Context, string) ([]graymarket.RankRecord, error)
	HasDailyClose(context.Context, string) (bool, error)
	HasEndOfDayArchive(context.Context, string) (bool, error)
	HasStockKlineArchive(context.Context, string) (bool, error)
	Quality(context.Context, string) ([]QualitySummary, error)
	StockArchiveQuality(context.Context, string) (StockArchiveQuality, error)
	StartRun(context.Context, CollectionRun) error
	FinishRun(context.Context, CollectionRun) error
	RecentRuns(context.Context, string, int) ([]CollectionRun, error)
	OperationalMetrics(context.Context) (OperationalMetrics, error)
	StartRelationSync(context.Context, RelationSyncRun) error
	StageRelations(context.Context, string, []graymarket.StockBoardRelation) error
	ApplyRelationScan(context.Context, string, string, time.Time) (RelationApplyResult, error)
	FailRelationSync(context.Context, RelationSyncRun) error
	HasSuccessfulRelationSync(context.Context, string) (bool, error)
	StockBoardRelations(context.Context, string, string) ([]graymarket.StockBoardRelation, error)
	BoardStockRelations(context.Context, graymarket.BoardType, string, string) ([]graymarket.StockBoardRelation, error)
	RelationChanges(context.Context, string, graymarket.BoardType) ([]graymarket.StockBoardRelationChange, error)
}
