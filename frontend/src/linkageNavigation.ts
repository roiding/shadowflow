import type { SystemStatus } from './api/types'

export type LinkageEventType = 'seal' | 'touch' | 'down'
export type LinkageScope = 'HS,GEM2STAR' | 'HS,GEM2STAR,ST,NEW'
export type LinkageSourceView = 'monitor' | 'focus' | 'stocks'

export interface LinkageContext {
  stockCode: string
  stockName: string
  asOf: string
  days: number
  scope: LinkageScope
  event: LinkageEventType
  minOverlap: number
}

export interface LinkageQuickTarget {
  code: string
  name: string
  asOf: string
  source: LinkageSourceView
}

export interface IntradayLinkageDateInput {
  previousTradingDate?: string
  latestTradingDay?: string
  tradingDay?: boolean
}

export const DEFAULT_LINKAGE_SCOPE: LinkageScope = 'HS,GEM2STAR'
export const DEFAULT_LINKAGE_DAYS = 90
export const DEFAULT_LINKAGE_EVENT: LinkageEventType = 'seal'
export const DEFAULT_LINKAGE_MIN_OVERLAP = 3

const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/
const MAX_LINKAGE_DAYS = 366
const MAX_MIN_OVERLAP = 10

export function isStockCode(value: string): boolean {
  return /^\d{6}$/.test(value.trim())
}

export function isLinkageScope(value: string): value is LinkageScope {
  return value === 'HS,GEM2STAR' || value === 'HS,GEM2STAR,ST,NEW'
}

export function isLinkageEvent(value: string): value is LinkageEventType {
  return value === 'seal' || value === 'touch' || value === 'down'
}

export function isLinkageSourceView(value: unknown): value is LinkageSourceView {
  return value === 'monitor' || value === 'focus' || value === 'stocks'
}

function validDate(value: string | null | undefined): string {
  if (!value || !DATE_PATTERN.test(value)) {
    return ''
  }
  const parsed = new Date(`${value}T00:00:00Z`)
  return !Number.isNaN(parsed.getTime()) && parsed.toISOString().slice(0, 10) === value
    ? value
    : ''
}

function boundedInteger(value: string | null, fallback: number, minimum: number, maximum: number): number {
  const parsed = Number(value)
  if (!Number.isInteger(parsed) || parsed < minimum || parsed > maximum) {
    return fallback
  }
  return parsed
}

export function createLinkageContext(overrides: Partial<LinkageContext> = {}): LinkageContext {
  const stockCode = overrides.stockCode && isStockCode(overrides.stockCode) ? overrides.stockCode.trim() : ''
  return {
    stockCode,
    stockName: stockCode ? (overrides.stockName ?? '') : '',
    asOf: validDate(overrides.asOf),
    days: overrides.days && Number.isInteger(overrides.days) && overrides.days >= 1 && overrides.days <= MAX_LINKAGE_DAYS
      ? overrides.days
      : DEFAULT_LINKAGE_DAYS,
    scope: overrides.scope && isLinkageScope(overrides.scope) ? overrides.scope : DEFAULT_LINKAGE_SCOPE,
    event: overrides.event && isLinkageEvent(overrides.event) ? overrides.event : DEFAULT_LINKAGE_EVENT,
    minOverlap: overrides.minOverlap && Number.isInteger(overrides.minOverlap) && overrides.minOverlap >= 1 && overrides.minOverlap <= MAX_MIN_OVERLAP
      ? overrides.minOverlap
      : DEFAULT_LINKAGE_MIN_OVERLAP,
  }
}

export function readLinkageContext(searchParams: URLSearchParams): LinkageContext | null {
  if (searchParams.get('view') !== 'linkage') {
    return null
  }
  const scopeValue = searchParams.get('scope') ?? ''
  const eventValue = searchParams.get('event') ?? ''
  return createLinkageContext({
    stockCode: searchParams.get('stock_code') ?? '',
    stockName: searchParams.get('stock_name') ?? '',
    asOf: searchParams.get('as_of') ?? '',
    days: boundedInteger(searchParams.get('days'), DEFAULT_LINKAGE_DAYS, 1, MAX_LINKAGE_DAYS),
    scope: isLinkageScope(scopeValue) ? scopeValue : DEFAULT_LINKAGE_SCOPE,
    event: isLinkageEvent(eventValue) ? eventValue : DEFAULT_LINKAGE_EVENT,
    minOverlap: boundedInteger(searchParams.get('min_overlap'), DEFAULT_LINKAGE_MIN_OVERLAP, 1, MAX_MIN_OVERLAP),
  })
}

export function writeLinkageContext(searchParams: URLSearchParams, context: LinkageContext): URLSearchParams {
  const nextParams = new URLSearchParams(searchParams)
  nextParams.set('view', 'linkage')
  if (context.stockCode) {
    nextParams.set('stock_code', context.stockCode)
  } else {
    nextParams.delete('stock_code')
  }
  if (context.stockName) {
    nextParams.set('stock_name', context.stockName)
  } else {
    nextParams.delete('stock_name')
  }
  if (context.asOf) {
    nextParams.set('as_of', context.asOf)
  } else {
    nextParams.delete('as_of')
  }
  nextParams.set('days', String(context.days))
  nextParams.set('scope', context.scope)
  nextParams.set('event', context.event)
  nextParams.set('min_overlap', String(context.minOverlap))
  return nextParams
}

export function buildViewSearchParams(
  nextView: string,
  context: LinkageContext,
  source?: LinkageSourceView | null,
): URLSearchParams {
  const params = new URLSearchParams()
  params.set('view', nextView)
  if (nextView !== 'linkage') {
    return params
  }
  const linkageParams = writeLinkageContext(params, context)
  if (source) {
    linkageParams.set('source', source)
  }
  return linkageParams
}

export function buildLinkageURL(
  context: LinkageContext,
  pathname = window.location.pathname,
  source?: LinkageSourceView,
): string {
  const params = new URLSearchParams()
  if (source) {
    params.set('source', source)
  }
  const search = writeLinkageContext(params, context).toString()
  return `${pathname}?${search}`
}

export function applyLinkageURL(
  currentURL: string,
  context: LinkageContext,
  source?: LinkageSourceView,
): string {
  const parsedURL = new URL(currentURL)
  parsedURL.search = buildLinkageURL(context, parsedURL.pathname, source).split('?')[1] ?? ''
  return parsedURL.toString()
}

export function buildSourceURL(view: Exclude<LinkageSourceView, never>, pathname = window.location.pathname): string {
  const search = new URLSearchParams({ view })
  return `${pathname}?${search.toString()}`
}

export function selectLinkageStock(context: LinkageContext, code: string, name: string): LinkageContext {
  if (!isStockCode(code)) {
    return context
  }
  return { ...context, stockCode: code.trim(), stockName: name.trim() }
}

export function linkageContextKey(context: LinkageContext): string {
  return [
    context.stockCode,
    context.asOf,
    context.days,
    context.scope,
    context.event,
    context.minOverlap,
  ].join('|')
}

export function resolveHistoricalLinkageAsOf(asOf: string): string {
  return validDate(asOf)
}

export function resolveIntradayLinkageAsOf(input: IntradayLinkageDateInput): string {
  const latestDate = validDate(input.latestTradingDay)
  if (input.tradingDay === false) {
    return latestDate
  }
  return validDate(input.previousTradingDate)
}

export function resolveMonitorResearchAsOf(
  status?: Pick<SystemStatus, 'event_research_as_of' | 'latest_trading_day' | 'trading_day'>,
  previousTradingDate?: string,
): string {
  if (!status) {
    return ''
  }
  const explicitDate = validDate(status.event_research_as_of)
  if (explicitDate) {
    return explicitDate
  }
  return resolveIntradayLinkageAsOf({
    previousTradingDate,
    latestTradingDay: status.latest_trading_day,
    tradingDay: status.trading_day,
  })
}

export function quickTargetToContext(
  target: LinkageQuickTarget,
  options: Pick<Partial<LinkageContext>, 'days' | 'scope' | 'event' | 'minOverlap'> = {},
): LinkageContext {
  return createLinkageContext({
    stockCode: target.code,
    stockName: target.name,
    asOf: target.asOf,
    days: options.days,
    scope: options.scope,
    event: options.event,
    minOverlap: options.minOverlap,
  })
}

export function isNoEventError(error: unknown): boolean {
  const errorCode = apiErrorCode(error)
  if (errorCode === 'limit_up_stock_not_found') {
    return true
  }
  if (!(error instanceof Error)) {
    return false
  }
  return error.message.includes('没有该股票的已完整采集事件') ||
    error.message.includes('当前范围无事件') ||
    error.message.includes('limit_up_stock_not_found')
}

function apiErrorCode(error: unknown): string {
  if (typeof error === 'object' && error !== null && 'code' in error) {
    const errorCode = (error as { code?: unknown }).code
    return typeof errorCode === 'string' ? errorCode : ''
  }
  return ''
}

export function isLimitUpUnavailableError(error: unknown): boolean {
  if (apiErrorCode(error) === 'limit_up_unavailable') {
    return true
  }
  return error instanceof Error && (
    error.message.includes('事件数据读取暂不可用') ||
    error.message.includes('limit_up_unavailable')
  )
}

export function isLimitUpBusyError(error: unknown): boolean {
  if (apiErrorCode(error) === 'limit_up_busy') {
    return true
  }
  return error instanceof Error && (
    error.message.includes('股票研究正在计算') ||
    error.message.includes('limit_up_busy')
  )
}

export function linkageQueryErrorMessage(error: unknown): string {
  if (isNoEventError(error)) {
    return '当前股票在所选范围和窗口内没有已归档事件。股票选择已保留，可调整截止日、窗口或共现事件后重试。'
  }
  if (isLimitUpUnavailableError(error)) {
    return '事件归档读取暂不可用。请先到“采集质量”确认事件归档是否已启用或数据库是否完成初始化。'
  }
  if (isLimitUpBusyError(error)) {
    return '股票研究正在计算，请稍后重试；当前页面不会触发任何采集。'
  }
  if (error instanceof Error && error.message) {
    return error.message
  }
  return '联动数据读取失败，请稍后重试。'
}

export function sourceLinkageAsOf(
  source: LinkageSourceView,
  dates: { monitorAsOf: string; focusAsOf: string; stockAsOf: string },
): string {
  if (source === 'monitor') {
    return dates.monitorAsOf
  }
  if (source === 'focus') {
    return dates.focusAsOf
  }
  return dates.stockAsOf
}

export function isHistoricalSourceDate(asOf: string, latestTradingDay?: string): boolean {
  return Boolean(validDate(asOf) && latestTradingDay && asOf < latestTradingDay)
}
