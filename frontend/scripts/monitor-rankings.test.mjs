import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('../src/monitorRankings.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const { derivedBoardTurnover, controlRate, rankControlRates, compareControlRanks, compareMonitorRecords, controlRankChangeDisplay } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)

const record = (code, control, extra = {}) => ({
  code, name: code, rank_type: 'industry', rank: 1, money_available: true,
  dark_money: 100, dark_activity: 0.1, main_money_inflow: control * 10,
  regular_money: control * 10 - 100, turnover: 999999999,
  ...extra,
})

test('monitor uses inferred turnover and does not count dark money twice or scale twice', () => {
  const row = record('BK001', 35.68)
  assert.equal(derivedBoardTurnover(row), 1000)
  assert.ok(Math.abs(controlRate(row) - 35.68) < 1e-10)
  assert.equal(derivedBoardTurnover({ ...row, dark_money: -100 }), 1000)
  assert.equal(controlRate(record('BK002', -5)), -5)
  assert.equal(controlRate(record('BK003', 0)), 0)
})

test('unavailable and non-finite data never become a valid zero control rank', () => {
  for (const patch of [
    { dark_activity: 0 }, { dark_activity: -1 }, { dark_activity: null }, { dark_activity: undefined },
    { dark_activity: NaN }, { dark_activity: Infinity }, { dark_money: NaN }, { dark_money: 0 },
    { dark_money: undefined }, { dark_money: null }, { money_available: false },
    { money_available: undefined }, { dark_money: Number.MAX_VALUE, dark_activity: Number.MIN_VALUE },
  ]) {
    const row = record('BK001', 3, patch)
    assert.equal(derivedBoardTurnover(row), null)
    assert.equal(controlRate(row), null)
  }
  for (const main_money_inflow of [NaN, Infinity, -Infinity, undefined, null]) {
    assert.equal(controlRate(record('BK001', 3, { main_money_inflow })), null)
  }
})

test('daily change is yesterday control rank minus latest control rank, not source rank', () => {
  const previous = [record('A', 4), record('B', 3), record('C', 2), record('D', 1)]
  const current = [record('A', 3), record('B', 2), record('C', 1), record('D', 4)]
  const changes = compareControlRanks(current, previous, 'industry')
  assert.deepEqual(changes.get('D'), { currentRank: 1, previousRank: 4, delta: 3, state: 'compared' })
  assert.deepEqual(changes.get('A'), { currentRank: 2, previousRank: 1, delta: -1, state: 'compared' })
  assert.equal(controlRankChangeDisplay(changes.get('D'), '2026-09-04', '2026-09-03').label, '↑3')
  assert.equal(controlRankChangeDisplay(changes.get('A'), '2026-09-04', '2026-09-03').label, '↓1')
  assert.match(controlRankChangeDisplay(changes.get('D'), '2026-09-04', '2026-09-03').title, /2026-09-03 收盘第 4 名 → 2026-09-04 最新第 1 名/)
})

test('ranking isolates industry/concept universes and does not mutate source ordering', () => {
  const rows = [record('B', 1), record('A', 2), record('A', 100, { rank_type: 'concept' })]
  const before = structuredClone(rows)
  assert.deepEqual([...rankControlRates(rows, 'industry')], [['A', 1], ['B', 2]])
  assert.deepEqual([...rankControlRates(rows, 'concept')], [['A', 1]])
  assert.deepEqual(rows, before)
})

test('exact ties share competition rank and unrounded differences retain their order', () => {
  const rows = [record('B', 2), record('A', 2), record('C', 1)]
  assert.deepEqual([...rankControlRates(rows, 'industry')], [['A', 1], ['B', 1], ['C', 3]])
  const changes = compareControlRanks(rows.toReversed(), rows, 'industry')
  assert.equal(changes.get('A').delta, 0)
  assert.equal(changes.get('B').delta, 0)
  assert.equal(controlRankChangeDisplay(changes.get('A'), 'today', 'previous').label, '—')
  assert.equal(rankControlRates([record('A', 2.00001), record('B', 2.00002)], 'industry').get('B'), 1)
})

test('missing baseline, invalid baseline row, new entry and unchanged are distinct', () => {
  const current = [record('A', 3), record('B', 2), record('C', 1), record('D', 0, { dark_activity: 0 })]
  for (const previous of [undefined, [], [record('Z', 0, { dark_activity: 0 })]]) {
    const change = compareControlRanks(current, previous, 'industry').get('A')
    assert.equal(change.state, 'unavailable')
    assert.equal(controlRankChangeDisplay(change, 'today', 'previous').label, '--')
  }
  const changes = compareControlRanks(current, [record('A', 3), record('B', 2, { dark_activity: 0 })], 'industry')
  assert.equal(changes.get('A').state, 'compared')
  assert.equal(changes.get('B').state, 'unavailable')
  assert.equal(changes.get('C').state, 'new')
  assert.equal(changes.get('D').state, 'unavailable')
  assert.equal(controlRankChangeDisplay(changes.get('C'), 'today', 'previous').label, '新入榜')
  assert.equal(controlRankChangeDisplay(changes.get('A'), 'today', 'previous', true).label, '…')
})

test('numeric ascending/descending sorts always put unavailable values last, including negative values', () => {
  const rows = [record('missing', 2, { dark_activity: 0 }), record('positive', 2), record('negative', -2), record('zero', 0)]
  for (const direction of ['asc', 'desc']) {
    const sorted = rows.toSorted((a, b) => compareMonitorRecords(a, b, { key: 'control_rate', direction }, new Map()))
    assert.deepEqual(sorted.map((row) => row.code), direction === 'asc'
      ? ['negative', 'zero', 'positive', 'missing'] : ['positive', 'zero', 'negative', 'missing'])
  }
})

test('searching, paging and UI sorting do not recompute control rank within the visible subset', () => {
  const previous = Array.from({ length: 240 }, (_, i) => record(String(i).padStart(3, '0'), 240 - i))
  const current = previous.map((row, i) => ({ ...row, main_money_inflow: i === 239 ? 3000 : row.main_money_inflow }))
  const changes = compareControlRanks(current, previous, 'industry')
  assert.equal(changes.get('239').delta, 239)
  const filtered = current.filter((row) => row.code === '239')
  assert.equal(changes.get(filtered[0].code).currentRank, 1)
  const sorted = current.toSorted((a, b) => compareMonitorRecords(a, b, { key: 'control_rank_change', direction: 'desc' }, changes))
  assert.equal(sorted[0].code, '239')
  assert.equal(changes.get(sorted.slice(25, 50)[0].code).delta, -1)
  assert.equal(current[239].rank, 1)
})

test('change sorting treats a missing delta as unavailable rather than zero', () => {
  const rows = [record('new', 4), record('A', 1), record('B', 3)]
  const changes = compareControlRanks(rows, [record('A', 3), record('B', 1)], 'industry')
  for (const direction of ['asc', 'desc']) {
    const sorted = rows.toSorted((a, b) => compareMonitorRecords(a, b, { key: 'control_rank_change', direction }, changes))
    assert.equal(sorted.at(-1).code, 'new')
  }
})

test('current control rank sorts independently of the previous close and movement', () => {
  const rows = [record('missing', 0, { dark_activity: 0 }), record('A', 1), record('B', 3), record('new', 4)]
  for (const previous of [undefined, [record('A', 3), record('B', 1)]]) {
    const changes = compareControlRanks(rows, previous, 'industry')
    assert.equal(changes.get('new').currentRank, 1)
    for (const direction of ['asc', 'desc']) {
      const sorted = rows.toSorted((a, b) => compareMonitorRecords(a, b, { key: 'control_rank', direction }, changes))
      assert.deepEqual(sorted.map((row) => row.code), direction === 'asc'
        ? ['new', 'B', 'A', 'missing'] : ['A', 'B', 'new', 'missing'])
    }
  }
})

test('closing stock control uses daily turnover and guards unavailable money or denominator', () => {
  const row = record('000001', 30, { rank_type: 'stock', turnover: 2000, dark_activity: 0 })
  assert.equal(controlRate(row, row.turnover), 15)
  assert.equal(controlRate({ ...row, main_money_inflow: -300 }, row.turnover), -15)
  assert.equal(controlRate({ ...row, main_money_inflow: 0 }, row.turnover), 0)
  assert.equal(controlRate({ ...row, money_available: false }, row.turnover), null)
  for (const turnover of [0, -1, NaN, Infinity, null]) {
    assert.equal(controlRate(row, turnover), null)
  }
})
