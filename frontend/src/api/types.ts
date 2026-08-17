export type RankType = 'industry' | 'concept' | 'stock'

export type BoardStockQuote = {
  stock_code: string
  stock_market: number
  stock_name: string
  board_code: string
  board_name: string
  board_type: 'industry' | 'concept'
  source_order: number
  effective_date: string
  latest_price: number
  open_price: number
  high_price: number
  low_price: number
  previous_close: number
  change_pct: number
  change_value: number
  volume: number
  turnover: number
  turnover_rate: number
  amplitude: number
  quote_time: string
  fetched_at?: string
  quote_available: boolean
  dark_rank: number
  dark_money: number
  main_money_inflow: number
  dark_activity: number
  dark_data_available: boolean
}

export interface RankRecord {
  trade_date: string
  snapshot_at: string
  rank_type: RankType
  rank: number
  market: number
  code: string
  name: string
  quote_time: string
  latest_price_raw: number
  open_price: number
  high_price: number
  low_price: number
  close_price: number
  previous_close: number
  change_value: number
  change_pct: number
  volume: number
  turnover: number
  turnover_rate: number
  amplitude: number
  quote_available: boolean
  dark_money: number
  regular_money: number
  main_money_inflow: number
  dark_activity: number
  dark_inflow_ratio: number
  up_count: number
  flat_count: number
  down_count: number
  leader_name: string
  leader_code: string
  fetched_at: string
}

export interface ApiEnvelope<T, M = Record<string, unknown>> {
  data?: T
  meta?: M
  error?: { code: string; message: string }
}

export interface PageMeta {
  count: number
  total: number
  page: number
  page_size: number
  pages: number
  trade_date: string
  rank_type: RankType
  snapshot_kind: 'daily_close'
  revision_id?: string
}

export interface SystemStatus {
  server_time: string
  timezone: string
  market_status: 'pre_open' | 'open' | 'lunch_break' | 'closed'
  trading_day: boolean
  latest_trading_day: string
  uptime_seconds: number
  trading_calendar: {
    valid_through?: string
    updated_at?: string
    source?: string
    days_remaining: number
    expired: boolean
  }
}

export interface QualitySummary {
  trade_date: string
  rank_type: RankType
  expected_minutes: number
  collected_minutes: number
  expected_research_snapshots: number
  collected_research_snapshots: number
  expected_daily_close_snapshots: number
  collected_daily_close_snapshots: number
  missing_minutes: string[]
  missing_research_snapshots: string[]
  missing_daily_close_snapshots: string[]
  compacted_at?: string
}

export interface StockArchiveQuality {
  trade_date: string
  expected_stocks: number
  expected_points: number
  expected_kline_stocks: number
  money_rows: number
  kline_rows: number
  daily_close_rows: number
  daily_kline_rows: number
  money_archived_at?: string
  kline_archived_at?: string
}

export interface DailyArchiveManifest {
  trade_date: string
  status: 'incomplete' | 'complete'
  industry_close_rows: number
  industry_money_rows: number
  concept_close_rows: number
  concept_money_rows: number
  stock_close_rows: number
  stock_money_rows: number
  stock_kline_rows: number
  stock_daily_kline_rows: number
  expected_stock_rows: number
  expected_stock_kline_rows: number
  code_count: number
  code_set_sha256: string
  kline_source_counts: Record<string, number>
  darktrade_contract: string
  darktradetick_contract: string
  stock_kline_contract: string
  parser_version: string
  validation_errors: string[]
  completed_at?: string
  updated_at?: string
  current_revision_id?: string
  revision_no?: number
}

export interface ArchiveRevision {
  revision_id: string
  trade_date: string
  revision_no: number
  previous_revision?: string
  content_sha256: string
  created_at: string
}

export interface DailyFeature {
  revision_id: string
  trade_date: string
  rank_type: RankType
  market: number
  code: string
  name: string
  primary_industry_code?: string
  signed_dark_activity: number
  capital_intensity: number
  control_coefficient: number
  rank_percentile: number
  turnover_percentile: number
  dark_money_percentile: number
  self_turnover_percentile_5?: number
  self_turnover_percentile_10?: number
  self_turnover_percentile_20?: number
  self_turnover_percentile_60?: number
  self_dark_money_percentile_5?: number
  self_dark_money_percentile_10?: number
  self_dark_money_percentile_20?: number
  self_dark_money_percentile_60?: number
  rank_change_1: number
  consecutive_inflow_days: number
  money_acceleration: number
  curve_available: boolean
  morning_dark_share: number
  afternoon_dark_share: number
  late_dark_share: number
  max_inflow_minute_index: number
  max_outflow_minute_index: number
  tail_acceleration: number
  max_dark_drawdown: number
  intraday_reversal: boolean
  price_money_divergence: boolean
}

export interface FutureReturnLabel {
  signal_revision_id: string
  target_revision_id: string
  signal_date: string
  target_date: string
  horizon: number
  rank_type: RankType
  market: number
  code: string
  return_rate: number
  relative_industry_return?: number
  max_favorable_return: number
  max_adverse_return: number
  label_version: string
  generated_at: string
}

export interface QualityMeta {
  trade_date: string
  stock_archive: StockArchiveQuality
  archive_manifest: DailyArchiveManifest
}

export interface StockResearchPoint {
  trade_date: string
  snapshot_at: string
  market: number
  code: string
  money_rank: number
  dark_money: number
  regular_money: number
  main_money_inflow: number
  open_price: number
  high_price: number
  low_price: number
  close_price: number
  volume: number
  turnover: number
  amplitude: number
  change_pct: number
  change_value: number
  turnover_rate: number
  kline_available: boolean
}

export interface CollectionRun {
  run_id: string
  snapshot_at: string
  snapshot_kind: string
  rank_type: RankType
  status: string
  requested_date: string
  actual_trade_date: string
  expected_total: number
  fetched_total: number
  page_count: number
  attempt_count: number
  started_at: string
  finished_at?: string
  duration_ms: number
  error_code: string
  error_message: string
}

export type MarketStatus = SystemStatus['market_status']

export interface FocusDailyMetric {
  trade_date: string
  turnover: number
  turnover_rate: number
  change_pct: number
  dark_money: number
  regular_money: number
  main_money_inflow: number
  dark_activity: number
  dark_inflow_ratio: number
  rank: number
  close_price: number
  amplitude: number
  volume: number
  up_count: number
  flat_count: number
  down_count: number
  control_coefficient: number
}

export type FocusField = 'turnover' | 'turnover_rate' | 'change_pct' | 'control_coefficient' | 'dark_money' | 'regular_money' | 'main_money_inflow' | 'dark_activity' | 'dark_inflow_ratio' | 'rank' | 'close_price' | 'amplitude' | 'volume' | 'up_count' | 'flat_count' | 'down_count'
export type FocusOperator = 'gt' | 'gte' | 'lt' | 'lte' | 'eq' | 'between'
export type FocusMatchMode = 'all' | 'any'

export interface FocusCondition {
  field: FocusField
  operator: FocusOperator
  value: number
  max_value?: number
}

export interface FocusScanRequest {
  as_of: string
  consecutive_days: number
  concept_match: FocusMatchMode
  concept_conditions: FocusCondition[]
  stock_match: FocusMatchMode
  stock_conditions: FocusCondition[]
  stock_scope: {
    main_board_only: boolean
    exclude_st: boolean
    require_qualified_concepts: boolean
  }
}

export interface FocusConceptRef {
  code: string
  name: string
}

export interface FocusConceptCandidate {
  code: string
  name: string
  days: FocusDailyMetric[]
  evaluations: FocusDayEvaluation[]
}

export interface FocusStockCandidate {
  market: number
  code: string
  name: string
  concepts: FocusConceptRef[]
  days: FocusDailyMetric[]
  evaluations: FocusDayEvaluation[]
}

export interface FocusConditionEvaluation {
  condition: FocusCondition
  actual_value: number
  passed: boolean
}

export interface FocusDayEvaluation {
  trade_date: string
  matched: boolean
  conditions: FocusConditionEvaluation[]
}

export interface FocusRejection {
  kind: 'concept' | 'stock'
  market?: number
  code: string
  name: string
  reason: 'condition_failed' | 'non_main_board' | 'st_excluded' | 'missing_daily_close'
  failed_date?: string
  evaluation?: FocusDayEvaluation
}

export interface FocusResult {
  requested_as_of: string
  as_of?: string
  ready: boolean
  trade_dates: string[]
  required_days: number
  request: FocusScanRequest
  concepts: FocusConceptCandidate[]
  stocks: FocusStockCandidate[]
  rejections: FocusRejection[]
  rejections_truncated: boolean
  stats: {
    concepts_evaluated: number
    concepts_qualified: number
    stocks_evaluated: number
    stocks_qualified: number
    non_main_board_excluded: number
    st_excluded: number
    missing_record_excluded: number
  }
}
