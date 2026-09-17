import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('../src/constituents.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const { showConstituentDarkData, constituentSort, compareConstituents } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)
const row = (extra = {}) => ({ stock_code: '000001', stock_name: '平安银行', source_order: 1,
  dark_data_available: true, dark_rank: 2, dark_money: 0, main_money_inflow: 0, dark_activity: 0,
  quote_available: true, latest_price: 10, change_pct: 0, turnover: 0, ...extra })

test('pre-open, open and lunch never show post-close money, including an old archived snapshot', () => {
  for (const market_status of ['pre_open', 'open', 'lunch_break']) {
    assert.equal(showConstituentDarkData([row()], { as_of: '2026-09-14', dark_data_available: true }, { market_status }), false)
  }
  assert.equal(showConstituentDarkData([row()], { dark_data_available: true }), false)
})

test('closed sessions expose money columns only after real money rows have been collected', () => {
  const closed = { market_status: 'closed' }
  assert.equal(showConstituentDarkData([row()], { dark_data_available: true }, closed), true, 'collected zeroes are valid data')
  assert.equal(showConstituentDarkData([row()], { dark_data_available: false }, closed), false)
  assert.equal(showConstituentDarkData([row()], undefined, closed), false)
  assert.equal(showConstituentDarkData([], { dark_data_available: true }, closed), false)
  assert.equal(showConstituentDarkData([row({ dark_data_available: false })], { dark_data_available: true }, closed), false)
  assert.equal(showConstituentDarkData([row({ dark_data_available: false }), row()], { dark_data_available: true }, closed), true)
})

test('hidden money columns never control the intraday sort; a quote sort survives mode changes', () => {
  const money = { key: 'dark_rank', direction: 'asc' }
  assert.deepEqual(constituentSort(money, false), { key: 'source_order', direction: 'asc' })
  assert.equal(constituentSort(money, true), money)
  for (const key of ['latest_price', 'change_pct', 'turnover', 'stock_name', 'stock_code']) {
    const sort = { key, direction: 'desc' }
    assert.equal(constituentSort(sort, false), sort)
    assert.equal(constituentSort(sort, true), sort)
  }
})

test('quote and money sorts keep unavailable rows last and retain real zero/negative values', () => {
  for (const key of ['latest_price', 'change_pct', 'turnover', 'dark_rank', 'dark_money', 'main_money_inflow', 'dark_activity']) {
    const rows = [row({ stock_code: 'missing', quote_available: false, dark_data_available: false }),
      row({ stock_code: 'negative', [key]: -1 }), row({ stock_code: 'zero', [key]: 0 }), row({ stock_code: 'positive', [key]: 1 })]
    for (const direction of ['asc', 'desc']) {
      const result = rows.toSorted((a, b) => compareConstituents(a, b, { key, direction }))
      assert.deepEqual(result.map((stock) => stock.stock_code), direction === 'asc'
        ? ['negative', 'zero', 'positive', 'missing'] : ['positive', 'zero', 'negative', 'missing'], key)
    }
    assert.equal(rows[0].stock_code, 'missing', 'sorting does not mutate cached rows')
  }
})
