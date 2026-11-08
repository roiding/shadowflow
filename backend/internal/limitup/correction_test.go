package limitup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

const countCorrectionEvidenceRaw = `{"code":"002856","name":"*ST美芝","market_id":33,"market_type":"HS","open_num":2,"first_limit_up_time":"1780536669","last_limit_up_time":"1780536996","change_rate":-5.0058,"change_tag":"LIMIT_DOWN"}`

func countCorrectionSnapshot(testingT *testing.T, brokenCount int) Snapshot {
	testingT.Helper()
	firstLimitAt := time.Unix(1780536669, 0).In(Shanghai())
	lastLimitAt := time.Unix(1780536996, 0).In(Shanghai())
	changePercent := -5.0058
	openCount := 2
	events := make([]Event, 0, 90+brokenCount+32)
	for index := 0; index < 90; index++ {
		events = append(events, Event{
			TradeDate: "2026-06-04", Code: fmt.Sprintf("1%05d", index), Name: fmt.Sprintf("涨停%d", index),
			MarketID: 33, MarketType: "HS", Pool: PoolSealed,
		})
	}
	for index := 0; index < brokenCount-1; index++ {
		events = append(events, Event{
			TradeDate: "2026-06-04", Code: fmt.Sprintf("3%05d", index), Name: fmt.Sprintf("炸板%d", index),
			MarketID: 33, MarketType: "HS", Pool: PoolBroken,
		})
	}
	events = append(events, Event{
		TradeDate: "2026-06-04", Code: "002856", Name: "*ST美芝", MarketID: 33, MarketType: "HS",
		Pool: PoolBroken, OpenCount: &openCount, FirstLimitAt: &firstLimitAt, LastLimitAt: &lastLimitAt,
		ChangePercent: &changePercent, Raw: json.RawMessage(countCorrectionEvidenceRaw),
	})
	for index := 0; index < 31; index++ {
		events = append(events, Event{
			TradeDate: "2026-06-04", Code: fmt.Sprintf("4%05d", index), Name: fmt.Sprintf("跌停%d", index),
			MarketID: 33, MarketType: "HS", Pool: PoolDown,
		})
	}
	downOpenCount := 2
	events = append(events, Event{
		TradeDate: "2026-06-04", Code: "002856", Name: "*ST美芝", MarketID: 33, MarketType: "HS",
		Pool: PoolDown, OpenCount: &downOpenCount, ChangePercent: &changePercent,
		Raw: json.RawMessage(`{"code":"002856","name":"*ST美芝","market_id":33,"market_type":"HS","open_num":2,"first_limit_up_time":null,"last_limit_up_time":null,"change_rate":-5.0058,"change_tag":"LIMIT_DOWN"}`),
	})
	return Snapshot{
		TradeDate: "2026-06-04", Source: SourceTHS, Scope: ScopeExpanded,
		FetchedAt: time.Date(2026, 6, 4, 16, 0, 0, 0, Shanghai()), SealedCount: 90,
		BrokenCount: brokenCount, DownCount: 32, TouchedCount: 90 + brokenCount, Events: events,
		RawPages: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
}

func countCorrectionEventIndex(snapshot Snapshot, poolName string, code string) int {
	for eventIndex, event := range snapshot.Events {
		if event.Pool == poolName && event.Code == code {
			return eventIndex
		}
	}
	return -1
}

func TestReconcileCountCorrectionRetainsSourceTotals(testingT *testing.T) {
	snapshot := countCorrectionSnapshot(testingT, 24)
	correction, correctionError := ReconcileCountCorrection(snapshot, 23, 113)
	if correctionError != nil {
		testingT.Fatalf("evidenced undercount was rejected: %v", correctionError)
	}
	if correction.Reason != CountCorrectionBrokenDownSummaryUndercount || correction.SourceBrokenCount != 23 ||
		correction.SourceTouchedCount != 113 || len(correction.Codes) != 1 || correction.Codes[0] != "002856" {
		testingT.Fatalf("source totals or evidence code changed: %+v", correction)
	}
	snapshot.CountCorrection = correction
	if validationError := ValidateSnapshot(snapshot); validationError != nil {
		testingT.Fatalf("valid corrected snapshot was rejected: %v", validationError)
	}
	encodedSnapshot, encodeError := json.Marshal(snapshot)
	if encodeError != nil {
		testingT.Fatal(encodeError)
	}
	for _, expectedField := range []string{`"count_correction"`, `"source_broken_count":23`, `"source_touched_count":113`, `"codes":["002856"]`} {
		if !bytes.Contains(encodedSnapshot, []byte(expectedField)) {
			testingT.Fatalf("snapshot JSON is missing %s: %s", expectedField, encodedSnapshot)
		}
	}
	dayJSON, dayError := json.Marshal(Day{
		TradeDate: "2026-06-04", SealedCount: 90, BrokenCount: 24, DownCount: 32, TouchedCount: 114,
		CountCorrection: correction,
	})
	if dayError != nil || !bytes.Contains(dayJSON, []byte(`"count_correction"`)) {
		testingT.Fatalf("day JSON does not expose count correction: %s %v", dayJSON, dayError)
	}
}

func TestReconcileCountCorrectionRequiresExactEvidence(testingT *testing.T) {
	testCases := []struct {
		Name       string
		Snapshot   func(*testing.T) Snapshot
		SourceOpen int
		SourceHist int
	}{
		{
			Name: "missing broken tag",
			Snapshot: func(testingT *testing.T) Snapshot {
				snapshot := countCorrectionSnapshot(testingT, 24)
				eventIndex := countCorrectionEventIndex(snapshot, PoolBroken, "002856")
				snapshot.Events[eventIndex].Raw = json.RawMessage(`{"code":"002856","name":"*ST美芝","market_id":33,"market_type":"HS","open_num":2,"first_limit_up_time":"1780536669","last_limit_up_time":"1780536996","change_rate":-5.0058}`)
				return snapshot
			},
			SourceOpen: 23,
			SourceHist: 113,
		},
		{
			Name: "missing down tag",
			Snapshot: func(testingT *testing.T) Snapshot {
				snapshot := countCorrectionSnapshot(testingT, 24)
				eventIndex := countCorrectionEventIndex(snapshot, PoolDown, "002856")
				snapshot.Events[eventIndex].Raw = json.RawMessage(`{"code":"002856","name":"*ST美芝","market_id":33,"market_type":"HS","open_num":2,"change_rate":-5.0058,"change_tag":"LIMIT_FAILED"}`)
				return snapshot
			},
			SourceOpen: 23,
			SourceHist: 113,
		},
		{
			Name: "missing limit time",
			Snapshot: func(testingT *testing.T) Snapshot {
				snapshot := countCorrectionSnapshot(testingT, 24)
				eventIndex := countCorrectionEventIndex(snapshot, PoolBroken, "002856")
				snapshot.Events[eventIndex].FirstLimitAt = nil
				snapshot.Events[eventIndex].Raw = json.RawMessage(`{"code":"002856","name":"*ST美芝","market_id":33,"market_type":"HS","open_num":2,"last_limit_up_time":"1780536996","change_rate":-5.0058,"change_tag":"LIMIT_DOWN"}`)
				return snapshot
			},
			SourceOpen: 23,
			SourceHist: 113,
		},
		{
			Name: "delta exceeds evidence",
			Snapshot: func(testingT *testing.T) Snapshot {
				return countCorrectionSnapshot(testingT, 25)
			},
			SourceOpen: 23,
			SourceHist: 113,
		},
		{
			Name: "non broken-down intersection",
			Snapshot: func(testingT *testing.T) Snapshot {
				snapshot := countCorrectionSnapshot(testingT, 24)
				eventIndex := countCorrectionEventIndex(snapshot, PoolBroken, "002856")
				snapshot.Events[eventIndex].Raw = json.RawMessage(`{"code":"002856","name":"*ST美芝","market_id":33,"market_type":"HS","open_num":2,"first_limit_up_time":"1780536669","last_limit_up_time":"1780536996","change_rate":-5.0058,"change_tag":"LIMIT_FAILED"}`)
				return snapshot
			},
			SourceOpen: 23,
			SourceHist: 113,
		},
		{
			Name: "missing intersection tag",
			Snapshot: func(testingT *testing.T) Snapshot {
				snapshot := countCorrectionSnapshot(testingT, 24)
				eventIndex := countCorrectionEventIndex(snapshot, PoolBroken, "002856")
				snapshot.Events[eventIndex].Raw = json.RawMessage(`{"code":"002856","name":"*ST美芝","market_id":33,"market_type":"HS","open_num":2,"first_limit_up_time":"1780536669","last_limit_up_time":"1780536996","change_rate":-5.0058}`)
				return snapshot
			},
			SourceOpen: 23,
			SourceHist: 113,
		},
	}
	for _, testCase := range testCases {
		testingT.Run(testCase.Name, func(testingT *testing.T) {
			snapshot := testCase.Snapshot(testingT)
			if correction, correctionError := ReconcileCountCorrection(snapshot, testCase.SourceOpen, testCase.SourceHist); correctionError == nil {
				testingT.Fatalf("untrusted correction was accepted: %+v", correction)
			}
		})
	}
}

func TestValidateSnapshotRejectsForgedCorrection(testingT *testing.T) {
	baseSnapshot := countCorrectionSnapshot(testingT, 24)
	validCorrection, correctionError := ReconcileCountCorrection(baseSnapshot, 23, 113)
	if correctionError != nil {
		testingT.Fatal(correctionError)
	}
	testCases := map[string]func(*Snapshot){
		"wrong touched source": func(snapshot *Snapshot) {
			snapshot.CountCorrection = &CountCorrection{
				Reason: validCorrection.Reason, SourceBrokenCount: 92, SourceTouchedCount: 91,
				Codes: append([]string{}, validCorrection.Codes...),
			}
		},
		"wrong evidence code": func(snapshot *Snapshot) {
			snapshot.CountCorrection = &CountCorrection{
				Reason: validCorrection.Reason, SourceBrokenCount: validCorrection.SourceBrokenCount,
				SourceTouchedCount: validCorrection.SourceTouchedCount, Codes: []string{"000001"},
			}
		},
		"duplicate evidence code": func(snapshot *Snapshot) {
			snapshot.CountCorrection = &CountCorrection{
				Reason: validCorrection.Reason, SourceBrokenCount: validCorrection.SourceBrokenCount,
				SourceTouchedCount: validCorrection.SourceTouchedCount, Codes: []string{"002856", "002856"},
			}
		},
		"raw field disagree": func(snapshot *Snapshot) {
			snapshot.CountCorrection = &CountCorrection{
				Reason: validCorrection.Reason, SourceBrokenCount: validCorrection.SourceBrokenCount,
				SourceTouchedCount: validCorrection.SourceTouchedCount, Codes: append([]string{}, validCorrection.Codes...),
			}
			eventIndex := countCorrectionEventIndex(*snapshot, PoolBroken, "002856")
			snapshot.Events[eventIndex].Raw = json.RawMessage(`{"code":"002856","name":"*ST美芝","market_id":33,"market_type":"HS","open_num":3,"first_limit_up_time":"1780536669","last_limit_up_time":"1780536996","change_rate":-5.0058,"change_tag":"LIMIT_DOWN"}`)
		},
		"correction on balanced counts": func(snapshot *Snapshot) {
			*snapshot = countCorrectionSnapshot(testingT, 23)
			snapshot.CountCorrection = validCorrection
		},
	}
	for name, mutate := range testCases {
		testingT.Run(name, func(testingT *testing.T) {
			snapshot := countCorrectionSnapshot(testingT, 24)
			mutate(&snapshot)
			if _, correctionError := ReconcileCountCorrection(snapshot, 23, 113); correctionError == nil {
				if validationError := ValidateSnapshot(snapshot); validationError == nil {
					testingT.Fatal("forged count correction was accepted")
				}
			}
		})
	}
}

func TestReconcileCountCorrectionIgnoresBalancedBrokenDownOverlap(testingT *testing.T) {
	snapshot := countCorrectionSnapshot(testingT, 23)
	correction, correctionError := ReconcileCountCorrection(snapshot, 23, 113)
	if correctionError != nil || correction != nil {
		testingT.Fatalf("balanced source totals must not be corrected: correction=%+v error=%v", correction, correctionError)
	}
	snapshot.CountCorrection = &CountCorrection{
		Reason: CountCorrectionBrokenDownSummaryUndercount, SourceBrokenCount: 23, SourceTouchedCount: 113,
		Codes: []string{"002856"},
	}
	if validationError := ValidateSnapshot(snapshot); validationError == nil {
		testingT.Fatal("balanced counts with a manufactured correction were accepted")
	}
}
