import type { ApiEnvelope, CollectionRun, PageMeta, QualitySummary, RankRecord, RankType, SystemStatus } from './types'

async function request<T, M = Record<string, unknown>>(path: string): Promise<ApiEnvelope<T, M>> {
  const response = await fetch(path, { headers: { Accept: 'application/json' } })
  const payload = (await response.json()) as ApiEnvelope<T, M>
  if (!response.ok || payload.error) {
    throw new Error(payload.error?.message ?? `请求失败 (${response.status})`)
  }
  return payload
}

export const api = {
  latest: (type: Exclude<RankType, 'stock'>) => request<RankRecord[]>(`/api/v1/ranks/latest?type=${type}`),
  rankAt: (type: Exclude<RankType, 'stock'>, date: string, at: string) =>
    request<RankRecord[]>(`/api/v1/ranks?type=${type}&trade_date=${date}&at=${at}`),
  intraday: (type: Exclude<RankType, 'stock'>, code: string, date: string) =>
    request<RankRecord[]>(`/api/v1/boards/${type}/${encodeURIComponent(code)}/intraday?trade_date=${date}`),
  trend: (type: Exclude<RankType, 'stock'>, code: string, from: string, to: string) =>
    request<RankRecord[]>(`/api/v1/boards/${type}/${encodeURIComponent(code)}/trend?from=${from}&to=${to}&interval=5m`),
  dailyClose: (type: RankType, date: string, query: string, sort: string, direction: 'asc' | 'desc', page: number, pageSize: number) => {
	const params = new URLSearchParams({ type, trade_date: date, q: query, sort, direction, page: String(page), page_size: String(pageSize) })
	return request<RankRecord[], PageMeta>(`/api/v1/ranks/daily-close?${params}`)
  },
  quality: (date: string) => request<QualitySummary[]>(`/api/v1/research/quality?trade_date=${date}`),
  runs: (date: string) => request<CollectionRun[]>(`/api/v1/collection-runs?trade_date=${date}&limit=120`),
  status: () => request<SystemStatus>('/api/v1/system/status'),
  tradingDays: (from: string, to: string) => request<string[]>(`/api/v1/trading-days?from=${from}&to=${to}`),
  exportURL: (type: Exclude<RankType, 'stock'>, code: string, from: string, to: string) =>
	`/api/v1/research/export?type=${type}&code=${encodeURIComponent(code)}&from=${from}&to=${to}&format=csv`,
  dailyCloseExportURL: (date: string) => `/api/v1/research/daily-close/export?trade_date=${date}`,
}
