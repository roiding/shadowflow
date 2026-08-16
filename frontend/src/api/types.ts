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
}

export interface SystemStatus {
  server_time: string
  timezone: string
  market_status: 'pre_open' | 'open' | 'lunch_break' | 'closed'
  trading_day: boolean
  latest_trading_day: string
  uptime_seconds: number
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

export interface QualityMeta {
  trade_date: string
  stock_archive: StockArchiveQuality
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
