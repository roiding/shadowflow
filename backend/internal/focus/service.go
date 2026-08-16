package focus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/roiding/shadowflow/internal/graymarket"
)

const (
	minConsecutiveDays = 1
	maxConsecutiveDays = 60
	maxConditions      = 20
)

var ErrInvalidRequest = errors.New("invalid focus scan request")

type Source interface {
	DailyCloseTradeDates(context.Context, string, int) ([]string, error)
	DailyCloseRecords(context.Context, string) ([]graymarket.RankRecord, error)
	BoardStockRelations(context.Context, graymarket.BoardType, string, string) ([]graymarket.StockBoardRelation, error)
}

type Service struct{ source Source }

type Field string

const (
	FieldTurnover           Field = "turnover"
	FieldTurnoverRate       Field = "turnover_rate"
	FieldChangePct          Field = "change_pct"
	FieldControlCoefficient Field = "control_coefficient"
	FieldDarkMoney          Field = "dark_money"
	FieldRegularMoney       Field = "regular_money"
	FieldMainMoneyInflow    Field = "main_money_inflow"
	FieldDarkActivity       Field = "dark_activity"
	FieldDarkInflowRatio    Field = "dark_inflow_ratio"
	FieldRank               Field = "rank"
	FieldClosePrice         Field = "close_price"
	FieldAmplitude          Field = "amplitude"
	FieldVolume             Field = "volume"
	FieldUpCount            Field = "up_count"
	FieldFlatCount          Field = "flat_count"
	FieldDownCount          Field = "down_count"
)

type Operator string

const (
	OperatorGT      Operator = "gt"
	OperatorGTE     Operator = "gte"
	OperatorLT      Operator = "lt"
	OperatorLTE     Operator = "lte"
	OperatorEQ      Operator = "eq"
	OperatorBetween Operator = "between"
)

type MatchMode string

const (
	MatchAll MatchMode = "all"
	MatchAny MatchMode = "any"
)

type Condition struct {
	Field    Field    `json:"field"`
	Operator Operator `json:"operator"`
	Value    float64  `json:"value"`
	MaxValue *float64 `json:"max_value,omitempty"`
}

type StockScope struct {
	MainBoardOnly            bool `json:"main_board_only"`
	ExcludeST                bool `json:"exclude_st"`
	RequireQualifiedConcepts bool `json:"require_qualified_concepts"`
}

type ScanRequest struct {
	AsOf              string      `json:"as_of"`
	ConsecutiveDays   int         `json:"consecutive_days"`
	ConceptMatch      MatchMode   `json:"concept_match"`
	ConceptConditions []Condition `json:"concept_conditions"`
	StockMatch        MatchMode   `json:"stock_match"`
	StockConditions   []Condition `json:"stock_conditions"`
	StockScope        StockScope  `json:"stock_scope"`
}

func DefaultRequest(asOf string) ScanRequest {
	conceptChangeMax, conceptControlMax := 0.06, 6.0
	stockChangeMax, stockControlMax := 0.06, 6.0
	return ScanRequest{
		AsOf:            asOf,
		ConsecutiveDays: 3,
		ConceptMatch:    MatchAll,
		ConceptConditions: []Condition{
			{Field: FieldTurnover, Operator: OperatorGT, Value: 50_000_000_000},
			{Field: FieldTurnoverRate, Operator: OperatorGT, Value: 0.03},
			{Field: FieldChangePct, Operator: OperatorBetween, Value: 0.01, MaxValue: &conceptChangeMax},
			{Field: FieldControlCoefficient, Operator: OperatorBetween, Value: 1.5, MaxValue: &conceptControlMax},
		},
		StockMatch: MatchAll,
		StockConditions: []Condition{
			{Field: FieldTurnover, Operator: OperatorGT, Value: 200_000_000},
			{Field: FieldTurnoverRate, Operator: OperatorGT, Value: 0.03},
			{Field: FieldChangePct, Operator: OperatorBetween, Value: 0.01, MaxValue: &stockChangeMax},
			{Field: FieldControlCoefficient, Operator: OperatorBetween, Value: 1.5, MaxValue: &stockControlMax},
		},
		StockScope: StockScope{MainBoardOnly: true, ExcludeST: true, RequireQualifiedConcepts: true},
	}
}

type DailyMetric struct {
	TradeDate          string  `json:"trade_date"`
	Turnover           int64   `json:"turnover"`
	TurnoverRate       float64 `json:"turnover_rate"`
	ChangePct          float64 `json:"change_pct"`
	DarkMoney          int64   `json:"dark_money"`
	RegularMoney       int64   `json:"regular_money"`
	MainMoneyInflow    int64   `json:"main_money_inflow"`
	DarkActivity       float64 `json:"dark_activity"`
	DarkInflowRatio    float64 `json:"dark_inflow_ratio"`
	Rank               int64   `json:"rank"`
	ClosePrice         float64 `json:"close_price"`
	Amplitude          float64 `json:"amplitude"`
	Volume             int64   `json:"volume"`
	UpCount            int64   `json:"up_count"`
	FlatCount          int64   `json:"flat_count"`
	DownCount          int64   `json:"down_count"`
	ControlCoefficient float64 `json:"control_coefficient"`
}

type ConceptRef struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type ConceptCandidate struct {
	Code string        `json:"code"`
	Name string        `json:"name"`
	Days []DailyMetric `json:"days"`
}

type StockCandidate struct {
	Market   int64         `json:"market"`
	Code     string        `json:"code"`
	Name     string        `json:"name"`
	Concepts []ConceptRef  `json:"concepts"`
	Days     []DailyMetric `json:"days"`
}

type Stats struct {
	ConceptsEvaluated     int `json:"concepts_evaluated"`
	ConceptsQualified     int `json:"concepts_qualified"`
	StocksEvaluated       int `json:"stocks_evaluated"`
	StocksQualified       int `json:"stocks_qualified"`
	NonMainBoardExcluded  int `json:"non_main_board_excluded"`
	STExcluded            int `json:"st_excluded"`
	MissingRecordExcluded int `json:"missing_record_excluded"`
}

type Result struct {
	RequestedAsOf string             `json:"requested_as_of"`
	AsOf          string             `json:"as_of,omitempty"`
	Ready         bool               `json:"ready"`
	TradeDates    []string           `json:"trade_dates"`
	RequiredDays  int                `json:"required_days"`
	Request       ScanRequest        `json:"request"`
	Concepts      []ConceptCandidate `json:"concepts"`
	Stocks        []StockCandidate   `json:"stocks"`
	Stats         Stats              `json:"stats"`
}

func New(source Source) *Service { return &Service{source: source} }

// Scan preserves the original fixed-screen endpoint as a reusable default template.
func (s *Service) Scan(ctx context.Context, asOf string) (Result, error) {
	return s.ScanWith(ctx, DefaultRequest(asOf))
}

func (s *Service) ScanWith(ctx context.Context, request ScanRequest) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	result := Result{RequestedAsOf: request.AsOf, RequiredDays: request.ConsecutiveDays, Request: request,
		Concepts: []ConceptCandidate{}, Stocks: []StockCandidate{}, TradeDates: []string{}}
	dates, err := s.source.DailyCloseTradeDates(ctx, request.AsOf, request.ConsecutiveDays)
	if err != nil {
		return result, err
	}
	result.TradeDates = dates
	if len(dates) < request.ConsecutiveDays {
		return result, nil
	}
	result.Ready, result.AsOf = true, dates[len(dates)-1]

	conceptRows := make(map[string]map[string]graymarket.RankRecord, len(dates))
	stockRows := make(map[string]map[string]graymarket.RankRecord, len(dates))
	for _, date := range dates {
		records, err := s.source.DailyCloseRecords(ctx, date)
		if err != nil {
			return result, err
		}
		conceptRows[date] = indexRecords(records, graymarket.RankConcept)
		stockRows[date] = indexRecords(records, graymarket.RankStock)
	}

	result.Concepts, result.Stats = evaluateConcepts(dates, conceptRows, request)
	universe := make(map[string]graymarket.StockBoardRelation)
	memberships := make(map[string][]ConceptRef)
	if request.StockScope.RequireQualifiedConcepts {
		for _, concept := range result.Concepts {
			relations, err := s.source.BoardStockRelations(ctx, graymarket.BoardConcept, concept.Code, result.AsOf)
			if err != nil {
				return result, err
			}
			ref := ConceptRef{Code: concept.Code, Name: concept.Name}
			for _, relation := range relations {
				universe[relation.StockCode] = relation
				memberships[relation.StockCode] = append(memberships[relation.StockCode], ref)
			}
		}
	} else {
		for code, record := range stockRows[result.AsOf] {
			universe[code] = graymarket.StockBoardRelation{StockMarket: record.Market, StockCode: code, StockName: record.Name}
		}
	}
	result.Stocks, result.Stats = evaluateStocks(dates, stockRows, universe, memberships, request, result.Stats)
	return result, nil
}

func validateRequest(request ScanRequest) error {
	if request.AsOf == "" {
		return fmt.Errorf("%w: as_of is required", ErrInvalidRequest)
	}
	if request.ConsecutiveDays < minConsecutiveDays || request.ConsecutiveDays > maxConsecutiveDays {
		return fmt.Errorf("%w: consecutive_days must be between %d and %d", ErrInvalidRequest, minConsecutiveDays, maxConsecutiveDays)
	}
	if request.ConceptMatch != MatchAll && request.ConceptMatch != MatchAny {
		return fmt.Errorf("%w: concept_match must be all or any", ErrInvalidRequest)
	}
	if request.StockMatch != MatchAll && request.StockMatch != MatchAny {
		return fmt.Errorf("%w: stock_match must be all or any", ErrInvalidRequest)
	}
	if err := validateConditions("concept_conditions", request.ConceptConditions); err != nil {
		return err
	}
	return validateConditions("stock_conditions", request.StockConditions)
}

func validateConditions(name string, conditions []Condition) error {
	if len(conditions) > maxConditions {
		return fmt.Errorf("%w: %s cannot contain more than %d conditions", ErrInvalidRequest, name, maxConditions)
	}
	for index, condition := range conditions {
		if !validField(condition.Field) {
			return fmt.Errorf("%w: %s[%d] has an unsupported field", ErrInvalidRequest, name, index)
		}
		if !validOperator(condition.Operator) {
			return fmt.Errorf("%w: %s[%d] has an unsupported operator", ErrInvalidRequest, name, index)
		}
		if math.IsNaN(condition.Value) || math.IsInf(condition.Value, 0) {
			return fmt.Errorf("%w: %s[%d] value must be finite", ErrInvalidRequest, name, index)
		}
		if condition.Operator == OperatorBetween {
			if condition.MaxValue == nil || math.IsNaN(*condition.MaxValue) || math.IsInf(*condition.MaxValue, 0) || condition.Value > *condition.MaxValue {
				return fmt.Errorf("%w: %s[%d] requires max_value greater than or equal to value", ErrInvalidRequest, name, index)
			}
		}
	}
	return nil
}

func validField(field Field) bool {
	switch field {
	case FieldTurnover, FieldTurnoverRate, FieldChangePct, FieldControlCoefficient, FieldDarkMoney,
		FieldRegularMoney, FieldMainMoneyInflow, FieldDarkActivity, FieldDarkInflowRatio, FieldRank,
		FieldClosePrice, FieldAmplitude, FieldVolume, FieldUpCount, FieldFlatCount, FieldDownCount:
		return true
	default:
		return false
	}
}

func validOperator(operator Operator) bool {
	switch operator {
	case OperatorGT, OperatorGTE, OperatorLT, OperatorLTE, OperatorEQ, OperatorBetween:
		return true
	default:
		return false
	}
}

func evaluateConcepts(dates []string, records map[string]map[string]graymarket.RankRecord, request ScanRequest) ([]ConceptCandidate, Stats) {
	var stats Stats
	codes := commonCodes(dates, records)
	stats.ConceptsEvaluated = len(codes)
	result := make([]ConceptCandidate, 0)
	for _, code := range codes {
		candidate := ConceptCandidate{Code: code, Name: records[dates[len(dates)-1]][code].Name}
		qualified := true
		for _, date := range dates {
			metric := metricFor(records[date][code])
			candidate.Days = append(candidate.Days, metric)
			if !conditionsMatch(metric, request.ConceptConditions, request.ConceptMatch) {
				qualified = false
			}
		}
		if qualified {
			result = append(result, candidate)
		}
	}
	sortCandidates(result)
	stats.ConceptsQualified = len(result)
	return result, stats
}

func evaluateStocks(dates []string, records map[string]map[string]graymarket.RankRecord, universe map[string]graymarket.StockBoardRelation, memberships map[string][]ConceptRef, request ScanRequest, stats Stats) ([]StockCandidate, Stats) {
	codes := make([]string, 0, len(universe))
	for code := range universe {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	stats.StocksEvaluated = len(codes)
	result := make([]StockCandidate, 0)
	for _, code := range codes {
		relation := universe[code]
		if request.StockScope.MainBoardOnly && !isMainBoard(relation.StockMarket, code) {
			stats.NonMainBoardExcluded++
			continue
		}
		if request.StockScope.ExcludeST && isST(relation.StockName) {
			stats.STExcluded++
			continue
		}
		candidate := StockCandidate{Market: relation.StockMarket, Code: code, Name: relation.StockName, Concepts: memberships[code]}
		if candidate.Concepts == nil {
			candidate.Concepts = []ConceptRef{}
		}
		qualified := true
		for _, date := range dates {
			record, exists := records[date][code]
			if !exists || !record.QuoteAvailable {
				stats.MissingRecordExcluded++
				qualified = false
				break
			}
			metric := metricFor(record)
			candidate.Days = append(candidate.Days, metric)
			if !conditionsMatch(metric, request.StockConditions, request.StockMatch) {
				qualified = false
			}
		}
		if qualified {
			result = append(result, candidate)
		}
	}
	sortStocks(result)
	stats.StocksQualified = len(result)
	return result, stats
}

func indexRecords(records []graymarket.RankRecord, rankType graymarket.RankType) map[string]graymarket.RankRecord {
	result := make(map[string]graymarket.RankRecord)
	for _, record := range records {
		if record.RankType == rankType && record.QuoteAvailable {
			result[record.Code] = record
		}
	}
	return result
}

func commonCodes(dates []string, records map[string]map[string]graymarket.RankRecord) []string {
	result := make([]string, 0)
	for code := range records[dates[0]] {
		present := true
		for _, date := range dates[1:] {
			if _, exists := records[date][code]; !exists {
				present = false
				break
			}
		}
		if present {
			result = append(result, code)
		}
	}
	sort.Strings(result)
	return result
}

func metricFor(record graymarket.RankRecord) DailyMetric {
	control := 0.0
	if record.Turnover > 0 {
		control = float64(record.RegularMoney+record.DarkMoney) / float64(record.Turnover) * 100
	}
	return DailyMetric{TradeDate: record.TradeDate, Turnover: record.Turnover, TurnoverRate: record.TurnoverRate,
		ChangePct: record.ChangePct, DarkMoney: record.DarkMoney, RegularMoney: record.RegularMoney,
		MainMoneyInflow: record.MainMoneyInflow, DarkActivity: record.DarkActivity, DarkInflowRatio: record.DarkInflowRatio,
		Rank: record.Rank, ClosePrice: record.ClosePrice, Amplitude: record.Amplitude, Volume: record.Volume,
		UpCount: record.UpCount, FlatCount: record.FlatCount, DownCount: record.DownCount, ControlCoefficient: control}
}

func conditionsMatch(metric DailyMetric, conditions []Condition, mode MatchMode) bool {
	if len(conditions) == 0 {
		return true
	}
	for _, condition := range conditions {
		matches := conditionMatches(metric, condition)
		if mode == MatchAll && !matches {
			return false
		}
		if mode == MatchAny && matches {
			return true
		}
	}
	return mode == MatchAll
}

func conditionMatches(metric DailyMetric, condition Condition) bool {
	actual := metricValue(metric, condition.Field)
	switch condition.Operator {
	case OperatorGT:
		return actual > condition.Value
	case OperatorGTE:
		return actual >= condition.Value
	case OperatorLT:
		return actual < condition.Value
	case OperatorLTE:
		return actual <= condition.Value
	case OperatorEQ:
		return actual == condition.Value
	case OperatorBetween:
		return condition.MaxValue != nil && actual >= condition.Value && actual <= *condition.MaxValue
	default:
		return false
	}
}

func metricValue(metric DailyMetric, field Field) float64 {
	switch field {
	case FieldTurnover:
		return float64(metric.Turnover)
	case FieldTurnoverRate:
		return metric.TurnoverRate
	case FieldChangePct:
		return metric.ChangePct
	case FieldControlCoefficient:
		return metric.ControlCoefficient
	case FieldDarkMoney:
		return float64(metric.DarkMoney)
	case FieldRegularMoney:
		return float64(metric.RegularMoney)
	case FieldMainMoneyInflow:
		return float64(metric.MainMoneyInflow)
	case FieldDarkActivity:
		return metric.DarkActivity
	case FieldDarkInflowRatio:
		return metric.DarkInflowRatio
	case FieldRank:
		return float64(metric.Rank)
	case FieldClosePrice:
		return metric.ClosePrice
	case FieldAmplitude:
		return metric.Amplitude
	case FieldVolume:
		return float64(metric.Volume)
	case FieldUpCount:
		return float64(metric.UpCount)
	case FieldFlatCount:
		return float64(metric.FlatCount)
	case FieldDownCount:
		return float64(metric.DownCount)
	default:
		return 0
	}
}

func sortCandidates(result []ConceptCandidate) {
	sort.Slice(result, func(i, j int) bool {
		return candidateBefore(result[i].Code, result[i].Days, result[j].Code, result[j].Days)
	})
}

func sortStocks(result []StockCandidate) {
	sort.Slice(result, func(i, j int) bool {
		return candidateBefore(result[i].Code, result[i].Days, result[j].Code, result[j].Days)
	})
}

func candidateBefore(leftCode string, left []DailyMetric, rightCode string, right []DailyMetric) bool {
	l, r := left[len(left)-1].ControlCoefficient, right[len(right)-1].ControlCoefficient
	if l == r {
		return leftCode < rightCode
	}
	return l > r
}

func isMainBoard(market int64, code string) bool {
	if len(code) != 6 {
		return false
	}
	if market == 1 {
		return hasPrefix(code, "600", "601", "603", "605")
	}
	if market == 0 {
		return hasPrefix(code, "000", "001", "002", "003")
	}
	return false
}

func isST(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	return strings.HasPrefix(name, "ST") || strings.HasPrefix(name, "*ST") || strings.HasPrefix(name, "S*ST") || strings.HasPrefix(name, "SST")
}

func hasPrefix(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
