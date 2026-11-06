import assert from 'node:assert/strict'
import test, { after } from 'node:test'
import { compileModules } from './support/compile.mjs'

const { urls, cleanup } = await compileModules([
  new URL('../src/api/limitup.ts', import.meta.url),
])
after(cleanup)

const {
  limitUpQualityParams,
  limitUpQualityQueryKey,
  limitUpQualityFailureNotice,
  limitUpScopeLabel,
} = await import(urls.limitup)

test('quality requests omit scope for backend defaults and include explicit scope otherwise', () => {
  assert.equal(limitUpQualityParams('2026-11-05').toString(), 'trade_date=2026-11-05')
  const extended = limitUpQualityParams('2026-11-05', 'HS,GEM2STAR,ST,NEW')
  assert.equal(extended.get('trade_date'), '2026-11-05')
  assert.equal(extended.get('scope'), 'HS,GEM2STAR,ST,NEW')
})

test('quality query keys separate the backend default from explicit scopes', () => {
  assert.deepEqual(limitUpQualityQueryKey('2026-11-05'), ['limit-up-quality', '2026-11-05', 'configured'])
  assert.deepEqual(
    limitUpQualityQueryKey('2026-11-05', 'HS,GEM2STAR,ST,NEW'),
    ['limit-up-quality', '2026-11-05', 'HS,GEM2STAR,ST,NEW'],
  )
})

test('scope labels make the extended universe explicit', () => {
  assert.equal(limitUpScopeLabel(''), '后端默认范围')
  assert.equal(limitUpScopeLabel('HS,GEM2STAR'), '沪深及双创')
  assert.equal(limitUpScopeLabel('HS,GEM2STAR,ST,NEW'), '含 ST、未开板新股')
  assert.equal(limitUpScopeLabel('OTHER,SCOPE'), 'OTHER,SCOPE')
})

test('a failed reseed is not hidden by the previous complete archive', () => {
  assert.equal(
    limitUpQualityFailureNotice({
      complete: true,
      last_run: { status: 'failed', error_message: 'upstream timeout' },
    }),
    '最近一次重采失败：upstream timeout；当前仍展示上次完整归档。',
  )
  assert.equal(
    limitUpQualityFailureNotice({ complete: false, last_run: { status: 'failed', error_message: '' } }),
    '最近一次重采失败：未提供错误原因。',
  )
  assert.equal(
    limitUpQualityFailureNotice({ complete: true, last_run: { status: 'success', error_message: '' } }),
    '',
  )
})
