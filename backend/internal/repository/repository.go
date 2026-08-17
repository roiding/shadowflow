package repository

import (
	"context"
	"errors"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

var ErrArchiveIncomplete = errors.New("archive is incomplete")

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

type DailyArchiveManifest struct {
	TradeDate              string         `json:"trade_date"`
	Status                 string         `json:"status"`
	IndustryCloseRows      int            `json:"industry_close_rows"`
	IndustryMoneyRows      int            `json:"industry_money_rows"`
	ConceptCloseRows       int            `json:"concept_close_rows"`
	ConceptMoneyRows       int            `json:"concept_money_rows"`
	StockCloseRows         int            `json:"stock_close_rows"`
	StockMoneyRows         int            `json:"stock_money_rows"`
	StockKlineRows         int            `json:"stock_kline_rows"`
	StockDailyKlineRows    int            `json:"stock_daily_kline_rows"`
	ExpectedStockRows      int            `json:"expected_stock_rows"`
	ExpectedStockKlineRows int            `json:"expected_stock_kline_rows"`
	CodeCount              int            `json:"code_count"`
	CodeSetSHA256          string         `json:"code_set_sha256"`
	KlineSourceCounts      map[string]int `json:"kline_source_counts"`
	DarkTradeContract      string         `json:"darktrade_contract"`
	DarkTradeTickContract  string         `json:"darktradetick_contract"`
	StockKlineContract     string         `json:"stock_kline_contract"`
	ParserVersion          string         `json:"parser_version"`
	ValidationErrors       []string       `json:"validation_errors"`
	CompletedAt            *time.Time     `json:"completed_at,omitempty"`
	UpdatedAt              *time.Time     `json:"updated_at,omitempty"`
	CurrentRevisionID      string         `json:"current_revision_id,omitempty"`
	RevisionNo             int            `json:"revision_no,omitempty"`
}

type ArchiveRevision struct {
	RevisionID       string    `json:"revision_id"`
	TradeDate        string    `json:"trade_date"`
	RevisionNo       int       `json:"revision_no"`
	PreviousRevision string    `json:"previous_revision,omitempty"`
	ContentSHA256    string    `json:"content_sha256"`
	CreatedAt        time.Time `json:"created_at"`
}

type DailyFeatureSet struct {
	RevisionID      string            `json:"revision_id"`
	TradeDate       string            `json:"trade_date"`
	FeatureVersion  string            `json:"feature_version"`
	SourceRevisions []ArchiveRevision `json:"source_revisions"`
	GeneratedAt     time.Time         `json:"generated_at"`
}

type DailyFeature struct {
	RevisionID                string              `json:"revision_id"`
	TradeDate                 string              `json:"trade_date"`
	RankType                  graymarket.RankType `json:"rank_type"`
	Market                    int64               `json:"market"`
	Code                      string              `json:"code"`
	Name                      string              `json:"name"`
	PrimaryIndustryCode       string              `json:"primary_industry_code,omitempty"`
	SignedDarkActivity        float64             `json:"signed_dark_activity"`
	CapitalIntensity          float64             `json:"capital_intensity"`
	ControlCoefficient        float64             `json:"control_coefficient"`
	RankPercentile            float64             `json:"rank_percentile"`
	TurnoverPercentile        float64             `json:"turnover_percentile"`
	DarkMoneyPercentile       float64             `json:"dark_money_percentile"`
	SelfTurnoverPercentile5   *float64            `json:"self_turnover_percentile_5,omitempty"`
	SelfTurnoverPercentile10  *float64            `json:"self_turnover_percentile_10,omitempty"`
	SelfTurnoverPercentile20  *float64            `json:"self_turnover_percentile_20,omitempty"`
	SelfTurnoverPercentile60  *float64            `json:"self_turnover_percentile_60,omitempty"`
	SelfDarkMoneyPercentile5  *float64            `json:"self_dark_money_percentile_5,omitempty"`
	SelfDarkMoneyPercentile10 *float64            `json:"self_dark_money_percentile_10,omitempty"`
	SelfDarkMoneyPercentile20 *float64            `json:"self_dark_money_percentile_20,omitempty"`
	SelfDarkMoneyPercentile60 *float64            `json:"self_dark_money_percentile_60,omitempty"`
	RankChange1               int64               `json:"rank_change_1"`
	ConsecutiveInflowDays     int                 `json:"consecutive_inflow_days"`
	MoneyAcceleration         int64               `json:"money_acceleration"`
	CurveAvailable            bool                `json:"curve_available"`
	MorningDarkShare          float64             `json:"morning_dark_share"`
	AfternoonDarkShare        float64             `json:"afternoon_dark_share"`
	LateDarkShare             float64             `json:"late_dark_share"`
	MaxInflowMinuteIndex      int                 `json:"max_inflow_minute_index"`
	MaxOutflowMinuteIndex     int                 `json:"max_outflow_minute_index"`
	TailAcceleration          int64               `json:"tail_acceleration"`
	MaxDarkDrawdown           int64               `json:"max_dark_drawdown"`
	IntradayReversal          bool                `json:"intraday_reversal"`
	PriceMoneyDivergence      bool                `json:"price_money_divergence"`
}

type FutureReturnLabel struct {
	SignalRevisionID       string              `json:"signal_revision_id"`
	TargetRevisionID       string              `json:"target_revision_id"`
	SignalDate             string              `json:"signal_date"`
	TargetDate             string              `json:"target_date"`
	Horizon                int                 `json:"horizon"`
	RankType               graymarket.RankType `json:"rank_type"`
	Market                 int64               `json:"market"`
	Code                   string              `json:"code"`
	ReturnRate             float64             `json:"return_rate"`
	RelativeIndustryReturn *float64            `json:"relative_industry_return,omitempty"`
	MaxFavorableReturn     float64             `json:"max_favorable_return"`
	MaxAdverseReturn       float64             `json:"max_adverse_return"`
	LabelVersion           string              `json:"label_version"`
	GeneratedAt            time.Time           `json:"generated_at"`
}

type MaintenanceResult struct {
	SuccessfulRunsDeleted int       `json:"successful_runs_deleted"`
	FailedRunsDeleted     int       `json:"failed_runs_deleted"`
	TransientRawDeleted   int       `json:"transient_raw_deleted"`
	RelationRunsDeleted   int       `json:"relation_runs_deleted"`
	WALBusy               int       `json:"wal_busy"`
	WALLogFrames          int       `json:"wal_log_frames"`
	WALCheckpointedFrames int       `json:"wal_checkpointed_frames"`
	Optimized             bool      `json:"optimized"`
	CompletedAt           time.Time `json:"completed_at"`
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
	MissingStockKlineCodes(context.Context, string) ([]string, error)
	CompactResearch(context.Context, string) ([]QualitySummary, error)
	CleanupIntraday(context.Context, string) error
	CleanupArchivedIntraday(context.Context, string) error
	LatestRank(context.Context, graymarket.RankType) ([]graymarket.RankRecord, error)
	RankAt(context.Context, graymarket.RankType, string, time.Time) ([]graymarket.RankRecord, error)
	IntradaySeries(context.Context, graymarket.RankType, string, string) ([]graymarket.RankRecord, error)
	ResearchSeries(context.Context, graymarket.RankType, string, time.Time, time.Time) ([]graymarket.RankRecord, error)
	BoardResearchRevisionSeries(context.Context, string, graymarket.RankType, string) ([]graymarket.RankRecord, error)
	StockResearchSeries(context.Context, string, string) ([]graymarket.StockResearchPoint, error)
	StockResearchRevisionSeries(context.Context, string, string) ([]graymarket.StockResearchPoint, error)
	DailyClosePage(context.Context, graymarket.RankType, string, string, string, bool, int, int) ([]graymarket.RankRecord, int, error)
	DailyCloseRevisionPage(context.Context, string, graymarket.RankType, string, string, bool, int, int) ([]graymarket.RankRecord, int, error)
	DailyCloseStocks(context.Context, string, []string) ([]graymarket.RankRecord, error)
	DailyCloseRecords(context.Context, string) ([]graymarket.RankRecord, error)
	DailyCloseRevisionRecords(context.Context, string) ([]graymarket.RankRecord, error)
	DailyCloseTradeDates(context.Context, string, int) ([]string, error)
	HasDailyClose(context.Context, string) (bool, error)
	HasEndOfDayArchive(context.Context, string) (bool, error)
	HasStockKlineArchive(context.Context, string) (bool, error)
	Quality(context.Context, string) ([]QualitySummary, error)
	StockArchiveQuality(context.Context, string) (StockArchiveQuality, error)
	ArchiveManifest(context.Context, string) (DailyArchiveManifest, error)
	SealArchiveRevision(context.Context, string, string) (ArchiveRevision, error)
	ArchiveRevisions(context.Context, string) ([]ArchiveRevision, error)
	DailyFeatures(context.Context, string, string, graymarket.RankType) ([]DailyFeature, DailyFeatureSet, error)
	FutureReturnLabels(context.Context, string, string, string, int) ([]FutureReturnLabel, error)
	RebuildAnalytics(context.Context, string) error
	Maintain(context.Context, time.Time, int, int) (MaintenanceResult, error)
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
	BoardStockRelationsBatch(context.Context, graymarket.BoardType, []string, string) ([]graymarket.StockBoardRelation, error)
	RelationChanges(context.Context, string, graymarket.BoardType) ([]graymarket.StockBoardRelationChange, error)
}
