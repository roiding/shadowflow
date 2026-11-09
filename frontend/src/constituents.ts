import type { BoardQuoteMeta, BoardStockQuote, SystemStatus } from './api/types'

export type ConstituentSortKey = 'stock_name' | 'stock_code' | 'source_order' | 'dark_rank' | 'dark_money' | 'main_money_inflow' | 'dark_activity' | 'latest_price' | 'change_pct' | 'turnover' | 'limit_up_seal_days' | 'limit_up_touch_days' | 'limit_up_broken_days' | 'limit_up_seal_rate' | 'limit_up_down_days'
export type ConstituentSort = { key: ConstituentSortKey; direction: 'asc' | 'desc' }

const darkFields: ConstituentSortKey[] = ['dark_rank', 'dark_money', 'main_money_inflow', 'dark_activity']
const quoteFields: ConstituentSortKey[] = ['latest_price', 'change_pct', 'turnover']
const limitUpFields: ConstituentSortKey[] = ['limit_up_seal_days', 'limit_up_touch_days', 'limit_up_broken_days', 'limit_up_seal_rate', 'limit_up_down_days']

export function showConstituentDarkData(stocks: BoardStockQuote[], meta?: BoardQuoteMeta, status?: SystemStatus) {
  // Quotes are live, while stock money is only collected after the close. A
  // stale board snapshot must not expose yesterday's money beside live prices.
  return status?.market_status === 'closed' && meta?.dark_data_available === true
    && stocks.some((stock) => stock.dark_data_available)
}

export function constituentSort(sort: ConstituentSort, showDark: boolean): ConstituentSort {
  return !showDark && darkFields.includes(sort.key) ? { key: 'source_order', direction: 'asc' } : sort
}

export function compareConstituents(left: BoardStockQuote, right: BoardStockQuote, sort: ConstituentSort) {
  const available = (stock: BoardStockQuote) => limitUpFields.includes(sort.key) ? stock.limit_up_stats_available
    : darkFields.includes(sort.key) ? stock.dark_data_available
    : quoteFields.includes(sort.key) ? stock.quote_available : true
  if (available(left) !== available(right)) return available(left) ? -1 : 1
  const statistic = (stock: BoardStockQuote) => sort.key === 'limit_up_seal_rate' ? constituentLimitUpRate(stock)
    : limitUpFields.includes(sort.key) ? (stock[sort.key] ?? 0) : stock[sort.key]
  const a = statistic(left)
  const b = statistic(right)
  if (a === null || b === null) {
    if (a === b) return left.stock_code.localeCompare(right.stock_code)
    return a === null ? 1 : -1
  }
  const compared = typeof a === 'string' && typeof b === 'string' ? a.localeCompare(b, 'zh-CN') : Number(a) - Number(b)
  return (sort.direction === 'asc' ? compared : -compared)
    || (left.source_order ?? 0) - (right.source_order ?? 0) || left.stock_code.localeCompare(right.stock_code)
}

export function constituentLimitUpRate(stock: Pick<BoardStockQuote, 'limit_up_seal_days' | 'limit_up_touch_days'>): number | null {
  const sealDays = stock.limit_up_seal_days
  const touchDays = stock.limit_up_touch_days
  if (sealDays === undefined || touchDays === undefined || !Number.isFinite(sealDays) || !Number.isFinite(touchDays) || touchDays <= 0) {
    return null
  }
  return sealDays / touchDays
}

export function constituentLimitUpSort(statsAvailable: boolean): ConstituentSort {
  return { key: statsAvailable ? 'limit_up_seal_days' : 'source_order', direction: statsAvailable ? 'desc' : 'asc' }
}
