package thsfocus

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validSnapshot() Snapshot {
	change := 1.25
	changePercent := 10.01
	return Snapshot{
		TradeDate:          "2026-11-06",
		Source:             Source,
		Scope:              ScopeDefault,
		MarketScope:        MarketScope,
		FetchedAt:          time.Date(2026, 11, 6, 16, 0, 0, 0, Shanghai()),
		Blocks:             []Block{{Code: "885001", Name: "合成板块", Change: &change, LimitUpCount: 1, Stocks: []Stock{{Code: "000001", Name: "合成银行", MarketID: 33, ContinueNum: 2, Reason: "合成理由", ReasonDetail: "合成详情", HighDays: "2天2板", ChangePercent: &changePercent}}}},
		Ladder:             []Level{{Height: 2, Stocks: []Stock{{Code: "000001", Name: "合成银行", MarketID: 33, ContinueNum: 2}}}},
		Market:             json.RawMessage(`{"rise_fall":{"rise":1000}}`),
		Comparison:         json.RawMessage(`{"today_date":"2026-11-06"}`),
		SourceDateVerified: false,
		Limitations:        []string{"block_top仅提供Top20"},
		RawPages: []json.RawMessage{
			json.RawMessage(`{"status_code":0,"data":[]}`),
			json.RawMessage(`{"status_code":0,"data":[]}`),
			json.RawMessage(`{"status_code":0,"data":{}}`),
			json.RawMessage(`{"status_code":0,"data":{}}`),
		},
	}
}

func TestValidateSnapshotAcceptsValidSnapshot(t *testing.T) {
	snapshot := validSnapshot()
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	encoded, marshalError := json.Marshal(snapshot)
	if marshalError != nil {
		t.Fatalf("marshal failed: %v", marshalError)
	}
	for _, fieldName := range []string{
		`"trade_date"`,
		`"source":"ths_focus"`,
		`"market_scope":"source_default"`,
		`"source_date_verified":false`,
		`"limit_up_count":1`,
		`"reason_detail"`,
		`"change_percent"`,
	} {
		if !strings.Contains(string(encoded), fieldName) {
			t.Errorf("snapshot JSON missing %s", fieldName)
		}
	}
}

func TestValidateSnapshotAllowsExplicitEmptyListsWithLimitation(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Blocks = []Block{}
	snapshot.Ladder = []Level{}
	snapshot.Limitations = []string{"显式空真实数组"}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("explicit empty lists with limitation rejected: %v", err)
	}
	snapshot.Limitations = nil
	if err := ValidateSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "empty source arrays") {
		t.Fatalf("empty lists without limitation should fail, got %v", err)
	}
}

func TestValidateSnapshotRequiresExplicitArrays(t *testing.T) {
	for name, mutate := range map[string]func(*Snapshot){
		"nil blocks": func(snapshot *Snapshot) { snapshot.Blocks = nil },
		"nil ladder": func(snapshot *Snapshot) { snapshot.Ladder = nil },
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := validSnapshot()
			mutate(&snapshot)
			if err := ValidateSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "explicit arrays") {
				t.Fatalf("expected explicit array error, got %v", err)
			}
		})
	}
}

func TestValidateSnapshotRejectsCountAndDuplicateMismatch(t *testing.T) {
	cases := map[string]func(*Snapshot){
		"block count": func(snapshot *Snapshot) { snapshot.Blocks[0].LimitUpCount = 2 },
		"block duplicate": func(snapshot *Snapshot) {
			snapshot.Blocks[0].Stocks = append(snapshot.Blocks[0].Stocks, snapshot.Blocks[0].Stocks[0])
			snapshot.Blocks[0].LimitUpCount = 2
		},
		"block duplicate between blocks": func(snapshot *Snapshot) {
			snapshot.Blocks = append(snapshot.Blocks, snapshot.Blocks[0])
		},
		"ladder duplicate height": func(snapshot *Snapshot) {
			snapshot.Ladder = append(snapshot.Ladder, snapshot.Ladder[0])
		},
		"invalid stock identity": func(snapshot *Snapshot) { snapshot.Blocks[0].Stocks[0].Code = "1" },
		"negative continue num":  func(snapshot *Snapshot) { snapshot.Ladder[0].Stocks[0].ContinueNum = -1 },
		"market scope":           func(snapshot *Snapshot) { snapshot.MarketScope = "scope_default" },
		"source date verified":   func(snapshot *Snapshot) { snapshot.SourceDateVerified = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := validSnapshot()
			mutate(&snapshot)
			if err := ValidateSnapshot(snapshot); err == nil {
				t.Fatalf("invalid snapshot accepted")
			}
		})
	}
}

func TestValidateSnapshotRejectsDateAndSizeViolations(t *testing.T) {
	cases := map[string]func(*Snapshot){
		"invalid date":             func(snapshot *Snapshot) { snapshot.TradeDate = "2026/11/06" },
		"future fetched at":        func(snapshot *Snapshot) { snapshot.TradeDate = "2026-11-07" },
		"before close":             func(snapshot *Snapshot) { snapshot.FetchedAt = time.Date(2026, 11, 6, 15, 29, 59, 0, Shanghai()) },
		"raw pages":                func(snapshot *Snapshot) { snapshot.RawPages = snapshot.RawPages[:3] },
		"invalid raw page":         func(snapshot *Snapshot) { snapshot.RawPages[0] = json.RawMessage(`{`) },
		"empty market":             func(snapshot *Snapshot) { snapshot.Market = json.RawMessage(`{}`) },
		"non-object market":        func(snapshot *Snapshot) { snapshot.Market = json.RawMessage(`[]`) },
		"comparison missing date":  func(snapshot *Snapshot) { snapshot.Comparison = json.RawMessage(`{"yesterday_date":"2026-11-05"}`) },
		"comparison date mismatch": func(snapshot *Snapshot) { snapshot.Comparison = json.RawMessage(`{"today_date":"2026-11-05"}`) },
		"comparison date type":     func(snapshot *Snapshot) { snapshot.Comparison = json.RawMessage(`{"today_date":20261106}`) },
		"too many limitations": func(snapshot *Snapshot) {
			snapshot.Limitations = make([]string, MaxLimitations+1)
			for index := range snapshot.Limitations {
				snapshot.Limitations[index] = "有效限制"
			}
		},
		"oversize limitation": func(snapshot *Snapshot) {
			snapshot.Limitations = []string{strings.Repeat("界", MaxLimitationBytes)}
		},
		"blank limitation": func(snapshot *Snapshot) {
			snapshot.Limitations = []string{"  "}
		},
		"oversize raw page": func(snapshot *Snapshot) {
			snapshot.RawPages[0] = json.RawMessage(`"` + strings.Repeat("x", MaxRawPageBytes) + `"`)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := validSnapshot()
			mutate(&snapshot)
			if err := ValidateSnapshot(snapshot); err == nil {
				t.Fatalf("invalid snapshot accepted")
			}
		})
	}
}

func TestIsUsableThemeBlockRejectsUnclassifiedAndReasonlessBuckets(t *testing.T) {
	base := Block{
		Code: "885517", Name: "机器人概念",
		Stocks: []Stock{{Code: "000001", Name: "样本", Reason: "机器人"}},
	}
	if !IsUsableThemeBlock(base) {
		t.Fatal("a reasoned theme block should be usable")
	}
	namedLikeIndustry := base
	namedLikeIndustry.Name = "其他电子"
	if !IsUsableThemeBlock(namedLikeIndustry) {
		t.Fatal("a real theme whose name merely contains 其他 must remain usable")
	}
	for _, name := range []string{"其他概念", "未分类", "原因不明", "其他题材"} {
		block := base
		block.Name = name
		if IsUsableThemeBlock(block) {
			t.Errorf("unclassified bucket %q was accepted", name)
		}
	}
	noReason := base
	noReason.Stocks = []Stock{{Code: "000001", Name: "样本"}}
	if IsUsableThemeBlock(noReason) {
		t.Fatal("a block without any reason should be rejected")
	}
}

func TestValidateSnapshotRejectsDuplicateCodeWithinLadderLevel(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Ladder[0].Stocks = append(snapshot.Ladder[0].Stocks, snapshot.Ladder[0].Stocks[0])
	err := ValidateSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "duplicate stock") {
		t.Fatalf("duplicate stock within one ladder level was accepted: %v", err)
	}
}

func TestValidateSnapshotAllowsSameStockAtDifferentLadderLevels(t *testing.T) {
	snapshot := validSnapshot()
	stock := Stock{Code: "000001", Name: "合成银行", MarketID: 33, ContinueNum: 3}
	snapshot.Ladder = append(snapshot.Ladder, Level{Height: 3, Stocks: []Stock{stock}})
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("same stock at a different ladder level should be allowed: %v", err)
	}
}

func TestValidateSnapshotRequiresComparisonTodayDateToMatchTradeDate(t *testing.T) {
	cases := map[string]string{
		"mismatch":   `{"today_date":"2026-11-05"}`,
		"missing":    `{"yesterday_date":"2026-11-05"}`,
		"wrong type": `{"today_date":20261106}`,
	}
	for name, comparison := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := validSnapshot()
			snapshot.Comparison = json.RawMessage(comparison)
			if err := ValidateSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "comparison") {
				t.Fatalf("invalid comparison date was accepted: %v", err)
			}
		})
	}
}
