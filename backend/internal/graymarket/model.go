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
	SnapshotStockKline SnapshotKind = "stock_kline_5m"
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
	OpenPrice        float64   `json:"open_price"`
	HighPrice        float64   `json:"high_price"`
	LowPrice         float64   `json:"low_price"`
	ClosePrice       float64   `json:"close_price"`
	PreviousClose    float64   `json:"previous_close"`
	ChangeValue      float64   `json:"change_value"`
	ChangePct        float64   `json:"change_pct"`
	Volume           int64     `json:"volume"`
	Turnover         int64     `json:"turnover"`
	TurnoverRate     float64   `json:"turnover_rate"`
	Amplitude        float64   `json:"amplitude"`
	QuoteAvailable   bool      `json:"quote_available"`
	MoneyAvailable   bool      `json:"money_available"`
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

// MoneyPoint is a post-close, upstream-revised point from darktradetick.
// The endpoint does not provide quote, activity, breadth, or leader fields, so
// those values deliberately do not share the full RankRecord model.
type MoneyPoint struct {
	TradeDate       string    `json:"trade_date"`
	SnapshotAt      time.Time `json:"snapshot_at"`
	RankType        RankType  `json:"rank_type"`
	Rank            int64     `json:"rank"`
	Market          int64     `json:"market"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	DarkMoney       int64     `json:"dark_money"`
	RegularMoney    int64     `json:"regular_money"`
	MainMoneyInflow int64     `json:"main_money_inflow"`
	SourceTime      int64     `json:"source_time"`
	FetchedAt       time.Time `json:"fetched_at"`
}

type StockKlinePoint struct {
	TradeDate    string    `json:"trade_date"`
	SnapshotAt   time.Time `json:"snapshot_at"`
	Market       int64     `json:"market"`
	Code         string    `json:"code"`
	Source       string    `json:"source"`
	OpenPrice    float64   `json:"open_price"`
	HighPrice    float64   `json:"high_price"`
	LowPrice     float64   `json:"low_price"`
	ClosePrice   float64   `json:"close_price"`
	Volume       int64     `json:"volume"`
	Turnover     int64     `json:"turnover"`
	Amplitude    float64   `json:"amplitude"`
	ChangePct    float64   `json:"change_pct"`
	ChangeValue  float64   `json:"change_value"`
	TurnoverRate float64   `json:"turnover_rate"`
	FetchedAt    time.Time `json:"fetched_at"`
}

const (
	KlineSourceFiveMinute = "stock_kline_5m"
	KlineSourceTrend241   = "stock_trends_1m_241"
	KlineSourceUnknown    = "unknown"
)

// StockResearchPoint joins one revised five-minute money point with the
// matching unadjusted five-minute market bar.
type StockResearchPoint struct {
	TradeDate       string    `json:"trade_date"`
	SnapshotAt      time.Time `json:"snapshot_at"`
	Market          int64     `json:"market"`
	Code            string    `json:"code"`
	MoneyRank       int64     `json:"money_rank"`
	DarkMoney       int64     `json:"dark_money"`
	RegularMoney    int64     `json:"regular_money"`
	MainMoneyInflow int64     `json:"main_money_inflow"`
	OpenPrice       float64   `json:"open_price"`
	HighPrice       float64   `json:"high_price"`
	LowPrice        float64   `json:"low_price"`
	ClosePrice      float64   `json:"close_price"`
	Volume          int64     `json:"volume"`
	Turnover        int64     `json:"turnover"`
	Amplitude       float64   `json:"amplitude"`
	ChangePct       float64   `json:"change_pct"`
	ChangeValue     float64   `json:"change_value"`
	TurnoverRate    float64   `json:"turnover_rate"`
	MoneyAvailable  bool      `json:"money_available"`
	KlineAvailable  bool      `json:"kline_available"`
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

// StockQuote is an upstream market quote used both for live constituent context
// and to enrich the stock daily-close snapshot with end-of-day market fields.
type StockQuote struct {
	StockCode     string    `json:"stock_code"`
	StockMarket   int64     `json:"stock_market"`
	StockName     string    `json:"stock_name"`
	LatestPrice   float64   `json:"latest_price"`
	OpenPrice     float64   `json:"open_price"`
	HighPrice     float64   `json:"high_price"`
	LowPrice      float64   `json:"low_price"`
	PreviousClose float64   `json:"previous_close"`
	ChangePct     float64   `json:"change_pct"`
	ChangeValue   float64   `json:"change_value"`
	Volume        int64     `json:"volume"`
	Turnover      int64     `json:"turnover"`
	TurnoverRate  float64   `json:"turnover_rate"`
	Amplitude     float64   `json:"amplitude"`
	QuoteTime     string    `json:"quote_time"`
	FetchedAt     time.Time `json:"fetched_at"`
	Available     bool      `json:"available"`
}

// BoardQuote is the end-of-day quote returned by Eastmoney's board list
// endpoint. Board codes use the BKxxxx form and therefore cannot be queried
// through the constituent-stock quote endpoint.
type BoardQuote struct {
	BoardCode     string    `json:"board_code"`
	BoardMarket   int64     `json:"board_market"`
	BoardName     string    `json:"board_name"`
	LatestPrice   float64   `json:"latest_price"`
	OpenPrice     float64   `json:"open_price"`
	HighPrice     float64   `json:"high_price"`
	LowPrice      float64   `json:"low_price"`
	PreviousClose float64   `json:"previous_close"`
	ChangePct     float64   `json:"change_pct"`
	ChangeValue   float64   `json:"change_value"`
	Volume        int64     `json:"volume"`
	Turnover      int64     `json:"turnover"`
	TurnoverRate  float64   `json:"turnover_rate"`
	Amplitude     float64   `json:"amplitude"`
	QuoteTime     string    `json:"quote_time"`
	FetchedAt     time.Time `json:"fetched_at"`
	Available     bool      `json:"available"`
}

func FormatQuoteTime(value int64) string {
	if value <= 0 {
		return ""
	}
	return fmt.Sprintf("%06d", value)
}
