import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('../src/qualityStatus.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const { isStockKlineComplete, isStockDailyKlineComplete } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)

const quality = (overrides = {}) => ({
  trade_date: '2026-09-08', expected_stocks: 0, expected_points: 48,
  expected_kline_stocks: 0, kline_stocks: 0, money_rows: 0, kline_rows: 0,
  daily_close_rows: 0, daily_kline_rows: 0, ...overrides,
})
const manifest = (overrides = {}) => ({
  trade_date: '2026-09-08', status: 'incomplete', ...overrides,
})

test('missing current-day quality cannot make zero-expected K-lines complete', () => {
  const current = quality()
  assert.equal(isStockKlineComplete(current, manifest()), false)
  assert.equal(isStockDailyKlineComplete(current, manifest()), false)
  assert.equal(isStockDailyKlineComplete(current, undefined), false)
})

test('zero-expected K-lines are complete only for a matching sealed manifest', () => {
  const current = quality()
  assert.equal(isStockKlineComplete(current, manifest({ status: 'complete' })), true)
  assert.equal(isStockDailyKlineComplete(current, manifest({ status: 'complete' })), true)
  assert.equal(isStockDailyKlineComplete(current, manifest({ trade_date: '2026-09-07', status: 'complete' })), false)
})

test('non-zero expectations still require their measured rows', () => {
  const current = quality({ expected_stocks: 2, expected_kline_stocks: 2, kline_rows: 96, daily_kline_rows: 2 })
  assert.equal(isStockKlineComplete(current, manifest()), true)
  assert.equal(isStockDailyKlineComplete(current, manifest()), true)
  assert.equal(isStockKlineComplete({ ...current, kline_rows: 95 }, manifest()), false)
  assert.equal(isStockDailyKlineComplete({ ...current, daily_kline_rows: 1 }, manifest()), false)
})
