package thsfocus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

func ValidateSnapshot(snapshot Snapshot) error {
	tradeDate, parseError := time.ParseInLocation(TradeDateFormat, snapshot.TradeDate, Shanghai())
	if parseError != nil || tradeDate.Format(TradeDateFormat) != snapshot.TradeDate {
		return fmt.Errorf("%w: trade date must use YYYY-MM-DD", ErrInvalid)
	}
	if snapshot.Source != Source || !ValidScope(snapshot.Scope) {
		return fmt.Errorf("%w: unsupported source or scope", ErrInvalid)
	}
	if snapshot.MarketScope != MarketScope {
		return fmt.Errorf("%w: market and comparison require source_default market scope", ErrInvalid)
	}
	if snapshot.FetchedAt.IsZero() || snapshot.FetchedAt.Before(tradeDate.Add(15*time.Hour+30*time.Minute)) {
		return fmt.Errorf("%w: final daily snapshot requires collection after 15:30 Asia/Shanghai", ErrInvalid)
	}
	fetchedDate := snapshot.FetchedAt.In(Shanghai())
	if tradeDate.After(time.Date(fetchedDate.Year(), fetchedDate.Month(), fetchedDate.Day(), 0, 0, 0, 0, Shanghai())) {
		return fmt.Errorf("%w: trade date is after the snapshot fetch date", ErrInvalid)
	}
	if snapshot.SourceDateVerified {
		return fmt.Errorf("%w: current source does not provide date evidence for every page", ErrInvalid)
	}
	if snapshot.Tabs != nil && !snapshot.TabDateVerified {
		return fmt.Errorf("%w: limit-up chart tabs require a verified source date", ErrInvalid)
	}
	if snapshot.Blocks == nil || snapshot.Ladder == nil {
		return fmt.Errorf("%w: blocks and ladder require explicit arrays", ErrInvalid)
	}
	if len(snapshot.Blocks) > MaxBlocks || len(snapshot.Ladder) > MaxLevels || len(snapshot.Tabs) > MaxTabs {
		return fmt.Errorf("%w: block, ladder or tab count exceeds the source limit", ErrInvalid)
	}
	if (len(snapshot.Blocks) == 0 || len(snapshot.Ladder) == 0) && len(snapshot.Limitations) == 0 {
		return fmt.Errorf("%w: empty source arrays require a coverage limitation", ErrInvalid)
	}
	if len(snapshot.Limitations) > MaxLimitations {
		return fmt.Errorf("%w: limitations exceed the count limit", ErrInvalid)
	}
	for _, limitation := range snapshot.Limitations {
		if strings.TrimSpace(limitation) == "" || len(limitation) > MaxLimitationBytes {
			return fmt.Errorf("%w: limitation is empty or exceeds the size limit", ErrInvalid)
		}
	}
	if validateError := validateJSONObject(snapshot.Market, "market"); validateError != nil {
		return validateError
	}
	if validateError := validateJSONObject(snapshot.Comparison, "comparison"); validateError != nil {
		return validateError
	}
	if validateError := validateComparisonDate(snapshot.Comparison, snapshot.TradeDate); validateError != nil {
		return validateError
	}
	if validateError := validateRawPages(snapshot.RawPages, snapshot.Tabs != nil); validateError != nil {
		return validateError
	}

	seenBlocks := make(map[string]bool, len(snapshot.Blocks))
	seenLevels := make(map[int]bool, len(snapshot.Ladder))
	totalStocks := 0
	for _, block := range snapshot.Blocks {
		if validateError := validateBlock(block); validateError != nil {
			return validateError
		}
		if seenBlocks[block.Code] {
			return fmt.Errorf("%w: duplicate block code %s", ErrInvalid, block.Code)
		}
		seenBlocks[block.Code] = true
		totalStocks += len(block.Stocks)
	}
	for _, level := range snapshot.Ladder {
		if validateError := validateLevel(level); validateError != nil {
			return validateError
		}
		if seenLevels[level.Height] {
			return fmt.Errorf("%w: duplicate ladder height %d", ErrInvalid, level.Height)
		}
		seenLevels[level.Height] = true
		totalStocks += len(level.Stocks)
	}
	seenTabs := make(map[string]bool, len(snapshot.Tabs))
	for _, tab := range snapshot.Tabs {
		if validateError := validateTab(tab); validateError != nil {
			return validateError
		}
		if seenTabs[tab.Name] {
			return fmt.Errorf("%w: duplicate tab name %s", ErrInvalid, tab.Name)
		}
		seenTabs[tab.Name] = true
		totalStocks += len(tab.Stocks)
	}
	if totalStocks > MaxSnapshotStocks {
		return fmt.Errorf("%w: snapshot stock references exceed the size limit", ErrInvalid)
	}
	return nil
}

func validateJSONObject(raw json.RawMessage, fieldName string) error {
	content, unmarshalError := decodeJSONObject(raw, fieldName)
	if unmarshalError != nil {
		return unmarshalError
	}
	if len(content) == 0 {
		return fmt.Errorf("%w: %s requires a non-empty JSON object", ErrInvalid, fieldName)
	}
	return nil
}

func decodeJSONObject(raw json.RawMessage, fieldName string) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, fmt.Errorf("%w: %s requires a JSON object", ErrInvalid, fieldName)
	}
	var content map[string]json.RawMessage
	if unmarshalError := json.Unmarshal(trimmed, &content); unmarshalError != nil {
		return nil, fmt.Errorf("%w: %s is not a valid JSON object", ErrInvalid, fieldName)
	}
	return content, nil
}

func validateComparisonDate(raw json.RawMessage, tradeDate string) error {
	comparison, unmarshalError := decodeJSONObject(raw, "comparison")
	if unmarshalError != nil {
		return unmarshalError
	}
	rawTodayDate, exists := comparison["today_date"]
	if !exists {
		return fmt.Errorf("%w: comparison is missing today_date", ErrInvalid)
	}
	var todayDate string
	if unmarshalDateError := json.Unmarshal(rawTodayDate, &todayDate); unmarshalDateError != nil {
		return fmt.Errorf("%w: comparison today_date must be a string", ErrInvalid)
	}
	parsedDate, dateParseError := time.ParseInLocation(TradeDateFormat, todayDate, Shanghai())
	if dateParseError != nil || parsedDate.Format(TradeDateFormat) != todayDate {
		return fmt.Errorf("%w: comparison today_date must use YYYY-MM-DD", ErrInvalid)
	}
	if todayDate != tradeDate {
		return fmt.Errorf("%w: comparison today_date does not match trade date", ErrInvalid)
	}
	return nil
}

func validateRawPages(rawPages []json.RawMessage, hasTabs bool) error {
	expectedPages := 4
	if hasTabs {
		expectedPages = 6
	}
	if len(rawPages) != expectedPages {
		return fmt.Errorf("%w: a complete snapshot requires %d raw pages", ErrInvalid, expectedPages)
	}
	totalBytes := 0
	for _, rawPage := range rawPages {
		if len(rawPage) == 0 || len(rawPage) > MaxRawPageBytes {
			return fmt.Errorf("%w: raw page is empty or exceeds the size limit", ErrInvalid)
		}
		if !json.Valid(rawPage) {
			return fmt.Errorf("%w: raw page is not valid JSON", ErrInvalid)
		}
		totalBytes += len(rawPage)
	}
	if totalBytes > MaxRawSnapshotBytes {
		return fmt.Errorf("%w: raw pages exceed the snapshot size limit", ErrInvalid)
	}
	return nil
}

func validateTab(tab Tab) error {
	if strings.TrimSpace(tab.Name) == "" || len(tab.Name) > MaxNameBytes {
		return fmt.Errorf("%w: invalid tab name", ErrInvalid)
	}
	if tab.Rank <= 0 || tab.Rank > MaxTabs {
		return fmt.Errorf("%w: invalid tab rank for %s", ErrInvalid, tab.Name)
	}
	if tab.Stocks == nil {
		return fmt.Errorf("%w: tab %s requires an explicit stock list", ErrInvalid, tab.Name)
	}
	seenStocks := make(map[string]bool, len(tab.Stocks))
	for _, stock := range tab.Stocks {
		if validateError := validateTabStock(stock); validateError != nil {
			return fmt.Errorf("tab %s: %w", tab.Name, validateError)
		}
		if seenStocks[stock.Code] {
			return fmt.Errorf("%w: duplicate stock %s in tab %s", ErrInvalid, stock.Code, tab.Name)
		}
		seenStocks[stock.Code] = true
	}
	return nil
}

func validateTabStock(stock TabStock) error {
	if !ValidCode(stock.Code) || strings.TrimSpace(stock.Name) == "" || len(stock.Name) > MaxNameBytes || stock.MarketID <= 0 {
		return fmt.Errorf("%w: invalid stock identity for %s", ErrInvalid, stock.Code)
	}
	if len(stock.Reason) > MaxReasonBytes ||
		len(stock.AbnormalReason) > MaxReasonBytes ||
		len(stock.DetailReason) > MaxReasonDetailBytes {
		return fmt.Errorf("%w: oversized stock text for %s", ErrInvalid, stock.Code)
	}
	return nil
}

func validateBlock(block Block) error {
	if !ValidCode(block.Code) || strings.TrimSpace(block.Name) == "" || len(block.Name) > MaxNameBytes {
		return fmt.Errorf("%w: invalid block identity", ErrInvalid)
	}
	if block.Stocks == nil || block.LimitUpCount < 0 || block.LimitUpCount != len(block.Stocks) {
		return fmt.Errorf("%w: block %s requires an explicit stock list and matching limit_up_count", ErrInvalid, block.Code)
	}
	if block.Change != nil && (math.IsNaN(*block.Change) || math.IsInf(*block.Change, 0)) {
		return fmt.Errorf("%w: non-finite block change for %s", ErrInvalid, block.Code)
	}
	seenStocks := make(map[string]bool, len(block.Stocks))
	for _, stock := range block.Stocks {
		if validateError := validateStock(stock); validateError != nil {
			return validateError
		}
		if seenStocks[stock.Code] {
			return fmt.Errorf("%w: duplicate stock %s in block %s", ErrInvalid, stock.Code, block.Code)
		}
		seenStocks[stock.Code] = true
	}
	return nil
}

func validateLevel(level Level) error {
	if level.Height <= 0 || level.Height > MaximumLadderHeight || level.Stocks == nil {
		return fmt.Errorf("%w: invalid ladder level %d", ErrInvalid, level.Height)
	}
	seenStocks := make(map[string]bool, len(level.Stocks))
	for _, stock := range level.Stocks {
		if validateError := validateStock(stock); validateError != nil {
			return validateError
		}
		if seenStocks[stock.Code] {
			return fmt.Errorf("%w: duplicate stock %s at ladder height %d", ErrInvalid, stock.Code, level.Height)
		}
		seenStocks[stock.Code] = true
	}
	return nil
}

func validateStock(stock Stock) error {
	if !ValidCode(stock.Code) || strings.TrimSpace(stock.Name) == "" || len(stock.Name) > MaxNameBytes || stock.MarketID <= 0 {
		return fmt.Errorf("%w: invalid stock identity for %s", ErrInvalid, stock.Code)
	}
	if stock.ContinueNum < 0 {
		return fmt.Errorf("%w: negative continue_num for %s", ErrInvalid, stock.Code)
	}
	if len(stock.Reason) > MaxReasonBytes || len(stock.ReasonDetail) > MaxReasonDetailBytes || len(stock.HighDays) > MaxHighDaysBytes {
		return fmt.Errorf("%w: oversized stock text for %s", ErrInvalid, stock.Code)
	}
	if stock.ChangePercent != nil && (math.IsNaN(*stock.ChangePercent) || math.IsInf(*stock.ChangePercent, 0)) {
		return fmt.Errorf("%w: non-finite change percent for %s", ErrInvalid, stock.Code)
	}
	return nil
}
