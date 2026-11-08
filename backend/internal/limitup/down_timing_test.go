package limitup

import (
	"context"
	"testing"
	"time"
)

func TestProfileDownTimingUsesOnlyFirstDownAt(testingT *testing.T) {
	firstDown := time.Date(2026, 8, 3, 9, 31, 0, 0, Shanghai())
	secondDown := time.Date(2026, 8, 4, 2, 2, 0, 0, time.UTC)
	thirdDown := time.Date(2026, 8, 5, 14, 59, 59, 0, Shanghai())
	lastDown := time.Date(2026, 8, 3, 14, 58, 0, 0, Shanghai())
	secondLastDown := time.Date(2026, 8, 4, 10, 30, 0, 0, Shanghai())
	thirdLastDown := time.Date(2026, 8, 5, 15, 0, 0, 0, Shanghai())
	missingDown := testEvent("2026-08-06", "000001", PoolDown)
	missingDecoy := time.Date(2026, 8, 6, 9, 0, 0, 0, Shanghai())
	missingDown.FirstLimitAt = &missingDecoy
	events := []Event{
		{TradeDate: "2026-08-03", Code: "000001", Name: "股票000001", MarketID: 33, MarketType: "HS", Pool: PoolDown, FirstDownAt: &firstDown, LastDownAt: &lastDown},
		{TradeDate: "2026-08-04", Code: "000001", Name: "股票000001", MarketID: 33, MarketType: "HS", Pool: PoolDown, FirstDownAt: &secondDown, LastDownAt: &secondLastDown},
		{TradeDate: "2026-08-05", Code: "000001", Name: "股票000001", MarketID: 33, MarketType: "HS", Pool: PoolDown, FirstDownAt: &thirdDown, LastDownAt: &thirdLastDown},
		missingDown,
	}
	reader := &memoryReader{dataset: testDataset([]string{"2026-08-03", "2026-08-04", "2026-08-05", "2026-08-06"}, events...)}
	result, err := New(reader).Analyze(context.Background(), Query{AsOf: "2026-08-06"}, "000001")
	if err != nil {
		testingT.Fatal(err)
	}
	profile := result.Profile
	if profile.DownDays != 4 || profile.FirstDownTimeSamples != 3 || profile.MedianFirstDownTime != "10:02:00" {
		testingT.Fatalf("incorrect down timing: %+v", profile)
	}
	if profile.TouchDays != 0 || profile.SealRate != nil || profile.BrokenRate != nil || profile.RecoveryRate != nil ||
		profile.FirstTimeSamples != 0 || profile.MedianFirstTime != "" {
		testingT.Fatalf("down timing polluted up-side metrics: %+v", profile)
	}
	for _, bucket := range profile.FirstTimeBuckets {
		if bucket.Count != 0 {
			testingT.Fatalf("down timing polluted first-limit buckets: %+v", profile.FirstTimeBuckets)
		}
	}
	if result.Events[len(result.Events)-1].LastDownAt == nil || !result.Events[len(result.Events)-1].LastDownAt.Equal(lastDown) {
		testingT.Fatal("last down time was not preserved")
	}
}

func TestDownTimingDoesNotChangeUpProfileDenominators(testingT *testing.T) {
	firstLimit := time.Date(2026, 8, 3, 9, 35, 0, 0, Shanghai())
	secondLimit := time.Date(2026, 8, 4, 10, 10, 0, 0, Shanghai())
	thirdLimit := time.Date(2026, 8, 5, 13, 30, 0, 0, Shanghai())
	fourthLimit := time.Date(2026, 8, 6, 14, 20, 0, 0, Shanghai())
	firstDown := time.Date(2026, 8, 3, 9, 45, 0, 0, Shanghai())
	secondDown := time.Date(2026, 8, 7, 10, 5, 0, 0, Shanghai())
	firstLastDown := time.Date(2026, 8, 3, 10, 0, 0, 0, Shanghai())
	secondLastDown := time.Date(2026, 8, 7, 10, 20, 0, 0, Shanghai())
	missingDecoy := time.Date(2026, 8, 8, 10, 0, 0, 0, Shanghai())
	twoOpens, zeroOpens, oneOpen := 2, 0, 1
	broken := testEvent("2026-08-03", "000001", PoolBroken)
	broken.OpenCount, broken.FirstLimitAt = &twoOpens, &firstLimit
	sealedUnopened := testEvent("2026-08-04", "000001", PoolSealed)
	sealedUnopened.OpenCount, sealedUnopened.FirstLimitAt = &zeroOpens, &secondLimit
	sealedRecovered := testEvent("2026-08-05", "000001", PoolSealed)
	sealedRecovered.OpenCount, sealedRecovered.FirstLimitAt = &oneOpen, &thirdLimit
	brokenUnknown := testEvent("2026-08-06", "000001", PoolBroken)
	brokenUnknown.FirstLimitAt = &fourthLimit
	downSameDay := testEvent("2026-08-03", "000001", PoolDown)
	downSameDay.FirstDownAt, downSameDay.LastDownAt = &firstDown, &firstLastDown
	downLater := testEvent("2026-08-07", "000001", PoolDown)
	downLater.FirstDownAt, downLater.LastDownAt = &secondDown, &secondLastDown
	downMissing := testEvent("2026-08-08", "000001", PoolDown)
	downMissing.FirstLimitAt = &missingDecoy
	reader := &memoryReader{dataset: testDataset(
		[]string{"2026-08-03", "2026-08-04", "2026-08-05", "2026-08-06", "2026-08-07", "2026-08-08"},
		broken, downSameDay, sealedUnopened, sealedRecovered, brokenUnknown, downLater, downMissing)}
	result, err := New(reader).Analyze(context.Background(), Query{AsOf: "2026-08-08"}, "000001")
	if err != nil {
		testingT.Fatal(err)
	}
	profile := result.Profile
	if profile.TouchDays != 4 || profile.SealDays != 2 || profile.BrokenDays != 2 || profile.DownDays != 3 ||
		profile.OpenCountKnownDays != 3 || profile.OpenCountUnknownDays != 1 || profile.OpenedDays != 3 ||
		profile.RecoveredDays != 1 || profile.UpToDownDays != 1 {
		testingT.Fatalf("up-side denominators changed: %+v", profile)
	}
	checkFloat(testingT, "up seal rate", *profile.SealRate, 0.5)
	checkFloat(testingT, "up broken rate", *profile.BrokenRate, 0.5)
	checkFloat(testingT, "up recovery rate", *profile.RecoveryRate, 1.0/3)
	checkFloat(testingT, "up mean open count", *profile.MeanOpenCount, 1)
	if profile.FirstTimeSamples != 4 || profile.MedianFirstTime != "11:50:00" {
		testingT.Fatalf("down timing polluted up first-limit profile: %+v", profile)
	}
	expectedBuckets := []int{0, 1, 1, 1, 1}
	if len(profile.FirstTimeBuckets) != len(expectedBuckets) {
		testingT.Fatalf("unexpected first-limit buckets: %+v", profile.FirstTimeBuckets)
	}
	for index, count := range expectedBuckets {
		if profile.FirstTimeBuckets[index].Count != count {
			testingT.Fatalf("unexpected first-limit buckets: %+v", profile.FirstTimeBuckets)
		}
	}
	if profile.FirstDownTimeSamples != 2 || profile.MedianFirstDownTime != "09:55:00" {
		testingT.Fatalf("incorrect down timing: %+v", profile)
	}
}
