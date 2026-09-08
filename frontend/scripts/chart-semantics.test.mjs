import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import postcss from 'postcss'

const modules = new Map()
function loadSource(name) {
  if (modules.has(name)) return modules.get(name)
  const source = readFileSync(new URL(`../src/${name}.ts`, import.meta.url), 'utf8')
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText
  const exports = {}
  // Only renderer registration is stubbed. These tests execute the production
  // value/option builders; browser checks cover actual ECharts rendering.
  const require = (specifier) => specifier.startsWith('echarts/') ? { use() {} } : loadSource(specifier.replace(/^\.\//, ''))
  new Function('require', 'exports', compiled)(require, exports)
  modules.set(name, exports)
  return exports
}

const { metricAvailable, metricUnit, chartMetricValue } = loadSource('chartMetrics')
const { continuousMetricValues } = loadSource('continuousSeries')
const { buildLineChartOption } = loadSource('chartRuntime')
const moneyMetrics = ['dark_money', 'regular_money', 'main_money_inflow']
const quoteMetrics = ['dark_activity', 'dark_inflow_ratio', 'change_pct', 'up_count']
const allMetrics = [...moneyMetrics, ...quoteMetrics, 'rank']
const record = (overrides = {}) => ({
  trade_date: '2026-09-07', rank_type: 'industry', rank: 2, money_available: true, quote_available: false,
  source_version: 101, dark_money: 200000000, regular_money: 100000000, main_money_inflow: 300000000,
  dark_activity: 0, dark_inflow_ratio: 0, change_pct: 0, up_count: 0,
  ...overrides,
})
const points = (records) => records.map((item, index) => ({ label: `09:${String(35 + index * 5).padStart(2, '0')}`, record: item }))
const chartOption = (metric, secondaryMetric, records) => {
  const timeline = points(records)
  return buildLineChartOption({
    metric, secondaryMetric, points: timeline,
    primaryValues: continuousMetricValues(timeline, metric, chartMetricValue),
    secondaryValues: secondaryMetric === 'none' ? [] : continuousMetricValues(timeline, secondaryMetric, chartMetricValue),
  })
}

test('money-only backfill and archive points do not invent quote indicators', () => {
  for (const source_version of [100, 0, undefined]) {
    const item = record({ source_version })
    for (const metric of quoteMetrics) {
      assert.equal(metricAvailable(item, metric), false, `${source_version}: ${metric}`)
      assert.equal(chartMetricValue(item, metric), null)
    }
    for (const metric of [...moneyMetrics, 'rank']) assert.equal(metricAvailable(item, metric), true)
  }
})

test('full darktrade metrics remain available without enriched quotes, including true zeroes', () => {
  const item = record()
  for (const metric of allMetrics) assert.equal(metricAvailable(item, metric), true, metric)
  for (const metric of quoteMetrics) assert.equal(chartMetricValue(item, metric), 0)
  assert.equal(chartMetricValue(record({ change_pct: -0.015 }), 'change_pct'), -1.5)
})

test('quote-only records, stock counts, missing money, ranks and nonfinite values are distinguished', () => {
  const item = record({ source_version: 0, quote_available: true, money_available: false, rank: 0 })
  assert.equal(metricAvailable(item, 'change_pct'), true)
  for (const metric of [...moneyMetrics, 'rank', 'dark_activity', 'dark_inflow_ratio', 'up_count']) {
    assert.equal(metricAvailable(item, metric), false, metric)
  }
  assert.equal(metricAvailable(record({ rank_type: 'stock' }), 'up_count'), false)
  for (const metric of allMetrics) {
    for (const value of [NaN, Infinity, -Infinity, undefined, null]) {
      assert.equal(chartMetricValue(record({ [metric]: value }), metric), null)
    }
  }
})

test('curves preserve missing points and tooltips never coerce null to zero', () => {
  const option = chartOption('change_pct', 'rank', [record({ source_version: 100 }), record()])
  assert.deepEqual(option.series[0].data, [null, 0])
  assert.equal(option.series[0].connectNulls, false)
  const missingTooltip = option.tooltip.formatter([{ dataIndex: 0, axisValue: '09:35', value: null }])
  assert.match(missingTooltip, /涨跌幅.*数据不可用/)
  assert.doesNotMatch(missingTooltip, /0\.00%/)
  assert.match(missingTooltip, /榜单排名.*2/)
  assert.match(option.tooltip.formatter([{ dataIndex: 1 }]), /0\.00%/)
})

test('multi-day money tooltips use cumulative plotted values and daily raw values', () => {
  const option = chartOption('dark_money', 'none', [
    record({ dark_money: 10 }), record({ trade_date: '2026-09-08', dark_money: 5 }),
  ])
  assert.deepEqual(option.series[0].data, [10, 15])
  assert.match(option.tooltip.formatter([{ dataIndex: 1 }]), /15.*当日 5/)
})

test('only identical physical units share an axis for every metric pairing', () => {
  for (const primary of allMetrics) {
    for (const secondary of allMetrics) {
      const option = chartOption(primary, secondary, [record()])
      const shared = metricUnit(primary) === metricUnit(secondary)
      assert.equal(option.yAxis.length, shared ? 1 : 2, `${primary} + ${secondary}`)
      assert.equal(option.series[1].yAxisIndex, shared ? 0 : 1)
      assert.equal(option.yAxis[0].minInterval, ['rank', 'up_count'].includes(primary) ? 1 : undefined)
      if (!shared) assert.equal(option.yAxis[1].minInterval, ['rank', 'up_count'].includes(secondary) ? 1 : undefined)
      if (!shared) assert.notEqual(option.yAxis[0].name, option.yAxis[1].name)
    }
  }
  assert.equal(chartOption('dark_money', 'rank', [record()]).yAxis[1].axisLabel.formatter(3), '3')
  assert.equal(chartOption('dark_money', 'none', [record()]).yAxis.length, 1)
})

test('time labels use width-aware automatic spacing with overlap suppression', () => {
  const option = chartOption('dark_money', 'rank', [record()])
  assert.equal(option.xAxis.axisLabel.interval, 'auto')
  assert.equal(option.xAxis.axisLabel.hideOverlap, true)
  assert.equal(option.tooltip.confine, true)
})

test('tooltip labels are escaped even when no record is available', () => {
  const option = buildLineChartOption({
    metric: 'dark_money', secondaryMetric: 'none',
    points: [{ label: '<img src=x onerror=alert(1)>', record: null }],
    primaryValues: [null], secondaryValues: [],
  })
  const tooltip = option.tooltip.formatter([{ dataIndex: 0 }])
  assert.match(tooltip, /&lt;img/)
  assert.doesNotMatch(tooltip, /<img/)
})

test('mobile hidden columns are semantic and never include board names', () => {
  const css = postcss.parse(readFileSync(new URL('../src/styles/global.css', import.meta.url), 'utf8'))
  const hidden = []
  css.walkAtRules('media', (media) => {
    if (!media.params.includes('720px')) return
    media.walkRules((rule) => {
      if (!rule.selector.includes('.rank-table')) return
      rule.walkDecls('display', (declaration) => {
        if (declaration.value === 'none') hidden.push(...rule.selectors)
      })
    })
  })
  assert.deepEqual(hidden.sort(), ['.rank-table .rank-code-column', '.rank-table .rank-main-money-column'])
  const app = ts.createSourceFile('App.tsx', readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8'), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const columns = new Set()
  function visit(node) {
    if (ts.isJsxAttribute(node) && node.name.getText(app) === 'className' && node.initializer && ts.isStringLiteral(node.initializer)) {
      if (node.initializer.text.includes('rank-name-column')) columns.add(node.parent.parent.tagName.getText(app))
    }
    ts.forEachChild(node, visit)
  }
  visit(app)
  assert.deepEqual([...columns].sort(), ['SortHead', 'td'])
})
