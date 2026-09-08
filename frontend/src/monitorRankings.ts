import type { RankRecord, RankType } from './api/types'

type BoardType = Exclude<RankType, 'stock'>
type MoneyRecord = Pick<RankRecord, 'money_available' | 'dark_money' | 'dark_activity' | 'main_money_inflow'>
export type MonitorSortKey = 'rank' | 'name' | 'code' | 'dark_money' | 'regular_money' | 'main_money_inflow' | 'derived_turnover' | 'control_rate' | 'control_rank' | 'control_rank_change' | 'change_pct' | 'dark_activity' | 'dark_inflow_ratio'
export type ControlRankChange = {
  currentRank: number | null
  previousRank: number | null
  delta: number | null
  state: 'compared' | 'new' | 'unavailable'
}

// Both snapshots use the same inferred turnover, rather than mixing today's
// estimate with yesterday's enriched quote. Activity is an unscaled decimal.
export function derivedBoardTurnover(record: Pick<MoneyRecord, 'money_available' | 'dark_money' | 'dark_activity'>): number | null {
  if (!record.money_available || !Number.isFinite(record.dark_money) || !Number.isFinite(record.dark_activity) || record.dark_activity <= 0) return null
  const turnover = Math.abs(record.dark_money) / record.dark_activity
  return Number.isFinite(turnover) && turnover > 0 ? turnover : null
}

export function controlRate(record: MoneyRecord, turnover = derivedBoardTurnover(record)): number | null {
  if (!record.money_available || turnover === null || !Number.isFinite(turnover) || turnover <= 0 || !Number.isFinite(record.main_money_inflow)) return null
  // main_money_inflow already includes dark money; do not add it again.
  const value = record.main_money_inflow / turnover * 100
  return Number.isFinite(value) ? value : null
}

export function rankControlRates(records: readonly RankRecord[], boardType: BoardType): Map<string, number> {
  const sorted = records.filter((record) => record.rank_type === boardType)
    .map((record) => ({ code: record.code, value: controlRate(record) }))
    .filter((item): item is { code: string; value: number } => item.value !== null)
    .sort((a, b) => b.value - a.value || a.code.localeCompare(b.code))
  const ranks = new Map<string, number>()
  let rank = 0
  sorted.forEach((item, index) => {
    // Compare unrounded values. Exact ties share a competition rank (1,1,3),
    // so upstream row ordering cannot create spurious day-over-day movement.
    if (index === 0 || item.value !== sorted[index - 1].value) rank = index + 1
    ranks.set(item.code, rank)
  })
  return ranks
}

export function compareControlRanks(current: readonly RankRecord[], previous: readonly RankRecord[] | undefined, boardType: BoardType): Map<string, ControlRankChange> {
  const currentRanks = rankControlRates(current, boardType)
  const previousRanks = rankControlRates(previous ?? [], boardType)
  const previousCodes = new Set(previous?.filter((record) => record.rank_type === boardType).map((record) => record.code))
  return new Map(current.filter((record) => record.rank_type === boardType).map((record) => {
    const currentRank = currentRanks.get(record.code) ?? null
    const previousRank = previousRanks.get(record.code) ?? null
    const delta = currentRank !== null && previousRank !== null ? previousRank - currentRank : null
    const state = delta !== null ? 'compared'
      : currentRank !== null && previousRanks.size > 0 && !previousCodes.has(record.code) ? 'new' : 'unavailable'
    return [record.code, { currentRank, previousRank, delta, state }]
  }))
}

export function compareMonitorRecords(a: RankRecord, b: RankRecord, sort: { key: MonitorSortKey; direction: 'asc' | 'desc' }, changes: ReadonlyMap<string, ControlRankChange>): number {
  const value = (record: RankRecord) => sort.key === 'derived_turnover' ? derivedBoardTurnover(record)
    : sort.key === 'control_rate' ? controlRate(record)
      : sort.key === 'control_rank' ? changes.get(record.code)?.currentRank ?? null
        : sort.key === 'control_rank_change' ? changes.get(record.code)?.delta ?? null : record[sort.key]
  const left = value(a), right = value(b)
  const missing = (item: unknown) => item == null || (typeof item === 'number' && !Number.isFinite(item))
  const leftMissing = missing(left), rightMissing = missing(right)
  // Unavailable is not zero and must sort after valid negative values too.
  if (leftMissing !== rightMissing) return leftMissing ? 1 : -1
  if (leftMissing && rightMissing) return a.code.localeCompare(b.code)
  const compared = typeof left === 'string' ? left.localeCompare(String(right), 'zh-CN') : Number(left) - Number(right)
  return (sort.direction === 'asc' ? compared : -compared) || a.code.localeCompare(b.code)
}

export function controlRankChangeDisplay(change: ControlRankChange | undefined, tradeDate: string, previousDate: string, loading = false) {
  if (loading) return { label: '…', tone: 'muted', title: `正在读取 ${previousDate} 收盘控盘度排名` }
  if (!change || change.state === 'unavailable') {
    return { label: '--', tone: 'muted', title: '今日或上一个交易日的控盘度数据不可用，无法比较（不代表排名持平）' }
  }
  if (change.state === 'new') {
    return { label: '新入榜', tone: 'rank-change-new', title: `${tradeDate} 控盘度第 ${change.currentRank} 名；${previousDate} 完整收盘榜中无此板块/概念` }
  }
  const delta = change.delta ?? 0
  return {
    label: delta > 0 ? `↑${delta}` : delta < 0 ? `↓${Math.abs(delta)}` : '—',
    tone: delta > 0 ? 'positive' : delta < 0 ? 'negative' : 'muted',
    title: `控盘度排名：${previousDate} 收盘第 ${change.previousRank} 名 → ${tradeDate} 最新第 ${change.currentRank} 名；${delta > 0 ? `上升 ${delta} 位` : delta < 0 ? `下降 ${Math.abs(delta)} 位` : '排名持平'}`,
  }
}
