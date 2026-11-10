import { useEffect, useMemo, useRef, useState } from 'react'
import type { RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowUpRight,
  ChevronDown,
  ChevronRight,
  Crown,
  Info,
  Network,
  RefreshCw,
  X,
} from 'lucide-react'

import { api } from './api/client'
import { MetricHint } from './MetricHint'
import type {
  LimitUpTodayCore,
  LimitUpTodayMember,
  LimitUpTodayNetwork,
  LimitUpTodayQuery,
} from './api/limitup'
import {
  calculateGraphCoordinates,
  formatNumber,
  formatPercent,
  formatPercentPoint,
  formatTimeText,
  paginateRecords,
} from './limitupPresentation'
import { linkageQueryErrorMessage } from './linkageNavigation'
import './views/linkage.css'

const CORE_TOP_OPTIONS = [3, 5, 8, 10] as const
const CORE_LOOKBACK_OPTIONS = [30, 90, 180, 365] as const
const CORE_MIN_OVERLAP_OPTIONS = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10] as const
const CORE_CANDIDATE_OPTIONS = [10, 20, 30, 50] as const
const CORE_LINK_PAGE_SIZE = 8

function formatSnapshotTime(value: string | undefined): string {
  if (!value) return '--'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
}

function coreRankClass(rank: number): string {
  return rank <= 3 ? ` top-${rank}` : ''
}

function CoreGraph({ core }: { core: LimitUpTodayCore }) {
  const layout = useMemo(
    () => calculateGraphCoordinates({ code: core.member.code, name: core.member.name }, core.links, 720, 390, 12),
    [core],
  )
  if (layout.neighborNodes.length === 0) {
    return null
  }
  return (
    <svg className="linkage-graph-svg today-core-graph" viewBox="0 0 720 390" role="img" aria-label={`${core.member.name}今日核心联动图`}>
      {layout.edges.map((edgeItem) => (
        <line
          key={`edge-${edgeItem.targetCode}`}
          className="linkage-graph-edge"
          x1={edgeItem.startX}
          y1={edgeItem.startY}
          x2={edgeItem.endX}
          y2={edgeItem.endY}
          strokeWidth={edgeItem.strokeWidth}
          strokeOpacity={edgeItem.strokeOpacity}
        >
          <title>{`${core.member.name} ↔ ${edgeItem.targetName}: 共同 ${edgeItem.overlapDays} 次`}</title>
        </line>
      ))}
      {layout.neighborNodes.map((neighbor) => (
        <g key={`node-${neighbor.code}`} className="linkage-graph-node linkage-graph-node-neighbor">
          <circle cx={neighbor.coordX} cy={neighbor.coordY} r={neighbor.radius} />
          <text x={neighbor.coordX} y={neighbor.coordY - 4}>{neighbor.name.slice(0, 4)}</text>
          <text x={neighbor.coordX} y={neighbor.coordY + 8} style={{ fontSize: '9px', fill: 'var(--muted)' }}>
            {neighbor.code}
          </text>
          <title>{`${neighbor.name} (${neighbor.code}) · 共同 ${neighbor.overlapDays} 次`}</title>
        </g>
      ))}
      {layout.centerNode && (
        <g className="linkage-graph-node linkage-graph-node-center">
          <circle cx={layout.centerNode.coordX} cy={layout.centerNode.coordY} r={layout.centerNode.radius} />
          <text x={layout.centerNode.coordX} y={layout.centerNode.coordY - 5}>{layout.centerNode.name.slice(0, 4)}</text>
          <text x={layout.centerNode.coordX} y={layout.centerNode.coordY + 9} style={{ fontSize: '10px', fill: '#ffffff' }}>
            {layout.centerNode.code}
          </text>
        </g>
      )}
    </svg>
  )
}

function CoreMemberHeader({ member, rank }: { member: LimitUpTodayMember; rank: number }) {
  return (
    <div className="today-core-member-head">
      <span className={`today-core-rank${coreRankClass(rank)}`}>{rank}</span>
      <div className="today-core-member-title">
        <strong>{member.name}</strong>
        <span>{member.code} · {member.market_type}</span>
      </div>
      <div className="today-core-member-quote">
        <span className={member.change_percent !== null && member.change_percent !== undefined && member.change_percent > 0 ? 'positive' : ''}>
          {formatPercentPoint(member.change_percent)}
        </span>
        <small>换手 {formatPercentPoint(member.turnover_rate)}</small>
      </div>
      <div className="today-core-score">
        <strong>{formatNumber(member.core_score, 1)}</strong>
        <span>
          核心度
          <MetricHint label="核心度" formula="100 × (45% 关系强度 + 30% 关系覆盖 + 25% 邻居强度)">
            在当前今日封板成员子图内的综合排序分，满分100。关系强度是本股与其他今日封板股的历史收盘封板共现加权和；关系覆盖是有有效关系的今日成员占比；邻居强度是相连成员自身强度的汇总。三项分别按本次结果中的最大值归一化后再加权。它只描述当前子图中的历史共现位置，不代表因果、带动能力或收益预测。
          </MetricHint>
        </span>
      </div>
    </div>
  )
}

function CoreDetail({ core, rank }: { core: LimitUpTodayCore; rank: number }) {
  const [page, setPage] = useState(1)
  useEffect(() => setPage(1), [core.member.code])
  const paged = paginateRecords(core.links, page, CORE_LINK_PAGE_SIZE)
  const member = core.member
  return (
    <article className="today-core-card">
      <CoreMemberHeader member={member} rank={rank} />
      <div className="today-core-facts">
        <div>
          <span>历史封板 / 触板</span>
          <strong>{member.seal_days} / {member.touch_days} 天</strong>
          <small>统计不含今日快照</small>
        </div>
        <div>
          <span>有效关系</span>
          <strong>{member.effective_links} 只</strong>
          <small>覆盖今日成员 {formatPercent(member.coverage)}</small>
        </div>
        <div>
          <span>关系强度</span>
          <strong>{formatNumber(member.strength, 2)}</strong>
          <small>热度折算共现之和</small>
        </div>
        <div>
          <span>最强关联</span>
          <strong>{member.strongest_peer_name || '--'}</strong>
          <small>
            {member.strongest_peer_code
              ? `${member.strongest_peer_code} · 共同 ${member.strongest_overlap_days} 次`
              : '当前阈值下无关系'}
          </small>
        </div>
        <div>
          <span>今日封板</span>
          <strong>{formatTimeText(member.first_limit_at)}</strong>
          <small>{member.high_days || '未标注几天几板'}</small>
        </div>
      </div>

      <CoreGraph core={core} />

      <div className="today-core-link-head">
        <div>
          <strong>核心联动关系</strong>
          <span>同属今日封板成员；历史按收盘封板共现统计，按热度折算共现排序。</span>
        </div>
        <span>{core.links.length} 只</span>
      </div>
      <div className="linkage-table-wrap">
        <table className="linkage-table today-core-table" aria-label={`${member.name}今日封板成员历史联动关系`}>
          <thead>
            <tr>
              <th>关联股票</th>
              <th>
                <span className="linkage-column-label">
                  共同封板
                  <MetricHint label="共同封板" formula="c" align="start">
                    两个今日封板成员，在历史完整归档日里同一天收盘封板的次数。合作次数少时，下方百分比和提升倍数容易失真。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  P(B|A)
                  <MetricHint label="P(B|A) 条件共现率" formula="c / a" align="start">
                    本核心股历史封板的日子里，关联股也在同一天收盘封板的比例。a 是本核心股的历史封板天数。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  P(A|B)
                  <MetricHint label="P(A|B) 反向共现率" formula="c / b" align="start">
                    关联股历史封板的日子里，本核心股也在同一天收盘封板的比例。b 是关联股的历史封板天数。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  Lift
                  <MetricHint label="提升倍数 (Lift)" formula="(c / a) / (b / N) = cN / (ab)" align="start">
                    关联股在本核心封板日的出现率，相对它历史平时封板频率的倍数。大于1表示更常同日封板，等于1接近独立，小于1表示反而更少同时出现。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  Jaccard
                  <MetricHint label="Jaccard" formula="c / (a + b - c)" align="start">
                    两只股票历史封板日集合的交集除以并集，取值0到1。越高表示重合越多，但它是集合相似度，不是条件概率。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  Phi
                  <MetricHint label="Phi 相关系数" formula="(cN − ab) / √(ab(N−a)(N−b))" align="start">
                    把“当天是否收盘封板”看作0/1序列后的二元相关系数，范围通常为-1到1。正值表示两只股票封板日更常重叠，负值表示更常错开，接近0表示线性相关弱。常量序列或无样本差异时无定义，后端返回 null；它仍是历史描述统计，不代表因果。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  热度折算共现
                  <MetricHint label="热度折算共现" formula="Σ 1 / max(1, 当天全市场封板数 - 1)" align="start">
                    每个共同封板日按当天全市场封板股数降权，减少普涨日对关系强度的放大。分母使用完整归档的封板池，而不是今日成员数。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  样本
                  <MetricHint label="样本充分度" formula="N ≥ 20 且 c ≥ 3" align="start">
                    完整历史样本日至少20天、共同封板至少3次才标为充足。这是展示门槛，不代表统计显著性或因果。
                  </MetricHint>
                </span>
              </th>
            </tr>
          </thead>
          <tbody>
            {paged.paginatedItems.length === 0 && (
              <tr>
                <td colSpan={9} className="muted today-core-table-empty">当前阈值下没有符合条件的今日成员关系。</td>
              </tr>
            )}
            {paged.paginatedItems.map((link) => (
              <tr key={link.code}>
                <td>
                  <strong>{link.name}</strong>
                  <small>{link.code}</small>
                </td>
                <td>{link.overlap_days} 次</td>
                <td>{formatPercent(link.conditional_probability)}</td>
                <td>{formatPercent(link.reverse_probability)}</td>
                <td>{formatNumber(link.lift, 2)}</td>
                <td>{formatNumber(link.jaccard, 3)}</td>
                <td>{link.phi === null || link.phi === undefined ? '--' : formatNumber(link.phi, 3)}</td>
                <td>{formatNumber(link.heat_adjusted_overlap, 2)}</td>
                <td>
                  {link.sample_sufficient ? (
                    <span className="linkage-tag-success">充足</span>
                  ) : (
                    <span className="linkage-tag-warning">不足</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {paged.totalPages > 1 && (
        <div className="linkage-pagination">
          <button
            type="button"
            className="linkage-pagination-btn"
            aria-label="上一页"
            disabled={paged.currentPage <= 1}
            onClick={() => setPage(paged.currentPage - 1)}
          >
            <ChevronRight size={14} style={{ transform: 'rotate(180deg)' }} />
          </button>
          <span>{paged.currentPage} / {paged.totalPages} 页</span>
          <button
            type="button"
            className="linkage-pagination-btn"
            aria-label="下一页"
            disabled={paged.currentPage >= paged.totalPages}
            onClick={() => setPage(paged.currentPage + 1)}
          >
            <ChevronRight size={14} />
          </button>
        </div>
      )}
      <div className="today-core-card-note">
        共现只说明这些股票历史上常在同一个封板日出现，无法证明先后、资金因果或明日延续。
      </div>
    </article>
  )
}

function MemberRoster({ network }: { network: LimitUpTodayNetwork }) {
  const [expanded, setExpanded] = useState(false)
  const visible = expanded ? network.members : network.members.slice(0, 12)
  if (network.members.length === 0) {
    return null
  }
  return (
    <details className="today-core-roster" open={expanded} onToggle={(event) => setExpanded(event.currentTarget.open)}>
      <summary>
        <span>
          <strong>今日封板成员</strong>
          <small>{network.members.length} 只，按核心度排序；无有效历史关系的成员排在后面</small>
        </span>
        <span className="today-core-roster-action">{expanded ? '收起' : '展开'}</span>
      </summary>
      <div className="today-core-chip-grid">
        {visible.map((member, index) => (
          <div className="today-core-chip" key={member.code}>
            <span>{index + 1}</span>
            <strong>{member.name}</strong>
            <code>{member.code}</code>
            <small>封{member.seal_days} · 关{member.effective_links} · 分{formatNumber(member.core_score, 0)}</small>
          </div>
        ))}
      </div>
    </details>
  )
}

function CandidateTable({
  network,
  onOpenStock,
}: {
  network: LimitUpTodayNetwork
  onOpenStock: (code: string, name: string) => void
}) {
  const candidates = network.candidates ?? []
  if (candidates.length === 0) {
    return null
  }
  return (
    <section className="today-core-candidates" aria-labelledby="today-core-candidates-title">
      <div className="today-core-candidates-head">
        <div>
          <h3 id="today-core-candidates-title"><Network size={15} />外溢待选</h3>
          <p>历史上与今日封板成员同日封板、但今日未封板的股票；按核心度加权共现排序，不是推荐。</p>
        </div>
        <span>{candidates.length} 只</span>
      </div>
      <div className="linkage-table-wrap">
        <table className="linkage-table today-core-table today-core-candidate-table" aria-label="今日核心外溢待选股">
          <thead>
            <tr>
              <th>#</th>
              <th>股票</th>
              <th>
                <span className="linkage-column-label">
                  综合分
                  <MetricHint label="外溢综合分" formula="75% × 基础分 + 25% 题材贴合度">
                    基础分按 60% 主线关联度、15% 覆盖度、25% 最近度计算，再压缩到 75%；主线关联度用 φ 相关系数衡量候选股是否专门跟随今日核心，已扣除候选股与今日成员各自的封板频率，并按共现次数收缩稀疏样本，取最强 3 条关系的均值。题材贴合度是候选股命中的今日主线行业/概念板块权重之和，上限 1。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  题材贴合
                  <MetricHint label="题材贴合度" formula="Σ 命中主线权重，上限 1">
                    今日主线取今日封板成员中至少 2 只共同所属的行业/概念板块，按成员核心度加权，行业/概念各取前 5；融资融券、昨日高振幅、连续亏损等宽泛风格或状态标签不参与。候选命中主线板块的归一化权重之和即题材贴合度，未命中为 0。
                  </MetricHint>
                </span>
              </th>
              <th>最强关联核心</th>
              <th>关联覆盖</th>
              <th>历史股性</th>
              <th>最近共现</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {candidates.map((candidate, index) => (
              <tr key={candidate.code}>
                <td>{index + 1}</td>
                <td>
                  <strong>{candidate.name}</strong>
                  <small>{candidate.code}</small>
                </td>
                <td><strong>{formatNumber(candidate.score, 1)}</strong></td>
                <td>
                  <strong>{formatNumber((candidate.theme_score ?? 0) * 100, 0)}</strong>
                  <small>
                    {(candidate.themes ?? []).length > 0
                      ? (candidate.themes ?? []).slice(0, 2).map((theme) => theme.name).join('、')
                      : '未匹配主线'}
                  </small>
                </td>
                <td>
                  <strong>{candidate.strongest_core_name || '--'}</strong>
                  <small>
                    共同 {candidate.strongest_overlap_days} 次
                    {candidate.strongest_phi > 0 ? ` · 贴合 ${formatNumber(candidate.strongest_phi * 100, 0)}` : ''}
                  </small>
                </td>
                <td>
                  <strong>{candidate.effective_links} 只</strong>
                  <small>{formatPercent(candidate.coverage, 0)}</small>
                </td>
                <td>
                  <strong>封 {candidate.seal_days} · 触 {candidate.touch_days}</strong>
                  <small>{candidate.last_seal_date || '--'}</small>
                </td>
                <td>{candidate.last_shared_date || '--'}</td>
                <td>
                  <button
                    type="button"
                    className="today-core-candidate-open"
                    onClick={() => onOpenStock(candidate.code, candidate.name)}
                  >
                    联动
                    <ArrowUpRight size={12} />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="today-core-card-note">
        外溢待选只表示历史共现关系，不包含今日实时涨幅或资金确认；请结合盘口自行判断。
      </div>
    </section>
  )
}

export function TodayCorePanel({
  authRequired,
  defaultDays,
  defaultScope,
  onClose,
  onOpenStock,
  onOpenQuality,
  returnFocusRef,
}: {
  authRequired: boolean
  defaultDays: number
  defaultScope: LimitUpTodayNetwork['scope']
  onClose: () => void
  onOpenStock: (code: string, name: string) => void
  onOpenQuality?: () => void
  returnFocusRef?: RefObject<HTMLButtonElement | null>
}) {
  const [lookbackDays, setLookbackDays] = useState<number>(
    CORE_LOOKBACK_OPTIONS.includes(defaultDays as (typeof CORE_LOOKBACK_OPTIONS)[number]) ? defaultDays : 365,
  )
  const [marketScope, setMarketScope] = useState<LimitUpTodayNetwork['scope']>(defaultScope)
  const [minOverlap, setMinOverlap] = useState(1)
  const [top, setTop] = useState<number>(5)
  const [candidateLimit, setCandidateLimit] = useState<number>(20)
  const [selectedCode, setSelectedCode] = useState('')
  const panelRef = useRef<HTMLElement>(null)
  const closeRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    closeRef.current?.focus()
  }, [])

  useEffect(() => {
    if (!panelRef.current) return
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
      }
    }
    const keepFocusInside = (event: KeyboardEvent) => {
      if (event.key !== 'Tab' || !panelRef.current) return
      const focusable = Array.from(
        panelRef.current.querySelectorAll<HTMLElement>(
          'button:not(:disabled), select:not(:disabled), summary, a[href], [tabindex]:not([tabindex="-1"])',
        ),
      )
      if (focusable.length === 0) return
      const first = focusable[0]
      const last = focusable.at(-1)
      if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      } else if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last?.focus()
      }
    }
    document.addEventListener('keydown', closeOnEscape)
    document.addEventListener('keydown', keepFocusInside)
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', closeOnEscape)
      document.removeEventListener('keydown', keepFocusInside)
      document.body.style.overflow = previousOverflow
    }
  }, [onClose])

  useEffect(() => () => {
    window.requestAnimationFrame(() => returnFocusRef?.current?.focus())
  }, [returnFocusRef])

  const queryParams: LimitUpTodayQuery = {
    days: lookbackDays,
    scope: marketScope,
    min_overlap: minOverlap,
    top,
    candidates: candidateLimit,
  }
  const networkQuery = useQuery({
    queryKey: ['limit-up-today-network', lookbackDays, marketScope, minOverlap, top, candidateLimit],
    queryFn: ({ signal }) => api.limitUpTodayNetwork(queryParams, signal),
    enabled: !authRequired,
    retry: false,
    refetchInterval: false,
    staleTime: 0,
  })
  const network = networkQuery.data?.data
  const cores = useMemo(() => network?.cores ?? [], [network?.cores])
  const activeCore = cores.find((core) => core.member.code === selectedCode) ?? cores[0]
  const activeRank = activeCore ? cores.findIndex((core) => core.member.code === activeCore.member.code) + 1 : 0

  useEffect(() => {
    if (!network) return
    if (cores.length === 0) {
      if (selectedCode) setSelectedCode('')
      return
    }
    if (!cores.some((core) => core.member.code === selectedCode)) {
      setSelectedCode(cores[0].member.code)
    }
  }, [network, cores, selectedCode])

  return (
    <div
      className="today-core-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <section
        ref={panelRef}
        className="today-core-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="today-core-title"
      >
        <header className="today-core-head">
          <div>
            <p className="linkage-eyebrow">盘中实时 · 历史收盘封板关系</p>
            <h2 id="today-core-title"><Crown size={20} />今日涨停核心</h2>
            <p>
              只分析点击时仍封住涨停的股票，并在这些成员之间回看历史收盘封板共现。今日盘中快照不写入历史，也不进入历史分母。
            </p>
          </div>
          <button ref={closeRef} type="button" className="linkage-quick-close" onClick={onClose} aria-label="关闭今日核心" title="关闭（Esc）">
            <X size={17} />
          </button>
        </header>

        <div className="today-core-controls">
          <label>
            <span>历史回看</span>
            <select value={lookbackDays} onChange={(event) => setLookbackDays(Number(event.target.value))}>
              {CORE_LOOKBACK_OPTIONS.map((option) => <option key={option} value={option}>{option} 自然日</option>)}
            </select>
          </label>
          <label>
            <span>市场范围</span>
            <select value={marketScope} onChange={(event) => setMarketScope(event.target.value as LimitUpTodayNetwork['scope'])}>
              <option value="HS,GEM2STAR">沪深及双创</option>
              <option value="HS,GEM2STAR,ST,NEW">含 ST、未开板新股</option>
            </select>
          </label>
          <label>
            <span>最少共同</span>
            <select value={minOverlap} onChange={(event) => setMinOverlap(Number(event.target.value))}>
              {CORE_MIN_OVERLAP_OPTIONS.map((option) => <option key={option} value={option}>≥ {option} 次</option>)}
            </select>
          </label>
          <label>
            <span>核心数</span>
            <select value={top} onChange={(event) => setTop(Number(event.target.value))}>
              {CORE_TOP_OPTIONS.map((option) => <option key={option} value={option}>Top {option}</option>)}
            </select>
          </label>
          <label>
            <span>待选数</span>
            <select value={candidateLimit} onChange={(event) => setCandidateLimit(Number(event.target.value))}>
              {CORE_CANDIDATE_OPTIONS.map((option) => <option key={option} value={option}>Top {option}</option>)}
            </select>
          </label>
          <button
            type="button"
            className="today-core-refresh"
            onClick={() => void networkQuery.refetch()}
            disabled={networkQuery.isFetching}
            title="重新读取一次实时封板快照"
          >
            <RefreshCw size={14} className={networkQuery.isFetching ? 'spin' : ''} />
            {networkQuery.isFetching ? '读取中' : '刷新快照'}
          </button>
        </div>

        <div className="today-core-body" aria-live="polite">
          {authRequired && (
            <div className="today-core-empty">
              <AlertTriangle size={22} />
              <strong>需要访问令牌</strong>
              <span>验证令牌后才会读取实时涨停快照。</span>
            </div>
          )}

          {!authRequired && networkQuery.isLoading && (
            <div className="today-core-loading">
              <span className="today-core-loading-ring" />
              正在读取当前封板池并匹配历史归档…
            </div>
          )}

          {!authRequired && networkQuery.isError && (
            <div className="today-core-error">
              <AlertTriangle size={16} />
              <div>
                <strong>今日核心读取失败</strong>
                <span>{linkageQueryErrorMessage(networkQuery.error)}</span>
                <small>该功能需要盘中实时涨停接口与本地历史事件归档同时可用；页面不会自动采集或回补历史。</small>
              </div>
            </div>
          )}

          {network && (
            <>
              <div className="today-core-summary">
                <div className="today-core-snapshot">
                  <span className={`today-core-live-dot ${network.trade_status === 'open' ? 'open' : ''}`} />
                  <div>
                    <strong>{network.trade_status_name || network.trade_status || '状态未知'}</strong>
                    <span>{formatSnapshotTime(network.snapshot_at)} 快照 · 今日仍封板 {network.sealed_count} 只</span>
                  </div>
                </div>
                <div>
                  <span>历史关系截止</span>
                  <strong>{network.history_as_of || '暂无完整归档'}</strong>
                  <small>严格早于 {network.trade_date}</small>
                </div>
                <div>
                  <span>历史样本</span>
                  <strong>{network.coverage.observed_days} 个完整日</strong>
                  <small>
                    {network.coverage.first_date && network.coverage.latest_date
                      ? `${network.coverage.first_date} ~ ${network.coverage.latest_date}`
                      : `回看 ${network.lookback_days} 自然日`}
                  </small>
                  <small>自然日窗口 {network.coverage.from_date || '--'} ~ {network.history_as_of || '--'}</small>
                </div>
                <div>
                  <span>有效核心</span>
                  <strong>{cores.length} 只</strong>
                  <small>至少 1 条 ≥{network.min_overlap} 次关系</small>
                </div>
              </div>

              {(network.mainline_themes ?? []).length > 0 && (
                <div className="today-core-mainlines">
                  <span className="today-core-mainlines-label">
                    今日主线
                    <MetricHint label="今日主线" formula="至少 2 只今日封板成员共同所属，按核心度加权">
                      今日封板成员中至少 2 只共同所属的行业/概念板块；行业与概念各取加权成员数前 5。融资融券、昨日高振幅、连续亏损等宽泛风格或状态标签会被剔除，避免几乎所有股票都命中主线。
                    </MetricHint>
                  </span>
                  {(network.mainline_themes ?? []).map((theme) => (
                    <span
                      key={`${theme.type}:${theme.code}`}
                      className={`today-core-mainline ${theme.type}`}
                      title={`${theme.type === 'industry' ? '行业' : '概念'} · 权重 ${formatNumber(theme.weight, 2)}`}
                    >
                      {theme.name}
                      <small>{theme.member_count} 只封板</small>
                    </span>
                  ))}
                </div>
              )}

              <div className="today-core-caveat">
                <Info size={14} />
                <span>
                  快照会随开板、回封而变化，刷新后成员和排名可能不同。历史关系仅使用 {network.history_as_of || '最近完整日'} 及更早的收盘封板归档；今日实时封板不参与统计。共现不代表因果、带动或未来收益。
                </span>
              </div>

              {!network.history_available && (
                <div className="today-core-empty compact">
                  <Network size={22} />
                  <strong>今日之前的完整事件归档不足</strong>
                  <span>当前可展示实时封板成员，但无法计算历史收盘封板关系。请到“采集质量”确认历史归档是否已启用。</span>
                  {onOpenQuality && (
                    <button type="button" className="today-core-quality-link" onClick={onOpenQuality}>
                      查看采集质量
                    </button>
                  )}
                </div>
              )}

              {network.history_available && cores.length === 0 && (
                <div className="today-core-empty">
                  <Network size={22} />
                  <strong>当前阈值下没有形成历史关系网</strong>
                  <span>今日封板成员之间在所选回看窗口内没有达到最少共同次数的收盘封板共现，可降低阈值或扩大回看窗口。</span>
                </div>
              )}

              <MemberRoster network={network} />

              {cores.length > 0 && (
                <>
                  <div className="today-core-tabs" role="tablist" aria-label="核心股排名">
                    {cores.map((core, index) => (
                      <button
                        key={core.member.code}
                        type="button"
                        role="tab"
                        aria-selected={activeCore?.member.code === core.member.code}
                        className={activeCore?.member.code === core.member.code ? 'active' : ''}
                        onClick={() => setSelectedCode(core.member.code)}
                      >
                        <span className={`today-core-tab-rank${coreRankClass(index + 1)}`}>{index + 1}</span>
                        <span>
                          <strong>{core.member.name}</strong>
                          <small>{formatNumber(core.member.core_score, 1)} 分 · {core.links.length} 关系</small>
                        </span>
                      </button>
                    ))}
                  </div>
                  {activeCore && <CoreDetail key={activeCore.member.code} core={activeCore} rank={activeRank} />}
                </>
              )}

              <CandidateTable network={network} onOpenStock={onOpenStock} />

              <div className="today-core-actions">
                <span>想看某只股自己的完整股性？可直接打开股票联动。</span>
                {activeCore && (
                  <button type="button" onClick={() => onOpenStock(activeCore.member.code, activeCore.member.name)}>
                    查看 {activeCore.member.name} 完整联动
                    <ArrowUpRight size={14} />
                  </button>
                )}
              </div>
            </>
          )}
        </div>

        <footer className="today-core-footer">
          <span>数据来源：同花顺实时涨停池 + 本地盘后完整归档。不会自动刷新，也不会把盘中快照写成日终归档。</span>
          <button type="button" onClick={onClose}>
            <ChevronDown size={14} />
            关闭面板
          </button>
        </footer>
      </section>
    </div>
  )
}
