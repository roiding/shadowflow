import type { BoardQuoteMeta, BoardStockQuote, SystemStatus } from './api/types'

export type ConstituentSortKey = 'stock_name' | 'stock_code' | 'source_order' | 'dark_rank' | 'dark_money' | 'main_money_inflow' | 'dark_activity' | 'latest_price' | 'change_pct' | 'turnover'
export type ConstituentSort = { key: ConstituentSortKey; direction: 'asc' | 'desc' }

const darkFields: ConstituentSortKey[] = ['dark_rank', 'dark_money', 'main_money_inflow', 'dark_activity']
const quoteFields: ConstituentSortKey[] = ['latest_price', 'change_pct', 'turnover']

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
  const available = (stock: BoardStockQuote) => darkFields.includes(sort.key) ? stock.dark_data_available
    : quoteFields.includes(sort.key) ? stock.quote_available : true
  if (available(left) !== available(right)) return available(left) ? -1 : 1
  const a = left[sort.key]
  const b = right[sort.key]
  const compared = typeof a === 'string' && typeof b === 'string' ? a.localeCompare(b, 'zh-CN') : Number(a) - Number(b)
  return (sort.direction === 'asc' ? compared : -compared)
    || (left.source_order ?? 0) - (right.source_order ?? 0) || left.stock_code.localeCompare(right.stock_code)
}
