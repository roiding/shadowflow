package limitup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

type memoryReader struct {
	dataset Dataset
	from    string
	to      string
	scope   string
}

func (reader *memoryReader) ReadLimitUpWindow(_ context.Context, fromDate, asOf, scope string) (Dataset, error) {
	reader.from, reader.to, reader.scope = fromDate, asOf, scope
	result := Dataset{Days: []Day{}, Events: []Event{}}
	for _, day := range reader.dataset.Days {
		if day.TradeDate >= fromDate && day.TradeDate <= asOf {
			result.Days = append(result.Days, day)
		}
	}
	for _, event := range reader.dataset.Events {
		if event.TradeDate >= fromDate && event.TradeDate <= asOf {
			result.Events = append(result.Events, event)
		}
	}
	return result, nil
}

func testEvent(date, code, pool string) Event {
	return Event{TradeDate: date, Code: code, Name: "股票" + code, MarketID: 33, MarketType: "HS", Pool: pool}
}

func testDataset(days []string, events ...Event) Dataset {
	dataset := Dataset{Events: events}
	for _, date := range days {
		day := Day{TradeDate: date}
		for _, event := range events {
			if event.TradeDate != date {
				continue
			}
			if event.Pool != PoolDown {
				day.TouchedCount++
			}
			if event.Pool == PoolSealed {
				day.SealedCount++
			} else if event.Pool == PoolBroken {
				day.BrokenCount++
			} else {
				day.DownCount++
			}
		}
		dataset.Days = append(dataset.Days, day)
	}
	return dataset
}

func TestDownLinksPreserveUpToDownWithoutMixingRates(testingT *testing.T) {
	reader := &memoryReader{dataset: testDataset([]string{"2026-08-03", "2026-08-04"},
		testEvent("2026-08-03", "000001", PoolBroken), testEvent("2026-08-03", "000001", PoolDown),
		testEvent("2026-08-03", "000002", PoolDown), testEvent("2026-08-04", "000001", PoolSealed),
		testEvent("2026-08-04", "000002", PoolSealed), testEvent("2026-08-04", "000003", PoolDown))}
	query := Query{AsOf: "2026-08-04", Event: EventDown, MinOverlap: 1}
	result, err := New(reader).Analyze(context.Background(), query, "000001")
	if err != nil {
		testingT.Fatal(err)
	}
	if result.Profile.TouchDays != 2 || result.Profile.DownDays != 1 || result.Profile.UpToDownDays != 1 || len(result.Events) != 3 ||
		len(result.Links) != 1 || result.Links[0].Code != "000002" || result.Links[0].OverlapDays != 1 {
		testingT.Fatalf("up-to-down or down-only matching lost: %+v", result)
	}
	checkFloat(testingT, "up seal rate", *result.Profile.SealRate, 0.5)
	checkFloat(testingT, "up touch frequency", *result.Profile.EventFrequency, 1)
	checkFloat(testingT, "down frequency", *result.Profile.DownFrequency, 0.5)
	if result.Profile.OpenCountUnknownDays != 2 {
		testingT.Fatal("down events must not change up-side open-count coverage")
	}
	downOnly, err := New(reader).Analyze(context.Background(), query, "000003")
	if err != nil || downOnly.Profile.TouchDays != 0 || downOnly.Profile.SealRate != nil || downOnly.Profile.MeanOpenCount != nil ||
		downOnly.Profile.DownDays != 1 || downOnly.Profile.FirstTimeSamples != 0 {
		testingT.Fatalf("down-only stock must keep up-side metrics undefined: %+v %v", downOnly, err)
	}
	query.Event = EventTouched
	touch, err := New(reader).Analyze(context.Background(), query, "000001")
	if err != nil || len(touch.Links) != 1 || touch.Links[0].OverlapDays != 1 || touch.Links[0].AnchorDays != 2 {
		testingT.Fatalf("down events contaminated touched-up graph: %+v %v", touch, err)
	}
}

func checkFloat(testingT *testing.T, name string, actual, expected float64) {
	testingT.Helper()
	if math.Abs(actual-expected) > 1e-9 {
		testingT.Fatalf("%s: got %g, want %g", name, actual, expected)
	}
}

func TestLinksUseCompleteDaysEmptyDaysAndAsOf(testingT *testing.T) {
	reader := &memoryReader{dataset: testDataset([]string{"2026-08-03", "2026-08-04", "2026-08-06", "2026-08-07"},
		testEvent("2026-08-03", "000001", PoolSealed), testEvent("2026-08-03", "000002", PoolSealed),
		testEvent("2026-08-03", "300003", PoolBroken), testEvent("2026-08-06", "000001", PoolSealed),
		testEvent("2026-08-06", "000002", PoolBroken), testEvent("2026-08-07", "000001", PoolSealed),
		testEvent("2026-08-07", "000002", PoolSealed))}
	service := New(reader)
	query := Query{AsOf: "2026-08-06", Days: 4, MinOverlap: 1}
	result, err := service.Analyze(context.Background(), query, "000001")
	if err != nil {
		testingT.Fatal(err)
	}
	if reader.from != "2026-08-03" || reader.to != "2026-08-06" || reader.scope != ScopeDefault ||
		result.Coverage.ObservedDays != 3 || result.Coverage.LatestDate != "2026-08-06" || result.Coverage.SampleSufficient {
		testingT.Fatalf("incorrect coverage: reader=%+v result=%+v", reader, result.Coverage)
	}
	if len(result.Links) != 1 || result.Links[0].Code != "000002" || result.Links[0].OverlapDays != 1 {
		testingT.Fatalf("unexpected sealed links: %+v", result.Links)
	}
	link := result.Links[0]
	checkFloat(testingT, "conditional", link.ConditionalProbability, 0.5)
	checkFloat(testingT, "reverse", link.ReverseProbability, 1)
	checkFloat(testingT, "baseline", link.BaselineProbability, 1.0/3)
	checkFloat(testingT, "lift", link.Lift, 1.5)
	checkFloat(testingT, "Jaccard", link.Jaccard, 0.5)
	if link.Phi == nil {
		testingT.Fatal("nonconstant sequences need a phi coefficient")
	}
	checkFloat(testingT, "phi", *link.Phi, 0.5)
	checkFloat(testingT, "heat", link.HeatAdjustedOverlap, 1)
	query.Event, query.MinOverlap = EventTouched, 2
	result, err = service.Analyze(context.Background(), query, "000001")
	if err != nil || len(result.Links) != 1 || result.Links[0].OverlapDays != 2 {
		testingT.Fatalf("touch mode should include broken pools: %+v, %v", result, err)
	}
	checkFloat(testingT, "touch heat", result.Links[0].HeatAdjustedOverlap, 1.5)
	checkFloat(testingT, "touch Jaccard", result.Links[0].Jaccard, 1)
	if result.Links[0].SharedDates[0] != "2026-08-06" || result.Links[0].SampleSufficient {
		testingT.Fatalf("bad evidence or sample flag: %+v", result.Links[0])
	}
}

func TestProfileKeepsUnknownCountsAndRecomputesSealRate(testingT *testing.T) {
	unknown := testEvent("2026-08-03", "000001", PoolSealed)
	recovered := testEvent("2026-08-04", "000001", PoolSealed)
	brokenUnknown := testEvent("2026-08-05", "000001", PoolBroken)
	brokenKnown := testEvent("2026-08-06", "000001", PoolBroken)
	openTwice, openOnce, sourceRate := 2, 1, 1.0
	recovered.OpenCount, brokenKnown.OpenCount = &openTwice, &openOnce
	unknown.SourceSealRate, recovered.SourceSealRate = &sourceRate, &sourceRate
	firstTime := time.Date(2026, 8, 3, 9, 40, 0, 0, Shanghai())
	secondTime := time.Date(2026, 8, 4, 14, 20, 0, 0, Shanghai())
	unknown.FirstLimitAt, recovered.FirstLimitAt = &firstTime, &secondTime
	recovered.HighDays, recovered.LimitType = "5天4板", "换手板"
	reader := &memoryReader{dataset: testDataset([]string{"2026-08-03", "2026-08-04", "2026-08-05", "2026-08-06"}, unknown, recovered, brokenUnknown, brokenKnown)}
	result, err := New(reader).Analyze(context.Background(), Query{AsOf: "2026-08-06"}, "000001")
	if err != nil {
		testingT.Fatal(err)
	}
	profile := result.Profile
	if profile.TouchDays != 4 || profile.SealDays != 2 || profile.BrokenDays != 2 || profile.OpenCountKnownDays != 2 ||
		profile.OpenCountUnknownDays != 2 || profile.OpenedDays != 3 || profile.RecoveredDays != 1 ||
		profile.FirstTimeSamples != 2 || profile.MedianFirstTime != "12:00:00" {
		testingT.Fatalf("incorrect behavior counts: %+v", profile)
	}
	checkFloat(testingT, "local seal rate", *profile.SealRate, 0.5)
	checkFloat(testingT, "known mean open count", *profile.MeanOpenCount, 1.5)
	checkFloat(testingT, "known recovery rate", *profile.RecoveryRate, 1.0/3)
	if result.Events[2].HighDays != "5天4板" || result.Events[3].OpenCount != nil {
		testingT.Fatal("source labels or missing values were changed")
	}
}

func TestConstantEventsReturnNullPhiAndFiniteJSON(testingT *testing.T) {
	reader := &memoryReader{dataset: testDataset([]string{"2026-08-03", "2026-08-04"},
		testEvent("2026-08-03", "000001", PoolSealed), testEvent("2026-08-03", "000002", PoolSealed),
		testEvent("2026-08-04", "000001", PoolSealed), testEvent("2026-08-04", "000002", PoolSealed))}
	result, err := New(reader).Analyze(context.Background(), Query{AsOf: "2026-08-04", MinOverlap: 1}, "000001")
	if err != nil || len(result.Links) != 1 || result.Links[0].Phi != nil {
		testingT.Fatalf("constant sequence must have undefined phi: %+v, %v", result.Links, err)
	}
	if _, err := json.Marshal(result); err != nil {
		testingT.Fatalf("undefined metrics must not emit NaN/Inf: %v", err)
	}
}

func TestHotMarketDayContributesLessAndLimitsAreDeterministic(testingT *testing.T) {
	reader := &memoryReader{dataset: testDataset([]string{"2026-08-03", "2026-08-04"},
		testEvent("2026-08-03", "000001", PoolSealed), testEvent("2026-08-03", "000002", PoolSealed),
		testEvent("2026-08-03", "000003", PoolSealed), testEvent("2026-08-03", "000004", PoolSealed),
		testEvent("2026-08-04", "000001", PoolSealed), testEvent("2026-08-04", "000005", PoolSealed))}
	result, err := New(reader).Analyze(context.Background(), Query{AsOf: "2026-08-04", MinOverlap: 1, Limit: 2}, "000001")
	if err != nil || len(result.Links) != 2 || result.TotalLinks != 4 || result.Links[0].Code != "000005" || result.Links[1].Code != "000002" {
		testingT.Fatalf("unexpected order or total: %+v, %v", result, err)
	}
	checkFloat(testingT, "hot day weight", result.Links[1].HeatAdjustedOverlap, 1.0/3)
}

func TestRawOverlapOrderPrioritizesCooccurrenceCount(testingT *testing.T) {
	days := []string{"2026-08-03", "2026-08-04", "2026-08-05", "2026-08-06", "2026-08-07"}
	events := []Event{}
	for _, day := range days[:2] {
		events = append(events, testEvent(day, "000001", PoolSealed))
		events = append(events, testEvent(day, "000002", PoolSealed))
	}
	for _, day := range days[2:] {
		events = append(events, testEvent(day, "000001", PoolSealed))
		events = append(events, testEvent(day, "000003", PoolSealed))
		for peer := 0; peer < 9; peer++ {
			events = append(events, testEvent(day, fmt.Sprintf("80000%d", peer), PoolSealed))
		}
	}
	reader := &memoryReader{dataset: testDataset(days, events...)}
	result, err := New(reader).Analyze(context.Background(), Query{AsOf: "2026-08-07", Days: 5, MinOverlap: 1, Order: "overlap"}, "000001")
	if err != nil {
		testingT.Fatal(err)
	}
	if len(result.Links) < 2 || result.Links[0].Code != "000003" || result.Links[0].OverlapDays != 3 {
		testingT.Fatalf("raw overlap order ignored cooccurrence count: %+v", result.Links)
	}
	heatOrdered, err := New(reader).Analyze(context.Background(), Query{AsOf: "2026-08-07", Days: 5, MinOverlap: 1}, "000001")
	if err != nil || heatOrdered.Links[0].Code != "000002" || heatOrdered.Links[0].OverlapDays != 2 {
		testingT.Fatalf("heat order must remain the default and use hot-day weights: %+v %v", heatOrdered.Links, err)
	}
	for _, query := range []Query{{Order: "invalid"}, {Order: "overlap", MinOverlap: 21}} {
		if _, err := New(reader).Overview(context.Background(), query); !errors.Is(err, ErrInvalid) {
			testingT.Fatalf("accepted invalid order query %+v: %v", query, err)
		}
	}
}

func TestEmptyMissingAndInvalidDataset(testingT *testing.T) {
	service := New(&memoryReader{})
	result, err := service.Overview(context.Background(), Query{AsOf: "2026-08-04"})
	if err != nil || result.Coverage.ObservedDays != 0 || result.Days == nil || result.Stocks == nil {
		testingT.Fatalf("empty source should stay explicit: %+v %v", result, err)
	}
	if _, err := service.Analyze(context.Background(), Query{AsOf: "2026-08-04"}, "000001"); !errors.Is(err, ErrMissing) {
		testingT.Fatalf("expected missing stock: %v", err)
	}
	for _, query := range []Query{{Days: 367}, {AsOf: "2026-02-30"}, {Scope: "HS"}, {Event: "returns"}, {MinOverlap: 21}, {Limit: 101}} {
		if _, err := service.Overview(context.Background(), query); !errors.Is(err, ErrInvalid) {
			testingT.Fatalf("accepted invalid query %+v: %v", query, err)
		}
	}
	corrupt := &memoryReader{dataset: testDataset([]string{"2026-08-03"}, testEvent("2026-08-03", "000001", PoolSealed))}
	corrupt.dataset.Days[0].SealedCount = 2
	if _, err := New(corrupt).Overview(context.Background(), Query{AsOf: "2026-08-04"}); err == nil {
		testingT.Fatal("incomplete stored pool must not be analyzed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Overview(ctx, Query{}); !errors.Is(err, context.Canceled) {
		testingT.Fatalf("expected cancellation: %v", err)
	}
}
