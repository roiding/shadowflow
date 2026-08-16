package scheduler

import (
	"testing"
	"time"
)

func TestTradingMinuteBoundaries(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tests := map[string]bool{
		"09:30": false, "09:31": true, "11:30": true, "11:31": false,
		"13:00": false, "13:01": true, "15:00": true, "15:01": false,
	}
	for value, expected := range tests {
		parsed, _ := time.ParseInLocation("15:04", value, location)
		if actual := isTradingMinute(parsed); actual != expected {
			t.Errorf("%s: expected %v, got %v", value, expected, actual)
		}
	}
}

func TestTradingDayContainsExactly240MinuteJobs(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, location)
	minutes := 0
	for current := start; current.Before(start.Add(24 * time.Hour)); current = current.Add(time.Minute) {
		if jobKind(current) == "minute" {
			minutes++
		}
	}
	if minutes != 240 {
		t.Fatalf("expected 240 minute jobs, got %d", minutes)
	}
}

func TestScheduledJobBoundaries(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tests := map[string]string{
		"07:59": "", "08:00": "relations", "08:01": "", "08:50": "relations", "08:51": "",
		"08:59": "", "09:00": "cleanup", "09:01": "", "09:15": "relations", "09:16": "", "09:30": "",
		"15:05": "", "15:30": "", "16:00": "end-of-day", "16:05": "end-of-day", "16:10": "end-of-day", "16:11": "",
		"16:15": "stock-kline", "17:30": "stock-kline", "20:00": "stock-kline", "20:01": "",
	}
	for value, expected := range tests {
		parsed, _ := time.ParseInLocation("15:04", value, location)
		if actual := jobKind(parsed); actual != expected {
			t.Errorf("%s: expected %q, got %q", value, expected, actual)
		}
	}
}
