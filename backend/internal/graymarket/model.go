package graymarket

import (
	"errors"
	"fmt"
	"time"
)

type RankType string

const (
	RankStock    RankType = "stock"
	RankIndustry RankType = "industry"
	RankConcept  RankType = "concept"
)

func ParseRankType(value string) (RankType, error) {
	switch RankType(value) {
	case RankStock, RankIndustry, RankConcept:
		return RankType(value), nil
	default:
		return "", fmt.Errorf("unknown rank type %q", value)
	}
}

type SnapshotKind string

const (
	SnapshotMinuteWork SnapshotKind = "minute_work"
	SnapshotResearch5m SnapshotKind = "research_5m"
	SnapshotDailyClose SnapshotKind = "daily_close"
)

type BoardType string

const (
	BoardIndustry BoardType = "industry"
	BoardConcept  BoardType = "concept"
)

func ParseBoardType(value string) (BoardType, error) {
	switch BoardType(value) {
	case BoardIndustry, BoardConcept:
		return BoardType(value), nil
	default:
		return "", fmt.Errorf("unknown board type %q", value)
	}
}

type RelationChangeType string

const (
	RelationAdded   RelationChangeType = "added"
	RelationRemoved RelationChangeType = "removed"
)

const (
	RelationSourceQuoteClist       = "quote_clist"
	RelationScopeBoardConstituents = "all_industry_concept"
)

var (
	ErrNoData = errors.New("upstream returned no data")
	ErrDecode = errors.New("upstream response decode failed")
)

type RankRecord struct {
	TradeDate        string    `json:"trade_date"`
	SnapshotAt       time.Time `json:"snapshot_at"`
	RankType         RankType  `json:"rank_type"`
	Rank             int64     `json:"rank"`
	Market           int64     `json:"market"`
	Code             string    `json:"code"`
	Name             string    `json:"name"`
	QuoteTime        string    `json:"quote_time"`
	LatestPriceRaw   int64     `json:"latest_price_raw"`
	ChangePct        float64   `json:"change_pct"`
	DarkMoney        int64     `json:"dark_money"`
	RegularMoney     int64     `json:"regular_money"`
	MainMoneyInflow  int64     `json:"main_money_inflow"`
	DarkActivity     float64   `json:"dark_activity"`
	DarkInflowRatio  float64   `json:"dark_inflow_ratio"`
	UpCount          int64     `json:"up_count"`
	FlatCount        int64     `json:"flat_count"`
	DownCount        int64     `json:"down_count"`
	LeaderName       string    `json:"leader_name"`
	LeaderCode       string    `json:"leader_code"`
	SourceVersion    int       `json:"source_version"`
	SourceSortFlag   int       `json:"source_sort_flag"`
	SourceDescending bool      `json:"source_descending"`
	FetchedAt        time.Time `json:"fetched_at"`
}

type RawPage struct {
	Page            int
	ContentEncoding string
	Body            []byte
	FetchedAt       time.Time
}

type RankSnapshot struct {
	RequestedDate string
	TradeDate     string
	RankType      RankType
	SnapshotAt    time.Time
	Records       []RankRecord
	RawPages      []RawPage
	ExpectedTotal int
	FetchedAt     time.Time
}

type Board struct {
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	Type       BoardType `json:"type"`
	SourceRank int       `json:"source_rank"`
}

type StockBoardRelation struct {
	StockCode      string    `json:"stock_code"`
	StockMarket    int64     `json:"stock_market"`
	StockName      string    `json:"stock_name"`
	BoardCode      string    `json:"board_code"`
	BoardName      string    `json:"board_name"`
	BoardType      BoardType `json:"board_type"`
	SourceOrder    int       `json:"source_order"`
	RelationSource string    `json:"relation_source"`
	RelationScope  string    `json:"relation_scope"`
	EffectiveDate  string    `json:"effective_date,omitempty"`
	DetectedAt     time.Time `json:"detected_at"`
	RawData        string    `json:"raw_data,omitempty"`
}

type StockBoardRelationChange struct {
	StockBoardRelation
	ChangeType RelationChangeType `json:"change_type"`
	RunID      string             `json:"run_id"`
}

func FormatQuoteTime(value int64) string {
	if value <= 0 {
		return ""
	}
	return fmt.Sprintf("%06d", value)
}
