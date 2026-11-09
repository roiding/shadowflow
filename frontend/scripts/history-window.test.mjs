import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('../src/historyWindow.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
const { firstArchivedDayInRange, latestArchivedDay, shiftISODate, trendWindowEndingAt } = await import(moduleURL)

test('uses the latest archived trading day, not the calendar day', () => {
  assert.equal(latestArchivedDay(['2026-10-30', '2026-11-02', '2026-11-06']), '2026-11-06')
  assert.equal(latestArchivedDay([]), '')
})

test('shifts ISO dates across month and year boundaries', () => {
  assert.equal(shiftISODate('2026-11-06', -30), '2026-10-07')
  assert.equal(shiftISODate('2026-01-01', -1), '2025-12-31')
  assert.equal(shiftISODate('2026-02-28', 1), '2026-03-01')
})

test('builds a default trend window ending at the archived day', () => {
  assert.deepEqual(trendWindowEndingAt('2026-11-06'), { from: '2026-10-07', to: '2026-11-06' })
})

test('finds the first archived day inside the selected range', () => {
  const days = ['2026-10-12', '2026-10-13', '2026-10-16', '2026-11-06']
  assert.equal(firstArchivedDayInRange(days, '2026-10-13', '2026-11-06'), '2026-10-13')
  assert.equal(firstArchivedDayInRange(days, '2026-10-14', '2026-11-06'), '2026-10-16')
  assert.equal(firstArchivedDayInRange(days, '2026-11-07', '2026-11-09'), '')
})
