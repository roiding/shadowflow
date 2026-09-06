import type { ApiEnvelope, PageMeta, RankRecord, RankType } from './types'

// Do not rank the first 200 concepts as if they were the whole universe.
// Every page must belong to the requested date/type and have consistent totals.
export async function loadCompleteBoardClose(
  boardType: Exclude<RankType, 'stock'>,
  tradeDate: string,
  fetchPage: (page: number) => Promise<ApiEnvelope<RankRecord[], PageMeta>>,
): Promise<RankRecord[]> {
  const records: RankRecord[] = []
  const codes = new Set<string>()
  let total: number | undefined
  let pages = 1
  for (let page = 1; page <= pages; page++) {
    const result = await fetchPage(page)
    const meta = result.meta
    const rows = result.data ?? []
    if (!meta || meta.trade_date !== tradeDate || meta.rank_type !== boardType || meta.snapshot_kind !== 'daily_close'
      || meta.page !== page || meta.page_size !== 200 || !Number.isInteger(meta.total) || meta.total < 0
      || meta.pages !== Math.ceil(meta.total / 200) || (total !== undefined && total !== meta.total)
      || rows.length !== Math.min(200, Math.max(0, meta.total - (page - 1) * 200)) || meta.count !== rows.length) {
      throw new Error('昨收榜单分页不完整或截面已变化，请刷新后重试')
    }
    total = meta.total
    pages = meta.pages
    for (const row of rows) {
      if (row.trade_date !== tradeDate || row.rank_type !== boardType || !row.code || codes.has(row.code)) {
        throw new Error('昨收榜单包含重复记录或错误截面，无法比较控盘排名')
      }
      codes.add(row.code)
      records.push(row)
    }
  }
  return records
}
