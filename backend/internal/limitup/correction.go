package limitup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const CountCorrectionBrokenDownSummaryUndercount = "broken_down_summary_undercount"

type countCorrectionEvidence struct {
	Code          string
	Name          string
	MarketID      int
	MarketType    string
	OpenCount     *int
	FirstLimitAt  *time.Time
	LastLimitAt   *time.Time
	ChangePercent *float64
	ChangeTag     string
}

func ReconcileCountCorrection(snapshot Snapshot, sourceBrokenCount int, sourceTouchedCount int) (*CountCorrection, error) {
	if sourceBrokenCount < 0 || sourceTouchedCount < 0 {
		return nil, fmt.Errorf("%w: source broken counts cannot be negative", ErrInvalid)
	}
	if sourceBrokenCount == snapshot.BrokenCount && sourceTouchedCount == snapshot.TouchedCount {
		if snapshot.CountCorrection != nil {
			return nil, fmt.Errorf("%w: count correction is only valid for a reconciled discrepancy", ErrInvalid)
		}
		return nil, nil
	}

	brokenDelta := snapshot.BrokenCount - sourceBrokenCount
	touchedDelta := snapshot.TouchedCount - sourceTouchedCount
	if brokenDelta <= 0 || touchedDelta <= 0 || brokenDelta != touchedDelta {
		return nil, fmt.Errorf("%w: broken and touched count discrepancies must be positive and equal", ErrInvalid)
	}
	if sourceTouchedCount != sourceBrokenCount+snapshot.SealedCount {
		return nil, fmt.Errorf("%w: source touched count does not match source broken and sealed counts", ErrInvalid)
	}

	evidenceCodes, evidenceError := brokenDownEvidenceCodes(snapshot.Events, snapshot.TradeDate)
	if evidenceError != nil {
		return nil, evidenceError
	}
	if len(evidenceCodes) != brokenDelta {
		return nil, fmt.Errorf("%w: count discrepancy %d does not match %d evidenced broken-down intersections", ErrInvalid, brokenDelta, len(evidenceCodes))
	}

	codes := make([]string, 0, len(evidenceCodes))
	for code := range evidenceCodes {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	correction := &CountCorrection{
		Reason:             CountCorrectionBrokenDownSummaryUndercount,
		SourceBrokenCount:  sourceBrokenCount,
		SourceTouchedCount: sourceTouchedCount,
		Codes:              codes,
	}
	reconciled := snapshot
	reconciled.CountCorrection = correction
	if validationError := validateCountCorrection(reconciled); validationError != nil {
		return nil, validationError
	}
	return correction, nil
}

func validateCountCorrection(snapshot Snapshot) error {
	correction := snapshot.CountCorrection
	if correction == nil {
		return nil
	}
	if correction.Reason != CountCorrectionBrokenDownSummaryUndercount {
		return fmt.Errorf("%w: unsupported count correction reason", ErrInvalid)
	}
	if correction.SourceBrokenCount < 0 || correction.SourceTouchedCount < 0 || len(correction.Codes) == 0 {
		return fmt.Errorf("%w: count correction requires non-negative source counts and evidence codes", ErrInvalid)
	}
	if correction.SourceBrokenCount == snapshot.BrokenCount && correction.SourceTouchedCount == snapshot.TouchedCount {
		return fmt.Errorf("%w: count correction requires a positive source discrepancy", ErrInvalid)
	}
	brokenDelta := snapshot.BrokenCount - correction.SourceBrokenCount
	touchedDelta := snapshot.TouchedCount - correction.SourceTouchedCount
	if brokenDelta <= 0 || touchedDelta <= 0 || brokenDelta != touchedDelta || brokenDelta != len(correction.Codes) {
		return fmt.Errorf("%w: count correction arithmetic does not match local and source totals", ErrInvalid)
	}
	if correction.SourceTouchedCount != correction.SourceBrokenCount+snapshot.SealedCount {
		return fmt.Errorf("%w: count correction source totals do not match sealed and broken counts", ErrInvalid)
	}
	intersections, intersectionError := brokenDownIntersections(snapshot.Events)
	if intersectionError != nil {
		return intersectionError
	}
	if len(intersections) != len(correction.Codes) {
		return fmt.Errorf("%w: count correction does not cover every broken-down intersection", ErrInvalid)
	}
	correctionCodes := make(map[string]bool, len(correction.Codes))
	for _, code := range correction.Codes {
		if !ValidCode(code) || correctionCodes[code] {
			return fmt.Errorf("%w: count correction codes must be valid and unique", ErrInvalid)
		}
		correctionCodes[code] = true
	}
	evidenceCodes, evidenceError := brokenDownEvidenceCodesFromIntersections(intersections, snapshot.TradeDate)
	if evidenceError != nil {
		return evidenceError
	}
	if len(evidenceCodes) != len(correctionCodes) {
		return fmt.Errorf("%w: count correction codes do not match broken-down evidence", ErrInvalid)
	}
	for code := range correctionCodes {
		if !evidenceCodes[code] {
			return fmt.Errorf("%w: count correction code %s has no broken-down evidence", ErrInvalid, code)
		}
	}
	return nil
}

func brokenDownEvidenceCodes(events []Event, tradeDate string) (map[string]bool, error) {
	intersections, intersectionError := brokenDownIntersections(events)
	if intersectionError != nil {
		return nil, intersectionError
	}
	return brokenDownEvidenceCodesFromIntersections(intersections, tradeDate)
}

func brokenDownEvidenceCodesFromIntersections(intersections map[string]brokenDownIntersection, tradeDate string) (map[string]bool, error) {
	evidenceCodes := make(map[string]bool)
	for code, intersection := range intersections {
		changeTag, tagError := rawStringField(intersection.Broken.Raw, "change_tag")
		if tagError != nil {
			return nil, fmt.Errorf("%w: invalid broken event evidence for %s", ErrInvalid, code)
		}
		if changeTag == "" {
			return nil, fmt.Errorf("%w: broken-down intersection for %s is missing change_tag evidence", ErrInvalid, code)
		}
		if changeTag != "LIMIT_DOWN" {
			continue
		}
		if evidenceError := validateBrokenDownEvidence(intersection.Broken, intersection.Down, tradeDate); evidenceError != nil {
			return nil, evidenceError
		}
		evidenceCodes[code] = true
	}
	return evidenceCodes, nil
}

type brokenDownIntersection struct {
	Broken Event
	Down   Event
}

func brokenDownIntersections(events []Event) (map[string]brokenDownIntersection, error) {
	brokenEvents := make(map[string]Event)
	downEvents := make(map[string]Event)
	for _, event := range events {
		switch event.Pool {
		case PoolBroken:
			brokenEvents[event.Code] = event
		case PoolDown:
			downEvents[event.Code] = event
		}
	}
	intersections := make(map[string]brokenDownIntersection)
	for code, brokenEvent := range brokenEvents {
		downEvent, intersects := downEvents[code]
		if intersects {
			intersections[code] = brokenDownIntersection{Broken: brokenEvent, Down: downEvent}
		}
	}
	return intersections, nil
}

func validateBrokenDownEvidence(brokenEvent Event, downEvent Event, tradeDate string) error {
	brokenEvidence, brokenError := parseCountCorrectionEvidence(brokenEvent, tradeDate)
	if brokenError != nil {
		return brokenError
	}
	downEvidence, downError := parseCountCorrectionEvidence(downEvent, tradeDate)
	if downError != nil {
		return downError
	}
	if brokenEvidence.ChangeTag != "LIMIT_DOWN" || downEvidence.ChangeTag != "LIMIT_DOWN" {
		return fmt.Errorf("%w: broken-down evidence for %s requires LIMIT_DOWN source tags", ErrInvalid, brokenEvent.Code)
	}
	if brokenEvidence.FirstLimitAt == nil || brokenEvidence.LastLimitAt == nil || brokenEvidence.OpenCount == nil || *brokenEvidence.OpenCount <= 0 {
		return fmt.Errorf("%w: broken-down evidence for %s requires same-day limit times and a positive open count", ErrInvalid, brokenEvent.Code)
	}
	if brokenEvidence.Name != downEvidence.Name || brokenEvidence.MarketID != downEvidence.MarketID ||
		brokenEvidence.MarketType != downEvidence.MarketType || brokenEvidence.ChangePercent == nil ||
		downEvidence.ChangePercent == nil || *brokenEvidence.ChangePercent != *downEvidence.ChangePercent {
		return fmt.Errorf("%w: broken-down evidence identity or change rate differs for %s", ErrInvalid, brokenEvent.Code)
	}
	return nil
}

func parseCountCorrectionEvidence(event Event, tradeDate string) (countCorrectionEvidence, error) {
	if len(event.Raw) == 0 || !json.Valid(event.Raw) {
		return countCorrectionEvidence{}, fmt.Errorf("%w: missing raw evidence for %s", ErrInvalid, event.Code)
	}
	code, codeError := rawStringField(event.Raw, "code")
	name, nameError := rawStringField(event.Raw, "name")
	marketID, marketIDPresent, marketIDError := rawIntegerField(event.Raw, "market_id")
	marketType, marketTypeError := rawStringField(event.Raw, "market_type")
	openCount, openCountPresent, openCountError := rawIntegerField(event.Raw, "open_num")
	firstLimitAt, firstLimitError := rawTimestampField(event.Raw, "first_limit_up_time", tradeDate)
	lastLimitAt, lastLimitError := rawTimestampField(event.Raw, "last_limit_up_time", tradeDate)
	changePercent, changePercentPresent, changePercentError := rawFloatField(event.Raw, "change_rate")
	changeTag, changeTagError := rawStringField(event.Raw, "change_tag")
	if codeError != nil || nameError != nil || marketIDError != nil || !marketIDPresent || marketTypeError != nil ||
		openCountError != nil || firstLimitError != nil || lastLimitError != nil || changePercentError != nil ||
		!changePercentPresent || changeTagError != nil {
		return countCorrectionEvidence{}, fmt.Errorf("%w: malformed raw evidence for %s", ErrInvalid, event.Code)
	}
	normalizedCode := strings.TrimSpace(code)
	if len(normalizedCode) > 0 && len(normalizedCode) < 6 {
		normalizedCode = fmt.Sprintf("%06s", normalizedCode)
	}
	if normalizedCode != event.Code || strings.TrimSpace(name) != event.Name || marketID != event.MarketID ||
		strings.TrimSpace(marketType) != event.MarketType || !sameOptionalInt(openCount, openCountPresent, event.OpenCount) ||
		!sameOptionalTime(firstLimitAt, event.FirstLimitAt) || !sameOptionalTime(lastLimitAt, event.LastLimitAt) ||
		!sameOptionalFloat(changePercent, changePercentPresent, event.ChangePercent) {
		return countCorrectionEvidence{}, fmt.Errorf("%w: raw evidence fields disagree with event %s", ErrInvalid, event.Code)
	}
	openCountPointer := event.OpenCount
	changePercentPointer := event.ChangePercent
	return countCorrectionEvidence{
		Code:          normalizedCode,
		Name:          strings.TrimSpace(name),
		MarketID:      marketID,
		MarketType:    strings.TrimSpace(marketType),
		OpenCount:     openCountPointer,
		FirstLimitAt:  firstLimitAt,
		LastLimitAt:   lastLimitAt,
		ChangePercent: changePercentPointer,
		ChangeTag:     strings.TrimSpace(changeTag),
	}, nil
}

func rawStringField(raw json.RawMessage, fieldName string) (string, error) {
	fields, fieldError := rawObjectFields(raw)
	if fieldError != nil {
		return "", fieldError
	}
	value, exists := fields[fieldName]
	if !exists || len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return "", nil
	}
	var text string
	if unmarshalError := json.Unmarshal(value, &text); unmarshalError == nil {
		return text, nil
	}
	var number json.Number
	if unmarshalError := json.Unmarshal(value, &number); unmarshalError == nil {
		return number.String(), nil
	}
	return "", fmt.Errorf("field %s is not a string or number", fieldName)
}

func rawIntegerField(raw json.RawMessage, fieldName string) (int, bool, error) {
	fields, fieldError := rawObjectFields(raw)
	if fieldError != nil {
		return 0, false, fieldError
	}
	value, exists := fields[fieldName]
	if !exists || len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return 0, false, nil
	}
	var integer int
	if unmarshalError := json.Unmarshal(value, &integer); unmarshalError == nil {
		return integer, true, nil
	}
	var text string
	if unmarshalError := json.Unmarshal(value, &text); unmarshalError == nil {
		parsed, parseError := strconv.Atoi(strings.TrimSpace(text))
		return parsed, parseError == nil, parseError
	}
	return 0, false, fmt.Errorf("field %s is not an integer", fieldName)
}

func rawFloatField(raw json.RawMessage, fieldName string) (float64, bool, error) {
	fields, fieldError := rawObjectFields(raw)
	if fieldError != nil {
		return 0, false, fieldError
	}
	value, exists := fields[fieldName]
	if !exists || len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return 0, false, nil
	}
	var number float64
	if unmarshalError := json.Unmarshal(value, &number); unmarshalError == nil {
		return number, true, nil
	}
	var text string
	if unmarshalError := json.Unmarshal(value, &text); unmarshalError == nil {
		parsed, parseError := strconv.ParseFloat(strings.TrimSpace(text), 64)
		return parsed, parseError == nil, parseError
	}
	return 0, false, fmt.Errorf("field %s is not a float", fieldName)
}

func rawTimestampField(raw json.RawMessage, fieldName string, tradeDate string) (*time.Time, error) {
	seconds, present, secondsError := rawIntegerField(raw, fieldName)
	if secondsError != nil || !present || seconds <= 0 {
		return nil, secondsError
	}
	timestamp := time.Unix(int64(seconds), 0).In(Shanghai())
	if timestamp.Format("2006-01-02") != tradeDate {
		return nil, fmt.Errorf("field %s has a different trade date", fieldName)
	}
	return &timestamp, nil
}

func rawObjectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if unmarshalError := json.Unmarshal(raw, &fields); unmarshalError != nil {
		return nil, unmarshalError
	}
	return fields, nil
}

func sameOptionalInt(first int, firstPresent bool, second *int) bool {
	if firstPresent != (second != nil) {
		return false
	}
	return second == nil || first == *second
}

func sameOptionalTime(first *time.Time, second *time.Time) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.Equal(*second)
}

func sameOptionalFloat(first float64, firstPresent bool, second *float64) bool {
	if firstPresent != (second != nil) {
		return false
	}
	return second == nil || first == *second
}
