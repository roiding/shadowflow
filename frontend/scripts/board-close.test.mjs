import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('../src/api/boardClose.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const { loadCompleteBoardClose } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)
const date = '2026-09-03'
const row = (code) => ({ code, rank_type: 'concept', trade_date: date })
const pageFor = (page, total = 451) => ({
  data: Array.from({ length: Math.min(200, Math.max(0, total - (page - 1) * 200)) }, (_, i) => row(`BK${String((page - 1) * 200 + i).padStart(4, '0')}`)),
  meta: { total, page, page_size: 200, count: Math.min(200, Math.max(0, total - (page - 1) * 200)), pages: Math.ceil(total / 200), rank_type: 'concept', trade_date: date, snapshot_kind: 'daily_close' },
})

test('collects every page of a 451-concept baseline', async () => {
  const requested = []
  const result = await loadCompleteBoardClose('concept', date, async (page) => {
    requested.push(page)
    return pageFor(page)
  })
  assert.deepEqual(requested, [1, 2, 3])
  assert.equal(result.length, 451)
  assert.equal(result.at(-1).code, 'BK0450')
})

test('empty daily archive remains empty; no earlier date is fetched', async () => {
  let calls = 0
  const result = await loadCompleteBoardClose('concept', date, async () => { calls++; return pageFor(1, 0) })
  assert.deepEqual(result, [])
  assert.equal(calls, 1)
})

test('rejects partial, duplicate, wrong-type/date and changing-total pages', async () => {
  const corruptions = [
    (page) => { page.data.pop() },
    (page) => { page.data[0].code = 'BK0000' },
    (page) => { page.meta.total-- },
    (page) => { page.meta.trade_date = '2026-09-02' },
    (page) => { page.meta.rank_type = 'industry' },
    (page) => { page.data[0].trade_date = '2026-09-02' },
    (page) => { page.data[0].rank_type = 'industry' },
    (page) => { page.meta.snapshot_kind = 'minute_work' },
    (page) => { page.meta.count = 0 },
    (page) => { page.meta.pages = 1 },
    (page) => { page.meta.page = 1 },
    (page) => { page.meta.page_size = 100 },
    (page) => { delete page.meta },
  ]
  for (const corrupt of corruptions) {
    await assert.rejects(loadCompleteBoardClose('concept', date, async (number) => {
      const page = pageFor(number)
      if (number === 2) corrupt(page)
      return page
    }), /昨收榜单/)
  }
})

test('does not return partial records if a later page request fails', async () => {
  await assert.rejects(loadCompleteBoardClose('concept', date, async (page) => {
    if (page === 2) throw new Error('network failure')
    return pageFor(page)
  }), /network failure/)
})
