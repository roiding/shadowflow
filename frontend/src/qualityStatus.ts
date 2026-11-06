import type { DailyArchiveManifest, StockArchiveQuality } from './api/types'

function zeroExpectedArchiveIsComplete(stockQuality?: StockArchiveQuality, manifest?: DailyArchiveManifest) {
  return stockQuality?.expected_kline_stocks === 0 &&
    manifest?.trade_date === stockQuality.trade_date &&
    manifest.status === 'complete'
}

export function isStockKlineComplete(stockQuality?: StockArchiveQuality, manifest?: DailyArchiveManifest) {
  if (!stockQuality) return false
  if (stockQuality.expected_kline_stocks === 0) return zeroExpectedArchiveIsComplete(stockQuality, manifest)
  return stockQuality.kline_rows === stockQuality.expected_kline_stocks * stockQuality.expected_points
}

export function isStockDailyKlineComplete(stockQuality?: StockArchiveQuality, manifest?: DailyArchiveManifest) {
  if (!stockQuality) return false
  if (stockQuality.expected_kline_stocks === 0) return zeroExpectedArchiveIsComplete(stockQuality, manifest)
  return stockQuality.daily_kline_rows === stockQuality.expected_kline_stocks
}

export function collectionRunLabel(snapshotKind: string): string {
  if (snapshotKind === 'daily_close') return '日终'
  if (snapshotKind === 'minute_work') return '分钟'
  if (snapshotKind === 'research_5m') return '资金归档'
  if (snapshotKind === 'stock_kline_5m') return '行情归档'
  if (snapshotKind === 'limit_up_events') return '涨跌停事件'
  return snapshotKind
}
