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
  change_pct: number
  change_value: number
  volume: number
  turnover: number
  quote_time: string
  fetched_at?: string
  quote_available: boolean
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
  change_pct: number
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
