package limitup

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

const (
	MaxDailyEvents  = 20000
	MaxWindowEvents = 150000
	MaxPoolPages    = 200
	MaxSourcePages  = 3 * MaxPoolPages
)

var stockCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

func ValidCode(code string) bool {
	return stockCodePattern.MatchString(code)
}

func ValidScope(scope string) bool {
	return scope == ScopeDefault || scope == ScopeExpanded
}

func Shanghai() *time.Location {
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}

func ValidateSnapshot(snapshot Snapshot) error {
	tradeDate, err := time.ParseInLocation("2006-01-02", snapshot.TradeDate, Shanghai())
	if err != nil || tradeDate.Format("2006-01-02") != snapshot.TradeDate {
		return fmt.Errorf("%w: trade date must use YYYY-MM-DD", ErrInvalid)
	}
	if snapshot.Source != SourceTHS || !ValidScope(snapshot.Scope) {
		return fmt.Errorf("%w: unsupported event source or market scope", ErrInvalid)
	}
	if snapshot.FetchedAt.Before(tradeDate.Add(15*time.Hour + 30*time.Minute)) {
		return fmt.Errorf("%w: a final daily snapshot requires collection after 15:30 Asia/Shanghai", ErrInvalid)
	}
	if snapshot.SealedCount < 0 || snapshot.BrokenCount < 0 || snapshot.DownCount < 0 || len(snapshot.Events) > MaxDailyEvents ||
		snapshot.TouchedCount != snapshot.SealedCount+snapshot.BrokenCount || snapshot.TouchedCount+snapshot.DownCount != len(snapshot.Events) {
		return fmt.Errorf("%w: daily pool totals do not match", ErrInvalid)
	}
	if len(snapshot.RawPages) < 3 || len(snapshot.RawPages) > MaxSourcePages {
		return fmt.Errorf("%w: all three pools require source page evidence", ErrInvalid)
	}
	rawSize := 0
	for _, page := range snapshot.RawPages {
		rawSize += len(page)
		if !json.Valid(page) || rawSize > 32<<20 {
			return fmt.Errorf("%w: source pages are invalid or exceed the size limit", ErrInvalid)
		}
	}
	seen := make(map[string]map[string]bool, len(snapshot.Events))
	sealedCount, brokenCount, downCount := 0, 0, 0
	for _, event := range snapshot.Events {
		if err := validateEvent(event, snapshot.TradeDate); err != nil {
			return err
		}
		if err := addPool(seen, event.Code, event.Pool); err != nil {
			return err
		}
		if event.Pool == PoolSealed {
			sealedCount++
		} else if event.Pool == PoolBroken {
			brokenCount++
		} else if event.Pool == PoolDown {
			downCount++
		}
	}
	if sealedCount != snapshot.SealedCount || brokenCount != snapshot.BrokenCount || downCount != snapshot.DownCount {
		return fmt.Errorf("%w: daily pool totals do not match their records", ErrInvalid)
	}
	if err := validateCountCorrection(snapshot); err != nil {
		return err
	}
	return nil
}

func addPool(seen map[string]map[string]bool, key, pool string) error {
	pools := seen[key]
	if pools == nil {
		pools = make(map[string]bool)
		seen[key] = pools
	}
	if pools[pool] || (pool == PoolSealed && len(pools) > 0) || (pool != PoolSealed && pools[PoolSealed]) {
		return fmt.Errorf("%w: duplicate or conflicting daily pools for %s", ErrInvalid, key)
	}
	pools[pool] = true
	return nil
}

func validateEvent(event Event, tradeDate string) error {
	if event.TradeDate != tradeDate || !ValidCode(event.Code) || strings.TrimSpace(event.Name) == "" ||
		event.MarketID <= 0 || strings.TrimSpace(event.MarketType) == "" {
		return fmt.Errorf("%w: invalid stock identity or event date", ErrInvalid)
	}
	if event.Pool != PoolSealed && event.Pool != PoolBroken && event.Pool != PoolDown {
		return fmt.Errorf("%w: unsupported pool for %s", ErrInvalid, event.Code)
	}
	if event.OpenCount != nil && *event.OpenCount < 0 {
		return fmt.Errorf("%w: negative open count for %s", ErrInvalid, event.Code)
	}
	for _, timestamp := range []*time.Time{event.FirstLimitAt, event.LastLimitAt, event.FirstDownAt, event.LastDownAt} {
		if timestamp != nil && timestamp.In(Shanghai()).Format("2006-01-02") != tradeDate {
			return fmt.Errorf("%w: event timestamp has a different trade date for %s", ErrInvalid, event.Code)
		}
	}
	if event.FirstLimitAt != nil && event.LastLimitAt != nil && event.LastLimitAt.Before(*event.FirstLimitAt) {
		return fmt.Errorf("%w: last limit time precedes first for %s", ErrInvalid, event.Code)
	}
	if event.FirstDownAt != nil && event.LastDownAt != nil && event.LastDownAt.Before(*event.FirstDownAt) {
		return fmt.Errorf("%w: last limit-down time precedes first for %s", ErrInvalid, event.Code)
	}
	for _, value := range []*float64{event.SourceSealRate, event.ChangePercent, event.TurnoverRate} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return fmt.Errorf("%w: non-finite event metric for %s", ErrInvalid, event.Code)
		}
	}
	if event.SourceSealRate != nil && (*event.SourceSealRate < 0 || *event.SourceSealRate > 1) {
		return fmt.Errorf("%w: invalid source seal rate for %s", ErrInvalid, event.Code)
	}
	if len(event.Raw) > 0 && !json.Valid(event.Raw) {
		return fmt.Errorf("%w: invalid raw event for %s", ErrInvalid, event.Code)
	}
	return nil
}

func NormalizeQuery(query Query) (Query, string, error) {
	if query.AsOf == "" {
		query.AsOf = time.Now().In(Shanghai()).Format("2006-01-02")
	}
	if query.Days == 0 {
		query.Days = 90
	}
	if query.Scope == "" {
		query.Scope = ScopeDefault
	}
	if query.Event == "" {
		query.Event = EventSealed
	}
	if query.MinOverlap == 0 {
		query.MinOverlap = 3
	}
	if query.Limit == 0 {
		query.Limit = 30
	}
	if query.Order == "" {
		query.Order = "heat"
	}
	asOf, err := time.ParseInLocation("2006-01-02", query.AsOf, Shanghai())
	if err != nil || asOf.Format("2006-01-02") != query.AsOf {
		return Query{}, "", fmt.Errorf("%w: as_of must use YYYY-MM-DD", ErrInvalid)
	}
	if query.Days < 1 || query.Days > 366 || query.Limit < 1 || query.Limit > 100 || query.MinOverlap < 1 || query.MinOverlap > 20 {
		return Query{}, "", fmt.Errorf("%w: days must be 1..366, limit 1..100 and min_overlap 1..20", ErrInvalid)
	}
	if !ValidScope(query.Scope) || (query.Event != EventSealed && query.Event != EventTouched && query.Event != EventDown) ||
		(query.Order != "heat" && query.Order != "overlap") {
		return Query{}, "", fmt.Errorf("%w: unsupported scope, event, or order", ErrInvalid)
	}
	return query, asOf.AddDate(0, 0, 1-query.Days).Format("2006-01-02"), nil
}
