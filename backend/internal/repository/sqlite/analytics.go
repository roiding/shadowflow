package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/roiding/shadowflow/internal/graymarket"
	"github.com/roiding/shadowflow/internal/repository"
)

const (
	dailyFeatureVersion = "daily-features-v1"
	futureLabelVersion  = "future-returns-v1"
	maxFeatureHistory   = 60
)

var labelHorizons = []int{1, 3, 5, 10, 20}

type revisionRef struct {
	repository.ArchiveRevision
}

type featureKey struct {
	rankType graymarket.RankType
	market   int64
	code     string
}

type historicalRecord struct {
	rank            int64
	turnover        int64
	darkMoney       int64
	mainMoneyInflow int64
}

type curveValues struct {
	dark []int64
}

type derivedCurve struct {
	available        bool
	morningShare     float64
	afternoonShare   float64
	lateShare        float64
	maxInflowIndex   int
	maxOutflowIndex  int
	tailAcceleration int64
	maxDrawdown      int64
	reversal         bool
}

func buildAnalyticsForRevision(ctx context.Context, tx *sql.Tx, revisionID, tradeDate string) error {
	refs, err := loadCurrentRevisionRefs(ctx, tx, tradeDate, maxFeatureHistory)
	if err != nil {
		return err
	}
	if len(refs) == 0 || refs[0].RevisionID != revisionID {
		return fmt.Errorf("revision %s is not current for %s", revisionID, tradeDate)
	}
	current, err := loadArchiveRecordMap(ctx, tx, tradeDate)
	if err != nil {
		return err
	}
	history := make([]map[featureKey]historicalRecord, 0, len(refs))
	for _, ref := range refs {
		records, err := loadArchiveHistoryMap(ctx, tx, ref.TradeDate)
		if err != nil {
			return err
		}
		history = append(history, records)
	}
	industries, err := loadPrimaryIndustries(ctx, tx, tradeDate)
	if err != nil {
		return err
	}
	curves, err := loadArchiveCurves(ctx, tx, tradeDate)
	if err != nil {
		return err
	}

	crossRank := make(map[graymarket.RankType][]float64)
	crossTurnover := make(map[graymarket.RankType][]float64)
	crossDarkMoney := make(map[graymarket.RankType][]float64)
	for _, record := range current {
		crossRank[record.RankType] = append(crossRank[record.RankType], -float64(record.Rank))
		crossTurnover[record.RankType] = append(crossTurnover[record.RankType], float64(record.Turnover))
		crossDarkMoney[record.RankType] = append(crossDarkMoney[record.RankType], float64(record.DarkMoney))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM daily_feature WHERE revision_id=?`, revisionID); err != nil {
		return err
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO daily_feature
(revision_id,trade_date,rank_type,market,code,name,primary_industry_code,
signed_dark_activity,capital_intensity,control_coefficient,rank_percentile,
turnover_percentile,dark_money_percentile,self_turnover_percentile_5,
self_turnover_percentile_10,self_turnover_percentile_20,self_turnover_percentile_60,
self_dark_money_percentile_5,self_dark_money_percentile_10,self_dark_money_percentile_20,
self_dark_money_percentile_60,rank_change_1,consecutive_inflow_days,money_acceleration,
curve_available,morning_dark_share,afternoon_dark_share,late_dark_share,
max_inflow_minute_index,max_outflow_minute_index,tail_acceleration,max_dark_drawdown,
intraday_reversal,price_money_divergence)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer statement.Close()

	keys := make([]featureKey, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].rankType != keys[j].rankType {
			return keys[i].rankType < keys[j].rankType
		}
		if keys[i].market != keys[j].market {
			return keys[i].market < keys[j].market
		}
		return keys[i].code < keys[j].code
	})
	for _, key := range keys {
		record := current[key]
		signedActivity := record.DarkActivity
		if record.DarkMoney < 0 {
			signedActivity = -signedActivity
		}
		capitalIntensity, controlCoefficient := 0.0, 0.0
		if record.Turnover != 0 {
			capitalIntensity = float64(record.MainMoneyInflow) / float64(record.Turnover)
			controlCoefficient = float64(record.RegularMoney+record.DarkMoney) / float64(record.Turnover) * 100
		}
		rankChange, acceleration := int64(0), int64(0)
		if len(history) > 1 {
			if previous, ok := history[1][key]; ok {
				rankChange = previous.rank - record.Rank
			}
		}
		if len(history) > 2 {
			previous, previousOK := history[1][key]
			older, olderOK := history[2][key]
			if previousOK && olderOK {
				acceleration = record.MainMoneyInflow - 2*previous.mainMoneyInflow + older.mainMoneyInflow
			}
		}
		consecutive := 0
		for _, rows := range history {
			historical, ok := rows[key]
			if !ok || historical.mainMoneyInflow <= 0 {
				break
			}
			consecutive++
		}
		curve := deriveCurve(curves[key])
		priceDivergence := curve.available && record.ChangePct != 0 &&
			len(curves[key].dark) == 48 && float64(curves[key].dark[47])*record.ChangePct < 0
		industryCode := ""
		if key.rankType == graymarket.RankStock {
			industryCode = industries[key.code]
		}
		if _, err := statement.ExecContext(ctx,
			revisionID, tradeDate, string(key.rankType), key.market, key.code, record.Name, industryCode,
			signedActivity, capitalIntensity, controlCoefficient,
			percentile(-float64(record.Rank), crossRank[key.rankType]),
			percentile(float64(record.Turnover), crossTurnover[key.rankType]),
			percentile(float64(record.DarkMoney), crossDarkMoney[key.rankType]),
			historicalPercentile(history, key, 5, func(item historicalRecord) float64 { return float64(item.turnover) }),
			historicalPercentile(history, key, 10, func(item historicalRecord) float64 { return float64(item.turnover) }),
			historicalPercentile(history, key, 20, func(item historicalRecord) float64 { return float64(item.turnover) }),
			historicalPercentile(history, key, 60, func(item historicalRecord) float64 { return float64(item.turnover) }),
			historicalPercentile(history, key, 5, func(item historicalRecord) float64 { return float64(item.darkMoney) }),
			historicalPercentile(history, key, 10, func(item historicalRecord) float64 { return float64(item.darkMoney) }),
			historicalPercentile(history, key, 20, func(item historicalRecord) float64 { return float64(item.darkMoney) }),
			historicalPercentile(history, key, 60, func(item historicalRecord) float64 { return float64(item.darkMoney) }),
			rankChange, consecutive, acceleration, boolInt(curve.available),
			curve.morningShare, curve.afternoonShare, curve.lateShare,
			curve.maxInflowIndex, curve.maxOutflowIndex, curve.tailAcceleration,
			curve.maxDrawdown, boolInt(curve.reversal), boolInt(priceDivergence)); err != nil {
			return err
		}
	}
	sourceJSON, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	generatedAt := time.Now().UTC().Format(timestampLayout)
	if _, err := tx.ExecContext(ctx, `INSERT INTO daily_feature_set
(revision_id,trade_date,feature_version,source_revisions_json,generated_at)
VALUES (?,?,?,?,?) ON CONFLICT(revision_id) DO UPDATE SET
trade_date=excluded.trade_date,feature_version=excluded.feature_version,
source_revisions_json=excluded.source_revisions_json,generated_at=excluded.generated_at`,
		revisionID, tradeDate, dailyFeatureVersion, string(sourceJSON), generatedAt); err != nil {
		return err
	}
	return rebuildFutureLabels(ctx, tx, tradeDate)
}

func (s *Store) RebuildAnalytics(ctx context.Context, revisionID string) error {
	var tradeDate string
	if err := s.db.QueryRowContext(ctx, `SELECT trade_date FROM daily_archive_revision WHERE revision_id=?`, revisionID).Scan(&tradeDate); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := buildAnalyticsForRevision(ctx, tx, revisionID, tradeDate); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DailyFeatures(ctx context.Context, tradeDate, revisionID string, rankType graymarket.RankType) ([]repository.DailyFeature, repository.DailyFeatureSet, error) {
	if revisionID == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT revision_id FROM daily_archive_current WHERE trade_date=?`, tradeDate).Scan(&revisionID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return []repository.DailyFeature{}, repository.DailyFeatureSet{}, nil
			}
			return nil, repository.DailyFeatureSet{}, err
		}
	}
	var set repository.DailyFeatureSet
	var sourceJSON, generatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT revision_id,trade_date,feature_version,
source_revisions_json,generated_at FROM daily_feature_set WHERE revision_id=?`, revisionID).
		Scan(&set.RevisionID, &set.TradeDate, &set.FeatureVersion, &sourceJSON, &generatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return []repository.DailyFeature{}, set, nil
	}
	if err != nil {
		return nil, set, err
	}
	if err := json.Unmarshal([]byte(sourceJSON), &set.SourceRevisions); err != nil {
		return nil, set, err
	}
	set.GeneratedAt, err = time.Parse(timestampLayout, generatedAt)
	if err != nil {
		return nil, set, err
	}
	where := "revision_id=?"
	args := []any{revisionID}
	if rankType != "" {
		where += " AND rank_type=?"
		args = append(args, string(rankType))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT revision_id,trade_date,rank_type,market,code,name,
primary_industry_code,signed_dark_activity,capital_intensity,control_coefficient,
rank_percentile,turnover_percentile,dark_money_percentile,self_turnover_percentile_5,
self_turnover_percentile_10,self_turnover_percentile_20,self_turnover_percentile_60,
self_dark_money_percentile_5,self_dark_money_percentile_10,self_dark_money_percentile_20,
self_dark_money_percentile_60,rank_change_1,consecutive_inflow_days,money_acceleration,
curve_available,morning_dark_share,afternoon_dark_share,late_dark_share,max_inflow_minute_index,
max_outflow_minute_index,tail_acceleration,max_dark_drawdown,intraday_reversal,price_money_divergence
FROM daily_feature WHERE `+where+` ORDER BY rank_type,code`, args...)
	if err != nil {
		return nil, set, err
	}
	defer rows.Close()
	result := make([]repository.DailyFeature, 0)
	for rows.Next() {
		var item repository.DailyFeature
		var rankTypeValue string
		var turnover5, turnover10, turnover20, turnover60 sql.NullFloat64
		var dark5, dark10, dark20, dark60 sql.NullFloat64
		var curveAvailable, reversal, divergence int
		if err := rows.Scan(&item.RevisionID, &item.TradeDate, &rankTypeValue, &item.Market,
			&item.Code, &item.Name, &item.PrimaryIndustryCode, &item.SignedDarkActivity,
			&item.CapitalIntensity, &item.ControlCoefficient, &item.RankPercentile,
			&item.TurnoverPercentile, &item.DarkMoneyPercentile, &turnover5, &turnover10,
			&turnover20, &turnover60, &dark5, &dark10, &dark20, &dark60,
			&item.RankChange1, &item.ConsecutiveInflowDays, &item.MoneyAcceleration,
			&curveAvailable, &item.MorningDarkShare, &item.AfternoonDarkShare,
			&item.LateDarkShare, &item.MaxInflowMinuteIndex, &item.MaxOutflowMinuteIndex,
			&item.TailAcceleration, &item.MaxDarkDrawdown, &reversal, &divergence); err != nil {
			return nil, set, err
		}
		item.RankType = graymarket.RankType(rankTypeValue)
		item.SelfTurnoverPercentile5 = nullableFloat(turnover5)
		item.SelfTurnoverPercentile10 = nullableFloat(turnover10)
		item.SelfTurnoverPercentile20 = nullableFloat(turnover20)
		item.SelfTurnoverPercentile60 = nullableFloat(turnover60)
		item.SelfDarkMoneyPercentile5 = nullableFloat(dark5)
		item.SelfDarkMoneyPercentile10 = nullableFloat(dark10)
		item.SelfDarkMoneyPercentile20 = nullableFloat(dark20)
		item.SelfDarkMoneyPercentile60 = nullableFloat(dark60)
		item.CurveAvailable = curveAvailable != 0
		item.IntradayReversal = reversal != 0
		item.PriceMoneyDivergence = divergence != 0
		result = append(result, item)
	}
	return result, set, rows.Err()
}

func (s *Store) FutureReturnLabels(ctx context.Context, tradeDate, revisionID, targetRevisionID string, horizon int) ([]repository.FutureReturnLabel, error) {
	if revisionID == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT revision_id FROM daily_archive_current WHERE trade_date=?`, tradeDate).Scan(&revisionID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return []repository.FutureReturnLabel{}, nil
			}
			return nil, err
		}
	}
	where := "label.signal_revision_id=?"
	args := []any{revisionID}
	if horizon > 0 {
		where += " AND label.horizon=?"
		args = append(args, horizon)
	}
	join := `JOIN daily_archive_current AS target_current
  ON target_current.trade_date=label.target_date
 AND target_current.revision_id=label.target_revision_id`
	if targetRevisionID != "" {
		join = ""
		where += " AND label.target_revision_id=?"
		args = append(args, targetRevisionID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT label.signal_revision_id,label.target_revision_id,
label.signal_date,label.target_date,label.horizon,label.rank_type,label.market,label.code,
label.return_rate,label.relative_industry_return,label.max_favorable_return,
label.max_adverse_return,label.label_version,label.generated_at
FROM future_return_label AS label
`+join+`
WHERE `+where+
		` ORDER BY label.horizon,label.rank_type,label.code`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]repository.FutureReturnLabel, 0)
	for rows.Next() {
		var item repository.FutureReturnLabel
		var rankTypeValue, generatedAt string
		var relative sql.NullFloat64
		if err := rows.Scan(&item.SignalRevisionID, &item.TargetRevisionID, &item.SignalDate,
			&item.TargetDate, &item.Horizon, &rankTypeValue, &item.Market, &item.Code,
			&item.ReturnRate, &relative, &item.MaxFavorableReturn, &item.MaxAdverseReturn,
			&item.LabelVersion, &generatedAt); err != nil {
			return nil, err
		}
		item.RankType = graymarket.RankType(rankTypeValue)
		item.RelativeIndustryReturn = nullableFloat(relative)
		item.GeneratedAt, err = time.Parse(timestampLayout, generatedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func loadCurrentRevisionRefs(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, throughDate string, limit int) ([]revisionRef, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT revision.revision_id,revision.trade_date,
revision.revision_no,coalesce(revision.previous_revision_id,''),revision.content_sha256,revision.created_at
FROM daily_archive_current AS current
JOIN daily_archive_revision AS revision ON revision.revision_id=current.revision_id
WHERE current.trade_date<=? ORDER BY current.trade_date DESC LIMIT ?`, throughDate, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]revisionRef, 0)
	for rows.Next() {
		var ref revisionRef
		var createdAt string
		if err := rows.Scan(&ref.RevisionID, &ref.TradeDate, &ref.RevisionNo,
			&ref.PreviousRevision, &ref.ContentSHA256, &createdAt); err != nil {
			return nil, err
		}
		ref.CreatedAt, err = time.Parse(timestampLayout, createdAt)
		if err != nil {
			return nil, err
		}
		result = append(result, ref)
	}
	return result, rows.Err()
}

func loadArchiveRecordMap(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, tradeDate string) (map[featureKey]graymarket.RankRecord, error) {
	// Suspended/abnormal securities remain in the daily identity snapshot, but
	// without a usable daily bar they must not enter derived features.
	rows, err := queryer.QueryContext(ctx, `SELECT `+recordColumns+`
FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close'
AND (rank_type!='stock' OR quote_available=1)`, tradeDate)
	if err != nil {
		return nil, err
	}
	records, err := scanRecords(rows)
	if err != nil {
		return nil, err
	}
	result := make(map[featureKey]graymarket.RankRecord, len(records))
	for _, record := range records {
		result[featureKey{rankType: record.RankType, market: record.Market, code: record.Code}] = record
	}
	return result, nil
}

func loadArchiveHistoryMap(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, tradeDate string) (map[featureKey]historicalRecord, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT rank_type,market,code,rank,turnover,dark_money,main_money_inflow
FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close'
AND (rank_type!='stock' OR quote_available=1)`, tradeDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[featureKey]historicalRecord)
	for rows.Next() {
		var rankTypeValue, code string
		var market int64
		var item historicalRecord
		if err := rows.Scan(&rankTypeValue, &market, &code, &item.rank, &item.turnover,
			&item.darkMoney, &item.mainMoneyInflow); err != nil {
			return nil, err
		}
		result[featureKey{rankType: graymarket.RankType(rankTypeValue), market: market, code: code}] = item
	}
	return result, rows.Err()
}

func loadPrimaryIndustries(ctx context.Context, tx *sql.Tx, asOf string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `WITH selected_baseline AS (
    SELECT max(baseline_date) AS baseline_date FROM stock_board_relation_baseline WHERE baseline_date<=?
), events AS (
    SELECT baseline_date AS effective_date,'added' AS change_type,stock_code,board_code,source_order,detected_at
    FROM stock_board_relation_baseline
    WHERE baseline_date=(SELECT baseline_date FROM selected_baseline) AND board_type='industry'
    UNION ALL
    SELECT effective_date,change_type,stock_code,board_code,source_order,detected_at
    FROM stock_board_relation_change
    WHERE effective_date>=(SELECT baseline_date FROM selected_baseline)
      AND effective_date<=? AND board_type='industry'
), latest AS (
    SELECT *,row_number() OVER (
        PARTITION BY stock_code,board_code
        ORDER BY effective_date DESC,detected_at DESC
    ) AS event_rank
    FROM events
), active AS (
    SELECT stock_code,board_code,source_order,row_number() OVER (
        PARTITION BY stock_code ORDER BY source_order,board_code
    ) AS source_rank
    FROM latest WHERE event_rank=1 AND change_type='added'
)
SELECT stock_code,board_code FROM active WHERE source_rank=1 ORDER BY stock_code`, asOf, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var stockCode, boardCode string
		if err := rows.Scan(&stockCode, &boardCode); err != nil {
			return nil, err
		}
		result[stockCode] = boardCode
	}
	return result, rows.Err()
}

func loadArchiveCurves(ctx context.Context, tx *sql.Tx, tradeDate string) (map[featureKey]curveValues, error) {
	result := make(map[featureKey]curveValues)
	rows, err := tx.QueryContext(ctx, `SELECT rank_type,market,code,dark_money
FROM board_money_5m WHERE trade_date=? ORDER BY rank_type,market,code,snapshot_at`, tradeDate)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rankTypeValue, code string
		var market, darkMoney int64
		if err := rows.Scan(&rankTypeValue, &market, &code, &darkMoney); err != nil {
			rows.Close()
			return nil, err
		}
		key := featureKey{rankType: graymarket.RankType(rankTypeValue), market: market, code: code}
		curve := result[key]
		curve.dark = append(curve.dark, darkMoney)
		result[key] = curve
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT market,code,dark_money
FROM stock_research_5m WHERE trade_date=? ORDER BY market,code,minute_index`, tradeDate)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var code string
		var market, darkMoney int64
		if err := rows.Scan(&market, &code, &darkMoney); err != nil {
			rows.Close()
			return nil, err
		}
		key := featureKey{rankType: graymarket.RankStock, market: market, code: code}
		curve := result[key]
		curve.dark = append(curve.dark, darkMoney)
		result[key] = curve
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func deriveCurve(values curveValues) derivedCurve {
	result := derivedCurve{maxInflowIndex: -1, maxOutflowIndex: -1}
	if len(values.dark) != 48 {
		return result
	}
	result.available = true
	final := values.dark[47]
	if final != 0 {
		result.morningShare = float64(values.dark[23]) / float64(final)
		result.afternoonShare = float64(values.dark[41]-values.dark[23]) / float64(final)
		result.lateShare = float64(values.dark[47]-values.dark[41]) / float64(final)
	}
	deltas := make([]int64, 48)
	var previous, peak int64
	var previousSign int
	maxInflow, maxOutflow := int64(math.MinInt64), int64(math.MaxInt64)
	for index, value := range values.dark {
		delta := value - previous
		deltas[index] = delta
		if delta > maxInflow {
			maxInflow, result.maxInflowIndex = delta, index
		}
		if delta < maxOutflow {
			maxOutflow, result.maxOutflowIndex = delta, index
		}
		if index == 0 || value > peak {
			peak = value
		}
		if drawdown := value - peak; drawdown < result.maxDrawdown {
			result.maxDrawdown = drawdown
		}
		sign := signInt64(value)
		if sign != 0 {
			if previousSign != 0 && sign != previousSign {
				result.reversal = true
			}
			previousSign = sign
		}
		previous = value
	}
	result.tailAcceleration = deltas[47] - deltas[46]
	return result
}

func percentile(value float64, values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	less, equal := 0, 0
	for _, candidate := range values {
		if candidate < value {
			less++
		} else if candidate == value {
			equal++
		}
	}
	return (float64(less) + float64(equal)/2) / float64(len(values))
}

func historicalPercentile(history []map[featureKey]historicalRecord, key featureKey, window int, value func(historicalRecord) float64) any {
	values := make([]float64, 0, window)
	for _, records := range history {
		if record, ok := records[key]; ok {
			values = append(values, value(record))
			if len(values) == window {
				break
			}
		}
	}
	if len(values) < window {
		return nil
	}
	result := percentile(values[0], values)
	return result
}

func signInt64(value int64) int {
	switch {
	case value < 0:
		return -1
	case value > 0:
		return 1
	default:
		return 0
	}
}

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func rebuildFutureLabels(ctx context.Context, tx *sql.Tx, changedDate string) error {
	refs, err := loadAllCurrentRevisionRefs(ctx, tx)
	if err != nil {
		return err
	}
	changedIndex := -1
	for index, ref := range refs {
		if ref.TradeDate == changedDate {
			changedIndex = index
			break
		}
	}
	if changedIndex < 0 {
		return nil
	}
	start := max(0, changedIndex-20)
	recordCache := make(map[string]map[featureKey]float64)
	loadRecords := func(ref revisionRef) (map[featureKey]float64, error) {
		if records, ok := recordCache[ref.TradeDate]; ok {
			return records, nil
		}
		records, err := loadArchiveCloseMap(ctx, tx, ref.TradeDate)
		if err == nil {
			recordCache[ref.TradeDate] = records
		}
		return records, err
	}
	generatedAt := time.Now().UTC().Format(timestampLayout)
	statement, err := tx.PrepareContext(ctx, `INSERT INTO future_return_label
(signal_revision_id,target_revision_id,signal_date,target_date,horizon,rank_type,market,code,
return_rate,relative_industry_return,max_favorable_return,max_adverse_return,label_version,generated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(signal_revision_id,target_revision_id,horizon,rank_type,market,code) DO UPDATE SET
target_date=excluded.target_date,
return_rate=excluded.return_rate,relative_industry_return=excluded.relative_industry_return,
max_favorable_return=excluded.max_favorable_return,max_adverse_return=excluded.max_adverse_return,
label_version=excluded.label_version,generated_at=excluded.generated_at`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for signalIndex := start; signalIndex <= changedIndex; signalIndex++ {
		signalRef := refs[signalIndex]
		signalRecords, err := loadRecords(signalRef)
		if err != nil {
			return err
		}
		industries, err := loadFeatureIndustries(ctx, tx, signalRef.RevisionID)
		if err != nil {
			return err
		}
		for _, horizon := range labelHorizons {
			targetIndex := signalIndex + horizon
			if targetIndex >= len(refs) {
				continue
			}
			targetRef := refs[targetIndex]
			if _, err := tx.ExecContext(ctx, `DELETE FROM future_return_label
WHERE signal_revision_id=? AND target_revision_id=? AND horizon=?`,
				signalRef.RevisionID, targetRef.RevisionID, horizon); err != nil {
				return err
			}
			targetRecords, err := loadRecords(targetRef)
			if err != nil {
				return err
			}
			path := make([]map[featureKey]float64, 0, horizon)
			for pathIndex := signalIndex + 1; pathIndex <= targetIndex; pathIndex++ {
				records, err := loadRecords(refs[pathIndex])
				if err != nil {
					return err
				}
				path = append(path, records)
			}
			for key, signalClose := range signalRecords {
				targetClose, ok := targetRecords[key]
				if !ok || signalClose <= 0 || targetClose <= 0 {
					continue
				}
				returnRate := targetClose/signalClose - 1
				maxFavorable, maxAdverse := returnRate, returnRate
				for _, pathRecords := range path {
					if pointClose, exists := pathRecords[key]; exists && pointClose > 0 {
						value := pointClose/signalClose - 1
						maxFavorable = max(maxFavorable, value)
						maxAdverse = min(maxAdverse, value)
					}
				}
				var relative any
				if key.rankType == graymarket.RankStock {
					if industryCode := industries[key.code]; industryCode != "" {
						signalIndustry, signalOK := closeByTypeCode(signalRecords, graymarket.RankIndustry, industryCode)
						targetIndustry, targetOK := closeByTypeCode(targetRecords, graymarket.RankIndustry, industryCode)
						if signalOK && targetOK && signalIndustry > 0 && targetIndustry > 0 {
							value := returnRate - (targetIndustry/signalIndustry - 1)
							relative = value
						}
					}
				}
				if _, err := statement.ExecContext(ctx, signalRef.RevisionID, targetRef.RevisionID,
					signalRef.TradeDate, targetRef.TradeDate, horizon, string(key.rankType), key.market,
					key.code, returnRate, relative, maxFavorable, maxAdverse, futureLabelVersion,
					generatedAt); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func loadAllCurrentRevisionRefs(ctx context.Context, tx *sql.Tx) ([]revisionRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT revision.revision_id,revision.trade_date,
revision.revision_no,coalesce(revision.previous_revision_id,''),revision.content_sha256,revision.created_at
FROM daily_archive_current AS current
JOIN daily_archive_revision AS revision ON revision.revision_id=current.revision_id
ORDER BY current.trade_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]revisionRef, 0)
	for rows.Next() {
		var ref revisionRef
		var createdAt string
		if err := rows.Scan(&ref.RevisionID, &ref.TradeDate, &ref.RevisionNo,
			&ref.PreviousRevision, &ref.ContentSHA256, &createdAt); err != nil {
			return nil, err
		}
		ref.CreatedAt, err = time.Parse(timestampLayout, createdAt)
		if err != nil {
			return nil, err
		}
		result = append(result, ref)
	}
	return result, rows.Err()
}

func loadFeatureIndustries(ctx context.Context, tx *sql.Tx, revisionID string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT code,primary_industry_code FROM daily_feature
WHERE revision_id=? AND rank_type='stock' AND primary_industry_code!=''`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var code, industryCode string
		if err := rows.Scan(&code, &industryCode); err != nil {
			return nil, err
		}
		result[code] = industryCode
	}
	return result, rows.Err()
}

func loadArchiveCloseMap(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, tradeDate string) (map[featureKey]float64, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT rank_type,market,code,close_price
FROM rank_snapshot WHERE trade_date=? AND snapshot_kind='daily_close'
AND (rank_type!='stock' OR quote_available=1)`, tradeDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[featureKey]float64)
	for rows.Next() {
		var rankTypeValue, code string
		var market int64
		var closePrice float64
		if err := rows.Scan(&rankTypeValue, &market, &code, &closePrice); err != nil {
			return nil, err
		}
		result[featureKey{rankType: graymarket.RankType(rankTypeValue), market: market, code: code}] = closePrice
	}
	return result, rows.Err()
}

func closeByTypeCode(records map[featureKey]float64, rankType graymarket.RankType, code string) (float64, bool) {
	for key, closePrice := range records {
		if key.rankType == rankType && key.code == code {
			return closePrice, true
		}
	}
	return 0, false
}

func migrateAnalytics(store *Store) error {
	var migrated int
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM database_maintenance
WHERE name='analytics_v1')`).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 1 {
		return nil
	}
	rows, err := store.db.Query(`SELECT current.revision_id
FROM daily_archive_current AS current
LEFT JOIN daily_feature_set AS features ON features.revision_id=current.revision_id
WHERE features.revision_id IS NULL ORDER BY current.trade_date`)
	if err != nil {
		return err
	}
	var revisionIDs []string
	for rows.Next() {
		var revisionID string
		if err := rows.Scan(&revisionID); err != nil {
			rows.Close()
			return err
		}
		revisionIDs = append(revisionIDs, revisionID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, revisionID := range revisionIDs {
		if err := store.RebuildAnalytics(context.Background(), revisionID); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(timestampLayout)
	_, err = store.db.Exec(`INSERT INTO database_maintenance(name,completed_at)
VALUES ('analytics_v1',?) ON CONFLICT(name) DO UPDATE SET completed_at=excluded.completed_at`, now)
	return err
}
