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
