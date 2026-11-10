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

// IntradaySnapshot is a read-only view of the stocks currently sealed at the
// limit. It deliberately excludes the broken and limit-down pools because the
// source's daily archive is only a valid final snapshot after the close.
// Callers must not persist this type into limit_up_day.
type IntradaySnapshot struct {
	TradeDate string `json:"trade_date"`
	Source    string `json:"source"`
	Scope     string `json:"scope"`
	// ArchivedThrough is the newest complete archive day the caller resolved
	// for this snapshot's source and scope. AnalyzeToday rejects history that
	// extends past it, so a live snapshot can never silently combine members
	// from one scope with a newer history window from another.
	ArchivedThrough string    `json:"-"`
	FetchedAt       time.Time `json:"fetched_at"`
	TradeStatus     string    `json:"trade_status"`
	TradeStatusName string    `json:"trade_status_name"`
	SealedCount     int       `json:"sealed_count"`
	Events          []Event   `json:"events"`
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

type IntradayReader interface {
	FetchSealedToday(context.Context, string) (IntradaySnapshot, error)
}

// LatestDayReader finds the newest complete archive day strictly before a
// date. It lets the intraday-only view exclude today from historical
// relationship calculations even when today's closing archive has already
// been written.
type LatestDayReader interface {
	LatestLimitUpDay(context.Context, string, string) (string, error)
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

// TodayMember is one stock in the current intraday sealed pool. Historical
// fields come only from complete archive days strictly before today.
type TodayMember struct {
	Code                 string     `json:"code"`
	Name                 string     `json:"name"`
	MarketType           string     `json:"market_type"`
	FirstLimitAt         *time.Time `json:"first_limit_at"`
	LastLimitAt          *time.Time `json:"last_limit_at"`
	OpenCount            *int       `json:"open_count"`
	LimitType            string     `json:"limit_type"`
	HighDays             string     `json:"high_days"`
	Reason               string     `json:"reason"`
	ChangePercent        *float64   `json:"change_percent"`
	TurnoverRate         *float64   `json:"turnover_rate"`
	SealDays             int        `json:"seal_days"`
	TouchDays            int        `json:"touch_days"`
	BrokenDays           int        `json:"broken_days"`
	DownDays             int        `json:"down_days"`
	CoreScore            float64    `json:"core_score"`
	Coverage             float64    `json:"coverage"`
	Strength             float64    `json:"strength"`
	EffectiveLinks       int        `json:"effective_links"`
	StrongestPeerCode    string     `json:"strongest_peer_code"`
	StrongestPeerName    string     `json:"strongest_peer_name"`
	StrongestOverlapDays int        `json:"strongest_overlap_days"`
}

type TodayCore struct {
	Member TodayMember `json:"member"`
	Links  []Link      `json:"links"`
}

type CandidateLink struct {
	CoreCode       string  `json:"core_code"`
	CoreName       string  `json:"core_name"`
	OverlapDays    int     `json:"overlap_days"`
	Heat           float64 `json:"heat_adjusted_overlap"`
	Phi            float64 `json:"phi"`
	LastSharedDate string  `json:"last_shared_date"`
}

type ThemeRef struct {
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	Type   string  `json:"type"`
	Weight float64 `json:"weight"`
}

type MainlineTheme struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Weight      float64 `json:"weight"`
	MemberCount int     `json:"member_count"`
}

type TodayCandidate struct {
	Code                 string          `json:"code"`
	Name                 string          `json:"name"`
	MarketType           string          `json:"market_type"`
	Score                float64         `json:"score"`
	Strength             float64         `json:"strength"`
	Coverage             float64         `json:"coverage"`
	EffectiveLinks       int             `json:"effective_links"`
	SealDays             int             `json:"seal_days"`
	TouchDays            int             `json:"touch_days"`
	LastSealDate         string          `json:"last_seal_date"`
	LastSharedDate       string          `json:"last_shared_date"`
	StrongestCoreCode    string          `json:"strongest_core_code"`
	StrongestCoreName    string          `json:"strongest_core_name"`
	StrongestOverlapDays int             `json:"strongest_overlap_days"`
	StrongestHeat        float64         `json:"strongest_heat"`
	StrongestPhi         float64         `json:"strongest_phi"`
	ThemeScore           float64         `json:"theme_score"`
	Themes               []ThemeRef      `json:"themes"`
	TopLinks             []CandidateLink `json:"top_links"`
}

type TodayNetwork struct {
	TradeDate        string           `json:"trade_date"`
	SnapshotAt       time.Time        `json:"snapshot_at"`
	TradeStatus      string           `json:"trade_status"`
	TradeStatusName  string           `json:"trade_status_name"`
	SealedCount      int              `json:"sealed_count"`
	HistoryAsOf      string           `json:"history_as_of"`
	LookbackDays     int              `json:"lookback_days"`
	Scope            string           `json:"scope"`
	MinOverlap       int              `json:"min_overlap"`
	Coverage         Coverage         `json:"coverage"`
	HistoryAvailable bool             `json:"history_available"`
	Members          []TodayMember    `json:"members"`
	Cores            []TodayCore      `json:"cores"`
	Candidates       []TodayCandidate `json:"candidates"`
	CandidateLimit   int              `json:"candidate_limit"`
	MainlineThemes   []MainlineTheme  `json:"mainline_themes"`
}
