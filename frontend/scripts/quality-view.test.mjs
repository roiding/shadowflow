import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const qualityViewSource = readFileSync(new URL('../src/views/QualityView.tsx', import.meta.url), 'utf8')
const archivePanelSource = readFileSync(new URL('../src/views/LimitUpArchivePanel.tsx', import.meta.url), 'utf8')
const appSource = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8')

test('event archive stays mounted outside quality loading and error branches', () => {
  const archivePanelIndex = qualityViewSource.indexOf('<LimitUpArchivePanel')
  const qualityConditionalIndex = qualityViewSource.indexOf('{error ?')
  assert.ok(archivePanelIndex >= 0)
  assert.ok(qualityConditionalIndex > archivePanelIndex)
  assert.equal(qualityViewSource.split('<LimitUpArchivePanel').length - 1, 1)
  assert.ok(qualityViewSource.includes('scope={limitUpScope}'))
  assert.ok(qualityViewSource.includes('onScopeChange={setLimitUpScope}'))
})

test('event archive run rows use the shared Chinese label', () => {
  assert.ok(qualityViewSource.includes('collectionRunLabel(run.snapshot_kind)'))
})

test('archive manifest reports both Eastmoney and THS five-minute sources', () => {
  assert.ok(qualityViewSource.includes('sources.stock_kline_5m'))
  assert.ok(qualityViewSource.includes('sources.stock_trends_1m_241'))
  assert.ok(qualityViewSource.includes('sources.ths_kline_5m_actual'))
})

test('event archive requests and query keys carry the selected scope', () => {
  assert.ok(archivePanelSource.includes('limitUpQualityQueryKey(date, scope)'))
  assert.ok(archivePanelSource.includes('api.limitUpQuality(date, scope || undefined, signal)'))
  assert.ok(archivePanelSource.includes('LIMIT_UP_SCOPE_OPTIONS'))
})

test('a complete archive keeps the latest failed reseed visible', () => {
  assert.ok(archivePanelSource.includes('limitUpQualityFailureNotice'))
  assert.ok(archivePanelSource.includes('event-archive-failure'))
})

test('linkage quality navigation uses the current research date and complete URL params', () => {
  assert.ok(appSource.includes('setQualityDate(linkageContext.asOf)'))
  assert.ok(appSource.includes('setQualityScope(linkageContext.scope)'))
  assert.ok(appSource.includes('buildViewSearchParams(nextView, nextContext, nextSource)'))
  assert.equal(appSource.includes('writeLinkageContext(params, nextContext)'), false)
})
