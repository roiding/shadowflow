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

func TestAnalyzeTodayThemeClusterCanFormCoreWithoutRawCoSeal(t *testing.T) {
	snapshot := IntradaySnapshot{
		TradeDate: "2026-11-09", Source: SourceTHS, Scope: ScopeDefault, SealedCount: 2,
		FetchedAt: time.Date(2026, 11, 9, 10, 42, 0, 0, Shanghai()), TradeStatus: "open",
		Events: []Event{
			todayEvent("2026-11-09", "000001", PoolSealed),
			todayEvent("2026-11-09", "000002", PoolSealed),
		},
	}
	history := Dataset{
		Days: []Day{{TradeDate: "2026-11-06"}, {TradeDate: "2026-11-05"}},
		Events: []Event{
			todayEvent("2026-11-06", "000001", PoolSealed),
			todayEvent("2026-11-05", "000002", PoolSealed),
		},
	}
	robot := ThemeTag{Code: "885517", Name: "机器人概念", Occurrences: 3, LastDate: "2026-11-06"}
	profiles := map[string]ThemeStockProfile{
		"000001": {Code: "000001", Name: "甲", LabelDays: 2, Tags: []ThemeTag{robot}},
		"000002": {Code: "000002", Name: "乙", LabelDays: 2, Tags: []ThemeTag{robot}},
	}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10",
		LookbackDays: 365, MinOverlap: 1, TopCores: 2, ThemeProfiles: profiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.ThemeMainline) != 1 || network.ThemeMainline[0].MemberCount != 2 {
		t.Fatalf("theme mainline was not formed: %+v", network.ThemeMainline)
	}
	if len(network.Cores) != 2 {
		t.Fatalf("theme-only members must still form cores: %+v", network.Cores)
	}
	for _, member := range network.Members {
		if member.EffectiveLinks != 0 || member.ThemeNeighborCount != 1 || member.ThemeStrength <= 0 || member.CoreScore <= 0 {
			t.Fatalf("theme affinity was not reflected in member/core score: %+v", member)
		}
	}
}

func TestAnalyzeTodayAppliesLiveSidePoolQuality(t *testing.T) {
	lastSeal := time.Date(2026, 11, 9, 14, 0, 0, 0, Shanghai())
	snapshot := IntradaySnapshot{
		TradeDate: "2026-11-09", Source: SourceTHS, Scope: ScopeDefault, SealedCount: 2,
		FetchedAt: time.Date(2026, 11, 9, 14, 0, 0, 0, Shanghai()), TradeStatus: "open",
		Events: []Event{
			{TradeDate: "2026-11-09", Code: "000001", Name: "封板甲", MarketID: 33, MarketType: "HS", Pool: PoolSealed, LastLimitAt: &lastSeal},
			{TradeDate: "2026-11-09", Code: "000002", Name: "封板乙", MarketID: 33, MarketType: "HS", Pool: PoolSealed, LastLimitAt: &lastSeal},
		},
		BrokenCount: 1,
		BrokenEvents: []Event{
			{TradeDate: "2026-11-09", Code: "000011", Name: "炸板丙", MarketID: 33, MarketType: "HS", Pool: PoolBroken},
		},
		DownCount: 1,
		DownEvents: []Event{
			{TradeDate: "2026-11-09", Code: "000012", Name: "跌停丁", MarketID: 33, MarketType: "HS", Pool: PoolDown},
		},
	}
	history := Dataset{
		Days:   []Day{{TradeDate: "2026-11-06"}},
		Events: []Event{todayEvent("2026-11-06", "000001", PoolSealed), todayEvent("2026-11-06", "000002", PoolSealed)},
	}
	robot := ThemeTag{Code: "885517", Name: "机器人概念", Occurrences: 3, LastDate: "2026-11-06"}
	profiles := map[string]ThemeStockProfile{
		"000001": {Code: "000001", Tags: []ThemeTag{robot}},
		"000002": {Code: "000002", Tags: []ThemeTag{robot}},
		"000011": {Code: "000011", Tags: []ThemeTag{robot}},
		"000012": {Code: "000012", Tags: []ThemeTag{robot}},
	}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10",
		LookbackDays: 365, MinOverlap: 1, TopCores: 2, ThemeProfiles: profiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	if network.BrokenCount != 1 || network.DownCount != 1 || len(network.ThemeMainline) != 1 {
		t.Fatalf("live side pools were not carried into the network: %+v", network)
	}
	theme := network.ThemeMainline[0]
	if theme.BrokenCount != 1 || theme.DownCount != 1 ||
		math.Abs(theme.SealQuality-(2.0/3.5)) > 1e-9 ||
		math.Abs(theme.Freshness-1) > 1e-9 {
		t.Fatalf("unexpected live side-pool quality: %+v", theme)
	}
}

func TestAnalyzeTodayUsesHistoricalTHSThemeClusters(t *testing.T) {
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
	robot := ThemeTag{Code: "885517", Name: "机器人概念", Occurrences: 4, RecentOccurrences: 2, LastDate: "2026-11-06"}
	other := ThemeTag{Code: "885999", Name: "旁支题材", Occurrences: 2, LastDate: "2026-11-06"}
	profiles := map[string]ThemeStockProfile{
		"000001": {Code: "000001", Name: "股票000001", LabelDays: 4, Tags: []ThemeTag{robot}},
		"000002": {Code: "000002", Name: "股票000002", LabelDays: 3, Tags: []ThemeTag{robot}},
		"000010": {Code: "000010", Name: "股票000010", LabelDays: 3, Tags: []ThemeTag{robot}},
		"000020": {Code: "000020", Name: "股票000020", LabelDays: 2, Tags: []ThemeTag{other}},
		"000030": {Code: "000030", Name: "题材独有候选", LabelDays: 2, Tags: []ThemeTag{robot}},
	}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10",
		LookbackDays: 365, MinOverlap: 1, TopCores: 2, CandidateLimit: 5,
		ThemeProfiles: profiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.MainlineThemes) != 0 {
		t.Fatalf("Eastmoney-compatible mainline field must stay empty: %+v", network.MainlineThemes)
	}
	if len(network.ThemeMainline) != 1 || network.ThemeMainline[0].Code != robot.Code ||
		network.ThemeMainline[0].MemberCount != 2 || network.ThemeMainline[0].Weight != 1 {
		t.Fatalf("unexpected THS mainline themes: %+v", network.ThemeMainline)
	}
	byCode := make(map[string]TodayCandidate, len(network.Candidates))
	for _, candidate := range network.Candidates {
		byCode[candidate.Code] = candidate
	}
	themed := byCode["000010"]
	if math.Abs(themed.ThemeScore-(6.0/8.0)) > 1e-9 || len(themed.ThemeTags) != 1 || themed.ThemeTags[0].Code != robot.Code {
		t.Fatalf("mainline candidate did not receive theme alignment: %+v", themed)
	}
	unthemed := byCode["000020"]
	if unthemed.ThemeScore != 0 || len(unthemed.ThemeTags) != 0 {
		t.Fatalf("non-mainline candidate received theme alignment: %+v", unthemed)
	}
	if themed.Score <= unthemed.Score {
		t.Fatalf("theme alignment did not lift the mainline candidate: %+v", network.Candidates)
	}
	themeOnly, exists := byCode["000030"]
	if !exists || math.Abs(themeOnly.ThemeScore-(6.0/8.0)) > 1e-9 || themeOnly.EffectiveLinks != 0 ||
		themeOnly.Strength != 0 || themeOnly.Score <= 0 {
		t.Fatalf("theme-only candidate was not added explicitly: %+v", themeOnly)
	}
	if len(network.MainlineCandidates) != 1 || len(network.MainlineCandidates[0].Candidates) == 0 {
		t.Fatalf("per-mainline candidates were not built: %+v", network.MainlineCandidates)
	}
	if len(network.MainlineCandidates[0].Theme.MemberCodes) != 2 {
		t.Fatalf("mainline member intersection evidence is missing: %+v", network.MainlineCandidates[0].Theme)
	}
}

func TestAnalyzeTodayBuildsIntersectingMainlineCandidateGroups(t *testing.T) {
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
		Days:   []Day{{TradeDate: "2026-11-06"}},
		Events: []Event{todayEvent("2026-11-06", "000001", PoolSealed)},
	}
	robot := ThemeTag{Code: "885517", Name: "机器人概念", Occurrences: 5, RecentOccurrences: 2, LastDate: "2026-11-06"}
	energy := ThemeTag{Code: "885431", Name: "新能源汽车", Occurrences: 5, RecentOccurrences: 2, LastDate: "2026-11-06"}
	profiles := map[string]ThemeStockProfile{
		"000001": {Code: "000001", Name: "甲", LabelDays: 2, Tags: []ThemeTag{robot}},
		"000002": {Code: "000002", Name: "乙", LabelDays: 2, Tags: []ThemeTag{robot, energy}},
		"000003": {Code: "000003", Name: "丙", LabelDays: 2, Tags: []ThemeTag{energy}},
		"000010": {Code: "000010", Name: "双主线候选", LabelDays: 2, Tags: []ThemeTag{robot, energy}},
		"000020": {Code: "000020", Name: "机器人候选", LabelDays: 2, Tags: []ThemeTag{robot}},
		"000030": {Code: "000030", Name: "新能源候选", LabelDays: 2, Tags: []ThemeTag{energy}},
	}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: history, HistoryFrom: "2025-11-10",
		LookbackDays: 365, MinOverlap: 1, TopCores: 3, CandidateLimit: 5,
		ThemeProfiles: profiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.ThemeMainline) != 2 || len(network.MainlineCandidates) != 2 {
		t.Fatalf("two real-time mainlines must remain separate: %+v %+v", network.ThemeMainline, network.MainlineCandidates)
	}
	groups := make(map[string]TodayMainlineGroup, len(network.MainlineCandidates))
	for _, group := range network.MainlineCandidates {
		groups[group.Theme.Code] = group
	}
	robotGroup, robotExists := groups[robot.Code]
	energyGroup, energyExists := groups[energy.Code]
	if !robotExists || !energyExists {
		t.Fatalf("mainline candidate groups are incomplete: %+v", groups)
	}
	hasCandidate := func(group TodayMainlineGroup, code string) bool {
		for _, candidate := range group.Candidates {
			if candidate.Code == code {
				return true
			}
		}
		return false
	}
	if !hasCandidate(robotGroup, "000010") || !hasCandidate(energyGroup, "000010") ||
		!hasCandidate(robotGroup, "000020") || !hasCandidate(energyGroup, "000030") {
		t.Fatalf("candidate intersection was not preserved across mainlines: robot=%+v energy=%+v", robotGroup, energyGroup)
	}
	var crossed *TodayCandidate
	for index := range robotGroup.Candidates {
		if robotGroup.Candidates[index].Code == "000010" {
			crossed = &robotGroup.Candidates[index]
			break
		}
	}
	if crossed == nil || crossed.CrossMainlineCount != 2 || len(crossed.MainlineCodes) != 2 {
		t.Fatalf("cross-mainline candidate metadata is missing: %+v", crossed)
	}
	if len(network.MainlineIntersections) != 1 {
		t.Fatalf("expected one mainline intersection: %+v", network.MainlineIntersections)
	}
	intersection := network.MainlineIntersections[0]
	if intersection.SharedMemberCount != 1 || len(intersection.SharedMemberCodes) != 1 ||
		intersection.SharedMemberCodes[0] != "000002" {
		t.Fatalf("mainline intersection members are wrong: %+v", intersection)
	}
}

func TestTodayCandidateWeightsSumToOne(t *testing.T) {
	total := TodayCandidateWeightLive + TodayCandidateWeightMainline +
		TodayCandidateWeightHistory + TodayCandidateWeightCoverage
	if math.Abs(total-1) > 1e-12 {
		t.Fatalf("candidate weights must sum to one, got %v", total)
	}
	liveTotal := TodayLiveWeightStart + TodayLiveWeightRoom + TodayLiveWeightTurnover +
		TodayLiveWeightVolume + TodayLiveWeightFlow + TodayLiveWeightDark
	if math.Abs(liveTotal-1) > 1e-12 {
		t.Fatalf("live confirmation weights must sum to one, got %v", liveTotal)
	}
}

func TestLatestResearchMinuteIndexUsesCompletedBucket(t *testing.T) {
	cases := []struct {
		at    time.Time
		index int
		valid bool
	}{
		{time.Date(2026, 11, 11, 9, 34, 0, 0, Shanghai()), 0, false},
		{time.Date(2026, 11, 11, 10, 42, 0, 0, Shanghai()), 13, true},
		{time.Date(2026, 11, 11, 11, 45, 0, 0, Shanghai()), 23, true},
		{time.Date(2026, 11, 11, 14, 30, 0, 0, Shanghai()), 41, true},
		{time.Date(2026, 11, 11, 19, 20, 0, 0, Shanghai()), 47, true},
	}
	for _, test := range cases {
		index, valid := LatestResearchMinuteIndex(test.at)
		if index != test.index || valid != test.valid {
			t.Fatalf("minute index for %s = %d/%v, want %d/%v", test.at, index, valid, test.index, test.valid)
		}
	}
}

func TestScoreTodayCandidatesDoesNotRewardRecentLimitCount(t *testing.T) {
	candidates := []TodayCandidate{
		{
			Code: "000001", Name: "近期常涨停", ThemeScore: 0, Coverage: 0.9,
			SealDays: 20, TouchDays: 40, LastSealDate: "2026-11-10", HistoryScore: 1,
		},
		{
			Code: "000002", Name: "今日主线候选", ThemeScore: 0.8, Coverage: 0.5,
			SealDays: 2, TouchDays: 2, LastSealDate: "2026-11-05", HistoryScore: 0.7,
		},
	}
	scored := scoreTodayCandidates(candidates, nil, nil, nil)
	if scored[0].Code != "000002" || scored[0].Score <= scored[1].Score {
		t.Fatalf("today's mainline must outrank a recent-limit-frequency candidate: %+v", scored)
	}
}

func TestLiveCandidateUsesBoardSpecificLimitRatio(t *testing.T) {
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, Shanghai())
	if limitUpRatio("600002") != 0.10 {
		t.Fatalf("main-board ST must use the current 10%% limit, got %v", limitUpRatio("600002"))
	}
	main := TodayCandidate{Code: "600001", Name: "主板股", HistoryScore: 0, ThemeScore: 0}
	gem := TodayCandidate{Code: "300001", Name: "创业板股", HistoryScore: 0, ThemeScore: 0}
	quotes := map[string]LiveQuote{
		"600001": {
			Code: "600001", Available: true, PreviousClose: 10, HighPrice: 11,
			ChangePct: 0.099, TurnoverRate: 0.05, ListingDate: "20200101", FetchedAt: now,
		},
		"300001": {
			Code: "300001", Available: true, PreviousClose: 10, HighPrice: 11.9,
			ChangePct: 0.15, TurnoverRate: 0.05, ListingDate: "20200101", FetchedAt: now,
		},
	}
	scored := scoreTodayCandidates([]TodayCandidate{main, gem}, quotes, nil, nil)
	byCode := make(map[string]TodayCandidate, len(scored))
	for _, candidate := range scored {
		byCode[candidate.Code] = candidate
	}
	mainScored, mainExists := byCode["600001"]
	gemScored, gemExists := byCode["300001"]
	if !mainExists || !gemExists || mainScored.LimitRatio != 10 || gemScored.LimitRatio != 20 ||
		!mainScored.TouchedLimit || gemScored.TouchedLimit ||
		mainScored.DistanceToLimit == nil || *mainScored.DistanceToLimit <= 0 ||
		gemScored.DistanceToLimit == nil || math.Abs(*gemScored.DistanceToLimit-5) > 1e-9 {
		t.Fatalf("board-specific limit distance or touch flag is wrong: %+v", scored)
	}
}

func TestLiveCandidatePrefersRoomAndPenalizesTouchedLimit(t *testing.T) {
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, Shanghai())
	candidates := []TodayCandidate{
		{Code: "600001", Name: "接近涨停", ThemeScore: 0, HistoryScore: 0},
		{Code: "600002", Name: "仍有空间", ThemeScore: 0, HistoryScore: 0},
		{Code: "600003", Name: "触板未封", ThemeScore: 0, HistoryScore: 0},
	}
	quotes := map[string]LiveQuote{
		"600001": {
			Code: "600001", Available: true, PreviousClose: 10, HighPrice: 10.9,
			ChangePct: 0.087, TurnoverRate: 0.05, VolumeRatio: floatPointer(1),
			MainFlowPct: floatPointer(5), ListingDate: "20200101", FetchedAt: now,
		},
		"600002": {
			Code: "600002", Available: true, PreviousClose: 10, HighPrice: 10.5,
			ChangePct: 0.04, TurnoverRate: 0.05, VolumeRatio: floatPointer(1),
			MainFlowPct: floatPointer(5), ListingDate: "20200101", FetchedAt: now,
		},
		"600003": {
			Code: "600003", Available: true, PreviousClose: 10, HighPrice: 11,
			ChangePct: 0.04, TurnoverRate: 0.05, VolumeRatio: floatPointer(1),
			MainFlowPct: floatPointer(5), ListingDate: "20200101", FetchedAt: now,
		},
	}
	scored := scoreTodayCandidates(candidates, quotes, nil, nil)
	byCode := make(map[string]TodayCandidate, len(scored))
	for _, candidate := range scored {
		byCode[candidate.Code] = candidate
	}
	if byCode["600002"].LiveScore <= byCode["600001"].LiveScore {
		t.Fatalf("remaining room must outrank near-limit progress: %+v", scored)
	}
	if byCode["600003"].LiveScore >= byCode["600002"].LiveScore {
		t.Fatalf("touched-but-unsealed must be penalized, not rewarded: %+v", scored)
	}
	for _, expected := range []string{"涨幅接近涨停", "距涨停不足1.5%"} {
		found := false
		for _, flag := range byCode["600001"].RiskFlags {
			if flag == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing risk flag %q: %+v", expected, byCode["600001"].RiskFlags)
		}
	}
	if byCode["600003"].RiskPenalty <= byCode["600002"].RiskPenalty {
		t.Fatalf("touched-but-unsealed risk penalty was not applied: %+v", scored)
	}
}

func TestLiveCandidateUsesOwnHistoricalTurnoverPercentile(t *testing.T) {
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, Shanghai())
	candidates := []TodayCandidate{
		{Code: "600001", Name: "自身历史高换手", ThemeScore: 0, HistoryScore: 0, LastSealDate: "2026-11-01", SealDays: 1},
		{Code: "600002", Name: "自身历史低换手", ThemeScore: 0, HistoryScore: 0, LastSealDate: "2026-11-01", SealDays: 1},
	}
	quotes := map[string]LiveQuote{
		"600001": {
			Code: "600001", Available: true, PreviousClose: 10, HighPrice: 10.4,
			ChangePct: 0.04, TurnoverRate: 0.03, Turnover: 100_000_000,
			ListingDate: "20200101", FetchedAt: now,
		},
		"600002": {
			Code: "600002", Available: true, PreviousClose: 10, HighPrice: 10.4,
			ChangePct: 0.04, TurnoverRate: 0.06, Turnover: 100_000_000,
			ListingDate: "20200101", FetchedAt: now,
		},
	}
	histories := map[string]CandidateHistoryMetrics{
		"600001": {TurnoverRates: []float64{1, 1.2, 1.4, 1.6, 1.8}},
		"600002": {TurnoverRates: []float64{8, 9, 10, 11, 12}},
	}
	scored := scoreTodayCandidates(candidates, quotes, histories, nil)
	byCode := make(map[string]TodayCandidate, len(scored))
	for _, candidate := range scored {
		byCode[candidate.Code] = candidate
	}
	if byCode["600001"].LiveScore <= byCode["600002"].LiveScore {
		t.Fatalf("a stock should be judged against its own turnover history: %+v", scored)
	}
	if byCode["600001"].TurnoverPercentile == nil ||
		math.Abs(*byCode["600001"].TurnoverPercentile-100) > 1e-6 ||
		byCode["600002"].TurnoverPercentile == nil ||
		math.Abs(*byCode["600002"].TurnoverPercentile-0) > 1e-6 {
		t.Fatalf("self-history turnover percentiles were not exposed: %+v", byCode)
	}
}

func TestHighTurnoverRiskUsesOwnHistoryPercentile(t *testing.T) {
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, Shanghai())
	candidate := TodayCandidate{
		Code: "600001", Name: "自身历史放量", ThemeScore: 0, HistoryScore: 0,
		LastSealDate: "2026-11-01", SealDays: 1,
	}
	scored := scoreTodayCandidates([]TodayCandidate{candidate}, map[string]LiveQuote{
		"600001": {
			Code: "600001", Available: true, PreviousClose: 10, HighPrice: 10.4,
			ChangePct: 0.04, TurnoverRate: 0.10,
			Turnover: 100_000_000, ListingDate: "20200101", FetchedAt: now,
		},
	}, map[string]CandidateHistoryMetrics{
		"600001": {TurnoverRates: []float64{1, 2, 3, 4, 5}},
	}, nil)
	if len(scored) != 1 || scored[0].TurnoverPercentile == nil ||
		*scored[0].TurnoverPercentile < 90 {
		t.Fatalf("self-history turnover percentile is missing: %+v", scored)
	}
	found := false
	for _, flag := range scored[0].RiskFlags {
		if flag == "换手处于自身历史前10%" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("self-history turnover risk flag is missing: %+v", scored[0].RiskFlags)
	}
}

func TestLiveCandidateUsesDarkMoneySelfHistoryPercentile(t *testing.T) {
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, Shanghai())
	candidate := TodayCandidate{
		Code: "600001", Name: "暗盘样本", ThemeScore: 0, HistoryScore: 0,
		LastSealDate: "2026-11-01", SealDays: 1,
	}
	scored := scoreTodayCandidates([]TodayCandidate{candidate}, map[string]LiveQuote{
		"600001": {
			Code: "600001", Available: true, PreviousClose: 10, HighPrice: 10.4,
			ChangePct: 0.04, TurnoverRate: 0.05, Turnover: 100_000_000,
			ListingDate: "20200101", FetchedAt: now,
		},
	}, map[string]CandidateHistoryMetrics{
		"600001": {DarkFlowPcts: []float64{1, 2, 3, 4, 5}},
	}, map[string]LiveDarkMoney{
		"600001": {Code: "600001", Available: true, DarkMoney: 10_000_000},
	})
	if len(scored) != 1 || scored[0].DarkMoney == nil || *scored[0].DarkMoney != 10_000_000 ||
		scored[0].DarkMoneyPct == nil || math.Abs(*scored[0].DarkMoneyPct-10) > 1e-9 ||
		scored[0].DarkFlowPercentile == nil || math.Abs(*scored[0].DarkFlowPercentile-100) > 1e-9 {
		t.Fatalf("dark-money self-history percentile was not used: %+v", scored)
	}
}

func TestSparseSelfHistoryShrinksTowardNeutral(t *testing.T) {
	if got := shrinkSparsePercentile(1, 5); math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("5-sample percentile should shrink to 0.75, got %v", got)
	}
	if got := shrinkSparsePercentile(1, 20); math.Abs(got-0.9) > 1e-9 {
		t.Fatalf("20-sample percentile should shrink to 0.9, got %v", got)
	}
	if got := shrinkSparsePercentile(0, 20); math.Abs(got-0.1) > 1e-9 {
		t.Fatalf("zero percentile should shrink to 0.1, got %v", got)
	}
}

func TestCandidateCoverageIsSquareRootDamped(t *testing.T) {
	low := blendedCandidateScore(0.5, 0.5, 0.5, 0.25, 1, true)
	high := blendedCandidateScore(0.5, 0.5, 0.5, 1, 1, true)
	if math.Abs((high-low)-5) > 1e-9 {
		t.Fatalf("coverage should be square-root damped, low=%v high=%v", low, high)
	}
}

func TestLiveCandidateRiskFlagsIncludeSTNewListingAndLowLiquidity(t *testing.T) {
	now := time.Date(2026, 11, 11, 14, 30, 0, 0, Shanghai())
	candidate := TodayCandidate{
		Code: "600002", Name: "*ST样本", ThemeScore: 0, HistoryScore: 0,
	}
	scored := scoreTodayCandidates([]TodayCandidate{candidate}, map[string]LiveQuote{
		"600002": {
			Code: "600002", Available: true, PreviousClose: 10, HighPrice: 10.1,
			ChangePct: 0.01, TurnoverRate: 0.005, VolumeRatio: floatPointer(0.3),
			ListingDate: "20261020", FetchedAt: now,
		},
	}, nil, nil)
	if len(scored) != 1 || scored[0].RiskScore >= 1 || scored[0].RiskPenalty <= 0 {
		t.Fatalf("risk score was not reduced: %+v", scored)
	}
	for _, expected := range []string{"ST", "上市不足60日", "换手不足1%", "量比不足0.5", "成交额不足2000万", "近一年无收盘封板"} {
		found := false
		for _, flag := range scored[0].RiskFlags {
			if flag == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing risk flag %q: %+v", expected, scored[0].RiskFlags)
		}
	}
}

func TestAnalyzeTodayThemeMainlineIgnoresRealtimeReason(t *testing.T) {
	snapshot := IntradaySnapshot{
		TradeDate: "2026-11-11", Source: SourceTHS, Scope: ScopeDefault,
		FetchedAt:   time.Date(2026, 11, 11, 10, 30, 0, 0, Shanghai()),
		TradeStatus: "open", SealedCount: 2,
		Events: []Event{
			{TradeDate: "2026-11-11", Code: "000001", Name: "甲", MarketID: 33, MarketType: "HS", Pool: PoolSealed, Reason: "机器人"},
			{TradeDate: "2026-11-11", Code: "000002", Name: "乙", MarketID: 33, MarketType: "HS", Pool: PoolSealed, Reason: "机器人"},
		},
	}
	bank := ThemeTag{Code: "885001", Name: "银行", Occurrences: 3, LastDate: "2026-11-10"}
	network, err := AnalyzeToday(context.Background(), TodayInput{
		Snapshot: snapshot, History: Dataset{Days: []Day{}, Events: []Event{}},
		LookbackDays: 365, MinOverlap: 1, TopCores: 2,
		ThemeProfiles: map[string]ThemeStockProfile{
			"000001": {Code: "000001", Name: "甲", LabelDays: 3, Tags: []ThemeTag{bank}},
			"000002": {Code: "000002", Name: "乙", LabelDays: 3, Tags: []ThemeTag{bank}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.ThemeMainline) != 1 || network.ThemeMainline[0].Code != bank.Code {
		t.Fatalf("realtime reason must not become today's mainline: %+v", network.ThemeMainline)
	}
}

func floatPointer(value float64) *float64 {
	return &value
}
