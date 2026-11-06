import assert from 'node:assert/strict'
import test, { after } from 'node:test'
import { compileModules } from './support/compile.mjs'

const { urls, cleanup } = await compileModules([
  new URL('../src/pinyinSearch.ts', import.meta.url),
  new URL('../src/limitupPresentation.ts', import.meta.url),
])
after(cleanup)

const {
  getShanghaiDateString,
  formatPercent,
  formatPercentPoint,
  formatNumber,
  formatOpenCountDisplay,
  formatPoolStatus,
  formatEventRowKey,
  formatTimeText,
  filterStockCandidates,
  sortStockCandidates,
  calculateGraphCoordinates,
  paginateRecords,
} = await import(urls.limitupPresentation)

test('getShanghaiDateString formats to YYYY-MM-DD in Asia/Shanghai timezone', () => {
  const fixedUtcDate = new Date('2026-11-05T20:30:00Z')
  assert.equal(getShanghaiDateString(fixedUtcDate), '2026-11-06')
  const defaultCurrentDate = getShanghaiDateString()
  assert.match(defaultCurrentDate, /^\d{4}-\d{2}-\d{2}$/)
})

test('formatPercent and formatNumber handle finite numbers and missing values cleanly', () => {
  assert.equal(formatPercent(0.8523), '85.2%')
  assert.equal(formatPercent(0), '0.0%')
  assert.equal(formatPercent(null), '--')
  assert.equal(formatPercent(undefined), '--')
  assert.equal(formatPercent(Number.NaN), '--')

  assert.equal(formatNumber(1250, 0), '1,250')
  assert.equal(formatNumber(3.14159, 2), '3.14')
  assert.equal(formatNumber(null), '--')
  assert.equal(formatNumber(undefined), '--')
  assert.equal(formatNumber(Number.NaN), '--')
})

test('formatPercentPoint displays source percentage points without rescaling', () => {
  assert.equal(formatPercentPoint(10), '10.00%')
  assert.equal(formatPercentPoint(-10), '-10.00%')
  assert.equal(formatPercentPoint(0), '0.00%')
  assert.equal(formatPercentPoint(null), '--')
  assert.equal(formatPercentPoint(Number.NaN), '--')
})

test('formatOpenCountDisplay distinguishes zero, positive counts and unknown nulls', () => {
  assert.equal(formatOpenCountDisplay(0), '0次')
  assert.equal(formatOpenCountDisplay(2), '2次')
  assert.equal(formatOpenCountDisplay(null), '未知')
  assert.equal(formatOpenCountDisplay(undefined), '未知')
})

test('formatPoolStatus labels all three pools and keeps distinct CSS classes', () => {
  assert.deepEqual(formatPoolStatus('limit_up'), { label: '涨停', className: 'seal' })
  assert.deepEqual(formatPoolStatus('open_limit'), { label: '炸板', className: 'broken' })
  assert.deepEqual(formatPoolStatus('limit_down'), { label: '跌停', className: 'down' })
  assert.deepEqual(formatPoolStatus('unknown_pool'), { label: 'unknown_pool', className: 'default' })
})

test('formatEventRowKey keeps two same-day pool rows unique', () => {
  const openLimitKey = formatEventRowKey({ trade_date: '2026-11-05', pool: 'open_limit' })
  const limitDownKey = formatEventRowKey({ trade_date: '2026-11-05', pool: 'limit_down' })
  const limitUpKey = formatEventRowKey({ trade_date: '2026-11-05', pool: 'limit_up' })

  assert.equal(openLimitKey, '2026-11-05:open_limit')
  assert.equal(limitDownKey, '2026-11-05:limit_down')
  assert.equal(limitUpKey, '2026-11-05:limit_up')
  assert.equal(new Set([openLimitKey, limitDownKey, limitUpKey]).size, 3)
})

test('formatTimeText handles timestamps, standard time strings and invalid inputs', () => {
  assert.equal(formatTimeText('09:32:15'), '09:32:15')
  assert.equal(formatTimeText('09:30'), '09:30')
  assert.equal(formatTimeText(null), '--')
  assert.equal(formatTimeText(''), '--')
  const epochSeconds = String(Math.floor(new Date('2026-11-06T01:35:00Z').getTime() / 1000))
  assert.equal(formatTimeText(epochSeconds), '09:35:00')
})

test('filterStockCandidates supports code, name and pinyin matching', () => {
  const stockSamples = [
    { code: '000001', name: '平安银行', market_type: 'HS', touch_days: 5, seal_days: 4, broken_days: 1, down_days: 0, last_seen: '2026-11-05' },
    { code: '300750', name: '宁德时代', market_type: 'GEM2STAR', touch_days: 6, seal_days: 5, broken_days: 1, down_days: 0, last_seen: '2026-11-05' },
    { code: '600519', name: '贵州茅台', market_type: 'HS', touch_days: 2, seal_days: 2, broken_days: 0, down_days: 1, last_seen: '2026-11-04' },
  ]

  assert.equal(filterStockCandidates(stockSamples, '').length, 3)
  assert.equal(filterStockCandidates(stockSamples, '300750')[0]?.name, '宁德时代')
  assert.equal(filterStockCandidates(stockSamples, '茅台')[0]?.code, '600519')
  assert.equal(filterStockCandidates(stockSamples, 'payh')[0]?.code, '000001')
  assert.equal(filterStockCandidates(stockSamples, 'ningde')[0]?.code, '300750')
  assert.equal(filterStockCandidates(stockSamples, '不存在').length, 0)
})

test('sortStockCandidates ranks by the selected event while retaining down-only stocks', () => {
  const stockSamples = [
    { code: '000001', name: '涨停常客', market_type: 'HS', touch_days: 7, seal_days: 6, broken_days: 1, down_days: 0, last_seen: '2026-11-05' },
    { code: '000002', name: '触板常客', market_type: 'HS', touch_days: 9, seal_days: 2, broken_days: 7, down_days: 1, last_seen: '2026-11-05' },
    { code: '000003', name: '跌停常客', market_type: 'HS', touch_days: 0, seal_days: 0, broken_days: 0, down_days: 5, last_seen: '2026-11-05' },
    { code: '000004', name: '同频标的', market_type: 'HS', touch_days: 2, seal_days: 2, broken_days: 0, down_days: 2, last_seen: '2026-11-05' },
  ]

  assert.deepEqual(sortStockCandidates(stockSamples, 'seal').map((candidate) => candidate.code), [
    '000001',
    '000002',
    '000004',
    '000003',
  ])
  assert.deepEqual(sortStockCandidates(stockSamples, 'touch').map((candidate) => candidate.code), [
    '000002',
    '000001',
    '000004',
    '000003',
  ])
  assert.deepEqual(sortStockCandidates(stockSamples, 'down').map((candidate) => candidate.code), [
    '000003',
    '000004',
    '000002',
    '000001',
  ])
  assert.equal(sortStockCandidates(stockSamples, 'down').length, stockSamples.length)
})

test('calculateGraphCoordinates computes circular layout with scaled edge strokes', () => {
  const nullLayout = calculateGraphCoordinates(null, [], 600, 400)
  assert.equal(nullLayout.centerNode, null)
  assert.equal(nullLayout.neighborNodes.length, 0)
  assert.equal(nullLayout.edges.length, 0)

  const centerStock = { code: '000001', name: '平安银行' }
  const mockLinks = [
    {
      code: '000002',
      name: '万科A',
      market_type: 'HS',
      anchor_days: 10,
      peer_days: 8,
      overlap_days: 6,
      jaccard: 0.5,
      conditional_probability: 0.6,
      reverse_probability: 0.75,
      baseline_probability: 0.08,
      lift: 7.5,
      phi: 0.62,
      heat_adjusted_overlap: 4.8,
      shared_dates: ['2026-10-10', '2026-10-15'],
      sample_sufficient: true,
    },
    {
      code: '600036',
      name: '招商银行',
      market_type: 'HS',
      anchor_days: 10,
      peer_days: 5,
      overlap_days: 3,
      jaccard: 0.25,
      conditional_probability: 0.3,
      reverse_probability: 0.6,
      baseline_probability: 0.05,
      lift: 6.0,
      phi: 0.41,
      heat_adjusted_overlap: 2.2,
      shared_dates: ['2026-10-12'],
      sample_sufficient: true,
    },
  ]

  const graphLayout = calculateGraphCoordinates(centerStock, mockLinks, 700, 500, 10)
  assert.equal(graphLayout.centerNode?.coordX, 350)
  assert.equal(graphLayout.centerNode?.coordY, 250)
  assert.equal(graphLayout.neighborNodes.length, 2)
  assert.equal(graphLayout.edges.length, 2)
  assert.ok(graphLayout.edges[0].strokeWidth > graphLayout.edges[1].strokeWidth)
})

test('paginateRecords cleanly clamps pages and handles boundaries', () => {
  const recordItems = ['item1', 'item2', 'item3', 'item4', 'item5']
  const emptyPage = paginateRecords([], 1, 10)
  assert.equal(emptyPage.totalPages, 1)
  assert.equal(emptyPage.currentPage, 1)
  assert.equal(emptyPage.paginatedItems.length, 0)

  const pageFirst = paginateRecords(recordItems, 1, 2)
  assert.equal(pageFirst.totalPages, 3)
  assert.equal(pageFirst.currentPage, 1)
  assert.deepEqual(pageFirst.paginatedItems, ['item1', 'item2'])

  const pageClamped = paginateRecords(recordItems, 99, 2)
  assert.equal(pageClamped.currentPage, 3)
  assert.deepEqual(pageClamped.paginatedItems, ['item5'])
})
