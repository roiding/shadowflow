import type { ApiEnvelope, BoardStockQuote, CollectionRun, PageMeta, QualityMeta, QualitySummary, RankRecord, RankType, StockResearchPoint, SystemStatus } from './types'

const REQUEST_TIMEOUT_MS = 10_000

async function request<T, M = Record<string, unknown>>(path: string): Promise<ApiEnvelope<T, M>> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)
  try {
    const response = await fetch(path, { headers: { Accept: 'application/json' }, signal: controller.signal })
    const body = await response.text()
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
    request<BoardStockQuote[], { as_of: string; quote_source: string; quote_available: boolean; quoted_count?: number; quote_error?: string; dark_data_available: boolean; dark_data_count: number }>(`/api/v1/boards/${type}/${encodeURIComponent(code)}/quotes?as_of=${asOf}`),
  trend: (type: Exclude<RankType, 'stock'>, code: string, from: string, to: string) =>
    request<RankRecord[]>(`/api/v1/boards/${type}/${encodeURIComponent(code)}/trend?from=${from}&to=${to}&interval=5m`),
  dailyClose: (type: RankType, date: string, query: string, sort: string, direction: 'asc' | 'desc', page: number, pageSize: number) => {
    const params = new URLSearchParams({ type, trade_date: date, q: query, sort, direction, page: String(page), page_size: String(pageSize) })
    return request<RankRecord[], PageMeta>(`/api/v1/ranks/daily-close?${params}`)
  },
  quality: (date: string) => request<QualitySummary[], QualityMeta>(`/api/v1/research/quality?trade_date=${date}`),
  stockResearch: (code: string, date: string) => request<StockResearchPoint[]>(`/api/v1/stocks/${encodeURIComponent(code)}/research-5m?trade_date=${date}`),
  runs: (date: string) => request<CollectionRun[]>(`/api/v1/collection-runs?trade_date=${date}&limit=120`),
  status: () => request<SystemStatus>('/api/v1/system/status'),
  tradingDays: (from: string, to: string) => request<string[]>(`/api/v1/trading-days?from=${from}&to=${to}`),
  exportURL: (type: Exclude<RankType, 'stock'>, code: string, from: string, to: string) =>
    `/api/v1/research/export?type=${type}&code=${encodeURIComponent(code)}&from=${from}&to=${to}&format=csv`,
  dailyCloseExportURL: (date: string) => `/api/v1/research/daily-close/export?trade_date=${date}`,
}
