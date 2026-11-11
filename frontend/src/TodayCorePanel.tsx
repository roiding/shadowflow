import { useEffect, useId, useMemo, useRef, useState } from 'react'
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

function formatSignedPercentPoint(value: number | null | undefined, fractionDigits = 2): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '--'
  return `${value > 0 ? '+' : ''}${value.toFixed(fractionDigits)}%`
}

function formatMoney(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '--'
  const absolute = Math.abs(value)
  if (absolute >= 100_000_000) return `${(value / 100_000_000).toFixed(2)}亿`
  if (absolute >= 10_000) return `${(value / 10_000).toFixed(0)}万`
  return value.toFixed(0)
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
          <MetricHint label="核心度" formula="100 × (35% 关系强度 + 25% 关系覆盖 + 20% 邻居强度 + 20% 同题材聚类)">
            在当前今日封板成员子图内的综合排序分，满分100。关系强度是本股与其他今日封板股的历史收盘封板共现加权和；关系覆盖是有有效关系的今日成员占比；邻居强度是相连成员自身强度的汇总；同题材聚类是与今日其他成员共享有效题材标签的 IDF 加权强度。个股优先使用同花顺 Top20 历史标签；没有有效历史标签时，使用本地东财概念/行业关系兜底。四项分别按本次结果中的最大值归一化后再加权；没有有效题材标签时，题材项按 0 处理，其余三项重新归一化。它只描述当前子图中的历史关系与题材位置，不代表因果、带动能力或收益预测。
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
      <div className="today-core-theme-tags">
        <span>题材聚类</span>
        {(member.theme_tags ?? []).length > 0 ? (
          (member.theme_tags ?? []).map((tag) => (
            <span
              className="today-core-theme-tag"
              key={tag.code}
              title={tag.source === 'ths'
                ? `同花顺 Top20 · ${tag.occurrences} 次 · 最近 ${tag.last_date}`
                : `东财关系兜底 · ${tag.source === 'eastmoney_industry' ? '行业' : '概念'} · 当前关系`}
            >
              {tag.name}
              <small>{tag.source === 'ths' ? `${tag.occurrences}次` : '东财'}</small>
            </span>
          ))
        ) : (
          <small>该股在回看窗口内没有有效的同花顺 Top20 题材标签，当前也没有可用的东财概念/行业兜底关系。</small>
        )}
      </div>
      <div className="today-core-facts">
        <div>
          <span>历史封板 / 触板</span>
          <strong>{member.seal_days} / {member.touch_days} 天</strong>
          <small>统计不含今日快照</small>
        </div>
        <div>
          <span>有效关系</span>
          <strong>{member.effective_links} 只</strong>
          <small>覆盖今日成员 {formatPercent(member.coverage)} · 同题材邻居 {member.theme_neighbor_count ?? 0} 只</small>
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
                <td colSpan={9} className="muted today-core-table-empty">
                  当前阈值下没有符合条件的收盘封板关系；可参考上方同花顺题材标签和同题材邻居。
                </td>
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
            <small>封{member.seal_days} · 关{member.effective_links} · 题{member.theme_neighbor_count ?? 0} · 分{formatNumber(member.core_score, 0)}</small>
            {(member.theme_tags ?? []).length > 0 && (
              <small className="today-core-chip-themes">
                {(member.theme_tags ?? []).slice(0, 2).map((theme) => theme.name).join('、')}
              </small>
            )}
          </div>
        ))}
      </div>
    </details>
  )
}

function CandidateTable({
  network,
  onOpenStock,
  candidates: candidateInput,
  title = '外溢待选',
  description = '先按实时多主线与历史特异性选股，再用启动承接、剩余空间、换手、量比、主力和暗盘资金排序；换手、主力和暗盘资金按个股自身同一时点历史分位，量比直接使用东财自身基准，已触板未封会扣分，近期涨停次数本身不加分，不是推荐。',
}: {
  network: LimitUpTodayNetwork
  onOpenStock: (code: string, name: string) => void
  candidates?: LimitUpTodayNetwork['candidates']
  title?: string
  description?: string
}) {
  const candidates = candidateInput ?? network.candidates ?? []
  const headingId = useId()
  const mainlineNameByCode = useMemo(
    () => new Map((network.theme_mainline ?? []).map((theme) => [theme.code, theme.name])),
    [network.theme_mainline],
  )
  if (candidates.length === 0) {
    return null
  }
  return (
    <section className="today-core-candidates" aria-labelledby={headingId}>
      <div className="today-core-candidates-head">
        <div>
          <h3 id={headingId}><Network size={15} />{title}</h3>
          <p>{description}</p>
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
                  <MetricHint label="外溢综合分" formula="100 × (30% 盘中承接 + 40% 主线项 + 20% 历史 + 10% 覆盖) × 风险折价">
                    有候选行情时，盘中承接占 30%、主线项占 40%、历史特异性占 20%、关联覆盖占 10%。主线项在单主线表内使用该主线的贴合分，在跨主线表内使用多主线共振分。盘中承接衡量启动后的承接质量与剩余空间，不奖励接近涨停，也不把触板当成加分；已触板未封会扣分。换手、主力和暗盘资金使用每只股票最近20个交易日、同一5分钟时点的自身历史分位，样本少于5日时不参与该项，不用其他股票补位；量比直接使用东财已按个股自身近5日基准计算的字段，不再做跨股票分位。历史特异性用 φ 相关系数衡量候选是否专门跟随今日核心，已扣除双方各自封板频率并收缩稀疏样本。ST、上市不足60日、低换手、追涨和资金流出等风险只做最多 15% 的折价，不作为正分。股票近一年涨停次数本身不直接加分；行情不可用时实时项退出，其余三项重新归一化并明确标记为“仅历史观察”。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  盘中确认
                  <MetricHint label="盘中确认" formula="启动承接 30% + 剩余空间 20% + 换手自身历史分位 15% + 量比 10% + 主力资金自身历史分位 10% + 暗盘资金自身历史分位 15%；触板未封扣 20%">
                    先按“盘中确认满分且无风险折价”的理论上界分批读取东财行情、暗盘 tick 和自身历史，只有当前第 N 名真实分严格高于所有未取数候选的上界才停止；同分继续读取，数据不完整会明确失败。启动承接按涨幅相对涨停幅度的早期确认计算，剩余空间按距涨停的绝对空间计算；不再把涨幅进度或触板当成正向加分。换手、主力和暗盘资金按每只股票最近20个交易日、同一5分钟时点的自身历史计算分位，避免把不同股票的日常流动性水平放在一起比较；样本少于5日时不参与该项，不借用其他股票的分位。量比直接使用东财 f10 的自身基准值，它已经是个股当前累计成交量相对自身近期同时段均量的比较。距涨停按前收和板块涨跌幅计算：主板普通与主板 ST 10%、创业板/科创板 20%、北交所 30%。盘中最高价达到按前收四舍五入后的涨停价即标记触板；已触板但当前未封属于失败板观察，盘中确认扣 20%。缺少行情时本列显示“仅历史观察”，不能把历史分当成盘中确认。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  多主线共振
                  <MetricHint label="多主线共振" formula="1 − Π(1 − 单主线贴合)">
                    多主线共振把候选命中的每一条实时主线当作独立证据：同时命中越多，分数越高但边际收益递减。它不再使用最强80%+次强20%的固定主次结构。
                  </MetricHint>
                </span>
              </th>
              <th>
                <span className="linkage-column-label">
                  历史特异性
                  <MetricHint label="历史特异性" formula="top3 φ × 核心度 × o/(o+2) 的均值">
                    φ 衡量候选与今日封板核心的同日封板关联是否高于双方各自封板频率带来的偶然共现。负值截零，单次共现按 o/(o+2) 收缩；只保留最强 3 条关系取均值，并在本批候选中归一化。该项只看历史关系，不等于今日实时强度。
                  </MetricHint>
                </span>
              </th>
              <th>关联核心 / 覆盖</th>
              <th>历史股性</th>
              <th>
                <span className="linkage-column-label">
                  风险扣分
                  <MetricHint label="风险扣分" formula="0 = 未命中风险规则；越高越需要人工复核">
                    风险扣分由盘中追涨、已触板未封、距涨停空间不足、主力或暗盘资金净流出、资金处于自身历史后10%、换手处于自身历史前10%、量比过热、历史样本不足、ST、上市不足60日、低流动性等规则累计；它不是买入信号。风险只让综合分最多折价 15%，具体扣分和命中原因逐行展示。
                  </MetricHint>
                </span>
              </th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {candidates.map((candidate, index) => {
              const riskFlags = candidate.risk_flags ?? []
              const riskDiscount = (1 - (candidate.risk_score ?? 1)) * 15
              const riskPenalty = candidate.risk_penalty ?? (1 - (candidate.risk_score ?? 1)) * 100
              const mainlineValue = candidate.mainline_score ?? 0
              const matchedMainlines = (candidate.mainline_codes ?? [])
                .map((code) => mainlineNameByCode.get(code))
                .filter((name): name is string => Boolean(name))
              return (
                <tr key={candidate.code}>
                  <td>{index + 1}</td>
                  <td>
                    <strong>{candidate.name}</strong>
                    <small>{candidate.code}</small>
                  </td>
                  <td>
                    <strong>{formatNumber(candidate.score, 1)}</strong>
                    <small className="today-core-subscore">
                      主 {formatNumber(mainlineValue * 100, 0)}
                      {' · '}实 {formatNumber((candidate.live_score ?? 0) * 100, 0)}
                      {' · '}史 {formatNumber((candidate.history_score ?? 0) * 100, 0)}
                    </small>
                  </td>
                  <td className="today-core-candidate-live">
                    {candidate.live_available ? (
                      <>
                        <strong className={(candidate.change_percent ?? 0) > 0 ? 'positive' : (candidate.change_percent ?? 0) < 0 ? 'negative' : ''}>
                          {formatSignedPercentPoint(candidate.change_percent)}
                        </strong>
                        <small>
                          距涨停 {formatPercentPoint(candidate.distance_to_limit)}
                          {candidate.touched_limit ? ' · 已触板' : ' · 未触板'}
                        </small>
                        <small>
                          换手 {formatPercentPoint(candidate.turnover_rate)}
                          {candidate.volume_ratio !== null && candidate.volume_ratio !== undefined
                            ? ` · 量比 ${formatNumber(candidate.volume_ratio, 2)}`
                            : ' · 量比 --'}
                          {candidate.turnover_amount !== null && candidate.turnover_amount !== undefined
                            ? ` · 成交 ${formatMoney(candidate.turnover_amount)}`
                            : ''}
                        </small>
                        {(candidate.turnover_percentile !== null && candidate.turnover_percentile !== undefined) ||
                        (candidate.main_flow_percentile !== null && candidate.main_flow_percentile !== undefined) ||
                        (candidate.dark_money_percentile !== null && candidate.dark_money_percentile !== undefined) ? (
                          <small>
                            自身历史分位 换 {formatNumber(candidate.turnover_percentile, 0)}
                            {' · '}主力 {formatNumber(candidate.main_flow_percentile, 0)}
                            {' · '}暗盘 {formatNumber(candidate.dark_money_percentile, 0)}
                          </small>
                        ) : null}
                        {candidate.main_net_inflow_pct !== null && candidate.main_net_inflow_pct !== undefined ? (
                          <small className={candidate.main_net_inflow_pct > 0 ? 'positive' : candidate.main_net_inflow_pct < 0 ? 'negative' : ''}>
                            主力 {formatPercentPoint(candidate.main_net_inflow_pct)}
                            {candidate.main_net_inflow !== null && candidate.main_net_inflow !== undefined
                              ? ` · ${formatMoney(candidate.main_net_inflow)}`
                              : ''}
                          </small>
                        ) : (
                          <small>主力资金 --</small>
                        )}
                        {candidate.dark_money !== null && candidate.dark_money !== undefined ? (
                          <small className={candidate.dark_money > 0 ? 'positive' : candidate.dark_money < 0 ? 'negative' : ''}>
                            暗盘 {formatMoney(candidate.dark_money)}
                            {candidate.dark_money_pct !== null && candidate.dark_money_pct !== undefined
                              ? ` · 占成交 ${formatPercentPoint(candidate.dark_money_pct)}`
                              : ''}
                          </small>
                        ) : (
                          <small>暗盘资金 --</small>
                        )}
                      </>
                    ) : (
                      <>
                        <span className="today-core-observe">仅历史观察</span>
                        <small>{network.live_candidate ? '该股行情缺失' : '未读取候选行情'}</small>
                      </>
                    )}
                  </td>
                  <td>
                    <strong>{formatNumber(mainlineValue * 100, 0)}</strong>
                    {matchedMainlines.length > 0 ? (
                      <div className="today-core-candidate-mainlines" title={matchedMainlines.join('、')}>
                        {matchedMainlines.map((name) => (
                          <span
                            key={name}
                            className={/CPO|通信|5G|光|数据中心/.test(name) ? 'communication' : ''}
                          >
                            {name}
                          </span>
                        ))}
                      </div>
                    ) : (
                      <small>未匹配主线</small>
                    )}
                    {(candidate.cross_mainline_count ?? 0) > 1 && (
                      <small>跨 {candidate.cross_mainline_count} 条主线</small>
                    )}
                  </td>
                  <td>
                    <strong>{formatNumber((candidate.history_score ?? 0) * 100, 0)}</strong>
                    <small>
                      {candidate.strongest_core_name
                        ? `核心 ${candidate.strongest_core_name} · 共同 ${candidate.strongest_overlap_days} 次`
                        : '仅题材关联，无直接共封'}
                    </small>
                    <small>
                      {candidate.strongest_phi > 0 ? `φ ${formatNumber(candidate.strongest_phi, 2)}` : 'φ --'}
                      {candidate.last_shared_date ? ` · 最近 ${candidate.last_shared_date}` : ''}
                    </small>
                  </td>
                  <td>
                    <strong>{candidate.effective_links} 只</strong>
                    <small>覆盖 {formatPercent(candidate.coverage, 0)}</small>
                  </td>
                  <td>
                    <strong>封 {candidate.seal_days} · 触 {candidate.touch_days}</strong>
                    <small>最近封板 {candidate.last_seal_date || '--'}</small>
                  </td>
                  <td className="today-core-candidate-risk">
                    <strong>{formatNumber(riskPenalty, 0)}</strong>
                    <small>{riskDiscount > 0 ? `综合分折价 ${formatNumber(riskDiscount, 1)} 分` : '无风险折价'}</small>
                    <div className="today-core-risk-flags">
                      {riskFlags.length > 0 ? riskFlags.slice(0, 2).map((flag) => (
                        <span key={flag} title={flag}>{flag}</span>
                      )) : <span className="clear">未见触发项</span>}
                      {riskFlags.length > 2 && <span title={riskFlags.slice(2).join('、')}>+{riskFlags.length - 2}</span>}
                    </div>
                  </td>
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
              )
            })}
          </tbody>
        </table>
      </div>
      {!network.live_candidate && (
        <div className="today-core-card-note warning">
          当前未取得候选股实时行情：本表只是历史主线与历史特异性排序，不能视为盘中确认或“可买”。
        </div>
      )}
      {network.live_candidate && network.live_unavailable && (
        <div className="today-core-card-note warning">
          部分候选没有取到实时行情；这些行仅作历史观察，不参与“盘中已确认”的判断。
        </div>
      )}
      <div className="today-core-card-note">
        实时行情按理论上界分批读取，只有证明未取数候选不能反超才停止；数据不完整会明确失败。行情不写入归档。没有直接共封记录的题材候选会保留“仅题材关联”标记；所有分数只用于排序，不是买卖建议。
      </div>
    </section>
  )
}

function MainlineCandidateSections({
  network,
  onOpenStock,
}: {
  network: LimitUpTodayNetwork
  onOpenStock: (code: string, name: string) => void
}) {
  const candidates = network.candidates ?? []
  const groups = network.mainline_candidates ?? []
  if (candidates.length === 0) {
    return null
  }
  const mainlineCount = (network.theme_mainline ?? []).length || groups.length
  const crossCount = candidates.filter((candidate) => (candidate.cross_mainline_count ?? 0) > 1).length
  return (
    <>
      <div className="today-core-card-note">
        已把 {mainlineCount} 条主线的候选去重，统一按综合分排序；不再按主线重复列表。
        {crossCount > 0 ? ` 其中 ${crossCount} 只同时命中至少两条主线。` : ''}
      </div>
      <CandidateTable
        network={network}
        onOpenStock={onOpenStock}
        title="统一候选结果"
        description="所有实时主线的候选先去重，再按盘中承接、主线共振、历史特异性和关联覆盖统一排序；每只股票只出现一次。"
      />
    </>
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
  const [candidateLimit, setCandidateLimit] = useState<number>(10)
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
            <p className="linkage-eyebrow">盘中实时 · 题材聚类</p>
            <h2 id="today-core-title"><Crown size={20} />今日涨停核心</h2>
            <p>
              只分析点击时仍封住涨停的股票，优先用历史 Top20 题材标签聚类；没有有效历史标签的成员才用本地东财概念/行业关系兜底。所有主线候选先去重，最后只展示一张统一结果表。今日盘中快照不写入历史，也不进入历史分母。
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
            <span>候选结果数</span>
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
                    <span>
                      {formatSnapshotTime(network.snapshot_at)} 快照 · 今日仍封板 {network.sealed_count} 只
                      {' · '}炸板 {network.broken_count ?? 0} 只 · 跌停 {network.down_count ?? 0} 只
                    </span>
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
                <div>
                  <span>候选行情</span>
                  <strong>{network.live_candidate ? (network.live_unavailable ? '部分确认' : '已盘中确认') : '仅历史观察'}</strong>
                  <small>{network.live_candidate ? '按理论上界分批读取，不扫全市场' : '未取得候选实时行情'}</small>
                </div>
              </div>

              {(network.theme_mainline ?? []).length > 0 && (
                <div className="today-core-mainlines">
                  <span className="today-core-mainlines-label">
                    实时主线
                    <MetricHint label="实时多主线" formula="至少 2 只当前封板成员共享有效题材标签；原始权重 × 时效 × 封板质量">
                      个股优先使用同花顺 Top20 在历史日期中积累到个股上的题材标签；没有有效历史标签时，使用本地东财概念/行业关系兜底，并按名称映射到可识别的同花顺题材。不解析今日实时 reason。每条主线独立保留，至少 2 只当前封板成员共享同一标签才计入。时效 = `e^(-Δt/90) + (1-e^(-Δt/90)) × n/(n+5)`，Δt 是该主线当前封板成员最近一次封板距快照的交易分钟数，午休不计入；n 只统计当前仍封板且共享该题材的成员，不含未涨停或炸板股；回封会刷新时效。封板质量 = `封板 / (封板 + 0.5 × 炸板 + 1 × 跌停)`，炸板按半个封板损失、跌停按一个封板损失扣减。历史标签的最近出现日期不参与权重。主线可以交叉，候选也可以交叉。未分类、其他、原因不明或全组没有明确原因标签的题材桶不参与。
                    </MetricHint>
                  </span>
                  {(network.theme_mainline ?? []).map((theme) => (
                    <span
                      key={theme.code}
                      className={`today-core-mainline${/CPO|通信|5G|光|数据中心/.test(theme.name) ? ' communication' : ''}`}
                      title={`题材聚类 · 来源 ${(theme.sources ?? []).map((source) => source === 'ths' ? '同花顺' : source === 'eastmoney_industry' ? '东财行业' : '东财概念').join(' + ')} · 权重 ${formatNumber(theme.weight, 2)} · 时效 ${formatNumber(theme.freshness, 2)} · 封板质量 ${formatNumber(theme.seal_quality, 2)} · 最近封板 ${formatTimeText(theme.last_seal_at)}`}
                    >
                      {theme.name}
                      <small>
                        {theme.member_count} 只封板 · 炸板 {theme.broken_count ?? 0} · 跌停 {theme.down_count ?? 0}
                        {' · '}质量 {formatNumber(theme.seal_quality, 2)}
                        {' · '}时效 {formatNumber(theme.freshness, 2)}
                        {' · '}最近封板 {formatTimeText(theme.last_seal_at)}
                      </small>
                    </span>
                  ))}
                </div>
              )}
              {network.history_available && (network.theme_mainline ?? []).length === 0 && (
                <div className="today-core-caveat">
                  <Info size={14} />
                  <span>
                    当前没有至少 2 只今日封板成员共享有效题材标签；历史 Top20 和本地东财概念/行业关系都未能形成主线。
                  </span>
                </div>
              )}

              <div className="today-core-caveat">
                <Info size={14} />
                <span>
                  快照会随开板、回封而变化，刷新后成员和排名可能不同。历史题材优先使用 {network.history_as_of || '最近完整日'} 及更早的同花顺 Top20 归档；没有有效历史标签的封板股使用本地东财概念/行业关系兜底。今日实时封板与实时 reason 不参与题材命名；今日盘中快照不写入历史，也不进入历史分母。盘中行情按理论上界分批读取，只有证明未取数候选不能反超才停止；数据不完整时明确失败，不输出混合口径排名。共现不代表因果、带动或未来收益。
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
                          <small>
                            {formatNumber(core.member.core_score, 1)} 分 · {core.links.length} 关系
                            {(core.member.theme_neighbor_count ?? 0) > 0 ? ` · 题${core.member.theme_neighbor_count}` : ''}
                          </small>
                        </span>
                      </button>
                    ))}
                  </div>
                  {activeCore && <CoreDetail key={activeCore.member.code} core={activeCore} rank={activeRank} />}
                </>
              )}

              <MainlineCandidateSections network={network} onOpenStock={onOpenStock} />

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
