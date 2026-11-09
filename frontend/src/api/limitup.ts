import type { components } from './schema'

export type LimitUpCoverage = components['schemas']['LimitUpCoverage']
export interface LimitUpCountCorrection {
  reason: 'broken_down_summary_undercount'
  source_broken_count: number
  source_touched_count: number
  codes: string[]
}
export type LimitUpDay = components['schemas']['LimitUpDay'] & {
  count_correction?: LimitUpCountCorrection
}
export type LimitUpStock = components['schemas']['LimitUpStock']
export type LimitUpOverview = components['schemas']['LimitUpOverview']
export type LimitUpEvent = components['schemas']['LimitUpEvent'] & {
  first_down_at?: string | null
  last_down_at?: string | null
}
export type LimitUpCategory = components['schemas']['LimitUpCategory']
export type LimitUpProfile = components['schemas']['LimitUpProfile'] & {
  median_first_down_time?: string
  first_down_time_samples?: number
}
export type LimitUpLink = components['schemas']['LimitUpLink']
export type LimitUpAnalysis = components['schemas']['LimitUpAnalysis']
export type LimitUpTodayMember = components['schemas']['LimitUpTodayMember']
export type LimitUpTodayCore = components['schemas']['LimitUpTodayCore']
export type LimitUpTodayNetwork = components['schemas']['LimitUpTodayNetwork']
export type LimitUpArchiveQuality = Omit<components['schemas']['LimitUpArchiveQuality'], 'day'> & {
  day: LimitUpDay | null
}

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

export function limitUpCountCorrectionNotice(
  day?: Pick<LimitUpDay, 'broken_count' | 'touched_count' | 'count_correction'> | null,
): string {
  const correction = day?.count_correction
  if (!day || !correction || correction.reason !== 'broken_down_summary_undercount') return ''
  const evidence = correction.codes.length ? correction.codes.join('、') : '代码未提供'
  return `计数已校正：源炸板${correction.source_broken_count}→本地${day.broken_count}，源触板${correction.source_touched_count}→本地${day.touched_count}，证据${evidence}，原始汇总保留。`
}

export interface LimitUpQuery {
  as_of: string
  days: number
  scope: string
  event?: 'seal' | 'touch' | 'down'
  min_overlap?: number
  limit?: number
  order?: 'heat' | 'overlap'
}

export interface LimitUpTodayQuery {
  days?: number
  scope?: string
  min_overlap?: number
  top?: number
}
