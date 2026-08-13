package tradingcalendar

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCalendarOverridesWeekdays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calendar.json")
	if err := os.WriteFile(path, []byte(`{"holidays":["2026-10-01"],"workdays":["2026-10-10"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	calendar, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, test := range []struct {
		date string
		want bool
	}{
		{"2026-10-01", false},
		{"2026-10-10", true},
		{"2026-08-13", true},
		{"2026-08-15", false},
	} {
		value, _ := time.ParseInLocation("2006-01-02", test.date, location)
		if got := calendar.IsTradingDay(value); got != test.want {
			t.Errorf("%s: got %v, want %v", test.date, got, test.want)
		}
	}
}

func TestCalendarRejectsInvalidOrConflictingDates(t *testing.T) {
	for name, body := range map[string]string{
		"invalid":  `{"holidays":["2026-02-30"]}`,
		"conflict": `{"holidays":["2026-10-01"],"workdays":["2026-10-01"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "calendar.json")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected invalid calendar to be rejected")
			}
		})
	}
}
