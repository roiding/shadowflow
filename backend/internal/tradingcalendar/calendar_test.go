package tradingcalendar

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestPreviousTradingDaySkipsWeekendAndHoliday(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calendar.json")
	if err := os.WriteFile(path, []byte(`{"holidays":["2026-08-14"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	calendar, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	monday := time.Date(2026, 8, 17, 8, 0, 0, 0, location)
	if got := calendar.PreviousTradingDay(monday).Format("2006-01-02"); got != "2026-08-13" {
		t.Fatalf("unexpected previous trading day: %s", got)
	}
}

func TestRefreshIfNeededParsesAndPersistsOfficialSchedule(t *testing.T) {
	body := `<html><body><h2>2026年休市安排</h2>
<p>（一）元旦：1月1日至1月3日休市，1月5日起照常开市。</p>
<p>（二）春节：2月15日至2月23日休市，2月24日起照常开市。</p>
<p>（三）清明节：4月4日至4月6日休市，4月7日起照常开市。</p>
<p>（四）劳动节：5月1日至5月5日休市，5月6日起照常开市。</p>
<p>（五）端午节：6月19日至6月21日休市，6月22日起照常开市。</p>
<p>（六）中秋节：9月25日至9月27日休市，9月28日起照常开市。</p>
<p>（七）国庆节：10月1日至10月7日休市，10月8日起照常开市。</p>
<h2>相关公告</h2></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Fatal("calendar request has no user agent")
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "calendar.json")
	calendar, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, location)
	updated, err := calendar.RefreshIfNeeded(t.Context(), server.Client(), path, server.URL, now, 45)
	if err != nil || !updated {
		t.Fatalf("calendar was not refreshed: updated=%v err=%v", updated, err)
	}
	coverage := calendar.Coverage(now)
	if coverage.ValidThrough != "2026-12-31" || coverage.Expired || coverage.Source != server.URL {
		t.Fatalf("unexpected coverage: %+v", coverage)
	}
	for _, date := range []string{"2026-01-01", "2026-02-16", "2026-09-25", "2026-10-07"} {
		value, _ := time.ParseInLocation("2006-01-02", date, location)
		if calendar.IsTradingDay(value) {
			t.Fatalf("%s should be a holiday", date)
		}
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"valid_through": "2026-12-31"`) {
		t.Fatalf("calendar metadata was not persisted: %s", persisted)
	}
	updated, err = calendar.RefreshIfNeeded(t.Context(), server.Client(), path, server.URL, now, 45)
	if err != nil || updated {
		t.Fatalf("covered calendar should not refresh again: updated=%v err=%v", updated, err)
	}
}

func TestParseAnnualScheduleRejectsIncompletePage(t *testing.T) {
	if _, err := parseAnnualSchedule(`<h2>2026年休市安排</h2><p>1月1日休市。</p>`, 2026); err == nil {
		t.Fatal("expected incomplete official page to be rejected")
	}
}
