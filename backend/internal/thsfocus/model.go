package thsfocus

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
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
	MaxTabs              = 64
	MaxSnapshotStocks    = 5000
	MaxRawPages          = 6
	MaxRawPageBytes      = 8 << 20
	MaxRawSnapshotBytes  = 48 << 20
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
	ErrInvalid             = errors.New("invalid ths focus snapshot")
	stockCodePattern       = regexp.MustCompile(`^[0-9]{6}$`)
	unclassifiedThemeNames = []string{
		"其他", "其它", "未分类", "未归类", "原因不明", "无明确原因",
		"无原因", "未知", "待分类", "未明确", "综合题材", "其他题材", "其他概念",
	}
	unclassifiedThemePrefixes = []string{
		"其他概念", "其他题材", "未分类", "未归类", "原因不明",
		"无明确原因", "待分类", "未明确",
	}
)

type Snapshot struct {
	TradeDate          string            `json:"trade_date"`
	Source             string            `json:"source"`
	Scope              string            `json:"scope"`
	MarketScope        string            `json:"market_scope"`
	FetchedAt          time.Time         `json:"fetched_at"`
	Blocks             []Block           `json:"blocks"`
	Ladder             []Level           `json:"ladder"`
	Tabs               []Tab             `json:"tabs"`
	TabDateVerified    bool              `json:"tab_date_verified"`
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

// Tab is one category from the THS limit-up chart (涨停简图). Unlike the
// block_top endpoint, this is the source's own daily thematic classification
// and can include narrow labels such as "名称炒作" or "“东方系”".
type Tab struct {
	Name   string     `json:"name"`
	Rank   int        `json:"rank"`
	Stocks []TabStock `json:"stocks"`
}

// TabStock preserves the source's short and detailed reasons. The short
// abnormal_reason is often the thematic catalyst, while reason contains
// technical limit-up tags such as "放量涨停".
type TabStock struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	MarketID       int    `json:"market_id"`
	Reason         string `json:"reason"`
	AbnormalReason string `json:"abnormal_reason"`
	DetailReason   string `json:"detail_reason"`
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

// IsUsableThemeBlock excludes buckets that do not identify a tradable reason.
// The source can return a residual bucket such as "其他概念", or a block whose
// stocks have no reason_type at all. Treating either as a shared theme would
// manufacture a relationship between unrelated limit-up stocks.
func IsUsableThemeBlock(block Block) bool {
	name := strings.TrimSpace(block.Name)
	if strings.TrimSpace(block.Code) == "" || name == "" || len(block.Stocks) == 0 {
		return false
	}
	for _, term := range unclassifiedThemeNames {
		if name == term {
			return false
		}
	}
	for _, prefix := range unclassifiedThemePrefixes {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	for _, stock := range block.Stocks {
		if strings.TrimSpace(stock.Reason) != "" || strings.TrimSpace(stock.ReasonDetail) != "" {
			return true
		}
	}
	return false
}

// IsUsableThemeTab applies the same residual-bucket rule to the limit-up
// chart categories. A named category with at least one source explanation is
// usable; "其他概念" and similar buckets are never used to create a theme
// relationship.
func IsUsableThemeTab(tab Tab) bool {
	name := strings.TrimSpace(tab.Name)
	if name == "" || len(tab.Stocks) == 0 {
		return false
	}
	for _, term := range unclassifiedThemeNames {
		if name == term {
			return false
		}
	}
	for _, prefix := range unclassifiedThemePrefixes {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	for _, stock := range tab.Stocks {
		if strings.TrimSpace(stock.Reason) != "" ||
			strings.TrimSpace(stock.AbnormalReason) != "" ||
			strings.TrimSpace(stock.DetailReason) != "" {
			return true
		}
	}
	return false
}
