import type { components } from './schema'

export type LimitUpCoverage = components['schemas']['LimitUpCoverage']
export type LimitUpDay = components['schemas']['LimitUpDay']
export type LimitUpStock = components['schemas']['LimitUpStock']
export type LimitUpOverview = components['schemas']['LimitUpOverview']
export type LimitUpEvent = components['schemas']['LimitUpEvent']
export type LimitUpCategory = components['schemas']['LimitUpCategory']
export type LimitUpProfile = components['schemas']['LimitUpProfile']
export type LimitUpLink = components['schemas']['LimitUpLink']
export type LimitUpAnalysis = components['schemas']['LimitUpAnalysis']
export type LimitUpArchiveQuality = components['schemas']['LimitUpArchiveQuality']

export const LIMIT_UP_SCOPE_OPTIONS = [
  { value: '', label: '后端默认范围' },
  { value: 'HS,GEM2STAR', label: '沪深及双创' },
  { value: 'HS,GEM2STAR,ST,NEW', label: '含 ST、未开板新股' },
] as const

export function limitUpScopeLabel(scope?: string): string {
  return LIMIT_UP_SCOPE_OPTIONS.find((option) => option.value === (scope ?? ''))?.label ?? scope ?? '后端默认范围'
}

export function limitUpQualityParams(date: string, scope?: string): URLSearchParams {
  const params = new URLSearchParams({ trade_date: date })
  if (scope) params.set('scope', scope)
  return params
}

export function limitUpQualityQueryKey(date: string, scope?: string) {
  return ['limit-up-quality', date, scope || 'configured'] as const
}

export function limitUpQualityFailureNotice(
  quality?: Pick<LimitUpArchiveQuality, 'complete' | 'last_run'> | null,
): string {
  if (!quality || quality.last_run?.status !== 'failed') return ''
  const reason = quality.last_run.error_message || '未提供错误原因'
  return quality.complete
    ? `最近一次重采失败：${reason}；当前仍展示上次完整归档。`
    : `最近一次重采失败：${reason}。`
}

export interface LimitUpQuery {
  as_of: string
  days: number
  scope: string
  event?: 'seal' | 'touch' | 'down'
  min_overlap?: number
  limit?: number
}
