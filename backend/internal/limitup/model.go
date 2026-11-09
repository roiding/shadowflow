package limitup

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	SourceTHS      = "ths_limit_up"
	ScopeDefault   = "HS,GEM2STAR"
	ScopeExpanded  = "HS,GEM2STAR,ST,NEW"
	PoolSealed     = "limit_up"
	PoolBroken     = "open_limit"
	PoolDown       = "limit_down"
	EventSealed    = "seal"
	EventTouched   = "touch"
	EventDown      = "down"
	MinimumSamples = 20
)

var (
	ErrInvalid = errors.New("invalid limit-up request")
	ErrMissing = errors.New("stock has no events in this window")
)

type Event struct {
	TradeDate      string          `json:"trade_date"`
	Code           string          `json:"code"`
	Name           string          `json:"name"`
	MarketID       int             `json:"market_id"`
	MarketType     string          `json:"market_type"`
	Pool           string          `json:"pool"`
	OpenCount      *int            `json:"open_count"`
	FirstLimitAt   *time.Time      `json:"first_limit_at"`
	LastLimitAt    *time.Time      `json:"last_limit_at"`
	FirstDownAt    *time.Time      `json:"first_down_at,omitempty"`
	LastDownAt     *time.Time      `json:"last_down_at,omitempty"`
	LimitType      string          `json:"limit_type"`
	HighDays       string          `json:"high_days"`
	Reason         string          `json:"reason"`
	SourceSealRate *float64        `json:"source_seal_rate"`
	ChangePercent  *float64        `json:"change_percent"`
	TurnoverRate   *float64        `json:"turnover_rate"`
	Raw            json.RawMessage `json:"-"`
}

type Snapshot struct {
	TradeDate       string            `json:"trade_date"`
	Source          string            `json:"source"`
	Scope           string            `json:"scope"`
	FetchedAt       time.Time         `json:"fetched_at"`
	SealedCount     int               `json:"sealed_count"`
	BrokenCount     int               `json:"broken_count"`
	DownCount       int               `json:"down_count"`
	TouchedCount    int               `json:"touched_count"`
	CountCorrection *CountCorrection  `json:"count_correction,omitempty"`
	Events          []Event           `json:"events"`
	RawPages        []json.RawMessage `json:"-"`
}

type Day struct {
	TradeDate       string           `json:"trade_date"`
	FetchedAt       time.Time        `json:"fetched_at"`
	SealedCount     int              `json:"sealed_count"`
	BrokenCount     int              `json:"broken_count"`
	DownCount       int              `json:"down_count"`
	TouchedCount    int              `json:"touched_count"`
	CountCorrection *CountCorrection `json:"count_correction,omitempty"`
	ContentHash     string           `json:"content_hash"`
}

type CountCorrection struct {
	Reason             string   `json:"reason"`
	SourceBrokenCount  int      `json:"source_broken_count"`
	SourceTouchedCount int      `json:"source_touched_count"`
	Codes              []string `json:"codes"`
}

type Dataset struct {
	Days   []Day
	Events []Event
}

type Reader interface {
	ReadLimitUpWindow(context.Context, string, string, string) (Dataset, error)
}

type StockLimitStatsReader interface {
	ReadLimitUpStockStats(context.Context, string, string, string, []string) (StockLimitStatsWindow, error)
}

type Writer interface {
	SaveLimitUpSnapshot(context.Context, Snapshot) error
}

type Query struct {
	AsOf       string `json:"as_of"`
	Days       int    `json:"days"`
	Scope      string `json:"scope"`
	Event      string `json:"event"`
	MinOverlap int    `json:"min_overlap"`
	Limit      int    `json:"limit"`
	Order      string `json:"order"`
}

type Coverage struct {
	Source           string `json:"source"`
	Scope            string `json:"scope"`
	FromDate         string `json:"from_date"`
	AsOf             string `json:"as_of"`
	LookbackDays     int    `json:"lookback_days"`
	ObservedDays     int    `json:"observed_days"`
	FirstDate        string `json:"first_date"`
	LatestDate       string `json:"latest_date"`
	SampleSufficient bool   `json:"sample_sufficient"`
	MinimumSamples   int    `json:"minimum_samples"`
}

type StockSummary struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	MarketType string `json:"market_type"`
	TouchDays  int    `json:"touch_days"`
	SealDays   int    `json:"seal_days"`
	BrokenDays int    `json:"broken_days"`
	DownDays   int    `json:"down_days"`
	LastSeen   string `json:"last_seen"`
}

type StockLimitStats struct {
	Code       string `json:"code"`
	SealDays   int    `json:"seal_days"`
	TouchDays  int    `json:"touch_days"`
	BrokenDays int    `json:"broken_days"`
	DownDays   int    `json:"down_days"`
}

type StockLimitStatsWindow struct {
	FromDate     string            `json:"from_date"`
	AsOf         string            `json:"as_of"`
	FirstDate    string            `json:"first_date"`
	LatestDate   string            `json:"latest_date"`
	ObservedDays int               `json:"observed_days"`
	Available    bool              `json:"available"`
	Stocks       []StockLimitStats `json:"stocks"`
}

type Overview struct {
	Coverage Coverage       `json:"coverage"`
	Days     []Day          `json:"days"`
	Stocks   []StockSummary `json:"stocks"`
}

type CategoryCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type Profile struct {
	StockSummary
	SealRate             *float64        `json:"seal_rate"`
	BrokenRate           *float64        `json:"broken_rate"`
	EventFrequency       *float64        `json:"event_frequency"`
	DownFrequency        *float64        `json:"down_frequency"`
	UpToDownDays         int             `json:"up_to_down_days"`
	OpenCountKnownDays   int             `json:"open_count_known_days"`
	OpenCountUnknownDays int             `json:"open_count_unknown_days"`
	OpenedDays           int             `json:"opened_days"`
	RecoveredDays        int             `json:"recovered_days"`
	RecoveryRate         *float64        `json:"recovery_rate"`
	MeanOpenCount        *float64        `json:"mean_open_count"`
	MedianFirstTime      string          `json:"median_first_time"`
	FirstTimeSamples     int             `json:"first_time_samples"`
	MedianFirstDownTime  string          `json:"median_first_down_time,omitempty"`
	FirstDownTimeSamples int             `json:"first_down_time_samples,omitempty"`
	LimitTypes           []CategoryCount `json:"limit_types"`
	FirstTimeBuckets     []CategoryCount `json:"first_time_buckets"`
}

type Link struct {
	Code                   string   `json:"code"`
	Name                   string   `json:"name"`
	MarketType             string   `json:"market_type"`
	AnchorDays             int      `json:"anchor_days"`
	PeerDays               int      `json:"peer_days"`
	OverlapDays            int      `json:"overlap_days"`
	Jaccard                float64  `json:"jaccard"`
	ConditionalProbability float64  `json:"conditional_probability"`
	ReverseProbability     float64  `json:"reverse_probability"`
	BaselineProbability    float64  `json:"baseline_probability"`
	Lift                   float64  `json:"lift"`
	Phi                    *float64 `json:"phi"`
	HeatAdjustedOverlap    float64  `json:"heat_adjusted_overlap"`
	SharedDates            []string `json:"shared_dates"`
	SampleSufficient       bool     `json:"sample_sufficient"`
}

type Analysis struct {
	Coverage   Coverage `json:"coverage"`
	Event      string   `json:"event"`
	MinOverlap int      `json:"min_overlap"`
	TotalLinks int      `json:"total_links"`
	Profile    Profile  `json:"profile"`
	Links      []Link   `json:"links"`
	Events     []Event  `json:"events"`
}
