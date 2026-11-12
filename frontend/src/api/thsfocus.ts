import type { ApiEnvelope } from './types'

export type ThsFocusStatus = 'not_collected' | 'complete'

export interface ThsFocusStock {
  code: string
  name: string
  market_id: number
  continue_num: number
  reason: string
  reason_detail: string
  high_days: string
  change_percent: number | null
}

export interface ThsFocusBlock {
  code: string
  name: string
  change: number | null
  limit_up_count: number
  stocks: ThsFocusStock[]
}

export interface ThsFocusTabStock {
  code: string
  name: string
  market_id: number
  reason: string
  abnormal_reason: string
  detail_reason: string
}

export interface ThsFocusTab {
  name: string
  rank: number
  stocks: ThsFocusTabStock[]
}

export interface ThsFocusLevel {
  height: number
  stocks: ThsFocusStock[]
}

export interface ThsFocusSnapshot {
  trade_date: string
  source: 'ths_focus'
  scope: string
  market_scope: 'source_default'
  fetched_at: string
  source_date_verified: boolean
  blocks: ThsFocusBlock[]
  ladder: ThsFocusLevel[]
  tabs?: ThsFocusTab[] | null
  tab_date_verified?: boolean
  market: unknown
  comparison: unknown
  limitations: string[]
}

export interface ThsFocusPayload {
  trade_date: string
  scope: string
  status: ThsFocusStatus
  snapshot?: ThsFocusSnapshot | null
}

export type ThsFocusApiResponse = ApiEnvelope<ThsFocusPayload>

export interface ThsFocusBlockHit {
  block: ThsFocusBlock
  stock: ThsFocusStock
  source_order: number
}

export interface ThsFocusTabHit {
  tab: ThsFocusTab
  stock: ThsFocusTabStock
  source_order: number
}

export interface ThsFocusLadderHit {
  level: ThsFocusLevel
  stock: ThsFocusStock
}

export interface ThsFocusMarketSummary {
  rise?: number
  fall?: number
  flat?: number
  limitUp?: number
  limitDown?: number
  todayMaxBoard?: number
  yesterdayMaxBoard?: number
  todayDate?: string
  yesterdayDate?: string
}

export const THS_FOCUS_SCOPES = ['HS,GEM2STAR', 'HS,GEM2STAR,ST,NEW'] as const
export const THS_FOCUS_MARKET_SCOPE = 'source_default' as const

const TRADE_DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/
const STOCK_CODE_PATTERN = /^\d{6}$/
const MARKET_FIELD_NAMES = {
  rise: ['rise', 'up_count', 'up_num', 'up'],
  fall: ['fall', 'down_count', 'down_num', 'down'],
  flat: ['deuce', 'flat', 'flat_count', 'flat_num'],
  limitUp: ['limit_up', 'limit_up_count', 'limit_up_num'],
  limitDown: ['limit_down', 'limit_down_count', 'limit_down_num'],
  todayMaxBoard: ['today_max_board', 'today_max_height', 'max_board', 'highest_board'],
  yesterdayMaxBoard: ['yesterday_max_board', 'yesterday_max_height'],
  todayDate: ['today_date', 'date'],
  yesterdayDate: ['yesterday_date', 'previous_date'],
} as const

export function isThsFocusScope(scope: string): boolean {
  return THS_FOCUS_SCOPES.some((candidate) => candidate === scope)
}

export function thsFocusScopeLabel(scope: string): string {
  if (scope === 'HS,GEM2STAR') return '沪深及双创'
  if (scope === 'HS,GEM2STAR,ST,NEW') return '含 ST、未开板新股'
  return scope || '未选择市场范围'
}

export function thsFocusMarketScopeLabel(marketScope: string): string {
  return marketScope === THS_FOCUS_MARKET_SCOPE
    ? '源默认市场口径（独立于当前范围）'
    : marketScope || '未提供'
}

export function isThsFocusTradeDate(value: string): boolean {
  if (!TRADE_DATE_PATTERN.test(value)) return false
  const parsed = new Date(`${value}T00:00:00Z`)
  return !Number.isNaN(parsed.getTime()) && parsed.toISOString().slice(0, 10) === value
}

export function thsFocusQueryKey(tradeDate: string, scope: string) {
  return ['limit-up-focus', tradeDate || 'missing-date', scope || 'missing-scope'] as const
}

export function thsFocusQueryEnabled(input: {
  tradeDate: string
  scope: string
  authRequired: boolean
}): boolean {
  return !input.authRequired && isThsFocusTradeDate(input.tradeDate) && isThsFocusScope(input.scope)
}

function isThsFocusPayload(value: unknown): value is ThsFocusPayload {
  if (!value || typeof value !== 'object') return false
  const payload = value as Partial<ThsFocusPayload>
  return typeof payload.trade_date === 'string' &&
    typeof payload.scope === 'string' &&
    (payload.status === 'not_collected' || payload.status === 'complete')
}

export function thsFocusPayload(response: ThsFocusApiResponse | null | undefined): ThsFocusPayload | undefined {
  if (!response || typeof response !== 'object') return undefined
  if (isThsFocusPayload(response.data)) return response.data
  return undefined
}

export function thsFocusResponseMatches(
  payload: ThsFocusPayload | undefined,
  tradeDate: string,
  scope: string,
): boolean {
  if (!payload || payload.trade_date !== tradeDate || payload.scope !== scope) return false
  if (payload.status === 'not_collected') return true
  if (payload.status !== 'complete' || !payload.snapshot) return false
  return payload.snapshot.trade_date === tradeDate &&
    payload.snapshot.scope === scope &&
    payload.snapshot.source === 'ths_focus' &&
    payload.snapshot.market_scope === THS_FOCUS_MARKET_SCOPE &&
    Array.isArray(payload.snapshot.blocks) &&
    Array.isArray(payload.snapshot.ladder) &&
    Array.isArray(payload.snapshot.limitations) &&
    (payload.snapshot.tabs === undefined ||
      payload.snapshot.tabs === null ||
      Array.isArray(payload.snapshot.tabs))
}

export function selectedThsFocusBlocks(
  snapshot: ThsFocusSnapshot | undefined,
  stockCode: string,
): ThsFocusBlockHit[] {
  const normalizedCode = stockCode.trim()
  if (!snapshot || !STOCK_CODE_PATTERN.test(normalizedCode)) return []
  const hits: ThsFocusBlockHit[] = []
  snapshot.blocks.forEach((block, blockIndex) => {
    const stock = block.stocks.find((candidate) => candidate.code === normalizedCode)
    if (stock) {
      hits.push({ block, stock, source_order: blockIndex + 1 })
    }
  })
  return hits
}

export function selectedThsFocusTabs(
  snapshot: ThsFocusSnapshot | undefined,
  stockCode: string,
): ThsFocusTabHit[] {
  const normalizedCode = stockCode.trim()
  if (!snapshot || !STOCK_CODE_PATTERN.test(normalizedCode) || !Array.isArray(snapshot.tabs)) return []
  const hits: ThsFocusTabHit[] = []
  snapshot.tabs.forEach((tab, tabIndex) => {
    const stock = tab.stocks.find((candidate) => candidate.code === normalizedCode)
    if (stock) hits.push({ tab, stock, source_order: tab.rank || tabIndex + 1 })
  })
  return hits.sort((left, right) => left.source_order - right.source_order)
}

export function selectedThsFocusLadder(
  snapshot: ThsFocusSnapshot | undefined,
  stockCode: string,
): ThsFocusLadderHit | undefined {
  const normalizedCode = stockCode.trim()
  if (!snapshot || !STOCK_CODE_PATTERN.test(normalizedCode)) return undefined
  for (const level of snapshot.ladder) {
    const stock = level.stocks.find((candidate) => candidate.code === normalizedCode)
    if (stock) return { level, stock }
  }
  return undefined
}

export function sortedThsFocusLadder(snapshot: ThsFocusSnapshot | undefined): ThsFocusLevel[] {
  return [...(snapshot?.ladder ?? [])].sort((left, right) => right.height - left.height)
}

export function thsFocusLadderMismatch(levelHeight: number, continueNum: number): boolean {
  return Number.isFinite(levelHeight) && Number.isFinite(continueNum) && levelHeight !== continueNum
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  return value as Record<string, unknown>
}

function finiteNumber(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string' && value.trim() !== '') {
    const normalized = value.trim().replaceAll(',', '')
    if (!/^[+-]?(?:\d+\.?\d*|\.\d+)$/.test(normalized)) return undefined
    const parsed = Number(normalized)
    return Number.isFinite(parsed) ? parsed : undefined
  }
  return undefined
}

function textValue(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim()
  return normalized === '' ? undefined : normalized
}

function nestedRecord(
  source: Record<string, unknown> | undefined,
  keys: readonly string[],
): Record<string, unknown> | undefined {
  if (!source) return undefined
  for (const key of keys) {
    const nested = recordValue(source[key])
    if (nested) return nested
  }
  return undefined
}

function findNumber(
  sources: Array<Record<string, unknown> | undefined>,
  keys: readonly string[],
): number | undefined {
  for (const source of sources) {
    if (!source) continue
    for (const key of keys) {
      const value = finiteNumber(source[key])
      if (value !== undefined) return value
    }
  }
  return undefined
}

function findText(
  sources: Array<Record<string, unknown> | undefined>,
  keys: readonly string[],
): string | undefined {
  for (const source of sources) {
    if (!source) continue
    for (const key of keys) {
      const value = textValue(source[key])
      if (value !== undefined) return value
    }
  }
  return undefined
}

export function thsFocusMarketSummary(market: unknown, comparison: unknown): ThsFocusMarketSummary {
  const marketRecord = recordValue(market)
  const comparisonRecord = recordValue(comparison)
  const marketData = nestedRecord(marketRecord, ['data']) ?? marketRecord
  const comparisonData = nestedRecord(comparisonRecord, ['data']) ?? comparisonRecord
  const riseFallRecord = nestedRecord(marketData, ['rise_fall', 'market', 'breadth'])
  const marketSources = [riseFallRecord, marketData]
  const comparisonSources = [comparisonData]
  return {
    rise: findNumber(marketSources, MARKET_FIELD_NAMES.rise),
    fall: findNumber(marketSources, MARKET_FIELD_NAMES.fall),
    flat: findNumber(marketSources, MARKET_FIELD_NAMES.flat),
    limitUp: findNumber(marketSources, MARKET_FIELD_NAMES.limitUp),
    limitDown: findNumber(marketSources, MARKET_FIELD_NAMES.limitDown),
    todayMaxBoard: findNumber(comparisonSources, MARKET_FIELD_NAMES.todayMaxBoard),
    yesterdayMaxBoard: findNumber(comparisonSources, MARKET_FIELD_NAMES.yesterdayMaxBoard),
    todayDate: findText(comparisonSources, MARKET_FIELD_NAMES.todayDate),
    yesterdayDate: findText(comparisonSources, MARKET_FIELD_NAMES.yesterdayDate),
  }
}

export function hasThsFocusMarketValues(summary: ThsFocusMarketSummary): boolean {
  return [
    summary.rise,
    summary.fall,
    summary.flat,
    summary.limitUp,
    summary.limitDown,
    summary.todayMaxBoard,
    summary.yesterdayMaxBoard,
  ].some((value) => value !== undefined)
}

export function formatThsFocusPercent(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '--'
  return `${value > 0 ? '+' : ''}${value.toFixed(2)}%`
}

export function thsFocusDateVerificationText(verified: boolean | undefined): string {
  return verified
    ? '响应提供日期回显与核验标记。'
    : '源响应未完整回显日期；即使题材内部分股票时间戳可核对，连板梯队与市场摘要仍缺少日期证据，不能确认全部内容属于所选交易日。'
}

export function thsFocusCoverageNotice(): string {
  return '题材分类来自同花顺涨停简图的 tab_list/all_tab_data，是源当日整理口径而非全市场行业/概念全集；block_top 仅作普通板块视图，原因标签与说明仅用于辅助核对，不构成因果关系。'
}

export function thsFocusSourceLimitation(scope: string): string {
  return `市场摘要与两日对比采用同花顺默认市场口径，不受当前范围“${thsFocusScopeLabel(scope)}”的 ST/新股筛选控制，不能与题材和梯队的范围等同。`
}

export function thsFocusReadableLimitation(limitation: string): string {
  return limitation
    .replaceAll('默认市场source_default口径', '默认市场口径')
    .replaceAll('默认市场SourceDefault口径', '默认市场口径')
    .replaceAll('source_date_verified', '日期核验标记')
    .replaceAll('source_default', '源默认市场口径')
    .replaceAll('tab_list/all_tab_data', '涨停简图分类')
    .replaceAll('all_tab_data', '涨停简图分类')
    .replaceAll('tab_list', '涨停简图分类')
    .replaceAll('block_top', '题材列表')
    .replaceAll('continuous_limit_up', '连板梯队')
    .replaceAll('market_state', '市场摘要')
    .replaceAll('two_days_compare', '两日对比')
    .replaceAll('scope', '市场范围')
    .replaceAll('ST与NEW', 'ST与新股')
    .replaceAll('ST/NEW', 'ST/新股')
}

export function thsFocusDisplayLimitations(snapshot: Pick<ThsFocusSnapshot, 'market_scope' | 'limitations'>): string[] {
  const limitations = snapshot.limitations
    .filter((limitation): limitation is string => typeof limitation === 'string')
    .map((limitation) => limitation.trim())
    .filter((limitation) => limitation !== '')
  const readable = limitations.map((limitation) => thsFocusReadableLimitation(limitation))
  if (snapshot.market_scope === THS_FOCUS_MARKET_SCOPE) return readable
  return [`未识别市场口径：${String(snapshot.market_scope)}`, ...readable]
}
