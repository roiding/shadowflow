package eastmoney

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func mapRecord(rankType graymarket.RankType, tradeDate string, snapshotAt, fetchedAt time.Time, rank int64, row map[string]json.RawMessage) (graymarket.RankRecord, error) {
	code, err := stringValue(row, "4")
	if err != nil {
		return graymarket.RankRecord{}, fmt.Errorf("missing code: %w", err)
	}
	if code == "" {
		return graymarket.RankRecord{}, fmt.Errorf("missing code: field 4 is empty")
	}

	nameKey := "16"
	record := graymarket.RankRecord{
		TradeDate:        tradeDate,
		SnapshotAt:       snapshotAt,
		RankType:         rankType,
		Rank:             rank,
		Market:           intValue(row, "3"),
		Code:             code,
		Name:             optionalString(row, nameKey),
		QuoteTime:        graymarket.FormatQuoteTime(intValue(row, "5")),
		LatestPriceRaw:   intValue(row, "13"),
		ChangePct:        floatValue(row, "14"),
		DarkMoney:        intValue(row, "6"),
		RegularMoney:     intValue(row, "7"),
		MainMoneyInflow:  intValue(row, "8"),
		MoneyAvailable:   true,
		DarkActivity:     floatValue(row, "11"),
		DarkInflowRatio:  floatValue(row, "12"),
		SourceVersion:    101,
		SourceSortFlag:   6,
		SourceDescending: true,
		FetchedAt:        fetchedAt,
	}
	if rankType != graymarket.RankStock {
		record.UpCount = intValue(row, "9")
		record.DownCount = intValue(row, "10")
		record.LeaderName = optionalString(row, "15")
		record.LeaderCode = optionalString(row, "20")
	}
	return record, nil
}

func stringValue(row map[string]json.RawMessage, key string) (string, error) {
	raw, ok := row[key]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String(), nil
	}
	return "", fmt.Errorf("field %s is not a string", key)
}

func optionalString(row map[string]json.RawMessage, key string) string {
	value, _ := stringValue(row, key)
	return value
}

func intValue(row map[string]json.RawMessage, key string) int64 {
	raw, ok := row[key]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if value, err := number.Int64(); err == nil {
			return value
		}
		if value, err := strconv.ParseFloat(number.String(), 64); err == nil {
			return int64(value)
		}
	}
	return 0
}

func floatValue(row map[string]json.RawMessage, key string) float64 {
	raw, ok := row[key]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		value, _ := strconv.ParseFloat(number.String(), 64)
		return value
	}
	return 0
}
