import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { RankRecord } from './api/types'

echarts.use([LineChart, GridComponent, TooltipComponent, CanvasRenderer])

type Metric = 'dark_money' | 'regular_money' | 'main_money_inflow' | 'dark_activity' | 'dark_inflow_ratio' | 'change_pct' | 'rank' | 'up_count'
type TooltipPoint = { axisValue: string; seriesName: string; value: number; marker: string; dataIndex: number }
type TimelinePoint = { label: string; record: RankRecord | null }

const METRIC_LABELS: Record<Metric, string> = {
  dark_money: '暗盘资金估算', regular_money: '明盘资金', main_money_inflow: '主力净流入',
  dark_activity: '暗盘活跃度', dark_inflow_ratio: '暗盘流入占比', change_pct: '涨跌幅', rank: '榜单排名', up_count: '上涨家数',
}

function formatNumber(value: number, digits = 0) {
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: digits, minimumFractionDigits: digits }).format(value)
}

function formatMoney(value: number) {
  const abs = Math.abs(value)
  const unit = abs >= 100000000 ? '亿' : abs >= 10000 ? '万' : ''
  const divisor = unit === '亿' ? 100000000 : unit === '万' ? 10000 : 1
  return `${value < 0 ? '-' : ''}${formatNumber(abs / divisor, unit ? 2 : 0)}${unit}`
}

function metricDisplay(record: RankRecord, metric: Metric) {
  const value = record[metric]
  if (metric === 'dark_money' || metric === 'regular_money' || metric === 'main_money_inflow') return formatMoney(value)
  if (metric === 'change_pct' || metric === 'dark_inflow_ratio' || metric === 'dark_activity') return `${formatNumber(value * 100, 2)}%`
  return formatNumber(value)
}

function formatCompact(value: number, metric: Metric) {
  if (['dark_money', 'regular_money', 'main_money_inflow'].includes(metric)) return value >= 100000000 ? `${(value / 100000000).toFixed(1)}亿` : value >= 10000 ? `${(value / 10000).toFixed(0)}万` : `${Math.round(value)}`
  return ['change_pct', 'dark_inflow_ratio', 'dark_activity'].includes(metric) ? `${formatNumber(value, 1)}%` : formatNumber(value)
}

export function createLineChart(element: HTMLDivElement, options: {
  points: TimelinePoint[]
  primaryValues: Array<number | null>
  secondaryValues: Array<number | null>
  metric: Metric
  secondaryMetric: Metric | 'none'
  sameUnit: boolean
}) {
  const { points, primaryValues, secondaryValues, metric, secondaryMetric, sameUnit } = options
  const chart = echarts.init(element)
  chart.setOption({ animation: false, grid: { left: 48, right: secondaryMetric === 'none' ? 20 : 48, top: 20, bottom: 42 }, tooltip: { trigger: 'axis', formatter: (raw: unknown) => { const params = (Array.isArray(raw) ? raw : [raw]) as TooltipPoint[]; const point = points[params[0]?.dataIndex ?? 0]; if (!point?.record) return `<strong>${params[0]?.axisValue ?? ''}</strong><br/>缺少采集点`; return `<strong>${params[0]?.axisValue ?? ''}</strong><br/>${params.map((item) => `${item.marker}${item.seriesName}: ${metricDisplay(point.record!, item.seriesName === METRIC_LABELS[metric] ? metric : secondaryMetric as Metric)}`).join('<br/>')}` } }, xAxis: { type: 'category', boundaryGap: false, data: points.map((item) => item.label), axisLabel: { color: '#8a929e', interval: Math.max(0, Math.floor(points.length / 8) - 1) }, axisLine: { lineStyle: { color: '#dfe4ea' } } }, yAxis: [{ type: 'value', name: METRIC_LABELS[metric], scale: true, axisLabel: { color: '#8a929e', formatter: (value: number) => ['change_pct', 'dark_inflow_ratio', 'dark_activity'].includes(metric) ? `${value}%` : formatCompact(value, metric) }, splitLine: { lineStyle: { color: '#edf0f3' } } }, ...(secondaryMetric !== 'none' && !sameUnit ? [{ type: 'value', name: METRIC_LABELS[secondaryMetric], scale: true, position: 'right', axisLabel: { color: '#8a929e', formatter: (value: number) => formatCompact(value, secondaryMetric) }, splitLine: { show: false } }] : [])], series: [{ name: METRIC_LABELS[metric], type: 'line', connectNulls: false, smooth: 0.22, showSymbol: false, lineStyle: { width: 2.5, color: '#1d6ee8' }, itemStyle: { color: '#1d6ee8' }, areaStyle: { color: 'rgba(29,110,232,.08)' }, data: primaryValues }, ...(secondaryMetric !== 'none' ? [{ name: METRIC_LABELS[secondaryMetric], type: 'line', connectNulls: false, yAxisIndex: sameUnit ? 0 : 1, smooth: 0.22, showSymbol: false, lineStyle: { width: 2, color: '#e07a31' }, itemStyle: { color: '#e07a31' }, data: secondaryValues }] : [])] })
  const resize = () => chart.resize()
  const observer = new ResizeObserver(resize)
  observer.observe(element)
  window.addEventListener('resize', resize)
  requestAnimationFrame(resize)
  return () => { observer.disconnect(); window.removeEventListener('resize', resize); chart.dispose() }
}
