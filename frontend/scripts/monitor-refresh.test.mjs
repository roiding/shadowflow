import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { QueryClient, QueryObserver } from '@tanstack/react-query'
import ts from 'typescript'

const source = readFileSync(new URL('../src/monitorRefresh.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const { refreshMonitorQueries, quoteRefreshInterval } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)

test('a monitor tick publishes new ranks for both board types and refreshes active details', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } })
  const subscriptions = []
  const calls = new Map()
  let version = 1
  const register = async (key, active = true, rows = false) => {
    const id = key.join('/')
    const options = { queryKey: key, queryFn: async () => {
      calls.set(id, (calls.get(id) ?? 0) + 1)
      return rows ? [{ code: 'BK001', version }] : { version }
    } }
    if (active) {
      const observer = new QueryObserver(client, options)
      subscriptions.push(observer.subscribe(() => {}))
      await observer.refetch()
    } else await client.fetchQuery(options)
  }
  try {
    await register(['latest', 'industry'])
    await register(['latest', 'concept'], false)
    await register(['system-status'])
    await register(['intraday', 'industry', 'BK001', '2026-09-15'])
    await register(['board-quotes', 'industry', 'BK001', '2026-09-15'])
    await register(['board-quotes', 'concept', 'BK101', '2026-09-15'], false)
    await register(['monitor-previous-close', 'industry', '2026-09-14'], true, true)
    await register(['quality', '2026-09-15'])
    version = 2
    await refreshMonitorQueries(client)
    for (const key of [['latest', 'industry'], ['latest', 'concept'], ['system-status'],
      ['intraday', 'industry', 'BK001', '2026-09-15'], ['board-quotes', 'industry', 'BK001', '2026-09-15']]) {
      assert.equal(client.getQueryData(key).version, 2, key.join('/'))
      assert.equal(calls.get(key.join('/')), 2)
    }
    assert.equal(calls.get('board-quotes/concept/BK101/2026-09-15'), 1, 'do not query inactive constituent panels')
    assert.equal(client.getQueryState(['board-quotes', 'concept', 'BK101', '2026-09-15']).isInvalidated, true)
    assert.equal(calls.get('monitor-previous-close/industry/2026-09-14'), 1)
    assert.equal(calls.get('quality/2026-09-15'), 1)
    await refreshMonitorQueries(client, true)
    assert.equal(calls.get('monitor-previous-close/industry/2026-09-14'), 2, 'manual refresh reloads the baseline')
  } finally {
    subscriptions.forEach((unsubscribe) => unsubscribe())
    client.clear()
  }
})

test('missing and failed previous closes retry on a tick without resetting the selected query', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
  const subscriptions = []
  let ready = false
  try {
    for (const type of ['industry', 'concept']) {
      const observer = new QueryObserver(client, { queryKey: ['monitor-previous-close', type, '2026-09-14'], queryFn: async () => {
        if (type === 'concept' && !ready) throw new Error('not archived yet')
        return ready ? [{ code: type }] : []
      } })
      subscriptions.push(observer.subscribe(() => {}))
      await observer.refetch()
    }
    ready = true
    await refreshMonitorQueries(client)
    for (const type of ['industry', 'concept']) assert.deepEqual(client.getQueryData(['monitor-previous-close', type, '2026-09-14']), [{ code: type }])
  } finally {
    subscriptions.forEach((unsubscribe) => unsubscribe())
    client.clear()
  }
})

test('monitor ticks do not abort or restart an in-flight request', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
  let calls = 0
  let aborts = 0
  let release
  const pending = new Promise((resolve) => { release = resolve })
  const observer = new QueryObserver(client, { queryKey: ['latest', 'industry'], queryFn: async ({ signal }) => {
    calls++
    signal.addEventListener('abort', () => { aborts++ })
    if (calls > 1) await pending
    return { calls }
  } })
  const unsubscribe = observer.subscribe(() => {})
  try {
    await observer.refetch()
    const inFlight = observer.refetch()
    const tick = refreshMonitorQueries(client)
    release()
    await Promise.all([inFlight, tick])
    assert.equal(calls, 2)
    assert.equal(aborts, 0)
  } finally {
    release()
    unsubscribe()
    client.clear()
  }
})

test('quote warmup is followed promptly even without the page timer and stops after completion/failure', () => {
  for (const quote_status of ['warming', 'stale', 'unavailable', 'ready']) {
    assert.equal(quoteRefreshInterval({ quote_status, quote_refreshing: true }), 1000)
    assert.equal(quoteRefreshInterval({ quote_status, quote_refreshing: false }), false)
  }
  assert.equal(quoteRefreshInterval({ quote_refreshing: true, quote_error: 'previous attempt failed' }), 1000)
  assert.equal(quoteRefreshInterval({ quote_status: 'warming', quote_error: 'failed', quote_refreshing: false }), false)
  assert.equal(quoteRefreshInterval(), false)
  assert.equal(quoteRefreshInterval({ quote_status: 'warming' }), 1000)
  assert.equal(quoteRefreshInterval({ quote_status: 'stale' }), 1000)
  assert.equal(quoteRefreshInterval({ quote_status: 'warming', quote_error: 'failed' }), false)
  assert.equal(quoteRefreshInterval({ quote_status: 'ready' }), false)
  assert.equal(quoteRefreshInterval({ quote_status: 'warming', quote_refreshing: true }, true), false, 'HTTP failure must stop polling even if the cached response was warming')
})
