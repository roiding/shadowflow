import assert from 'node:assert/strict'
import test, { after } from 'node:test'
import { compileModules } from './support/compile.mjs'

const { urls, cleanup } = await compileModules([
  new URL('../src/linkageNavigation.ts', import.meta.url),
])
after(cleanup)

const {
  createLinkageContext,
  readLinkageContext,
  writeLinkageContext,
  buildViewSearchParams,
  buildLinkageURL,
  applyLinkageURL,
  selectLinkageStock,
  resolveIntradayLinkageAsOf,
  resolveMonitorResearchAsOf,
  resolveHistoricalLinkageAsOf,
  sourceLinkageAsOf,
  quickTargetToContext,
  isNoEventError,
  isLimitUpUnavailableError,
  isLimitUpBusyError,
  linkageQueryErrorMessage,
  isHistoricalSourceDate,
} = await import(urls.linkageNavigation)

test('readLinkageContext preserves an external stock even when no local candidate exists', () => {
  const params = new URLSearchParams(
    'view=linkage&stock_code=600519&stock_name=%E8%B4%B5%E5%B7%9E%E8%8C%85%E5%8F%B0&as_of=2026-11-05&days=180&scope=HS%2CGEM2STAR%2CST%2CNEW&event=down&min_overlap=1',
  )
  const context = readLinkageContext(params)
  assert.deepEqual(context, {
    stockCode: '600519',
    stockName: '贵州茅台',
    asOf: '2026-11-05',
    days: 180,
    scope: 'HS,GEM2STAR,ST,NEW',
    event: 'down',
    minOverlap: 1,
  })
})

test('readLinkageContext rejects malformed parameters without discarding a valid stock', () => {
  const params = new URLSearchParams(
    'view=linkage&stock_code=000001&as_of=not-a-date&days=999&scope=bad&event=minute&min_overlap=99',
  )
  assert.deepEqual(readLinkageContext(params), {
    stockCode: '000001',
    stockName: '',
    asOf: '',
    days: 90,
    scope: 'HS,GEM2STAR',
    event: 'seal',
    minOverlap: 3,
  })
  assert.equal(readLinkageContext(new URLSearchParams('view=linkage&stock_code=000001&as_of=2026-02-30')).asOf, '')
  assert.equal(readLinkageContext(new URLSearchParams('stock_code=000001')), null)
})

test('writeLinkageContext round-trips all research dimensions and removes empty fields', () => {
  const context = createLinkageContext({
    stockCode: '300750',
    stockName: '宁德时代',
    asOf: '2026-10-30',
    days: 365,
    scope: 'HS,GEM2STAR',
    event: 'touch',
    minOverlap: 4,
  })
  const params = writeLinkageContext(new URLSearchParams('keep=1'), context)
  assert.equal(params.get('keep'), '1')
  assert.equal(params.get('view'), 'linkage')
  assert.equal(params.get('stock_code'), '300750')
  assert.equal(params.get('stock_name'), '宁德时代')
  assert.equal(params.get('days'), '365')
  assert.equal(params.get('event'), 'touch')
  assert.equal(params.get('min_overlap'), '4')

  const emptyContext = writeLinkageContext(params, createLinkageContext())
  assert.equal(emptyContext.get('stock_code'), null)
  assert.equal(emptyContext.get('stock_name'), null)
  assert.equal(emptyContext.get('as_of'), null)

  assert.equal(createLinkageContext({ days: 90.5 }).days, 90)
  assert.equal(createLinkageContext({ minOverlap: 3.5 }).minOverlap, 3)
})

test('buildViewSearchParams returns the full linkage context before navigation', () => {
  const context = createLinkageContext({
    stockCode: '300750',
    stockName: '宁德时代',
    asOf: '2026-10-30',
    days: 180,
    event: 'down',
    minOverlap: 2,
  })
  const linkageParams = buildViewSearchParams('linkage', context, 'monitor')
  assert.equal(linkageParams.get('view'), 'linkage')
  assert.equal(linkageParams.get('stock_code'), '300750')
  assert.equal(linkageParams.get('stock_name'), '宁德时代')
  assert.equal(linkageParams.get('as_of'), '2026-10-30')
  assert.equal(linkageParams.get('days'), '180')
  assert.equal(linkageParams.get('event'), 'down')
  assert.equal(linkageParams.get('min_overlap'), '2')
  assert.equal(linkageParams.get('source'), 'monitor')

  assert.equal(buildViewSearchParams('quality', context, 'monitor').toString(), 'view=quality')
})

test('buildLinkageURL keeps the source page marker for the explicit return path', () => {
  const context = quickTargetToContext(
    { code: '600036', name: '招商银行', asOf: '2026-11-04', source: 'monitor' },
    { event: 'seal', days: 90, minOverlap: 3 },
  )
  const url = buildLinkageURL(context, '/', 'focus')
  assert.match(url, /^\/\?source=focus&view=linkage/)
  assert.match(url, /stock_code=600036/)
  assert.match(url, /as_of=2026-11-04/)
  assert.match(url, /days=90/)
  assert.match(url, /event=seal/)
  assert.match(url, /min_overlap=3/)
})

test('applyLinkageURL preserves the current origin and updates only the research context', () => {
  const context = createLinkageContext({
    stockCode: '000001',
    stockName: '平安银行',
    asOf: '2026-11-05',
  })
  const nextURL = new URL(applyLinkageURL('http://localhost:5173/?view=linkage&stock_code=600519', context, 'stocks'))
  assert.equal(nextURL.origin, 'http://localhost:5173')
  assert.equal(nextURL.searchParams.get('view'), 'linkage')
  assert.equal(nextURL.searchParams.get('stock_code'), '000001')
  assert.equal(nextURL.searchParams.get('source'), 'stocks')
  assert.equal(nextURL.searchParams.get('as_of'), '2026-11-05')
})

test('selectLinkageStock rejects invalid codes and keeps the current center otherwise', () => {
  const context = createLinkageContext({
    stockCode: '000001',
    stockName: '平安银行',
    asOf: '2026-11-05',
  })
  assert.equal(selectLinkageStock(context, 'ABC001', '坏代码').stockCode, '000001')
  assert.deepEqual(selectLinkageStock(context, '600000', '浦发银行'), {
    ...context,
    stockCode: '600000',
    stockName: '浦发银行',
  })
})

test('resolveIntradayLinkageAsOf uses previous trading date and never subtracts one calendar day', () => {
  assert.equal(resolveIntradayLinkageAsOf({
    previousTradingDate: '2026-10-09',
    latestTradingDay: '2026-10-12',
    tradingDay: true,
  }), '2026-10-09')

  assert.equal(resolveIntradayLinkageAsOf({
    previousTradingDate: '2026-10-09',
    latestTradingDay: '2026-10-12',
    tradingDay: false,
  }), '2026-10-12')

  assert.equal(resolveIntradayLinkageAsOf({}), '')
})

test('monitor research date prefers the backend event watermark over stale rank metadata', () => {
  assert.equal(resolveMonitorResearchAsOf({
    event_research_as_of: '2026-11-05',
    latest_trading_day: '2026-11-06',
    trading_day: true,
  }, '2026-11-04'), '2026-11-05')

  assert.equal(resolveMonitorResearchAsOf({
    event_research_as_of: '2026-11-05',
    latest_trading_day: '2026-11-06',
    trading_day: false,
  }, '2026-11-04'), '2026-11-05')

  assert.equal(resolveMonitorResearchAsOf({
    latest_trading_day: '2026-11-06',
    trading_day: true,
  }, '2026-11-05'), '2026-11-05')

  assert.equal(resolveMonitorResearchAsOf({
    latest_trading_day: '2026-11-06',
    trading_day: false,
  }, '2026-11-05'), '2026-11-06')

  assert.equal(resolveMonitorResearchAsOf(undefined, '2026-11-05'), '')
})

test('historical and source dates stay on the selected date instead of moving to today', () => {
  assert.equal(resolveHistoricalLinkageAsOf('2026-09-30'), '2026-09-30')
  assert.equal(resolveHistoricalLinkageAsOf('bad-date'), '')
  assert.equal(sourceLinkageAsOf('focus', {
    monitorAsOf: '2026-11-05',
    focusAsOf: '2026-09-30',
    stockAsOf: '2026-09-29',
  }), '2026-09-30')
  assert.equal(isHistoricalSourceDate('2026-11-04', '2026-11-05'), true)
  assert.equal(isHistoricalSourceDate('2026-11-05', '2026-11-05'), false)
})

test('only explicit no-event messages become a friendly empty state', () => {
  class ApiRequestError extends Error {
    constructor(message, code) {
      super(message)
      this.code = code
    }
  }
  assert.equal(isNoEventError(new ApiRequestError('所选范围内没有该股票', 'limit_up_stock_not_found')), true)
  assert.equal(isNoEventError(new ApiRequestError('busy', 'limit_up_busy')), false)
  assert.equal(isNoEventError(new Error('所选范围内没有该股票的已完整采集事件')), true)
  assert.equal(isNoEventError(new Error('limit_up_stock_not_found')), true)
  assert.equal(isNoEventError(new Error('股票研究正在计算，请稍后重试')), false)
  assert.equal(isNoEventError(new Error('网络连接失败')), false)
  assert.equal(isLimitUpUnavailableError(new ApiRequestError('事件数据读取暂不可用', 'limit_up_unavailable')), true)
  assert.equal(isLimitUpUnavailableError(new Error('limit_up_unavailable')), true)
  assert.equal(isLimitUpBusyError(new ApiRequestError('股票研究正在计算', 'limit_up_busy')), true)
  assert.equal(isLimitUpBusyError(new Error('股票研究正在计算，请稍后重试')), true)
  assert.equal(isLimitUpBusyError(new Error('网络连接失败')), false)
  assert.equal(
    linkageQueryErrorMessage(new Error('所选范围内没有该股票的已完整采集事件')),
    '当前股票在所选范围和窗口内没有已归档事件。股票选择已保留，可调整截止日、窗口或共现事件后重试。',
  )
  assert.equal(
    linkageQueryErrorMessage(new ApiRequestError('事件数据读取暂不可用', 'limit_up_unavailable')),
    '事件归档读取暂不可用。请先到“采集质量”确认事件归档是否已启用或数据库是否完成初始化。',
  )
  assert.equal(
    linkageQueryErrorMessage(new ApiRequestError('股票研究正在计算', 'limit_up_busy')),
    '股票研究正在计算，请稍后重试；当前页面不会触发任何采集。',
  )
  assert.equal(linkageQueryErrorMessage(new Error('服务不可用')), '服务不可用')
})
