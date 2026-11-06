package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/tradingcalendar"
)

func TestEventResearchAsOfShanghaiDateBoundary(testingT *testing.T) {
	calendarPath := filepath.Join(testingT.TempDir(), "calendar.json")
	if err := os.WriteFile(calendarPath, []byte(`{"holidays":["2026-09-25"],"valid_through":"2026-12-31"}`), 0o600); err != nil {
		testingT.Fatal(err)
	}
	calendar, err := tradingcalendar.Load(calendarPath)
	if err != nil {
		testingT.Fatal(err)
	}
	server, store := testServer(testingT, "")
	defer store.Close()
	server.calendar = calendar

	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"before cutoff", time.Date(2026, 9, 29, 15, 29, 59, 0, server.location), "2026-09-28"},
		{"at cutoff", time.Date(2026, 9, 29, 15, 30, 0, 0, server.location), "2026-09-29"},
		{"after cutoff", time.Date(2026, 9, 29, 16, 0, 0, 0, server.location), "2026-09-29"},
		{"explicit holiday", time.Date(2026, 9, 25, 10, 0, 0, 0, server.location), "2026-09-24"},
		{"weekend", time.Date(2026, 9, 26, 10, 0, 0, 0, server.location), "2026-09-24"},
		{"utc before cutoff", time.Date(2026, 9, 29, 7, 29, 59, 0, time.UTC), "2026-09-28"},
		{"utc at cutoff", time.Date(2026, 9, 29, 7, 30, 0, 0, time.UTC), "2026-09-29"},
	}
	for _, test := range cases {
		testingT.Run(test.name, func(testingT *testing.T) {
			if got := server.eventResearchAsOf(test.at); got != test.want {
				testingT.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}
