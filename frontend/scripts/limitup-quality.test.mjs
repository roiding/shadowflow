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
  limitUpCountCorrectionNotice,
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

test('count correction notice exposes source totals, local totals and evidence codes', () => {
  assert.equal(
    limitUpCountCorrectionNotice({
      broken_count: 24,
      touched_count: 114,
      count_correction: {
        reason: 'broken_down_summary_undercount',
        source_broken_count: 23,
        source_touched_count: 113,
        codes: ['002856'],
      },
    }),
    '计数已校正：源炸板23→本地24，源触板113→本地114，证据002856，原始汇总保留。',
  )
  assert.equal(
    limitUpCountCorrectionNotice({
      broken_count: 24,
      touched_count: 114,
      count_correction: {
        reason: 'broken_down_summary_undercount',
        source_broken_count: 23,
        source_touched_count: 113,
        codes: ['002856', '300716'],
      },
    }),
    '计数已校正：源炸板23→本地24，源触板113→本地114，证据002856、300716，原始汇总保留。',
  )
  assert.equal(limitUpCountCorrectionNotice({ broken_count: 19, touched_count: 97 }), '')
  assert.equal(limitUpCountCorrectionNotice(null), '')
})
