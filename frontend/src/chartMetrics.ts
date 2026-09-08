import type { RankRecord } from './api/types'
import type { ChartMetric } from './continuousSeries'

type MetricUnit = 'money' | 'percent' | 'rank' | 'count'

const units: Record<ChartMetric, MetricUnit> = {
  dark_money: 'money', regular_money: 'money', main_money_inflow: 'money',
  dark_activity: 'percent', dark_inflow_ratio: 'percent', change_pct: 'percent',
  rank: 'rank', up_count: 'count',
}

export function metricUnit(metric: ChartMetric): MetricUnit {
  return units[metric]
}

export function metricAvailable(record: RankRecord, metric: ChartMetric): boolean {
  if (!Number.isFinite(record[metric])) return false
  if (metricUnit(metric) === 'money') return record.money_available
  if (metric === 'rank') return record.rank > 0
  // Source 101 is the full darktrade ranking, which includes these metrics
  // even without an enriched quote. Source 100 backfills and board_money_5m
  // (source 0) contain only money and computed ranks, not quote indicators.
  const rankedMetrics = record.source_version === 101 && record.rank > 0
  if (metric === 'change_pct') return record.quote_available || rankedMetrics
  if (metric === 'up_count') return record.rank_type !== 'stock' && rankedMetrics
  return rankedMetrics
}

export function chartMetricValue(record: RankRecord, metric: ChartMetric): number | null {
  if (!metricAvailable(record, metric)) return null
  return record[metric] * (metricUnit(metric) === 'percent' ? 100 : 1)
}
