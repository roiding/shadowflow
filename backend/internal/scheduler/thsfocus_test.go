package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

type supplementaryTestCollector struct {
	limitUpTestCollector
	focusComplete bool
	focusCalls    int
	focusError    error
}

func (collector *supplementaryTestCollector) HasThsFocusArchive(context.Context, string) (bool, error) {
	return collector.focusComplete, nil
}

func (collector *supplementaryTestCollector) CollectThsFocus(context.Context, time.Time) error {
	collector.focusCalls++
	return collector.focusError
}

func TestThsFocusRunsAsSeparateJobAfterEvents(t *testing.T) {
	collector := &supplementaryTestCollector{}
	scheduler, err := New(collector, limitUpTestCalendar(t), loginDiscardLogger(), Options{LimitUpArchiveEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	eventsAt := time.Date(2026, 11, 6, 16, 30, 5, 0, scheduler.location)
	if err := scheduler.executeJob(context.Background(), newScheduledJob("end-of-day-events", eventsAt, "2026-11-06"), eventsAt); err != nil {
		t.Fatal(err)
	}
	if collector.focusCalls != 0 {
		t.Fatal("THS focus ran inside the end-of-day event job")
	}
	focusAt := time.Date(2026, 11, 6, 16, 45, 5, 0, scheduler.location)
	if err := scheduler.executeJob(context.Background(), newScheduledJob("ths-focus", focusAt, "2026-11-06"), focusAt); err != nil {
		t.Fatal(err)
	}
	if collector.focusCalls != 1 {
		t.Fatalf("THS focus job calls=%d, want 1", collector.focusCalls)
	}
}

func TestThsFocusJobSkipsCompletedArchiveAndRemainsRetriable(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		complete  bool
		focusErr  error
		wantCalls int
		wantError bool
	}{
		{name: "completed", complete: true, wantCalls: 0},
		{name: "missing", wantCalls: 1},
		{name: "failure", focusErr: errors.New("focus unavailable"), wantCalls: 1, wantError: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			collector := &supplementaryTestCollector{focusComplete: scenario.complete, focusError: scenario.focusErr}
			scheduler, err := New(collector, limitUpTestCalendar(t), loginDiscardLogger(), Options{LimitUpArchiveEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 6, 16, 45, 5, 0, scheduler.location)
			err = scheduler.executeJob(context.Background(), newScheduledJob("ths-focus", now, "2026-11-06"), now)
			if (err != nil) != scenario.wantError || collector.focusCalls != scenario.wantCalls {
				t.Fatalf("focus calls=%d err=%v", collector.focusCalls, err)
			}
		})
	}
}
