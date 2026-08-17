import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('../src/continuousSeries.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
const { continuousMetricValues } = await import(moduleURL)

const record = (tradeDate, value, activity = 0) => ({
  trade_date: tradeDate,
  dark_money: value,
  regular_money: value,
  main_money_inflow: value,
  dark_activity: activity,
})
const point = (tradeDate, value, activity = 0) => ({
  label: `${tradeDate} 09:35`,
  record: record(tradeDate, value, activity),
})
const valueFor = (item, metric) => item[metric]

test('multi-day cumulative money continues from the prior close', () => {
  const points = [
    point('2026-08-14', 10),
    point('2026-08-14', 15),
    point('2026-08-17', 3),
    point('2026-08-17', 5),
  ]
  assert.deepEqual(continuousMetricValues(points, 'dark_money', valueFor), [10, 15, 18, 20])
})

test('negative next-day cumulative flow decreases from the prior close', () => {
  const points = [
    point('2026-08-14', 10),
    point('2026-08-14', 15),
    point('2026-08-17', -4),
    point('2026-08-17', -1),
  ]
  assert.deepEqual(continuousMetricValues(points, 'main_money_inflow', valueFor), [10, 15, 11, 14])
})

test('non-cumulative metrics and missing points retain their original semantics', () => {
  const points = [
    point('2026-08-14', 10, 0.1),
    { label: 'missing', record: null },
    point('2026-08-17', 5, 0.2),
  ]
  assert.deepEqual(continuousMetricValues(points, 'dark_activity', valueFor), [0.1, null, 0.2])
})
