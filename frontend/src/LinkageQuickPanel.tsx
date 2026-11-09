import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowUpRight, AlertTriangle, Network, RotateCcw, X } from 'lucide-react'

import { api } from './api/client'
import { MetricHint } from './MetricHint'
import type { LimitUpAnalysis, LimitUpLink, LimitUpProfile } from './api/limitup'
import {
  createLinkageContext,
  isNoEventError,
  linkageQueryErrorMessage,
  type LinkageContext,
  type LinkageEventType,
  type LinkageQuickTarget,
} from './linkageNavigation'
import { formatNumber, formatPercent } from './limitupPresentation'
import './views/linkage.css'

const QUICK_EVENT_LABELS: Record<LinkageEventType, string> = {
  seal: '同日涨停',
  touch: '同日触板',
  down: '同日跌停',
}

const QUICK_LOOKBACK_OPTIONS = [30, 90, 180, 365] as const
const QUICK_MIN_OVERLAP_OPTIONS = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10] as const
const QUICK_SCOPE_OPTIONS = [
  { value: 'HS,GEM2STAR', label: '沪深及双创' },
  { value: 'HS,GEM2STAR,ST,NEW', label: '含 ST、未开板新股' },
] as const
const QUICK_DEFAULT_DAYS = 365
const QUICK_DEFAULT_MIN_OVERLAP = 1

export function LinkageQuickPanel({
  authRequired,
  target,
  open,
  onClose,
  onOpenFull,
}: {
  authRequired: boolean
  target: LinkageQuickTarget
  open: boolean
  onClose: () => void
  onOpenFull: (context: LinkageContext) => void
}) {
  const [eventType, setEventType] = useState<LinkageEventType>('seal')
  const [lookbackDays, setLookbackDays] = useState<number>(QUICK_DEFAULT_DAYS)
  const [marketScope, setMarketScope] = useState<LinkageContext['scope']>('HS,GEM2STAR')
  const [minOverlap, setMinOverlap] = useState<number>(QUICK_DEFAULT_MIN_OVERLAP)
  const [limit, setLimit] = useState<5 | 10>(5)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLElement>(null)

  const context = createLinkageContext({
    stockCode: target.code,
    stockName: target.name,
    asOf: target.asOf,
    days: lookbackDays,
    scope: marketScope,
    event: eventType,
    minOverlap,
  })

  const analysisQuery = useQuery({
    queryKey: [
      'limit-up-quick',
      target.code,
      target.asOf,
      lookbackDays,
      marketScope,
      eventType,
      minOverlap,
      limit,
    ],
    queryFn: ({ signal }) =>
      api.limitUpStock(
        target.code,
        {
          as_of: target.asOf,
          days: lookbackDays,
          scope: marketScope,
          event: eventType,
          min_overlap: minOverlap,
          limit,
          order: 'overlap',
        },
        signal,
      ),
    enabled: open && !authRequired && Boolean(target.asOf),
    retry: false,
  })

  const noEventFound = isNoEventError(analysisQuery.error)

  const overviewQuery = useQuery({
    queryKey: ['limit-up-quick-coverage', target.asOf, lookbackDays, marketScope],
    queryFn: ({ signal }) =>
      api.limitUpOverview(
        {
          as_of: target.asOf,
          days: lookbackDays,
          scope: marketScope,
        },
        signal,
      ),
    enabled:
      open &&
      !authRequired &&
      Boolean(target.asOf) &&
      noEventFound &&
      !analysisQuery.isFetching,
    retry: false,
  })

  useEffect(() => {
    if (!open) return
    closeButtonRef.current?.focus()
  }, [open])

  useEffect(() => {
    if (!open) return
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = previousOverflow
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    const closeOnEscape = (keyboardEvent: KeyboardEvent) => {
      if (keyboardEvent.key === 'Escape') {
        keyboardEvent.preventDefault()
        onClose()
      }
    }
    const keepFocusInside = (keyboardEvent: KeyboardEvent) => {
      if (keyboardEvent.key !== 'Tab' || !panelRef.current) {
        return
      }
      const focusable = Array.from(
        panelRef.current.querySelectorAll<HTMLElement>(
          'button:not(:disabled), input:not(:disabled), select:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])',
        ),
      )
      if (focusable.length === 0) {
        return
      }
      const first = focusable[0]
      const last = focusable.at(-1)
      if (!keyboardEvent.shiftKey && document.activeElement === last) {
        keyboardEvent.preventDefault()
        first.focus()
      } else if (keyboardEvent.shiftKey && document.activeElement === first) {
        keyboardEvent.preventDefault()
        last?.focus()
      }
    }
    document.addEventListener('keydown', closeOnEscape)
    document.addEventListener('keydown', keepFocusInside)
    return () => {
      document.removeEventListener('keydown', closeOnEscape)
      document.removeEventListener('keydown', keepFocusInside)
    }
  }, [onClose, open])

  if (!open) {
    return null
  }

  const analysisData: LimitUpAnalysis | undefined = analysisQuery.data?.data
  const profile: LimitUpProfile | null = analysisData?.profile ?? null
  const links: LimitUpLink[] = analysisData?.links ?? []
  const overviewCoverage = overviewQuery.data?.data?.coverage
  const coverage = analysisData?.coverage ?? overviewCoverage
  const latestSampleDate = coverage?.latest_date || target.asOf
  const subjectName = profile?.name || target.name || '所选股票'

  return (
    <div
      className="linkage-quick-backdrop"
      onMouseDown={(pointerEvent) => {
        if (pointerEvent.target === pointerEvent.currentTarget) {
          onClose()
        }
      }}
    >
      <aside
        ref={panelRef}
        className="linkage-quick-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="linkage-quick-title"
      >
        <header className="linkage-quick-head">
          <div>
            <p className="linkage-eyebrow">联动与股性快查</p>
            <h2 id="linkage-quick-title">
              {subjectName} <span>{target.code}</span>
            </h2>
            <p>
              样本截止 {latestSampleDate || '等待交易日历'} · 回看 {lookbackDays} 自然日
              {coverage ? ` · 完整样本日 ${coverage.observed_days} 天` : ''} · 本地已归档事件
            </p>
            {coverage?.latest_date && target.asOf && coverage.latest_date < target.asOf && (
              <p>请求截止 {target.asOf}；实际事件归档截至 {coverage.latest_date}，覆盖与样本指标按该日展示。</p>
            )}
          </div>
          <button
            ref={closeButtonRef}
            type="button"
            className="linkage-quick-close"
            onClick={onClose}
            aria-label="关闭联动快查"
            title="关闭（Esc）"
          >
            <X size={17} />
          </button>
        </header>

        <div className="linkage-quick-controls">
          <label className="linkage-quick-control">
            <span>回看</span>
            <select value={lookbackDays} onChange={(selectEvent) => setLookbackDays(Number(selectEvent.target.value))}>
              {QUICK_LOOKBACK_OPTIONS.map((daysOption) => <option value={daysOption} key={daysOption}>{daysOption} 自然日</option>)}
            </select>
          </label>
          <label className="linkage-quick-control">
            <span>范围</span>
            <select value={marketScope} onChange={(selectEvent) => setMarketScope(selectEvent.target.value as LinkageContext['scope'])}>
              {QUICK_SCOPE_OPTIONS.map((scopeOption) => <option value={scopeOption.value} key={scopeOption.value}>{scopeOption.label}</option>)}
            </select>
          </label>
          <label className="linkage-quick-control">
            <span>最少协同</span>
            <select value={minOverlap} onChange={(selectEvent) => setMinOverlap(Number(selectEvent.target.value))}>
              {QUICK_MIN_OVERLAP_OPTIONS.map((overlapOption) => <option value={overlapOption} key={overlapOption}>≥ {overlapOption} 次</option>)}
            </select>
          </label>
          <label className="linkage-quick-control">
            <span>展示</span>
            <select value={limit} onChange={(selectEvent) => setLimit(Number(selectEvent.target.value) as 5 | 10)}>
              <option value={5}>Top 5</option>
              <option value={10}>Top 10</option>
            </select>
          </label>
          <button
            type="button"
            className="linkage-quick-reset"
            title="恢复默认参数"
            aria-label="恢复默认参数"
            onClick={() => {
              setLookbackDays(QUICK_DEFAULT_DAYS)
              setMarketScope('HS,GEM2STAR')
              setEventType('seal')
              setMinOverlap(QUICK_DEFAULT_MIN_OVERLAP)
              setLimit(5)
            }}
          >
            <RotateCcw size={14} />
          </button>
        </div>

        <div className="linkage-quick-events" role="group" aria-label="共现事件">
          {(Object.keys(QUICK_EVENT_LABELS) as LinkageEventType[]).map((eventOption) => (
            <button
              type="button"
              key={eventOption}
              className={eventType === eventOption ? 'active' : ''}
              onClick={() => setEventType(eventOption)}
            >
              {QUICK_EVENT_LABELS[eventOption]}
            </button>
          ))}
        </div>

        <div className="linkage-quick-body" aria-live="polite">
          {!target.asOf && (
            <div className="linkage-quick-empty">
              <AlertTriangle size={22} />
              <strong>尚未确定上一交易日</strong>
              <span>当前监控或交易日历暂未返回可用日期，请稍后重试；面板不会触发采集或回补。</span>
            </div>
          )}

          {target.asOf && analysisQuery.isLoading && (
            <div className="linkage-quick-loading">正在读取本地强联动样本…</div>
          )}

          {target.asOf && noEventFound && analysisQuery.isFetching && (
            <div className="linkage-quick-loading">正在重新读取本地强联动样本…</div>
          )}

          {target.asOf && noEventFound && overviewQuery.isLoading && (
            <div className="linkage-quick-loading">正在核对当前窗口的本地样本覆盖…</div>
          )}

          {target.asOf && noEventFound && !overviewQuery.isLoading && overviewCoverage?.observed_days === 0 && (
            <div className="linkage-quick-empty">
              <Network size={22} />
              <strong>当前窗口尚无完整三池归档样本</strong>
              <span>
                截止 {target.asOf} 的回看窗口内没有涨停、炸板、跌停三池全部完成的交易日。
                请到“采集质量”查看事件归档是未启用、尚未采集还是失败；首次历史数据需手动回补。
              </span>
            </div>
          )}

          {target.asOf && noEventFound && !overviewQuery.isLoading && overviewQuery.isError && (
            <div className="linkage-quick-error">
              <AlertTriangle size={16} />
              <span>该股在当前范围无已归档事件；样本覆盖核对失败：{linkageQueryErrorMessage(overviewQuery.error)}</span>
            </div>
          )}

          {target.asOf && noEventFound && !overviewQuery.isLoading && !overviewQuery.isError && overviewCoverage?.observed_days !== 0 && (
            <div className="linkage-quick-empty">
              <Network size={22} />
              <strong>当前范围无事件记录</strong>
              <span>{linkageQueryErrorMessage(analysisQuery.error)}</span>
            </div>
          )}

          {target.asOf && analysisQuery.isError && !noEventFound && (
            <div className="linkage-quick-error">
              <AlertTriangle size={16} />
              <span>{linkageQueryErrorMessage(analysisQuery.error)}</span>
            </div>
          )}

          {target.asOf && analysisData && profile && (
            <>
              <section className="linkage-quick-profile" aria-label="股性摘要">
                <div>
                  <span>涨停天数</span>
                  <strong>{profile.seal_days}</strong>
                </div>
                <div>
                  <span>触板天数</span>
                  <strong>{profile.touch_days}</strong>
                </div>
                <div>
                  <span>封板率</span>
                  <strong>{formatPercent(profile.seal_rate)}</strong>
                </div>
                <div>
                  <span>炸板天数</span>
                  <strong>{profile.broken_days}</strong>
                </div>
                <div>
                  <span>跌停天数</span>
                  <strong>{profile.down_days}</strong>
                </div>
                <div>
                  <span>炸后收盘跌停</span>
                  <strong>{profile.up_to_down_days}</strong>
                </div>
              </section>
              <p className="linkage-quick-profile-note">
                封板率 = 收盘涨停天数 / 触板天数；跌停独立统计，不进入封板率分母。样本触板频率{' '}
                {formatPercent(profile.event_frequency)}，完整样本日 {analysisData.coverage.observed_days} 天。
              </p>

              <div className="linkage-quick-section-head">
                <div>
                  <strong>Top {limit} 联动股票</strong>
                  <span>
                    {QUICK_EVENT_LABELS[eventType]} · 至少协同 {minOverlap} 次 · 按协同次数降序 · 符合条件{' '}
                    {analysisData.total_links} 只 · 完整样本日 {analysisData.coverage.observed_days} 天
                  </span>
                </div>
                <span className={analysisData.coverage.sample_sufficient ? 'linkage-tag-success' : 'linkage-tag-warning'}>
                  {analysisData.coverage.sample_sufficient ? '样本充足' : '样本不足'}
                </span>
              </div>

              {links.length === 0 && (
                <div className="linkage-quick-empty compact">
                  <Network size={20} />
                  <strong>当前事件下没有符合条件的联动股票</strong>
                  <span>可切换事件类型，或降低“最少协同”次数后重试。</span>
                </div>
              )}

              <div className="linkage-quick-link-list">
                {links.map((linkItem, linkIndex) => (
                  <article className="linkage-quick-link-card" key={linkItem.code}>
                    <div className="linkage-quick-link-rank">{linkIndex + 1}</div>
                    <div className="linkage-quick-link-main">
                      <div className="linkage-quick-link-name">
                        <strong>{linkItem.name}</strong>
                        <span>{linkItem.code}</span>
                        {!linkItem.sample_sufficient && (
                          <em>样本不足</em>
                        )}
                      </div>
                      <div className="linkage-quick-link-metrics">
                        <span><strong>协同 {linkItem.overlap_days} 次</strong></span>
                        <span className="linkage-quick-metric">
                          P(B|A) {formatPercent(linkItem.conditional_probability)}
                          <MetricHint label="P(B|A) 条件共现率" formula="c / a">
                            中心股票发生事件的日子里，该关联股票也在同一天发生事件的比例。越高表示中心股票一动，它越常同步。
                          </MetricHint>
                        </span>
                        <span className="linkage-quick-metric">
                          Lift {formatNumber(linkItem.lift, 2)}
                          <MetricHint label="提升倍数 (Lift)" formula="(c / a) / (b / N)">
                            该股在中心股票事件日的出现率，相对它平时基准出现率的倍数。大于 1 表示更常同步，等于 1 接近独立。
                          </MetricHint>
                        </span>
                      </div>
                      <div className="linkage-quick-link-dates">
                        {linkItem.shared_dates.slice(0, 4).join('、')}
                        {linkItem.shared_dates.length > 4 ? ` 等 ${linkItem.shared_dates.length} 天` : ''}
                      </div>
                    </div>
                  </article>
                ))}
              </div>

              <div className="linkage-quick-note">
                条件概率表示中心股票事件日中出现该股同日事件的占比；提升倍数相对完整样本日基准频率。共现不代表先后、因果或未来收益。
              </div>
            </>
          )}
        </div>

        <footer className="linkage-quick-footer">
          <span>仅读取后端本地归档，不请求上游行情。</span>
          <button
            type="button"
            className="linkage-quick-full-button"
            onClick={() => onOpenFull(context)}
          >
            打开完整研究页
            <ArrowUpRight size={15} />
          </button>
        </footer>
      </aside>
    </div>
  )
}
