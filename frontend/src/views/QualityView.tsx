import { useEffect, useState, type ReactNode } from 'react'
import { AlertTriangle, CalendarDays, Check, ChevronLeft, ChevronRight, Info, Server } from 'lucide-react'

import type { CollectionRun, DailyArchiveManifest, QualitySummary, StockArchiveQuality } from '../api/types'

type BoardType = 'industry' | 'concept'

const BOARD_LABELS: Record<BoardType, string> = { industry: '行业', concept: '概念' }
const RUN_PAGE_SIZE = 20

function formatTime(value?: string) {
  if (!value) return '--'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value.slice(11, 16) : date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false, timeZone: 'Asia/Shanghai' })
}

function formatNumber(value: number, digits = 0) {
  if (!Number.isFinite(value)) return '--'
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: digits, minimumFractionDigits: digits }).format(value)
}

export function QualityView({ date, setDate, quality, stockQuality, manifest, runs, loading, error }: {
  date: string
  setDate: (value: string) => void
  quality: Array<QualitySummary & { rank_type: BoardType }>
  stockQuality?: StockArchiveQuality
  manifest?: DailyArchiveManifest
  runs: CollectionRun[]
  loading: boolean
  error: Error | null
}) {
  const [runPage, setRunPage] = useState(1)
  const runPages = Math.ceil(runs.length / RUN_PAGE_SIZE)
  const pageRuns = runs.slice((runPage - 1) * RUN_PAGE_SIZE, runPage * RUN_PAGE_SIZE)
  useEffect(() => setRunPage(1), [date])
  useEffect(() => setRunPage((current) => Math.max(1, Math.min(current, runPages || 1))), [runPages])
  // `||` instead of `??`: an expected_points of 0 would divide to NaN and
  // emit an invalid width style.
  const expectedPoints = stockQuality?.expected_points || 48
  const expectedStocks = stockQuality?.expected_stocks ?? 0
  const expectedKlineStocks = stockQuality?.expected_kline_stocks ?? 0
  const moneyOK = expectedStocks > 0 && stockQuality?.money_rows === expectedStocks * expectedPoints
  // A day where no stock traded has zero expected klines and is complete by
  // definition, not permanently pending.
  const klineOK = expectedKlineStocks === 0 ? Boolean(stockQuality) : stockQuality?.kline_rows === expectedKlineStocks * expectedPoints
  const closeOK = expectedStocks > 0 && stockQuality?.daily_close_rows === expectedStocks
  const dailyKOK = expectedKlineStocks === 0 ? Boolean(stockQuality) : stockQuality?.daily_kline_rows === expectedKlineStocks
  const stockAllOK = moneyOK && klineOK && closeOK && dailyKOK
  const moneyPoints = expectedStocks ? Math.floor((stockQuality?.money_rows ?? 0) / expectedStocks) : 0
  const klinePoints = expectedKlineStocks ? Math.floor((stockQuality?.kline_rows ?? 0) / expectedKlineStocks) : 0
  return <section className="quality-page"><div className="section-heading"><div><p className="eyebrow">运行记录</p><h1>采集质量</h1><span className="subline">检查盘中 240 点，以及盘后资金、行情和完整日终截面的长期归档。</span></div><label className="date-picker"><CalendarDays size={15} /><input type="date" value={date} onChange={(event) => setDate(event.target.value)} /></label></div>{error ? <InlineNotice text="采集质量读取失败，请检查后端服务后重试。" /> : loading ? <div className="loading-block">正在读取质量摘要…</div> : <><ArchiveManifestPanel manifest={manifest} /><div className="quality-grid">{(['industry', 'concept'] as BoardType[]).map((type) => { const item = quality.find((row) => row.rank_type === type); const minutePct = item ? Math.round(item.collected_minutes / Math.max(1, item.expected_minutes) * 100) : 0; const researchPct = item ? Math.round(item.collected_research_snapshots / Math.max(1, item.expected_research_snapshots) * 100) : 0; const boardCloseOK = item?.collected_daily_close_snapshots === item?.expected_daily_close_snapshots && Boolean(item?.expected_daily_close_snapshots); const allOK = minutePct === 100 && researchPct === 100 && boardCloseOK; return <article className="quality-card" key={type}><div className="quality-card-head"><span className="type-tag">{BOARD_LABELS[type]}</span><span className={allOK ? 'ok-label' : 'warn-label'}>{allOK ? <Check size={14} /> : <AlertTriangle size={14} />}{researchPct}%</span></div><div className="quality-metric"><span>盘中分钟</span><strong>{item?.collected_minutes ?? 0}<small> / {item?.expected_minutes ?? 240}</small></strong><div className="progress"><i style={{ width: `${Math.min(minutePct, 100)}%` }} /></div></div><div className="quality-metric"><span>五分钟资金点</span><strong>{item?.collected_research_snapshots ?? 0}<small> / {item?.expected_research_snapshots ?? 48}</small></strong><div className="progress orange"><i style={{ width: `${Math.min(researchPct, 100)}%` }} /></div></div><div className="quality-metric"><span>完整日终截面</span><strong>{item?.collected_daily_close_snapshots ?? 0}<small> / {item?.expected_daily_close_snapshots ?? 1}</small></strong><div className="progress"><i style={{ width: boardCloseOK ? '100%' : '0%' }} /></div></div><div className="missing-list">{!item ? '该交易日暂无归档数据' : item.missing_daily_close_snapshots?.length ? '日终截面缺失，盘中数据将继续保留' : item.missing_research_snapshots?.length ? `缺失资金点 ${item.missing_research_snapshots.slice(0, 5).join('、')}` : item.missing_minutes?.length ? `盘中缺失 ${item.missing_minutes.length} 个分钟点` : '盘中、资金及日终截面完整'}</div></article> })}<article className="quality-card"><div className="quality-card-head"><span className="type-tag">个股</span><span className={stockAllOK ? 'ok-label' : 'warn-label'}>{stockAllOK ? <Check size={14} /> : <AlertTriangle size={14} />}{stockAllOK ? '完整' : '待完成'}</span></div><div className="quality-metric"><span>五分钟资金点</span><strong>{Math.min(moneyPoints, expectedPoints)}<small> / {expectedPoints}</small></strong><div className="progress orange"><i style={{ width: moneyOK ? '100%' : `${Math.min(100, moneyPoints / expectedPoints * 100)}%` }} /></div></div><div className="quality-metric"><span>五分钟行情 K</span><strong>{Math.min(klinePoints, expectedPoints)}<small> / {expectedPoints}</small></strong><div className="progress"><i style={{ width: klineOK ? '100%' : `${Math.min(100, klinePoints / expectedPoints * 100)}%` }} /></div></div><div className="quality-metric"><span>完整日终截面</span><strong>{closeOK ? 1 : 0}<small> / 1</small></strong><div className="progress"><i style={{ width: closeOK ? '100%' : '0%' }} /></div></div><div className="quality-metric"><span>日 K</span><strong>{dailyKOK ? 1 : 0}<small> / 1</small></strong><div className="progress"><i style={{ width: dailyKOK ? '100%' : '0%' }} /></div></div><div className="missing-list">{stockAllOK ? `${expectedStocks} 只资金与日终、${expectedKlineStocks} 只有成交个股行情完整` : '任一长期归档未完成时，盘中工作数据不会清理'}</div></article></div><section className="runs-section"><div className="subsection-heading"><h2>最近运行</h2><span>{runs.length} 条</span></div><div className="run-table-wrap"><table className="run-table"><thead><tr><th>时间</th><th>类型</th><th>榜单</th><th>状态</th><th>记录数</th><th>耗时</th><th>错误</th></tr></thead><tbody>{pageRuns.map((run) => <tr key={run.run_id}><td>{formatTime(run.started_at)}</td><td>{run.snapshot_kind === 'daily_close' ? '日终' : run.snapshot_kind === 'minute_work' ? '分钟' : run.snapshot_kind === 'research_5m' ? '资金归档' : run.snapshot_kind === 'stock_kline_5m' ? '行情归档' : run.snapshot_kind}</td><td>{run.rank_type === 'stock' ? '个股' : BOARD_LABELS[run.rank_type as BoardType]}</td><td><span className={`run-status ${run.status}`}><span />{run.status === 'success' ? '成功' : run.status === 'partial' ? '部分完成' : run.status === 'failed' ? '失败' : run.status}</span></td><td>{formatNumber(run.fetched_total)}</td><td>{formatNumber(run.duration_ms)} ms</td><td className="muted">{run.error_message || '--'}</td></tr>)}</tbody></table>{!runs.length && <EmptyState icon={<Server size={22} />} title="暂无运行记录" detail="选择一个已采集的交易日查看任务状态。" />}</div>{runPages > 1 && <Pagination page={runPage} pages={runPages} setPage={setRunPage} />}</section></>}</section>
}

function ArchiveManifestPanel({ manifest }: { manifest?: DailyArchiveManifest }) {
  const complete = manifest?.status === 'complete'
  const persisted = Boolean(manifest?.updated_at)
  const sources = manifest?.kline_source_counts ?? {}
  const hash = manifest?.code_set_sha256 ? `${manifest.code_set_sha256.slice(0, 12)}…${manifest.code_set_sha256.slice(-8)}` : '--'
  return <section className="manifest-panel"><div className="manifest-heading"><div><span className="type-tag">每日归档清单</span><strong className={complete ? 'ok-label' : 'warn-label'}>{complete ? <Check size={14} /> : <AlertTriangle size={14} />}{complete ? `完整 · v${manifest?.revision_no ?? 0}` : '待完成'}</strong></div><span>{manifest?.updated_at ? `更新 ${formatTime(manifest.updated_at)}` : '暂无清单'}</span></div><div className="manifest-stats"><div><span>代码集合</span><strong>{formatNumber(manifest?.code_count ?? 0)}</strong></div><div><span>集合摘要</span><code title={manifest?.code_set_sha256}>{hash}</code></div><div><span>五分钟主接口</span><strong>{formatNumber(sources.stock_kline_5m ?? 0)} 只</strong></div><div><span>241 点备用</span><strong>{formatNumber(sources.stock_trends_1m_241 ?? 0)} 只</strong></div><div><span>历史未知来源</span><strong>{formatNumber(sources.unknown ?? 0)} 只</strong></div><div><span>解析口径</span><code>{manifest?.parser_version ?? '--'}</code></div></div>{!persisted ? <div className="manifest-pending"><Info size={13} />该交易日尚未生成归档清单</div> : manifest?.validation_errors?.length ? <div className="manifest-errors">{manifest.validation_errors.map((item, index) => <span key={`${index}-${item}`}><AlertTriangle size={13} />{item}</span>)}</div> : <div className="manifest-ok"><Check size={13} />三类日终截面、48 点资金曲线和个股行情覆盖均通过校验</div>}</section>
}

function Pagination({ page, pages, setPage }: { page: number; pages: number; setPage: (value: number) => void }) {
  return <nav className="pagination" aria-label="分页"><button type="button" title="上一页" aria-label="上一页" disabled={page <= 1} onClick={() => setPage(page - 1)}><ChevronLeft size={15} /></button><span>{page} / {pages}</span><button type="button" title="下一页" aria-label="下一页" disabled={page >= pages} onClick={() => setPage(page + 1)}><ChevronRight size={15} /></button></nav>
}

function InlineNotice({ text }: { text: string }) {
  return <div className="inline-notice error"><AlertTriangle size={15} />{text}</div>
}

function EmptyState({ icon, title, detail }: { icon: ReactNode; title: string; detail: string }) {
  return <div className="empty-state">{icon}<strong>{title}</strong><span>{detail}</span></div>
}
