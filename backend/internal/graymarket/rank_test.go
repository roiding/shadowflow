package graymarket

import (
	"testing"
	"time"
)

func moneyPoint(rankType RankType, at time.Time, market int64, code string, dark int64) MoneyPoint {
	return MoneyPoint{RankType: rankType, SnapshotAt: at, Market: market, Code: code, DarkMoney: dark}
}

func TestAssignMoneyRanksEmptyAndNil(t *testing.T) {
	AssignMoneyRanks(nil)
	AssignMoneyRanks([]MoneyPoint{})
}

func TestAssignMoneyRanksOrdersWithinSnapshot(t *testing.T) {
	at := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	points := []MoneyPoint{
		moneyPoint(RankConcept, at, 90, "BK002", 100),
		moneyPoint(RankConcept, at, 90, "BK001", 300),
		moneyPoint(RankConcept, at, 90, "BK003", 200),
	}
	AssignMoneyRanks(points)
	for index, want := range []struct {
		code string
		rank int64
	}{{"BK001", 1}, {"BK003", 2}, {"BK002", 3}} {
		if points[index].Code != want.code || points[index].Rank != want.rank {
			t.Fatalf("index %d: got %s/%d want %s/%d", index, points[index].Code, points[index].Rank, want.code, want.rank)
		}
	}
}

func TestAssignMoneyRanksRestartsPerSnapshot(t *testing.T) {
	first := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	second := first.Add(5 * time.Minute)
	points := []MoneyPoint{
		moneyPoint(RankIndustry, second, 90, "BK001", 50),
		moneyPoint(RankIndustry, first, 90, "BK001", 10),
		moneyPoint(RankIndustry, first, 90, "BK002", 20),
	}
	AssignMoneyRanks(points)
	if points[0].SnapshotAt != first || points[0].Code != "BK002" || points[0].Rank != 1 {
		t.Fatalf("first slot: %+v", points[0])
	}
	if points[1].Rank != 2 || points[2].SnapshotAt != second || points[2].Rank != 1 {
		t.Fatalf("ranks did not restart per snapshot: %+v", points)
	}
}

func TestAssignMoneyRanksBreaksTiesByCodeThenMarket(t *testing.T) {
	at := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	points := []MoneyPoint{
		moneyPoint(RankStock, at, 1, "600001", 100),
		moneyPoint(RankStock, at, 0, "600001", 100),
		moneyPoint(RankStock, at, 0, "000001", 100),
	}
	AssignMoneyRanks(points)
	if points[0].Code != "000001" || points[1].Market != 0 || points[1].Code != "600001" || points[2].Market != 1 {
		t.Fatalf("tie-break order wrong: %+v", points)
	}
	if points[0].Rank != 1 || points[1].Rank != 2 || points[2].Rank != 3 {
		t.Fatalf("tie ranks wrong: %+v", points)
	}
}

func TestAssignMoneyRanksPartitionsByRankType(t *testing.T) {
	at := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	points := []MoneyPoint{
		moneyPoint(RankStock, at, 0, "600001", 500),
		moneyPoint(RankConcept, at, 90, "BK001", 100),
		moneyPoint(RankConcept, at, 90, "BK002", 50),
	}
	AssignMoneyRanks(points)
	byKey := make(map[string]int64, len(points))
	for _, point := range points {
		byKey[string(point.RankType)+":"+point.Code] = point.Rank
	}
	// A mixed slice must rank each type independently: the stock must not
	// consume rank 1 of the concept partition or vice versa.
	if byKey["concept:BK001"] != 1 || byKey["concept:BK002"] != 2 || byKey["stock:600001"] != 1 {
		t.Fatalf("rank types were not partitioned: %+v", points)
	}
}

func TestAssignMoneyRanksZeroTimeFirstSnapshot(t *testing.T) {
	var zero time.Time
	points := []MoneyPoint{
		moneyPoint(RankIndustry, zero, 90, "BK001", 10),
		moneyPoint(RankIndustry, zero, 90, "BK002", 20),
	}
	AssignMoneyRanks(points)
	if points[0].Rank != 1 || points[1].Rank != 2 {
		t.Fatalf("zero-time snapshot ranks wrong: %+v", points)
	}
}
