import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { RankRecord } from './api/types'
import { isCumulativeMetric, type ChartMetric, type TimelinePoint } from './continuousSeries'
import { metricAvailable, metricUnit } from './chartMetrics'

echarts.use([LineChart, GridComponent, TooltipComponent, CanvasRenderer])

type Metric = ChartMetric
type TooltipPoint = { axisValue: string; dataIndex: number }

const METRIC_LABELS: Record<Metric, string> = {
  dark_money: '暗盘资金', regular_money: '明盘资金', main_money_inflow: '主力净流入（含暗盘）',
  dark_activity: '暗盘活跃度', dark_inflow_ratio: '暗盘流入家数比例', change_pct: '涨跌幅', rank: '榜单排名', up_count: '上涨家数',
}

function formatNumber(value: number, digits = 0) {
  if (!Number.isFinite(value)) return '--'
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: digits, minimumFractionDigits: digits }).format(value)
}

// The tooltip formatter is the only raw-HTML sink in the app. Everything
// interpolated today is app-generated (labels, metric names, numbers), but
// escape defensively so a future addition of upstream strings (board or
// stock names) cannot become a stored XSS.
function escapeHTML(value: string) {
  return value.replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char] ?? char))
}

function formatMoney(value: number) {
  const abs = Math.abs(value)
  const unit = abs >= 100000000 ? '亿' : abs >= 10000 ? '万' : ''
  const divisor = unit === '亿' ? 100000000 : unit === '万' ? 10000 : 1
  return `${value < 0 ? '-' : ''}${formatNumber(abs / divisor, unit ? 2 : 0)}${unit}`
}

function metricDisplay(record: RankRecord, metric: Metric) {
  if (!metricAvailable(record, metric)) return '数据不可用'
  const value = record[metric]
  if (metric === 'dark_money' || metric === 'regular_money' || metric === 'main_money_inflow') return formatMoney(value)
  if (metric === 'change_pct' || metric === 'dark_inflow_ratio' || metric === 'dark_activity') return `${formatNumber(value * 100, 2)}%`
  return formatNumber(value)
}

function metricDisplayValue(value: number, metric: Metric) {
  if (metric === 'dark_money' || metric === 'regular_money' || metric === 'main_money_inflow') return formatMoney(value)
  if (metric === 'change_pct' || metric === 'dark_inflow_ratio' || metric === 'dark_activity') return `${formatNumber(value, 2)}%`
  return formatNumber(value)
}

function formatCompact(value: number, metric: Metric) {
  if (['dark_money', 'regular_money', 'main_money_inflow'].includes(metric)) {
    const abs = Math.abs(value)
    const sign = value < 0 ? '-' : ''
    return abs >= 100000000 ? `${sign}${(abs / 100000000).toFixed(1)}亿` : abs >= 10000 ? `${sign}${(abs / 10000).toFixed(0)}万` : `${Math.round(value)}`
  }
  return ['change_pct', 'dark_inflow_ratio', 'dark_activity'].includes(metric) ? `${formatNumber(value, 1)}%` : formatNumber(value)
}

export type LineChartOptions = {
  points: TimelinePoint[]
  primaryValues: Array<number | null>
  secondaryValues: Array<number | null>
  metric: Metric
  secondaryMetric: Metric | 'none'
}

export type LineChartHandle = {
  update: (options: LineChartOptions) => void
  dispose: () => void
}

export function buildLineChartOption(options: LineChartOptions): echarts.EChartsCoreOption {
  const { points, primaryValues, secondaryValues, metric, secondaryMetric } = options
  const sameUnit = secondaryMetric !== 'none' && metricUnit(metric) === metricUnit(secondaryMetric)
  const dualAxis = secondaryMetric !== 'none' && !sameUnit
  const multiDay = new Set(points.flatMap((point) => point.record ? [point.record.trade_date] : [])).size > 1
  const plottedMetrics = [
    { metric, values: primaryValues, color: '#1d6ee8' },
    ...(secondaryMetric !== 'none' ? [{ metric: secondaryMetric, values: secondaryValues, color: '#e07a31' }] : []),
  ]
  const axisNames = { money: '金额', percent: '%', rank: '名次', count: '家数' }
  return {
    animation: false,
    grid: { left: 56, right: dualAxis ? 56 : 20, top: 26, bottom: 42 },
    tooltip: {
      trigger: 'axis', confine: true,
      formatter: (raw: unknown) => {
        const params = (Array.isArray(raw) ? raw : [raw]) as TooltipPoint[]
        const index = params[0]?.dataIndex ?? 0
        const point = points[index]
        const heading = `<strong>${escapeHTML(point?.label ?? params[0]?.axisValue ?? '')}</strong>`
        if (!point?.record) return `${heading}<br/>缺少采集点`
        return `${heading}<br/>${plottedMetrics.map(({ metric: selectedMetric, values, color }) => {
          const value = values[index]
          const available = value != null && Number.isFinite(value) && metricAvailable(point.record!, selectedMetric)
          const plotted = available ? metricDisplayValue(value, selectedMetric) : '数据不可用'
          const daily = available && multiDay && isCumulativeMetric(selectedMetric)
            ? ` <small>（当日 ${escapeHTML(metricDisplay(point.record!, selectedMetric))}）</small>` : ''
          return `<span style="color:${color}">${escapeHTML(METRIC_LABELS[selectedMetric])}</span>: ${escapeHTML(plotted)}${daily}`
        }).join('<br/>')}`
      },
    },
    xAxis: {
      type: 'category', boundaryGap: false, data: points.map((item) => item.label),
      axisLabel: { color: '#8a929e', interval: 'auto', hideOverlap: true },
      axisLine: { lineStyle: { color: '#dfe4ea' } },
    },
    yAxis: [
      {
        type: 'value', name: axisNames[metricUnit(metric)], scale: true,
        minInterval: metricUnit(metric) === 'rank' || metricUnit(metric) === 'count' ? 1 : undefined,
        axisLabel: { color: '#8a929e', formatter: (value: number) => formatCompact(value, metric) },
        splitLine: { lineStyle: { color: '#edf0f3' } },
      },
      ...(dualAxis ? [{
        type: 'value', name: axisNames[metricUnit(secondaryMetric)], scale: true, position: 'right',
        minInterval: metricUnit(secondaryMetric) === 'rank' || metricUnit(secondaryMetric) === 'count' ? 1 : undefined,
        axisLabel: { color: '#8a929e', formatter: (value: number) => formatCompact(value, secondaryMetric) },
        splitLine: { show: false },
      }] : []),
    ],
    series: plottedMetrics.map(({ metric: selectedMetric, values, color }, index) => ({
      name: METRIC_LABELS[selectedMetric], type: 'line', connectNulls: false, smooth: 0.22, showSymbol: false,
      yAxisIndex: index === 1 && dualAxis ? 1 : 0,
      lineStyle: { width: index === 0 ? 2.5 : 2, color }, itemStyle: { color },
      ...(index === 0 ? { areaStyle: { color: 'rgba(29,110,232,.08)' } } : {}),
      data: values,
    })),
  }
}

export function createLineChart(element: HTMLDivElement, options: LineChartOptions): LineChartHandle {
  const chart = echarts.init(element)
  chart.setOption(buildLineChartOption(options), { notMerge: true })
  const resize = () => chart.resize()
  const observer = new ResizeObserver(resize)
  observer.observe(element)
  window.addEventListener('resize', resize)
  const raf = requestAnimationFrame(resize)
  return {
    // Updating in place preserves the instance and its canvas: disposing and
    // re-initializing on every poll made the chart flash and reset tooltip
    // state once a minute.
    update: (next) => chart.setOption(buildLineChartOption(next), { notMerge: true }),
    dispose: () => {
      cancelAnimationFrame(raf)
      observer.disconnect()
      window.removeEventListener('resize', resize)
      chart.dispose()
    },
  }
}
