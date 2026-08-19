import type { RankRecord } from './api/types'

export type CumulativeMetric = 'dark_money' | 'regular_money' | 'main_money_inflow'
export type ChartMetric = CumulativeMetric | 'dark_activity' | 'dark_inflow_ratio' | 'change_pct' | 'rank' | 'up_count'
export type TimelinePoint = { label: string; record: RankRecord | null }

const cumulativeMetrics = new Set<string>(['dark_money', 'regular_money', 'main_money_inflow'])

export function isCumulativeMetric(metric: string): metric is CumulativeMetric {
  return cumulativeMetrics.has(metric)
}

export function continuousMetricValues(
  points: TimelinePoint[],
  metric: ChartMetric,
  valueFor: (record: RankRecord, metric: ChartMetric) => number | null,
): Array<number | null> {
  if (!isCumulativeMetric(metric) || new Set(points.flatMap((point) => point.record ? [point.record.trade_date] : [])).size <= 1) {
    return points.map((point) => point.record ? valueFor(point.record, metric) : null)
  }

  let activeDate = ''
  let dayOffset = 0
  let lastAdjusted: number | null = null
  return points.map((point) => {
    if (!point.record) return null
    if (point.record.trade_date !== activeDate) {
      if (activeDate && lastAdjusted !== null) dayOffset = lastAdjusted
      activeDate = point.record.trade_date
    }
    const value = valueFor(point.record, metric)
    if (value === null) return null
    const adjusted = dayOffset + value
    lastAdjusted = adjusted
    return adjusted
  })
}
