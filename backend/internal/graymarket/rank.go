package graymarket

import (
	"sort"
	"time"
)

// AssignMoneyRanks assigns a per-snapshot money rank to every point,
// partitioned by rank type so a mixed industry/concept/stock slice cannot
// interleave rankings. The rank preserves the archived funding curve order;
// it must be cheap to compute here, and the archive store recomputes it with
// the same ordering (dark_money DESC, code ASC, market ASC) once the full
// universe is durable — keep the two in sync (see finalizeBoardArchiveRanks
// and finalizeStockArchiveRanks in repository/sqlite).
func AssignMoneyRanks(points []MoneyPoint) {
	sort.Slice(points, func(i, j int) bool {
		if points[i].RankType != points[j].RankType {
			return points[i].RankType < points[j].RankType
		}
		if !points[i].SnapshotAt.Equal(points[j].SnapshotAt) {
			return points[i].SnapshotAt.Before(points[j].SnapshotAt)
		}
		if points[i].DarkMoney != points[j].DarkMoney {
			return points[i].DarkMoney > points[j].DarkMoney
		}
		if points[i].Code != points[j].Code {
			return points[i].Code < points[j].Code
		}
		return points[i].Market < points[j].Market
	})
	var currentType RankType
	var current time.Time
	var rank int64
	for index := range points {
		if points[index].RankType != currentType || !points[index].SnapshotAt.Equal(current) {
			currentType = points[index].RankType
			current = points[index].SnapshotAt
			rank = 1
		} else {
			rank++
		}
		points[index].Rank = rank
	}
}
