import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowUpRight, AlertTriangle, Network, X } from 'lucide-react'

import { api } from './api/client'
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
  seal: '同日封板',
  touch: '同日触板',
  down: '同日跌停',
}

const QUICK_DAYS = 90
const QUICK_MIN_OVERLAP = 3

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
  const [limit, setLimit] = useState<5 | 10>(5)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLElement>(null)

  const context = createLinkageContext({
    stockCode: target.code,
    stockName: target.name,
    asOf: target.asOf,
    days: QUICK_DAYS,
    scope: 'HS,GEM2STAR',
    event: eventType,
    minOverlap: QUICK_MIN_OVERLAP,
  })

  const analysisQuery = useQuery({
    queryKey: [
      'limit-up-quick',
      target.code,
      target.asOf,
      QUICK_DAYS,
      'HS,GEM2STAR',
      eventType,
      QUICK_MIN_OVERLAP,
      limit,
    ],
    queryFn: ({ signal }) =>
      api.limitUpStock(
        target.code,
        {
          as_of: target.asOf,
          days: QUICK_DAYS,
          scope: 'HS,GEM2STAR',
          event: eventType,
          min_overlap: QUICK_MIN_OVERLAP,
          limit,
        },
        signal,
      ),
    enabled: open && !authRequired && Boolean(target.asOf),
    retry: false,
  })

  const noEventFound = isNoEventError(analysisQuery.error)

  const overviewQuery = useQuery({
    queryKey: ['limit-up-quick-coverage', target.asOf, QUICK_DAYS, 'HS,GEM2STAR'],
    queryFn: ({ signal }) =>
      api.limitUpOverview(
        {
          as_of: target.asOf,
          days: QUICK_DAYS,
          scope: 'HS,GEM2STAR',
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
            <p className="linkage-eyebrow">历史强联动快查</p>
            <h2 id="linkage-quick-title">
              {subjectName} <span>{target.code}</span>
            </h2>
            <p>
              样本截止 {latestSampleDate || '等待交易日历'} · 回看 {QUICK_DAYS} 自然日
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
          <div className="linkage-quick-segmented" role="group" aria-label="共现事件">
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
          <div className="linkage-quick-segmented" role="group" aria-label="关联股票数量">
            {([5, 10] as const).map((limitOption) => (
              <button
                type="button"
                key={limitOption}
                className={limit === limitOption ? 'active' : ''}
                onClick={() => setLimit(limitOption)}
              >
                Top {limitOption}
              </button>
            ))}
          </div>
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
                  <span>封板率</span>
                  <strong>{formatPercent(profile.seal_rate)}</strong>
                </div>
                <div>
                  <span>触板天数</span>
                  <strong>{profile.touch_days}</strong>
                </div>
                <div>
                  <span>样本触板频率</span>
                  <strong>{formatPercent(profile.event_frequency)}</strong>
                </div>
                <div>
                  <span>跌停天数</span>
                  <strong>{profile.down_days}</strong>
                </div>
              </section>
              <p className="linkage-quick-profile-note">
                封板率与触板、开板指标只统计上涨侧；跌停天数独立统计，不进入封板率分母。
              </p>

              <div className="linkage-quick-section-head">
                <div>
                  <strong>Top {limit} 历史强联动</strong>
                  <span>
                    {QUICK_EVENT_LABELS[eventType]} · 至少共同 {QUICK_MIN_OVERLAP} 次 · 符合条件{' '}
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
                  <strong>当前事件下没有符合条件的强联动股</strong>
                  <span>可切换事件类型，或打开完整研究页降低最少共同次数。</span>
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
                        <span>共同 {linkItem.overlap_days} 天</span>
                        <span>P(B|A) {formatPercent(linkItem.conditional_probability)}</span>
                        <span>Lift {formatNumber(linkItem.lift, 2)}</span>
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
