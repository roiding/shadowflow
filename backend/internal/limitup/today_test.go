package limitup

import (
	"context"
	"math"
	"testing"
	"time"
)

func todayEvent(date, code, pool string) Event {
	return Event{TradeDate: date, Code: code, Name: "股票" + code, MarketID: 33, MarketType: "HS", Pool: pool}
}

func TestAnalyzeTodayBuildsOnlyTheSealedMemberInducedGraph(t *testing.T) {
	snapshot := IntradaySnapshot{
		TradeDate: "2026-11-09", Source: SourceTHS, Scope: ScopeDefault,
		FetchedAt:   time.Date(2026, 11, 9, 10, 42, 0, 0, Shanghai()),
		TradeStatus: "open", TradeStatusName: "交易中", SealedCount: 4,
		Events: []Event{
			todayEvent("2026-11-09", "000001", PoolSealed),
			todayEvent("2026-11-09", "000002", PoolSealed),
			todayEvent("2026-11-09", "000003", PoolSealed),
			todayEvent("2026-11-09", "000004", PoolSealed),
		},
	}
	history := Dataset{
		Days: []Day{
			{TradeDate: "2026-11-06"}, {TradeDate: "2026-11-05"}, {TradeDate: "2026-11-04"},
			{TradeDate: "2026-11-03"}, {TradeDate: "2026-11-02"},
		},
		Events: []Event{
			todayEvent("2026-11-06", "000001", PoolSealed), todayEvent("2026-11-06", "000002", PoolSealed),
			todayEvent("2026-11-05", "000001", PoolSealed), todayEvent("2026-11-05", "000002", PoolSealed),
			todayEvent("2026-11-04", "000001", PoolSealed), todayEvent("2026-11-04", "000002", PoolSealed),
			todayEvent("2026-11-03", "000001", PoolSealed), todayEvent("2026-11-03", "000002", PoolBroken),
			todayEvent("2026-11-02", "000001", PoolBroken), todayEvent("2026-11-02", "000003", PoolSealed),
			// This stock is not in today's sealed pool and must not become a
			// node or inflate any member's degree.
			todayEvent("2026-11-06", "000099", PoolSealed),
			// Limit-down-only history is counted separately, never as touch.
			todayEvent("2026-11-05", "000004", PoolDown),
		},
	}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10",
		LookbackDays: 365, MinOverlap: 1, TopCores: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if network.SealedCount != 4 || len(network.Members) != 4 || network.HistoryAsOf != "2026-11-06" ||
		network.SnapshotAt != snapshot.FetchedAt || network.TradeStatus != "open" {
		t.Fatalf("unexpected network header: %+v", network)
	}
	byCode := make(map[string]TodayMember)
	for _, member := range network.Members {
		byCode[member.Code] = member
		if member.Code == "000099" {
			t.Fatal("a non-member leaked into the induced graph")
		}
	}
	first := byCode["000001"]
	if first.SealDays != 4 || first.TouchDays != 5 || first.BrokenDays != 1 || first.DownDays != 0 ||
		first.EffectiveLinks != 1 || first.StrongestPeerCode != "000002" || first.StrongestOverlapDays != 3 {
		t.Fatalf("unexpected member history or strongest peer: %+v", first)
	}
	second := byCode["000002"]
	if second.SealDays != 3 || second.BrokenDays != 1 || second.TouchDays != 4 {
		t.Fatalf("unexpected peer history: %+v", second)
	}
	fourth := byCode["000004"]
	if fourth.DownDays != 1 || fourth.TouchDays != 0 || fourth.EffectiveLinks != 0 {
		t.Fatalf("limit-down history must remain independent: %+v", fourth)
	}
	if len(network.Cores) != 2 || network.Cores[0].Member.Code != "000001" ||
		len(network.Cores[0].Links) != 1 || network.Cores[0].Links[0].Code != "000002" {
		t.Fatalf("unexpected core ranking: %+v", network.Cores)
	}
	strongest := network.Cores[0].Links[0]
	if strongest.OverlapDays != 3 || strongest.AnchorDays != 4 || strongest.PeerDays != 3 ||
		strongest.Jaccard != 0.75 || strongest.ConditionalProbability != 0.75 ||
		strongest.ReverseProbability != 1 || strongest.Lift != 1.25 || strongest.Phi == nil ||
		math.Abs(*strongest.Phi-0.6123724356957945) > 1e-9 {
		t.Fatalf("unexpected strongest link metrics: %+v", strongest)
	}
	if network.Cores[0].Member.CoreScore < network.Cores[1].Member.CoreScore ||
		math.IsNaN(network.Cores[0].Member.CoreScore) || network.Cores[0].Member.CoreScore > 100 {
		t.Fatalf("invalid core scores: %+v", network.Cores)
	}
}

func TestAnalyzeTodayDeduplicatesRepeatedHistoryEvents(t *testing.T) {
	snapshot := IntradaySnapshot{
		TradeDate: "2026-11-09", Source: SourceTHS, Scope: ScopeDefault,
		FetchedAt: time.Date(2026, 11, 9, 10, 42, 0, 0, Shanghai()), TradeStatus: "open", SealedCount: 2,
		Events: []Event{
			todayEvent("2026-11-09", "000001", PoolSealed),
			todayEvent("2026-11-09", "000002", PoolSealed),
		},
	}
	// The repository stores unique rows, but the analyzer must remain correct
	// for caller-side event merging. Repeated event records must not inflate
	// seal counts, overlap, heat, or conditional probability.
	history := Dataset{
		Days: []Day{
			{TradeDate: "2026-11-06"}, {TradeDate: "2026-11-05"}, {TradeDate: "2026-11-04"},
			{TradeDate: "2026-11-03"}, {TradeDate: "2026-11-02"}, {TradeDate: "2026-10-30"},
		},
		Events: []Event{
			todayEvent("2026-11-06", "000001", PoolSealed),
			todayEvent("2026-11-06", "000001", PoolSealed),
			todayEvent("2026-11-06", "000002", PoolSealed),
			todayEvent("2026-11-06", "000010", PoolSealed),
			todayEvent("2026-11-06", "000010", PoolSealed),
			todayEvent("2026-11-05", "000001", PoolSealed),
			todayEvent("2026-11-05", "000002", PoolSealed),
		},
	}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10", LookbackDays: 365, MinOverlap: 1, TopCores: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if network.Coverage.ObservedDays != 6 {
		t.Fatalf("unexpected complete-day count: %+v", network.Coverage)
	}
	member := network.Members[0]
	if member.SealDays != 2 || member.CoreScore > 100 {
		t.Fatalf("duplicate events must not inflate member history: %+v", member)
	}
	if len(network.Cores) != 1 || len(network.Cores[0].Links) != 1 {
		t.Fatalf("unexpected duplicated graph: %+v", network.Cores)
	}
	link := network.Cores[0].Links[0]
	if link.OverlapDays != 2 || link.AnchorDays != 2 || link.PeerDays != 2 ||
		link.ConditionalProbability != 1 || link.Lift != 3 || link.HeatAdjustedOverlap != 1.5 {
		t.Fatalf("duplicate dates must not inflate link metrics: %+v", link)
	}
	if len(network.Candidates) != 1 || network.Candidates[0].Code != "000010" ||
		network.Candidates[0].SealDays != 1 || network.Candidates[0].EffectiveLinks != 2 ||
		network.Candidates[0].Strength <= 0 {
		t.Fatalf("duplicate events must not inflate candidate history: %+v", network.Candidates)
	}
}

func TestAnalyzeTodayRejectsTodayInHistoryAndEmptyMembers(t *testing.T) {
	snapshot := IntradaySnapshot{TradeDate: "2026-11-09", Source: SourceTHS, Scope: ScopeDefault, SealedCount: 1, Events: []Event{
		todayEvent("2026-11-09", "000001", PoolSealed),
	}}
	if _, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot,
		History:  Dataset{Days: []Day{{TradeDate: "2026-11-09"}}},
	}); err == nil {
		t.Fatal("today's archive must not be accepted as history")
	}
	empty := snapshot
	empty.SealedCount = 0
	empty.Events = nil
	network, err := AnalyzeToday(context.Background(), TodayInput{Snapshot: empty, History: Dataset{Days: []Day{}, Events: []Event{}}})
	if err != nil || network.SealedCount != 0 || len(network.Members) != 0 || len(network.Cores) != 0 || network.HistoryAvailable {
		t.Fatalf("empty snapshot must return an explicit empty graph: %+v %v", network, err)
	}
	if _, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot,
		History:  Dataset{Events: []Event{todayEvent("2026-11-06", "000001", PoolSealed)}},
	}); err == nil {
		t.Fatal("historical events without a complete archive day must be rejected")
	}
	cutoffSnapshot := snapshot
	cutoffSnapshot.ArchivedThrough = "2026-11-05"
	if _, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: cutoffSnapshot,
		History: Dataset{
			Days:   []Day{{TradeDate: "2026-11-06"}},
			Events: []Event{todayEvent("2026-11-06", "000001", PoolSealed)},
		},
	}); err == nil {
		t.Fatal("history newer than the resolved archive cutoff must be rejected")
	}
}

func TestAnalyzeTodayNeighborStrengthUsesMembersByCode(t *testing.T) {
	// Three members form a path A-B-C: A-B and B-C are strong, A-C is weak.
	// B must score above A and C because its neighbours' strengths are added
	// after members are sorted. A pointer into the unsorted members slice
	// would make this depend on slice growth rather than the graph.
	snapshot := IntradaySnapshot{
		TradeDate: "2026-11-09", Source: SourceTHS, Scope: ScopeDefault, SealedCount: 3,
		FetchedAt: time.Date(2026, 11, 9, 10, 42, 0, 0, Shanghai()), TradeStatus: "open",
		Events: []Event{
			todayEvent("2026-11-09", "000001", PoolSealed),
			todayEvent("2026-11-09", "000002", PoolSealed),
			todayEvent("2026-11-09", "000003", PoolSealed),
		},
	}
	history := Dataset{
		Days: []Day{
			{TradeDate: "2026-11-06"}, {TradeDate: "2026-11-05"}, {TradeDate: "2026-11-04"},
		},
		Events: []Event{
			todayEvent("2026-11-06", "000001", PoolSealed), todayEvent("2026-11-06", "000002", PoolSealed),
			todayEvent("2026-11-05", "000001", PoolSealed), todayEvent("2026-11-05", "000002", PoolSealed),
			todayEvent("2026-11-04", "000002", PoolSealed), todayEvent("2026-11-04", "000003", PoolSealed),
			todayEvent("2026-11-05", "000003", PoolBroken),
		},
	}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10", LookbackDays: 365, MinOverlap: 1, TopCores: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.Cores) != 3 {
		t.Fatalf("expected all three connected members: %+v", network.Cores)
	}
	if network.Cores[0].Member.Code != "000002" {
		t.Fatalf("middle node should rank first, got %s: %+v", network.Cores[0].Member.Code, network.Cores)
	}
	if network.Cores[1].Member.CoreScore <= network.Cores[2].Member.CoreScore {
		t.Fatalf("outer nodes must retain distinct stable scores: %+v", network.Cores)
	}
}

func TestAnalyzeTodayRanksNonSealedCoSealCandidates(t *testing.T) {
	snapshot := IntradaySnapshot{
		TradeDate: "2026-11-09", Source: SourceTHS, Scope: ScopeDefault, SealedCount: 2,
		FetchedAt: time.Date(2026, 11, 9, 10, 42, 0, 0, Shanghai()), TradeStatus: "open",
		Events: []Event{
			todayEvent("2026-11-09", "000001", PoolSealed),
			todayEvent("2026-11-09", "000002", PoolSealed),
		},
	}
	history := Dataset{
		Days: []Day{
			{TradeDate: "2026-11-06"}, {TradeDate: "2026-11-05"},
			{TradeDate: "2026-11-04"}, {TradeDate: "2026-11-03"},
		},
		Events: []Event{
			todayEvent("2026-11-06", "000001", PoolSealed), todayEvent("2026-11-06", "000002", PoolSealed),
			todayEvent("2026-11-06", "000010", PoolSealed), todayEvent("2026-11-06", "000020", PoolSealed),
			todayEvent("2026-11-05", "000001", PoolSealed), todayEvent("2026-11-05", "000002", PoolSealed),
			todayEvent("2026-11-05", "000010", PoolSealed),
			todayEvent("2026-11-04", "000001", PoolSealed), todayEvent("2026-11-04", "000010", PoolSealed),
			todayEvent("2026-11-03", "000030", PoolSealed),
		},
	}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10",
		LookbackDays: 365, MinOverlap: 1, TopCores: 2, CandidateLimit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.Candidates) != 2 {
		t.Fatalf("expected two non-sealed candidates, got %+v", network.Candidates)
	}
	if network.MainlineThemes == nil {
		t.Fatal("mainline themes must be a non-nil empty array when no theme lookup is configured")
	}
	for _, candidate := range network.Candidates {
		if candidate.Themes == nil {
			t.Fatalf("candidate themes must be a non-nil empty array: %+v", candidate)
		}
	}
	first := network.Candidates[0]
	if first.Code != "000010" || first.EffectiveLinks != 2 || first.SealDays != 3 ||
		first.StrongestCoreCode != "000001" || first.StrongestOverlapDays != 3 ||
		len(first.TopLinks) != 2 || first.TopLinks[0].CoreCode != "000001" || first.TopLinks[0].OverlapDays != 3 {
		t.Fatalf("unexpected strongest candidate: %+v", first)
	}
	if first.Score <= network.Candidates[1].Score || first.Score <= 0 || first.Score > 100 {
		t.Fatalf("candidate scores must be ordered and bounded: %+v", network.Candidates)
	}
	for _, candidate := range network.Candidates {
		if candidate.Code == "000001" || candidate.Code == "000002" {
			t.Fatalf("a sealed member leaked into candidates: %+v", candidate)
		}
	}
	limited, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10",
		LookbackDays: 365, MinOverlap: 1, TopCores: 2, CandidateLimit: 1,
	})
	if err != nil || len(limited.Candidates) != 1 || limited.Candidates[0].Code != "000010" {
		t.Fatalf("candidate limit was not applied: %+v %v", limited.Candidates, err)
	}
}

func TestAnalyzeTodayAppliesMainlineThemeAlignment(t *testing.T) {
	snapshot := IntradaySnapshot{
		TradeDate: "2026-11-09", Source: SourceTHS, Scope: ScopeDefault, SealedCount: 2,
		FetchedAt: time.Date(2026, 11, 9, 10, 42, 0, 0, Shanghai()), TradeStatus: "open",
		Events: []Event{
			todayEvent("2026-11-09", "000001", PoolSealed),
			todayEvent("2026-11-09", "000002", PoolSealed),
		},
	}
	history := Dataset{
		Days: []Day{
			{TradeDate: "2026-11-06"}, {TradeDate: "2026-11-05"},
			{TradeDate: "2026-11-04"}, {TradeDate: "2026-11-03"},
		},
		Events: []Event{
			todayEvent("2026-11-06", "000001", PoolSealed), todayEvent("2026-11-06", "000002", PoolSealed),
			todayEvent("2026-11-06", "000010", PoolSealed), todayEvent("2026-11-06", "000020", PoolSealed),
			todayEvent("2026-11-05", "000001", PoolSealed), todayEvent("2026-11-05", "000002", PoolSealed),
			todayEvent("2026-11-05", "000010", PoolSealed),
			todayEvent("2026-11-04", "000001", PoolSealed), todayEvent("2026-11-04", "000010", PoolSealed),
			todayEvent("2026-11-03", "000020", PoolSealed),
		},
	}
	themes := map[string][]ThemeRef{
		"000001": {
			{Code: "BK0001", Name: "主线行业", Type: "industry"},
			{Code: "BK9001", Name: "融资融券", Type: "concept"},
			{Code: "BK9002", Name: "昨日高振幅", Type: "concept"},
		},
		"000002": {
			{Code: "BK0001", Name: "主线行业", Type: "industry"},
			{Code: "BK9001", Name: "融资融券", Type: "concept"},
		},
		"000010": {
			{Code: "BK0001", Name: "主线行业", Type: "industry"},
			{Code: "BK9002", Name: "昨日高振幅", Type: "concept"},
		},
		"000020": {{Code: "BK0002", Name: "旁支行业", Type: "industry"}},
	}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10",
		LookbackDays: 365, MinOverlap: 1, TopCores: 2, CandidateLimit: 5,
		ThemeLookup: func(codes []string) (map[string][]ThemeRef, error) {
			result := make(map[string][]ThemeRef, len(codes))
			for _, code := range codes {
				result[code] = themes[code]
			}
			return result, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.MainlineThemes) != 1 || network.MainlineThemes[0].Code != "BK0001" ||
		network.MainlineThemes[0].MemberCount != 2 || network.MainlineThemes[0].Weight != 1 {
		t.Fatalf("unexpected mainline themes: %+v", network.MainlineThemes)
	}
	byCode := make(map[string]TodayCandidate, len(network.Candidates))
	for _, candidate := range network.Candidates {
		byCode[candidate.Code] = candidate
	}
	themed := byCode["000010"]
	if themed.ThemeScore != 1 || len(themed.Themes) != 1 || themed.Themes[0].Code != "BK0001" {
		t.Fatalf("mainline candidate did not receive theme alignment: %+v", themed)
	}
	unthemed := byCode["000020"]
	if unthemed.ThemeScore != 0 || len(unthemed.Themes) != 0 {
		t.Fatalf("non-mainline candidate received theme alignment: %+v", unthemed)
	}
	if themed.Score <= unthemed.Score {
		t.Fatalf("theme alignment did not lift the mainline candidate: %+v", network.Candidates)
	}
}

func TestIsThematicConceptFiltersBroadMarketAttributes(t *testing.T) {
	for _, name := range []string{
		"融资融券", "昨日高振幅", "连续亏损", "QFII重仓", "2026中报预增",
		"专精特新", "央国企改革", "小盘股", "标准普尔", "历史新高",
	} {
		if isThematicConcept(name) {
			t.Errorf("broad concept %q was treated as thematic", name)
		}
	}
	for _, name := range []string{
		"机器人概念", "人形机器人", "新能源车", "华为概念", "人工智能",
		"AI眼镜", "影视概念", "短剧互动游戏", "券商概念", "参股保险",
	} {
		if !isThematicConcept(name) {
			t.Errorf("thematic concept %q was filtered out", name)
		}
	}
}
