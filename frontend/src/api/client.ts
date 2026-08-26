import { getToken, notifyUnauthorized } from '../auth'
import type { ApiEnvelope, ArchiveRevision, BoardStockQuote, CollectionRun, DailyFeature, FutureReturnLabel, FocusResult, FocusScanRequest, PageMeta, QualityMeta, QualitySummary, RankRecord, RankType, StockResearchPoint, SystemStatus } from './types'

const REQUEST_TIMEOUT_MS = 25_000
const SCAN_TIMEOUT_MS = 35_000

async function request<T, M = Record<string, unknown>>(path: string, init?: RequestInit, timeoutMs = REQUEST_TIMEOUT_MS): Promise<ApiEnvelope<T, M>> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), timeoutMs)
  const token = getToken()
  try {
    const response = await fetch(path, { ...init, headers: { Accept: 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}), ...init?.headers }, signal: controller.signal })
    const body = await response.text()
    // Judge the status code before parsing: a gateway or proxy may answer 401
    // with an HTML page, and failing on the parse first would leave the Token
    // Gate permanently hidden.
    if (response.status === 401) {
      notifyUnauthorized(token)
      throw new Error('访问令牌无效或已过期')
    }
    let payload: ApiEnvelope<T, M>
    try {
      payload = body ? JSON.parse(body) as ApiEnvelope<T, M> : {}
    } catch {
      throw new Error(`服务返回了无法识别的响应 (${response.status})`)
    }
    if (!response.ok || payload.error) {
      throw new Error(payload.error?.message ?? `请求失败 (${response.status})`)
    }
    return payload
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') {
      throw new Error('请求超时，请检查后端服务')
    }
    throw error
  } finally {
    window.clearTimeout(timeout)
  }
}

export const api = {
  latest: (type: Exclude<RankType, 'stock'>) => request<RankRecord[]>(`/api/v1/ranks/latest?type=${type}`),
  rankAt: (type: Exclude<RankType, 'stock'>, date: string, at: string) =>
    request<RankRecord[]>(`/api/v1/ranks?type=${type}&trade_date=${date}&at=${at}`),
  intraday: (type: Exclude<RankType, 'stock'>, code: string, date: string) =>
    request<RankRecord[]>(`/api/v1/boards/${type}/${encodeURIComponent(code)}/intraday?trade_date=${date}`),
  boardQuotes: (type: Exclude<RankType, 'stock'>, code: string, asOf: string) =>
    request<BoardStockQuote[], { as_of: string; quote_source: string; quote_available: boolean; quoted_count?: number; quote_error?: string; quote_status: string; stale: boolean; cache_age_ms?: number; dark_data_available: boolean; dark_data_count: number }>(`/api/v1/boards/${type}/${encodeURIComponent(code)}/quotes?as_of=${asOf}`),
  trend: (type: Exclude<RankType, 'stock'>, code: string, from: string, to: string, revisionId?: string) => {
    const params = new URLSearchParams({ from, to, interval: '5m' })
    if (revisionId) params.set('revision_id', revisionId)
    return request<RankRecord[]>(`/api/v1/boards/${type}/${encodeURIComponent(code)}/trend?${params}`)
  },
  dailyClose: (type: RankType, date: string, query: string, sort: string, direction: 'asc' | 'desc', page: number, pageSize: number, revisionId?: string) => {
    const params = new URLSearchParams({ type, trade_date: date, q: query, sort, direction, page: String(page), page_size: String(pageSize) })
    if (revisionId) params.set('revision_id', revisionId)
    return request<RankRecord[], PageMeta>(`/api/v1/ranks/daily-close?${params}`)
  },
  quality: (date: string) => request<QualitySummary[], QualityMeta>(`/api/v1/research/quality?trade_date=${date}`),
  revisions: (date: string) => request<ArchiveRevision[], { trade_date: string; count: number; current_revision_id?: string }>(`/api/v1/research/revisions?trade_date=${date}`),
  features: (date: string, type?: RankType, revisionId?: string) => {
    const params = new URLSearchParams({ trade_date: date })
    if (type) params.set('type', type)
    if (revisionId) params.set('revision_id', revisionId)
    return request<DailyFeature[], { count: number; trade_date: string; rank_type?: RankType; feature_set: unknown }>(`/api/v1/research/features?${params}`)
  },
  labels: (date: string, horizon?: number, type?: RankType, revisionId?: string, targetRevisionId?: string) => {
    const params = new URLSearchParams({ trade_date: date })
    if (horizon) params.set('horizon', String(horizon))
    if (type) params.set('type', type)
    if (revisionId) params.set('revision_id', revisionId)
    if (targetRevisionId) params.set('target_revision_id', targetRevisionId)
    return request<FutureReturnLabel[], { count: number; trade_date: string; rank_type?: RankType; horizon: number }>(`/api/v1/research/labels?${params}`)
  },
  stockResearch: (code: string, date: string, revisionId?: string) => {
    const params = new URLSearchParams({ trade_date: date })
    if (revisionId) params.set('revision_id', revisionId)
    return request<StockResearchPoint[]>(`/api/v1/stocks/${encodeURIComponent(code)}/research-5m?${params}`)
  },
  runs: (date: string) => request<CollectionRun[]>(`/api/v1/collection-runs?trade_date=${date}&limit=120`),
  status: () => request<SystemStatus>('/api/v1/system/status'),
  tradingDays: (from: string, to: string) => request<string[]>(`/api/v1/trading-days?from=${from}&to=${to}`),
  threeDayFocus: (asOf: string) => request<FocusResult>(`/api/v1/focus/three-day?as_of=${asOf}`),
  focusScan: (scan: FocusScanRequest) => request<FocusResult>('/api/v1/focus/scan', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(scan),
  }, SCAN_TIMEOUT_MS),
  exportURL: (type: Exclude<RankType, 'stock'>, code: string, from: string, to: string) =>
    `/api/v1/research/export?type=${type}&code=${encodeURIComponent(code)}&from=${from}&to=${to}&format=csv`,
  dailyCloseExportURL: (date: string, revisionId?: string) => `/api/v1/research/daily-close/export?trade_date=${date}${revisionId ? `&revision_id=${encodeURIComponent(revisionId)}` : ''}`,

  download: async (path: string) => {
    const controller = new AbortController()
    const timeout = window.setTimeout(() => controller.abort(), 130_000)
    const token = getToken()
    try {
      const response = await fetch(path, { headers: { Accept: 'text/csv, application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) }, signal: controller.signal })
      if (response.status === 401) {
        notifyUnauthorized(token)
        throw new Error('访问令牌无效或已过期')
      }
      if (!response.ok) throw new Error(`导出失败 (${response.status})`)
      const blob = await response.blob()
      const disposition = response.headers.get('Content-Disposition') ?? ''
      const match = disposition.match(/filename\*=UTF-8''([^;]+)|filename="?([^";]+)"?/i)
      // Only the filename*=UTF-8'' form is percent-encoded; decoding the plain
      // filename= form would throw on a literal '%' and fail the whole export.
      let filename = match?.[2] || path.split('/').pop()?.split('?')[0] || 'shadowflow-export.csv'
      if (match?.[1]) {
        try {
          filename = decodeURIComponent(match[1])
        } catch {
          filename = match[1]
        }
      }
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = filename
      document.body.appendChild(anchor)
      anchor.click()
      anchor.remove()
      // Firefox/Safari may not have committed the download yet; revoking
      // synchronously can abort large blobs.
      window.setTimeout(() => URL.revokeObjectURL(url), 10_000)
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') throw new Error('导出超时，请缩小日期范围')
      throw error
    } finally {
      window.clearTimeout(timeout)
    }
  },
}