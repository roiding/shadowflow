package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/roiding/shadowflow/internal/repository"
	"github.com/roiding/shadowflow/internal/tradingcalendar"
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
		"09:05": "maintenance",
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

type schedulerCollector struct {
	mu         sync.Mutex
	calls      []string
	endStarted chan struct{}
	endRelease chan struct{}
	hasEnd     bool
	hasKline   bool
}

func (c *schedulerCollector) CollectBoards(context.Context, time.Time) error {
	c.mu.Lock()
	c.calls = append(c.calls, "minute")
	c.mu.Unlock()
	return nil
}

func (c *schedulerCollector) CollectEndOfDay(context.Context, time.Time) error {
	c.mu.Lock()
	c.calls = append(c.calls, "end-of-day")
	c.mu.Unlock()
	if c.endStarted != nil {
		close(c.endStarted)
		<-c.endRelease
	}
	c.mu.Lock()
	c.hasEnd = true
	c.mu.Unlock()
	return nil
}

func (c *schedulerCollector) CollectStockKlines(context.Context, time.Time) error {
	c.mu.Lock()
	c.calls = append(c.calls, "stock-kline")
	c.hasKline = true
	c.mu.Unlock()
	return nil
}

func (c *schedulerCollector) CleanupArchivedIntraday(context.Context, string) error {
	return nil
}

func (c *schedulerCollector) Maintain(context.Context, time.Time, int, int) (repository.MaintenanceResult, error) {
	return repository.MaintenanceResult{}, nil
}

func (c *schedulerCollector) HasEndOfDayArchive(context.Context, string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hasEnd
}

func (c *schedulerCollector) HasStockKlineArchive(context.Context, string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hasKline
}

func (c *schedulerCollector) CollectStockBoardRelations(context.Context, string) error {
	c.mu.Lock()
	c.calls = append(c.calls, "relations")
	c.mu.Unlock()
	return nil
}

func (c *schedulerCollector) HasStockBoardRelations(context.Context, string) bool {
	return false
}

func TestSchedulerUsesIndependentIntradayAndArchiveLanes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	calendar, err := tradingcalendar.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := &schedulerCollector{endStarted: make(chan struct{}), endRelease: make(chan struct{})}
	s, err := New(fake, calendar, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := time.Date(2026, 8, 17, 16, 0, 5, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	if !s.startJob(ctx, "end-of-day", current, "2026-08-17") {
		t.Fatal("end-of-day job did not start")
	}
	select {
	case <-fake.endStarted:
	case <-time.After(time.Second):
		t.Fatal("end-of-day job did not reach collector")
	}
	if !s.startJob(ctx, "minute", current, "2026-08-17") {
		t.Fatal("minute collection was blocked by archive lane")
	}
	if s.startJob(ctx, "stock-kline", current, "2026-08-17") {
		t.Fatal("stock-kline should wait for the active archive lane")
	}
	close(fake.endRelease)
	time.Sleep(20 * time.Millisecond)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 2 || fake.calls[0] != "end-of-day" || fake.calls[1] != "minute" {
		t.Fatalf("unexpected lane calls: %v", fake.calls)
	}
}

func TestRecoveryTradeDateOnlyRunsOutsideTradingSession(t *testing.T) {
	calendar, err := tradingcalendar.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	tests := []struct {
		at       string
		wantDate string
		wantRun  bool
	}{
		{"2026-08-17 08:30", "2026-08-14", true},
		{"2026-08-17 09:29", "2026-08-14", true},
		{"2026-08-17 09:30", "", false},
		{"2026-08-17 15:30", "", false},
		{"2026-08-17 16:01", "2026-08-17", true},
		{"2026-08-16 10:00", "2026-08-14", true},
	}
	for _, test := range tests {
		at, parseErr := time.ParseInLocation("2006-01-02 15:04", test.at, location)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		gotDate, gotRun := recoveryTradeDate(calendar, at)
		if gotDate != test.wantDate || gotRun != test.wantRun {
			t.Errorf("%s: got (%s,%v), want (%s,%v)", test.at, gotDate, gotRun, test.wantDate, test.wantRun)
		}
	}
}
