package thsfocus

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const (
	Source               = "ths_focus"
	TradeDateFormat      = "2006-01-02"
	MarketScope          = "source_default"
	ScopeDefault         = "HS,GEM2STAR"
	ScopeExpanded        = "HS,GEM2STAR,ST,NEW"
	MaxBlocks            = 20
	MaxLevels            = 64
	MaxSnapshotStocks    = 5000
	MaxRawPages          = 4
	MaxRawPageBytes      = 8 << 20
	MaxRawSnapshotBytes  = 32 << 20
	MaxNameBytes         = 256
	MaxReasonBytes       = 1024
	MaxReasonDetailBytes = 32 << 10
	MaxHighDaysBytes     = 128
	MaxTimestampBytes    = 32
	MaxLimitations       = 16
	MaxLimitationBytes   = 512
	MaximumLadderHeight  = 1000
)

var (
	ErrInvalid       = errors.New("invalid ths focus snapshot")
	stockCodePattern = regexp.MustCompile(`^[0-9]{6}$`)
)

type Snapshot struct {
	TradeDate          string            `json:"trade_date"`
	Source             string            `json:"source"`
	Scope              string            `json:"scope"`
	MarketScope        string            `json:"market_scope"`
	FetchedAt          time.Time         `json:"fetched_at"`
	Blocks             []Block           `json:"blocks"`
	Ladder             []Level           `json:"ladder"`
	Market             json.RawMessage   `json:"market"`
	Comparison         json.RawMessage   `json:"comparison"`
	SourceDateVerified bool              `json:"source_date_verified"`
	Limitations        []string          `json:"limitations"`
	RawPages           []json.RawMessage `json:"-"`
}

type Block struct {
	Code         string   `json:"code"`
	Name         string   `json:"name"`
	Change       *float64 `json:"change"`
	LimitUpCount int      `json:"limit_up_count"`
	Stocks       []Stock  `json:"stocks"`
}

type Stock struct {
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	MarketID      int      `json:"market_id"`
	ContinueNum   int      `json:"continue_num"`
	Reason        string   `json:"reason"`
	ReasonDetail  string   `json:"reason_detail"`
	HighDays      string   `json:"high_days"`
	ChangePercent *float64 `json:"change_percent"`
}

type Level struct {
	Height int     `json:"height"`
	Stocks []Stock `json:"stocks"`
}

func Shanghai() *time.Location {
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}

func ValidScope(scope string) bool {
	return scope == ScopeDefault || scope == ScopeExpanded
}

func ValidCode(code string) bool {
	return stockCodePattern.MatchString(code)
}
