package graymarket

import (
	"sort"
	"time"
)

// AssignMoneyRanks assigns a per-snapshot money rank to every point. The rank
// is only used to preserve the archived funding curve order; it must be cheap
// to compute and must not be recalculated with a database-side window update.
func AssignMoneyRanks(points []MoneyPoint) {
	sort.Slice(points, func(i, j int) bool {
		if points[i].SnapshotAt.Equal(points[j].SnapshotAt) {
			if points[i].DarkMoney == points[j].DarkMoney {
				return points[i].Code < points[j].Code
			}
			return points[i].DarkMoney > points[j].DarkMoney
		}
		return points[i].SnapshotAt.Before(points[j].SnapshotAt)
	})
	var current time.Time
	var rank int64
	for index := range points {
		if !points[index].SnapshotAt.Equal(current) {
			current = points[index].SnapshotAt
			rank = 1
		} else {
			rank++
		}
		points[index].Rank = rank
	}
}
