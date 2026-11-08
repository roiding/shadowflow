import assert from 'node:assert/strict'
import test, { after } from 'node:test'
import { compileModules } from './support/compile.mjs'

const { urls, cleanup } = await compileModules([
  new URL('../src/api/thsfocus.ts', import.meta.url),
])
after(cleanup)

const {
  formatThsFocusPercent,
  hasThsFocusMarketValues,
  isThsFocusScope,
  isThsFocusTradeDate,
  selectedThsFocusBlocks,
  selectedThsFocusLadder,
  sortedThsFocusLadder,
  thsFocusCoverageNotice,
  thsFocusDateVerificationText,
  thsFocusDisplayLimitations,
  thsFocusLadderMismatch,
  thsFocusMarketScopeLabel,
  thsFocusMarketSummary,
  thsFocusPayload,
  thsFocusQueryEnabled,
  thsFocusQueryKey,
  thsFocusReadableLimitation,
  thsFocusResponseMatches,
  thsFocusScopeLabel,
  thsFocusSourceLimitation,
} = await import(urls.thsfocus)

const stock = (code, name, continueNum, reason = '', changePercent = 10.0198) => ({
  code,
  name,
  market_id: 33,
  continue_num: continueNum,
  reason,
  reason_detail: `${name}的源说明`,
  high_days: '6天5板',
  change_percent: changePercent,
})

const snapshot = {
  trade_date: '2026-11-06',
  source: 'ths_focus',
  scope: 'HS,GEM2STAR',
  market_scope: 'source_default',
  fetched_at: '2026-11-06T15:36:00+08:00',
  source_date_verified: false,
  limitations: [
    'block_top仅提供Top20板块列表，不代表市场全部板块或完整成份。',
    'market_state与two_days_compare固定采用默认市场source_default口径。',
  ],
  blocks: [
    {
      code: '885386',
      name: '建筑节能',
      change: -1.0764,
      limit_up_count: 1,
      stocks: [stock('002088', '鲁阳节能', 4, '陶瓷纤维', null)],
    },
    {
      code: '885705',
      name: '乡村振兴',
      change: 0.9266,
      limit_up_count: 1,
      stocks: [stock('002088', '鲁阳节能', 4, '乡村建设')],
    },
    {
      code: '885001',
      name: '其它题材',
      change: null,
      limit_up_count: 1,
      stocks: [stock('600000', '浦发银行', 1)],
    },
  ],
  ladder: [
    { height: 2, stocks: [stock('002086', '东方海洋', 2)] },
    { height: 5, stocks: [stock('002578', '闽发铝业', 5), stock('601599', '浙文影业', 4)] },
    { height: 4, stocks: [stock('002088', '鲁阳节能', 4)] },
  ],
  market: {
    rise_fall: {
      rise: 758,
      fall: 4741,
      deuce: 74,
      limit_up: 33,
      limit_down: 15,
    },
  },
  comparison: {
    today_date: '2026-11-06',
    yesterday_date: '2026-11-05',
    today_max_board: 5,
    yesterday_max_board: 4,
  },
}

test('trade dates and scopes are validated before enabling the request', () => {
  assert.equal(isThsFocusTradeDate('2026-11-06'), true)
  assert.equal(isThsFocusTradeDate('2026-02-30'), false)
  assert.equal(isThsFocusTradeDate('20261106'), false)
  assert.equal(isThsFocusScope('HS,GEM2STAR'), true)
  assert.equal(isThsFocusScope('HS,GEM2STAR,ST,NEW'), true)
  assert.equal(isThsFocusScope('HS'), false)
  assert.equal(thsFocusScopeLabel('HS,GEM2STAR'), '沪深及双创')
  assert.equal(thsFocusScopeLabel('HS,GEM2STAR,ST,NEW'), '含 ST、未开板新股')
  assert.equal(thsFocusMarketScopeLabel('source_default'), '源默认市场口径（独立于当前范围）')
  assert.equal(thsFocusMarketScopeLabel(''), '未提供')

  assert.deepEqual(
    thsFocusQueryKey('2026-11-06', 'HS,GEM2STAR'),
    ['limit-up-focus', '2026-11-06', 'HS,GEM2STAR'],
  )
  assert.equal(thsFocusQueryEnabled({ tradeDate: '2026-11-06', scope: 'HS,GEM2STAR', authRequired: false }), true)
  assert.equal(thsFocusQueryEnabled({ tradeDate: '', scope: 'HS,GEM2STAR', authRequired: false }), false)
  assert.equal(thsFocusQueryEnabled({ tradeDate: '2026-11-06', scope: '', authRequired: false }), false)
  assert.equal(thsFocusQueryEnabled({ tradeDate: '2026-11-06', scope: 'HS,GEM2STAR', authRequired: true }), false)
})

test('API envelope is unwrapped and a nullable snapshot stays absent', () => {
  const notCollected = {
    trade_date: '2026-11-06',
    scope: 'HS,GEM2STAR',
    status: 'not_collected',
    snapshot: null,
  }
  assert.deepEqual(thsFocusPayload({ data: notCollected }), notCollected)
  assert.equal(thsFocusPayload(notCollected), undefined)
  assert.equal(thsFocusPayload({ data: undefined }), undefined)
  assert.equal(thsFocusPayload(null), undefined)
})

test('response dates and scopes must match the requested isolation', () => {
  const completePayload = {
    trade_date: '2026-11-06',
    scope: 'HS,GEM2STAR',
    status: 'complete',
    snapshot,
  }
  assert.equal(thsFocusResponseMatches(completePayload, '2026-11-06', 'HS,GEM2STAR'), true)
  assert.equal(thsFocusResponseMatches(completePayload, '2026-11-06', 'HS,GEM2STAR,ST,NEW'), false)
  assert.equal(thsFocusResponseMatches({
    ...completePayload,
    snapshot: { ...snapshot, market_scope: 'requested_scope' },
  }, '2026-11-06', 'HS,GEM2STAR'), false)
  assert.equal(thsFocusResponseMatches({
    ...completePayload,
    snapshot: { ...snapshot, limitations: 'not-an-array' },
  }, '2026-11-06', 'HS,GEM2STAR'), false)
  assert.equal(thsFocusResponseMatches({
    ...completePayload,
    snapshot: { ...snapshot, scope: 'HS,GEM2STAR,ST,NEW' },
  }, '2026-11-06', 'HS,GEM2STAR'), false)
  assert.equal(thsFocusResponseMatches({
    trade_date: '2026-11-06',
    scope: 'HS,GEM2STAR',
    status: 'complete',
  }, '2026-11-06', 'HS,GEM2STAR'), false)
  assert.equal(thsFocusResponseMatches({
    trade_date: '2026-11-06',
    scope: 'HS,GEM2STAR',
    status: 'complete',
    snapshot: null,
  }, '2026-11-06', 'HS,GEM2STAR'), false)
})

test('selected stock keeps every source block hit and its source order', () => {
  const hits = selectedThsFocusBlocks(snapshot, '002088')
  assert.deepEqual(hits.map((hit) => hit.block.code), ['885386', '885705'])
  assert.deepEqual(hits.map((hit) => hit.source_order), [1, 2])
  assert.equal(hits[0].stock.reason, '陶瓷纤维')
  assert.equal(hits[0].stock.change_percent, null)
  assert.equal(hits[0].block.change, -1.0764)
  assert.equal(selectedThsFocusBlocks(snapshot, '600000')[0].block.change, null)
  assert.equal(selectedThsFocusBlocks(snapshot, '999999').length, 0)
  assert.equal(selectedThsFocusBlocks(snapshot, '2088').length, 0)
})

test('ladder keeps source height and continue_num without deriving from high_days', () => {
  assert.deepEqual(sortedThsFocusLadder(snapshot).map((level) => level.height), [5, 4, 2])
  assert.equal(selectedThsFocusLadder(snapshot, '002088').level.height, 4)
  assert.equal(selectedThsFocusLadder(snapshot, '002088').stock.high_days, '6天5板')
  assert.equal(thsFocusLadderMismatch(5, 4), true)
  assert.equal(thsFocusLadderMismatch(4, 4), false)
  const mismatchedLevel = sortedThsFocusLadder(snapshot)[0]
  assert.equal(mismatchedLevel.height, 5)
  assert.equal(mismatchedLevel.stocks[1].continue_num, 4)
})

test('market summary reads source default market counts and comparison max boards', () => {
  const summary = thsFocusMarketSummary(snapshot.market, snapshot.comparison)
  assert.deepEqual(summary, {
    rise: 758,
    fall: 4741,
    flat: 74,
    limitUp: 33,
    limitDown: 15,
    todayMaxBoard: 5,
    yesterdayMaxBoard: 4,
    todayDate: '2026-11-06',
    yesterdayDate: '2026-11-05',
  })
  assert.equal(hasThsFocusMarketValues(summary), true)
  const missing = thsFocusMarketSummary({}, {})
  assert.equal(hasThsFocusMarketValues(missing), false)
  assert.deepEqual(missing, {
    rise: undefined,
    fall: undefined,
    flat: undefined,
    limitUp: undefined,
    limitDown: undefined,
    todayMaxBoard: undefined,
    yesterdayMaxBoard: undefined,
    todayDate: undefined,
    yesterdayDate: undefined,
  })
  const stringValues = thsFocusMarketSummary(
    { data: { rise_fall: { rise: '1,234', fall: '4,321', deuce: '74', limit_up: '33', limit_down: '15' } } },
    { today_date: '2026-11-06', today_max_board: '5' },
  )
  assert.equal(stringValues.rise, 1234)
  assert.equal(stringValues.fall, 4321)
  assert.equal(stringValues.todayMaxBoard, 5)
  const unsafeValues = thsFocusMarketSummary(
    { rise_fall: { rise: '<b>999</b>', fall: { bad: true }, deuce: null } },
    { today_max_board: '<img src=x onerror=alert(1)>', yesterday_max_board: null },
  )
  assert.equal(unsafeValues.rise, undefined)
  assert.equal(unsafeValues.fall, undefined)
  assert.equal(unsafeValues.flat, undefined)
  assert.equal(unsafeValues.todayMaxBoard, undefined)
  assert.equal(unsafeValues.yesterdayMaxBoard, undefined)
})

test('formatting and copy preserve source scope and non-causal caveats', () => {
  assert.equal(formatThsFocusPercent(10.0198), '+10.02%')
  assert.equal(formatThsFocusPercent(-1.0764), '-1.08%')
  assert.equal(formatThsFocusPercent(null), '--')
  assert.equal(formatThsFocusPercent(undefined), '--')
  assert.match(thsFocusDateVerificationText(false), /未完整回显日期/)
  assert.match(thsFocusCoverageNotice(), /Top 题材最多 20 项/)
  assert.match(thsFocusCoverageNotice(), /完整成份/)
  assert.match(thsFocusCoverageNotice(), /不构成因果关系/)
  const limitation = thsFocusSourceLimitation('HS,GEM2STAR,ST,NEW')
  assert.match(limitation, /默认市场口径/)
  assert.match(limitation, /ST\/新股筛选控制/)
  assert.match(limitation, /不能与题材和梯队的范围等同/)
  assert.deepEqual(thsFocusDisplayLimitations(snapshot), [
    '题材列表仅提供Top20板块列表，不代表市场全部板块或完整成份。',
    '市场摘要与两日对比固定采用默认市场口径。',
  ])
  assert.deepEqual(
    thsFocusDisplayLimitations({ market_scope: 'unexpected', limitations: ['  ', '限制一'] }),
    ['未识别市场口径：unexpected', '限制一'],
  )
  assert.equal(
    thsFocusReadableLimitation('block_top与continuous_limit_up按scope隔离；market_state使用source_default。'),
    '题材列表与连板梯队按市场范围隔离；市场摘要使用源默认市场口径。',
  )
})
