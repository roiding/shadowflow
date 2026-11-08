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

func TestThsSupplementRetriesOnlyMissingPart(testingT *testing.T) {
	for _, scenario := range []struct {
		name          string
		events        bool
		focus         bool
		eventError    bool
		focusError    bool
		wantEventCall int
		wantFocusCall int
	}{
		{name: "events complete", events: true, wantFocusCall: 1},
		{name: "focus complete", focus: true, wantEventCall: 1},
		{name: "both complete", events: true, focus: true},
		{name: "event failure keeps supplement independent", eventError: true, wantEventCall: 1, wantFocusCall: 1},
		{name: "supplement failure is retriable", events: true, focusError: true, wantFocusCall: 1},
	} {
		testingT.Run(scenario.name, func(testingT *testing.T) {
			collector := &supplementaryTestCollector{focusComplete: scenario.focus}
			collector.hasEvents = scenario.events
			if scenario.eventError {
				collector.eventErr = errors.New("event unavailable")
			}
			if scenario.focusError {
				collector.focusError = errors.New("supplement unavailable")
			}
			scheduler, err := New(collector, limitUpTestCalendar(testingT), loginDiscardLogger(), Options{LimitUpArchiveEnabled: true})
			if err != nil {
				testingT.Fatal(err)
			}
			now := time.Date(2026, 11, 6, 16, 0, 0, 0, scheduler.location)
			err = scheduler.executeJob(context.Background(), newScheduledJob("end-of-day-events", now, "2026-11-06"), now)
			if (err != nil) != (scenario.eventError || scenario.focusError) || len(collector.eventCallsSnapshot()) != scenario.wantEventCall || collector.focusCalls != scenario.wantFocusCall {
				testingT.Fatalf("bad independent recovery: events=%d focus=%d err=%v", len(collector.eventCallsSnapshot()), collector.focusCalls, err)
			}
		})
	}
}
