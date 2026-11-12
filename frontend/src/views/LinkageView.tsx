import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  AlertTriangle,
  CalendarDays,
  Check,
  ChevronLeft,
  ChevronRight,
  Info,
  Network,
  RefreshCw,
  Search,
  Sparkles,
} from 'lucide-react'

import { api } from '../api/client'
import { MetricHint } from '../MetricHint'
import { ThsFocusPanel } from '../ThsFocusPanel'
import { TodayCorePanel } from '../TodayCorePanel'
import type {
  LimitUpCoverage,
  LimitUpDay,
  LimitUpEvent,
  LimitUpLink,
  LimitUpProfile,
} from '../api/limitup'
import {
  calculateGraphCoordinates,
  eventTouchTimes,
  filterStockCandidates,
  formatEventRowKey,
  formatNumber,
  formatOpenCountDisplay,
  formatPercent,
  formatPercentPoint,
  formatPoolStatus,
  formatTimeText,
  paginateRecords,
  sortStockCandidates,
  type LinkageEventType,
} from '../limitupPresentation'
import {
  isNoEventError,
  isStockCode,
  linkageQueryErrorMessage,
  type LinkageContext,
} from '../linkageNavigation'
import './linkage.css'

const LOOKBACK_OPTIONS = [30, 90, 180, 365] as const
const MIN_OVERLAP_OPTIONS = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10] as const
const SCOPE_OPTIONS = [
  { value: 'HS,GEM2STAR', label: '沪深及双创' },
  { value: 'HS,GEM2STAR,ST,NEW', label: '含 ST、未开板新股' },
] as const

const EVENT_LABELS: Record<LinkageEventType, string> = {
  seal: '同日收盘涨停',
  touch: '同日曾触板',
  down: '同日收盘跌停',
}

const EVENT_SCOPE_NOTES: Record<LinkageEventType, string> = {
  seal: '只取收盘涨停池，炸板与跌停记录不参与。',
  touch: '取收盘涨停池与炸板池的并集，跌停记录不参与。',
  down: '只取收盘跌停池，涨停与炸板记录不参与。',
}

const FIXED_TIME_BUCKET_LABELS = [
  '09:30前',
  '09:30–10:00',
  '10:00–11:30',
  '13:00–14:00',
  '14:00后',
] as const

const RELATION_PAGE_SIZE = 10
const EVENT_PAGE_SIZE = 10
const MAX_VISIBLE_CANDIDATES = 24

export function LinkageView({
  authRequired,
  context,
  onContextChange,
  onBackToSource,
  onOpenQuality,
}: {
  authRequired: boolean
  context: LinkageContext
  onContextChange: (context: LinkageContext) => void
  onBackToSource?: () => void
  onOpenQuality?: () => void
}) {
  const asOfDate = context.asOf
  const lookbackDays = context.days
  const marketScope = context.scope
  const eventType = context.event
  const minOverlap = context.minOverlap
  const selectedStockCode = context.stockCode
  const [stockSearchKeyword, setStockSearchKeyword] = useState<string>('')
  const [directStockInput, setDirectStockInput] = useState<string>(context.stockCode)
  const [directSearchOpen, setDirectSearchOpen] = useState(false)
  const [candidatePickerOpen, setCandidatePickerOpen] = useState(false)
  const [todayCoreOpen, setTodayCoreOpen] = useState(false)
  const todayCoreReturnRef = useRef<HTMLButtonElement>(null)
  const [relationPage, setRelationPage] = useState<number>(1)
  const [themePage, setThemePage] = useState<number>(1)
  const [eventPage, setEventPage] = useState<number>(1)
  const stockSearchRef = useRef<HTMLInputElement>(null)
  const candidatePickerRef = useRef<HTMLDetailsElement>(null)

  useEffect(() => {
    setDirectStockInput(context.stockCode)
    setDirectSearchOpen(false)
  }, [context.stockCode])

  const needsInitialCandidates = !context.stockCode

  const stockQuery = useQuery({
    queryKey: [
      'limit-up-stock',
      selectedStockCode,
      asOfDate,
      lookbackDays,
      marketScope,
      eventType,
      minOverlap,
    ],
    queryFn: ({ signal }) =>
      api.limitUpStock(
        selectedStockCode,
        {
          as_of: asOfDate,
          days: lookbackDays,
          scope: marketScope,
          event: eventType,
          min_overlap: minOverlap,
          limit: 50,
        },
        signal,
      ),
    enabled: !authRequired && Boolean(selectedStockCode && asOfDate),
    refetchInterval: false,
    retry: false,
  })

  const noEventFound = isNoEventError(stockQuery.error)

  const overviewQuery = useQuery({
    queryKey: ['limit-up-overview', asOfDate, lookbackDays, marketScope],
    queryFn: ({ signal }) =>
      api.limitUpOverview(
        {
          as_of: asOfDate,
          days: lookbackDays,
          scope: marketScope,
        },
        signal,
      ),
    enabled:
      !authRequired &&
      Boolean(asOfDate) &&
      (needsInitialCandidates || candidatePickerOpen || noEventFound || directStockInput.trim().length > 0) &&
      !stockQuery.isFetching,
    refetchInterval: false,
  })

  const overviewData = overviewQuery.data?.data
  const coverageData = overviewData?.coverage ?? stockQuery.data?.data?.coverage
  const availableStocks = useMemo(() => overviewData?.stocks ?? [], [overviewData?.stocks])
  const sortedStocks = useMemo(
    () => sortStockCandidates(availableStocks, eventType),
    [availableStocks, eventType],
  )

  useEffect(() => {
    if (selectedStockCode || sortedStocks.length === 0) {
      return
    }
    if (!candidatePickerOpen && !needsInitialCandidates) {
      return
    }
    const firstCandidate = sortedStocks[0]
    onContextChange({
      ...context,
      stockCode: firstCandidate.code,
      stockName: firstCandidate.name,
    })
    setCandidatePickerOpen(false)
  }, [sortedStocks, selectedStockCode, onContextChange, context, candidatePickerOpen, needsInitialCandidates])

  const selectedStockRecord = useMemo(
    () => availableStocks.find((stockCandidate) => stockCandidate.code === selectedStockCode) ?? null,
    [availableStocks, selectedStockCode],
  )

  const analysisData = stockQuery.data?.data
  const profileData: LimitUpProfile | null = analysisData?.profile ?? null
  const themeData = analysisData?.theme ?? null
  const linkList: LimitUpLink[] = useMemo(() => analysisData?.links ?? [], [analysisData?.links])
  const eventList: LimitUpEvent[] = useMemo(() => analysisData?.events ?? [], [analysisData?.events])

  useEffect(() => {
    setRelationPage(1)
    setThemePage(1)
    setEventPage(1)
  }, [selectedStockCode, eventType, minOverlap, lookbackDays, marketScope, asOfDate])

  const filteredStockCandidates = useMemo(
    () => filterStockCandidates(sortedStocks, stockSearchKeyword),
    [sortedStocks, stockSearchKeyword],
  )
  const visibleStockCandidates = useMemo(
    () => filteredStockCandidates.slice(0, MAX_VISIBLE_CANDIDATES),
    [filteredStockCandidates],
  )
  const directStockMatches = useMemo(
    () => filterStockCandidates(sortedStocks, directStockInput).slice(0, 8),
    [sortedStocks, directStockInput],
  )

  const paginatedLinks = useMemo(
    () => paginateRecords(linkList, relationPage, RELATION_PAGE_SIZE),
    [linkList, relationPage],
  )
  const paginatedThemeLinks = useMemo(
    () => paginateRecords(themeData?.links ?? [], themePage, RELATION_PAGE_SIZE),
    [themeData?.links, themePage],
  )

  const paginatedEvents = useMemo(
    () => paginateRecords(eventList, eventPage, EVENT_PAGE_SIZE),
    [eventList, eventPage],
  )

  const graphLayout = useMemo(() => {
    const centerMeta = selectedStockRecord
      ? { code: selectedStockRecord.code, name: selectedStockRecord.name }
      : profileData
        ? { code: profileData.code, name: profileData.name }
        : null
    return calculateGraphCoordinates(centerMeta, linkList, 760, 460, 14)
  }, [selectedStockRecord, profileData, linkList])

  const eventPoolSummary = useMemo(() => {
    let limitUpCount = 0
    let openLimitCount = 0
    let limitDownCount = 0
    for (const eventRecord of eventList) {
      if (eventRecord.pool === 'limit_up') {
        limitUpCount++
      } else if (eventRecord.pool === 'open_limit') {
        openLimitCount++
      } else if (eventRecord.pool === 'limit_down') {
        limitDownCount++
      }
    }
    return `涨停 ${limitUpCount} · 炸板 ${openLimitCount} · 跌停 ${limitDownCount}`
  }, [eventList])

  const handleSelectStock = (targetCode: string) => {
    if (!isStockCode(targetCode)) {
      return
    }
    const matchedCandidate = availableStocks.find((stockCandidate) => stockCandidate.code === targetCode)
      ?? (analysisData?.profile.code === targetCode ? analysisData.profile : null)
    onContextChange({
      ...context,
      stockCode: targetCode,
      stockName: matchedCandidate?.name
        ?? (targetCode === context.stockCode ? context.stockName : ''),
    })
    setStockSearchKeyword('')
    setCandidatePickerOpen(false)
  }


  const handleDirectStockSubmit = () => {
    const normalizedInput = directStockInput.trim()
    if (!normalizedInput) {
      stockSearchRef.current?.focus()
      return
    }
    if (isStockCode(normalizedInput)) {
      handleSelectStock(normalizedInput)
      setDirectSearchOpen(false)
      return
    }
    if (directStockMatches.length > 0) {
      handleSelectStock(directStockMatches[0].code)
      setDirectSearchOpen(false)
      return
    }
    stockSearchRef.current?.focus()
  }

  const handleRefreshAll = () => {
    if (candidatePickerOpen && overviewQuery.isFetched) {
      void overviewQuery.refetch()
      return
    }
    if (candidatePickerOpen && !selectedStockCode) {
      void overviewQuery.refetch()
      return
    }
    void stockQuery.refetch()
  }

  const profileDataOrFallback = profileData
  const analysisName = profileDataOrFallback?.name || selectedStockRecord?.name || context.stockName || ''

  const handleCandidateToggle = (open: boolean) => {
    if (!open && candidatePickerRef.current) {
      candidatePickerRef.current.open = false
    }
    setCandidatePickerOpen(open)
  }

  return (
    <section className="linkage-page" aria-label="股票联动与股性画像">
      <div className="linkage-header-card">
        <div className="linkage-title-row">
          <div className="linkage-heading-group">
            <p className="linkage-eyebrow">股票联动</p>
            <h1 className="linkage-title">联动性与股性画像</h1>
            <p className="linkage-subline">
              先按同花顺涨停简图的历史累计分类标签做股票聚类，再保留日终涨停池、炸板池与跌停池的全市场同日共振口径供核对。页面只读本地已归档数据，统计代表历史聚类或同日共现，非收益或因果预测。
            </p>
            <div className="linkage-disclaimer">
              <Info size={14} />
              <span>
                提示：同题材聚类不是完整行业/概念成份；未分类/其他/原因不明的题材桶不会参与。全市场同日共振也不等于同题材联动，无法推导先后、资金因果或未来收益。
              </span>
            </div>
          </div>

          <div className="linkage-header-actions">
            {onBackToSource && (
              <button
                type="button"
                className="linkage-back-button"
                onClick={onBackToSource}
              >
                <ChevronLeft size={14} />
                返回来源页
              </button>
            )}
            <button
              ref={todayCoreReturnRef}
              type="button"
              className="linkage-today-core-button"
              onClick={() => setTodayCoreOpen(true)}
              title="读取点击时仍封板的今日涨停股，并在这些成员之间分析历史收盘封板关系"
            >
              <Sparkles size={14} />
              今日涨停核心
            </button>
            <button
              type="button"
              className="linkage-refresh-button"
              onClick={handleRefreshAll}
              disabled={overviewQuery.isFetching || stockQuery.isFetching}
              title="重新从后端读取本地数据"
            >
              <RefreshCw size={13} className={overviewQuery.isFetching || stockQuery.isFetching ? 'spin' : ''} />
              <span>重新读取本地数据</span>
            </button>
          </div>
        </div>

        <div className="linkage-controls-bar">
          <div className="linkage-control-item">
            <label htmlFor="linkage-as-of-input">截止日期：</label>
            <div className="date-picker">
              <CalendarDays size={13} />
              <input
                id="linkage-as-of-input"
                type="date"
                value={asOfDate}
                onChange={(inputEvent) =>
                  onContextChange({ ...context, asOf: inputEvent.target.value })
                }
              />
            </div>
          </div>

          <div className="linkage-control-item">
            <label htmlFor="linkage-lookback-select">回看窗口：</label>
            <select
              id="linkage-lookback-select"
              className="linkage-select"
              value={lookbackDays}
              onChange={(selectEvent) =>
                onContextChange({ ...context, days: Number(selectEvent.target.value) })
              }
            >
              {LOOKBACK_OPTIONS.map((daysOption) => (
                <option key={daysOption} value={daysOption}>
                  {daysOption} 自然日
                </option>
              ))}
            </select>
          </div>

          <div className="linkage-control-item">
            <label htmlFor="linkage-scope-select">市场范围：</label>
            <select
              id="linkage-scope-select"
              className="linkage-select"
              value={marketScope}
              onChange={(selectEvent) => {
                const scopeValue = selectEvent.target.value
                if (scopeValue === 'HS,GEM2STAR' || scopeValue === 'HS,GEM2STAR,ST,NEW') {
                  onContextChange({ ...context, scope: scopeValue })
                }
              }}
            >
              {SCOPE_OPTIONS.map((scopeOption) => (
                <option key={scopeOption.value} value={scopeOption.value}>
                  {scopeOption.label}
                </option>
              ))}
            </select>
          </div>

          <div className="linkage-control-item">
            <label htmlFor="linkage-event-select">共现事件：</label>
            <select
              id="linkage-event-select"
              className="linkage-select"
              value={eventType}
              onChange={(selectEvent) => {
                const eventValue = selectEvent.target.value
                if (eventValue === 'seal' || eventValue === 'touch' || eventValue === 'down') {
                  onContextChange({ ...context, event: eventValue })
                }
              }}
            >
              <option value="seal">同日收盘封板</option>
              <option value="touch">同日曾触板</option>
              <option value="down">同日收盘跌停</option>
            </select>
          </div>

          <div className="linkage-control-item">
            <label htmlFor="linkage-min-overlap-select">最少共同次数：</label>
            <select
              id="linkage-min-overlap-select"
              className="linkage-select linkage-select-small"
              value={minOverlap}
              onChange={(selectEvent) =>
                onContextChange({ ...context, minOverlap: Number(selectEvent.target.value) })
              }
            >
              {MIN_OVERLAP_OPTIONS.map((overlapOption) => (
                <option key={overlapOption} value={overlapOption}>
                  ≥ {overlapOption} 次
                </option>
              ))}
            </select>
          </div>
        </div>

        {coverageData && (
          <CoverageSummary
            coverage={coverageData}
            days={overviewData?.days ?? []}
            totalStocks={overviewData ? availableStocks.length : null}
            requestedAsOf={asOfDate}
            onUseLatestDate={
              coverageData.latest_date && asOfDate && coverageData.latest_date < asOfDate
                ? () => onContextChange({ ...context, asOf: coverageData.latest_date })
                : undefined
            }
          />
        )}
      </div>

      {!asOfDate && (
        <div className="linkage-empty-box" role="status" aria-live="polite">
          <CalendarDays size={24} />
          <strong>等待可用研究截止日</strong>
          <span>
            正在读取交易日历与事件归档水位。页面只读取本地已归档数据，不会自动采集或回补历史。
          </span>
        </div>
      )}

      {overviewQuery.isError && (
        <div className="linkage-error-box">
          <AlertTriangle size={15} />
          <span>读取涨停概览失败：{linkageQueryErrorMessage(overviewQuery.error)}，请重试或检查后端。</span>
        </div>
      )}

      {asOfDate && !selectedStockCode && overviewQuery.isLoading && (
        <div className="linkage-loading-box">
          <span>正在读取本地涨停概览数据…</span>
        </div>
      )}

      {asOfDate && !selectedStockCode && !overviewQuery.isLoading && !overviewQuery.isError && availableStocks.length === 0 && (
        <div className="linkage-empty-box">
          <AlertTriangle size={24} />
          {overviewData?.coverage.observed_days ? (
            <>
              <strong>窗口内没有可选标的</strong>
              <span>
                当前截止日期 <code>{asOfDate}</code> 的样本窗口已有 {overviewData.coverage.observed_days}{' '}
                个三池完整归档日，但三池均无事件。这是有效的空样本，无需补采。
              </span>
            </>
          ) : (
            <>
              <strong>窗口内暂无完整的三池归档样本</strong>
              <span>
                当前截止日期 <code>{asOfDate}</code> 的样本窗口尚无涨停、炸板、跌停三池全部完成归档的交易日。
                请到“采集质量”确认事件归档是未启用、尚未采集还是失败；首次历史数据需手动回补。
              </span>
              {onOpenQuality && (
                <button type="button" className="linkage-switch-btn" onClick={onOpenQuality}>
                  查看采集质量
                </button>
              )}
            </>
          )}
        </div>
      )}

      <div className="linkage-direct-search-card">
        <div className="linkage-direct-search">
          <label htmlFor="linkage-stock-code-input">直接查看股票</label>
          <div className="linkage-direct-input-wrap">
            <Search size={14} />
            <input
              id="linkage-stock-code-input"
              ref={stockSearchRef}
              type="text"
              placeholder="输入代码、名称、拼音首字母或全拼，例如 600519 / 贵州茅台 / gzmt"
              value={directStockInput}
              onChange={(inputEvent) => {
                setDirectStockInput(inputEvent.target.value)
                setDirectSearchOpen(true)
              }}
              onFocus={() => setDirectSearchOpen(true)}
              onBlur={() => window.setTimeout(() => setDirectSearchOpen(false), 120)}
              onKeyDown={(keyboardEvent) => {
                if (keyboardEvent.key === 'Enter') {
                  handleDirectStockSubmit()
                } else if (keyboardEvent.key === 'Escape') {
                  setDirectSearchOpen(false)
                }
              }}
            />
            {directSearchOpen && directStockInput.trim() && (
              <div className="linkage-direct-suggestions" role="listbox" aria-label="股票搜索结果">
                {directStockMatches.length === 0 ? (
                  <span className="linkage-direct-suggestion-empty">
                    当前窗口没有匹配股票；六位代码仍可直接查看
                  </span>
                ) : (
                  directStockMatches.map((stockCandidate) => (
                    <button
                      type="button"
                      key={stockCandidate.code}
                      className="linkage-direct-suggestion"
                      role="option"
                      onMouseDown={(mouseEvent) => {
                        mouseEvent.preventDefault()
                        handleSelectStock(stockCandidate.code)
                        setDirectSearchOpen(false)
                      }}
                    >
                      <strong>{stockCandidate.name}</strong>
                      <code>{stockCandidate.code}</code>
                      <small>封 {stockCandidate.seal_days} 天 · 触 {stockCandidate.touch_days} 天</small>
                    </button>
                  ))
                )}
              </div>
            )}
          </div>
          <button
            type="button"
            className="linkage-direct-submit"
            onClick={handleDirectStockSubmit}
            disabled={!asOfDate}
            title={asOfDate ? '按代码、名称或拼音读取本地历史事件' : '等待可用研究截止日'}
          >
            查看该股
          </button>
          {selectedStockCode && (
            <span className="linkage-current-subject">
              当前：{analysisName || '待读取'} <code>{selectedStockCode}</code>
            </span>
          )}
        </div>
      </div>

      <details
        ref={candidatePickerRef}
        className="linkage-selector-card linkage-candidate-disclosure"
        open={candidatePickerOpen ? true : undefined}
        onToggle={(toggleEvent) => handleCandidateToggle(toggleEvent.currentTarget.open)}
      >
        <summary className="linkage-selector-header">
            <h2>
              浏览候选股票
              {overviewData ? `（${availableStocks.length} 只）` : overviewQuery.isLoading ? '（读取中）' : ''}
            </h2>
            <span className="linkage-candidate-summary-hint">
              默认折叠，避免候选压过联动结论；可搜索代码、名称或拼音
            </span>
        </summary>
        {candidatePickerOpen && (
          <>
          {overviewQuery.isLoading && (
            <div className="linkage-loading-box compact">
              <span>正在读取候选股票概览…</span>
            </div>
          )}
          {overviewQuery.isError && (
            <div className="linkage-error-box">
              <AlertTriangle size={15} />
              <span>候选概览读取失败：{linkageQueryErrorMessage(overviewQuery.error)}</span>
            </div>
          )}
          {!overviewQuery.isLoading && !overviewQuery.isError && availableStocks.length === 0 && (
            <span className="linkage-candidate-summary-hint">
              当前截止日期与市场范围内暂无可选股票。
            </span>
          )}
          {availableStocks.length > 0 && (
            <>
          <div className="linkage-selector-header">
            <span className="linkage-candidate-summary-hint">
              窗口内任一池出现过的股票；外部指定股票即使无事件也会保留
            </span>
            <div className="linkage-selector-search">
              <Search size={14} color="var(--muted)" />
              <input
                type="text"
                placeholder="输入代码、股票名称或拼音筛选…"
                value={stockSearchKeyword}
                onChange={(inputEvent) => setStockSearchKeyword(inputEvent.target.value)}
              />
            </div>
          </div>

          <div className="linkage-stock-chip-list" role="listbox" aria-label="候选股票列表">
            {filteredStockCandidates.length === 0 && (
              <span className="muted" style={{ padding: '8px 4px', fontSize: '11px' }}>
                未找到匹配“{stockSearchKeyword}”的股票
              </span>
            )}
            {visibleStockCandidates.map((stockCandidate) => {
              const isSelected = stockCandidate.code === selectedStockCode
              return (
                <button
                  type="button"
                  key={stockCandidate.code}
                  className={`linkage-stock-chip ${isSelected ? 'active' : ''}`}
                  onClick={() => handleSelectStock(stockCandidate.code)}
                  role="option"
                  aria-selected={isSelected}
                >
                  <strong>{stockCandidate.name}</strong>
                  <span className="linkage-stock-chip-badge">{stockCandidate.code}</span>
                  <span className="linkage-stock-chip-badge">封{stockCandidate.seal_days}天</span>
                  <span className="linkage-stock-chip-badge">跌{stockCandidate.down_days}天</span>
                </button>
              )
            })}
          </div>
          {filteredStockCandidates.length > MAX_VISIBLE_CANDIDATES && (
            <span className="linkage-candidate-limit">
              仅显示前 {MAX_VISIBLE_CANDIDATES} 只，请用搜索缩小范围（当前匹配 {filteredStockCandidates.length} 只）
            </span>
          )}
            </>
          )}
          </>
        )}
      </details>

      {selectedStockCode && (
        <>
          {stockQuery.isFetching && !analysisData && (
            <div className="linkage-loading-box">
              <span>正在分析【{selectedStockRecord?.name ?? selectedStockCode}】的联动画像与股性，只读取本地归档…</span>
            </div>
          )}

          {stockQuery.isError && !stockQuery.isFetching && (
            noEventFound ? (
              <div className="linkage-empty-box linkage-selected-no-event" role="status" aria-live="polite">
                <Network size={24} />
                <strong>
                  {overviewQuery.isLoading
                    ? '正在核对当前窗口的本地样本覆盖…'
                    : overviewData?.coverage.observed_days === 0
                    ? '当前窗口尚无完整三池归档样本'
                    : '当前范围无事件记录'}
                </strong>
                {!overviewQuery.isLoading && (
                  <span>
                    {overviewData?.coverage.observed_days === 0
                      ? `截止 ${asOfDate} 的回看窗口内没有涨停、炸板、跌停三池全部完成的交易日。请到“采集质量”确认事件归档是未启用、尚未采集还是失败；首次历史数据需手动回补。`
                      : linkageQueryErrorMessage(stockQuery.error)}
                  </span>
                )}
                {overviewQuery.isError && (
                  <span>样本覆盖核对失败：{linkageQueryErrorMessage(overviewQuery.error)}</span>
                )}
                {overviewData?.coverage.observed_days === 0 && onOpenQuality && (
                  <button type="button" className="linkage-switch-btn" onClick={onOpenQuality}>
                    查看采集质量
                  </button>
                )}
              </div>
            ) : (
              <div className="linkage-error-box">
                <AlertTriangle size={15} />
                <span>读取股票分析数据失败：{linkageQueryErrorMessage(stockQuery.error)}</span>
              </div>
            )
          )}

          {!stockQuery.isLoading && !stockQuery.isError && analysisData && profileData && (
            <>
              {themeData && themeData.profile.code && (
                <section className="linkage-panel linkage-theme-panel" aria-labelledby="linkage-theme-heading">
                  <div className="linkage-panel-head">
                    <div>
                      <h2 id="linkage-theme-heading">
                        同花顺题材聚类：{themeData.profile.name || analysisData.profile.name} ({themeData.profile.code})
                      </h2>
                      <span>
                        只使用 {asOfDate} 及更早的涨停简图历史分类标签；未分类、其他、原因不明或全组没有明确原因标签的分类不参与。
                      </span>
                      <span className="linkage-scope-note">
                        同一股票可以同时拥有多个历史题材标签；同题材天数按“同一天进入至少一个相同题材”去重。该视图不依赖东财行业/概念。
                      </span>
                    </div>
                    <div>
                      <span className="linkage-tag-success">
                        有效标签日：{themeData.label_days} 天
                      </span>
                    </div>
                  </div>

                  <div className="linkage-theme-profile">
                    <div className="linkage-theme-profile-head">
                      <strong>历史累计题材标签</strong>
                      <span>
                        出现次数越多、最近越常出现，标签越靠前
                        <MetricHint label="同花顺题材标签" formula="窗口内进入同一涨停简图分类的交易日数">
                          同花顺每日盘后整理涨停简图分类；某只股票只要在某天进入某个分类，就积累该分类标签。标签不是完整成份，也不代表当天盘前已知；不同日期的分类可以同时保留，用来解释同一只股票在不同阶段的涨停原因。
                        </MetricHint>
                      </span>
                    </div>
                    <div className="linkage-theme-tag-list">
                      {(themeData.profile.tags ?? []).length > 0 ? (themeData.profile.tags ?? []).map((tag) => (
                        <span className="linkage-theme-tag" key={tag.code} title={`首次 ${tag.first_date} · 最近 ${tag.last_date} · 近60日 ${tag.recent_occurrences} 次`}>
                          {tag.name}
                          <small>{tag.occurrences}次</small>
                        </span>
                      )) : (
                        <span className="muted">当前窗口没有有效的涨停简图分类标签。</span>
                      )}
                    </div>
                  </div>

                  <div className="linkage-table-context">
                    同题材联动按“同题材共现天数”降序，其次按题材相似度和同日封板次数。题材相似度使用 IDF 加权余弦；Jaccard 是题材日期集合的交集/并集；题材纯度 = 同题材共现天数 / 同日收盘封板次数，用于识别跨题材共振。
                  </div>

                  <div className="linkage-table-wrap">
                    <table className="linkage-table" aria-label="同花顺题材聚类关联股票">
                      <thead>
                        <tr>
                          <th>关联股票</th>
                          <th>
                            <span className="linkage-column-label">
                              同题材共现
                              <MetricHint label="同题材共现" formula="同一天进入至少一个相同涨停简图分类的天数" align="start">
                                两只股票在同一天都进入同一个同花顺涨停简图分类的天数；同一天共享多个分类只算一天，避免多标签重复加分。
                              </MetricHint>
                            </span>
                          </th>
                          <th>共享题材</th>
                          <th>
                            <span className="linkage-column-label">
                              题材相似度
                              <MetricHint label="题材相似度" formula="IDF 加权题材向量余弦" align="start">
                                把每只股票的历史题材标签出现次数按 IDF 加权后比较方向相似度。常见大题材权重较低，小众但反复共同出现的题材权重较高。
                              </MetricHint>
                            </span>
                          </th>
                          <th>
                            <span className="linkage-column-label">
                              题材 Jaccard
                              <MetricHint label="题材 Jaccard" formula="同题材天数 / 双方标签日并集" align="start">
                                两只股票有题材标签的日期集合相似度，取值 0 到 1；它不使用同日封板次数做分母。
                              </MetricHint>
                            </span>
                          </th>
                          <th>
                            <span className="linkage-column-label">
                              题材纯度
                              <MetricHint label="题材纯度" formula="同题材天数 / 同日封板次数" align="start">
                                同日一起封板的次数里，有多少次至少共享一个同花顺题材。纯度高说明不是跨题材普涨共振；同日封板为 0 时不计算。
                              </MetricHint>
                            </span>
                          </th>
                          <th>同日封板</th>
                          <th>最近同题材</th>
                          <th>操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {paginatedThemeLinks.paginatedItems.length === 0 && (
                          <tr>
                            <td colSpan={9} className="muted" style={{ textAlign: 'center', padding: '18px' }}>
                              当前窗口没有可配对的同题材股票。
                            </td>
                          </tr>
                        )}
                        {paginatedThemeLinks.paginatedItems.map((themeLink) => (
                          <tr key={themeLink.code}>
                            <td>
                              <strong>{themeLink.name}</strong>
                              <small>{themeLink.code}</small>
                            </td>
                            <td><strong>{themeLink.theme_days} 天</strong></td>
                            <td>
                              <span title={(themeLink.shared_themes ?? []).map((theme) => theme.name).join('、')}>
                                {(themeLink.shared_themes ?? []).slice(0, 3).map((theme) => theme.name).join('、') || '--'}
                              </span>
                              {(themeLink.shared_themes ?? []).length > 3 && (
                                <small>等 {(themeLink.shared_themes ?? []).length} 个</small>
                              )}
                            </td>
                            <td>{formatPercent(themeLink.theme_similarity)}</td>
                            <td>{formatNumber(themeLink.theme_jaccard, 3)}</td>
                            <td>{formatPercent(themeLink.theme_purity)}</td>
                            <td>{themeLink.co_seal_days} 次</td>
                            <td>{themeLink.last_shared_date || '--'}</td>
                            <td>
                              <button
                                type="button"
                                className="linkage-switch-btn"
                                onClick={() => handleSelectStock(themeLink.code)}
                              >
                                设为中心
                              </button>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {paginatedThemeLinks.totalPages > 1 && (
                    <div className="linkage-pagination">
                      <button
                        type="button"
                        className="linkage-pagination-btn"
                        disabled={paginatedThemeLinks.currentPage <= 1}
                        onClick={() => setThemePage(paginatedThemeLinks.currentPage - 1)}
                        title="上一页"
                        aria-label="同题材联动上一页"
                      >
                        <ChevronLeft size={14} />
                      </button>
                      <span>{paginatedThemeLinks.currentPage} / {paginatedThemeLinks.totalPages} 页</span>
                      <button
                        type="button"
                        className="linkage-pagination-btn"
                        disabled={paginatedThemeLinks.currentPage >= paginatedThemeLinks.totalPages}
                        onClick={() => setThemePage(paginatedThemeLinks.currentPage + 1)}
                        title="下一页"
                        aria-label="同题材联动下一页"
                      >
                        <ChevronRight size={14} />
                      </button>
                    </div>
                  )}
                </section>
              )}

              <section className="linkage-panel" aria-labelledby="linkage-network-heading">
                <div className="linkage-panel-head">
                  <div>
                    <h2 id="linkage-network-heading">
                      全市场同日共振：{analysisData.profile.name} ({analysisData.profile.code}) 共现网络
                    </h2>
                    <span>
                      共现事件：{EVENT_LABELS[eventType]} · 最少共同次数 ≥ {minOverlap} 次（可在上方切换 1..10 探索）
                    </span>
                    <span className="linkage-scope-note">{EVENT_SCOPE_NOTES[eventType]}</span>
                  </div>
                  <div>
                    <span className="linkage-tag-success">
                      关联标的总数：{analysisData.total_links} 只
                    </span>
                  </div>
                </div>

                <div className="linkage-graph-container">
                  {graphLayout.neighborNodes.length === 0 ? (
                    <div className="linkage-empty-box" style={{ minHeight: '160px', width: '100%' }}>
                      <Network size={20} />
                      <strong>在当前阈值（≥{minOverlap}次）下未发现符合条件的关联股票</strong>
                      <span>可尝试将“最少共同次数”调低至 1~2 次进行宽松探索。</span>
                    </div>
                  ) : (
                    <>
                      <svg
                        className="linkage-graph-svg"
                        viewBox="0 0 760 460"
                        role="img"
                        aria-label={`${EVENT_LABELS[eventType]}股票共现拓扑关系图`}
                      >
                        {graphLayout.edges.map((edgeItem) => (
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
                            <title>
                              {analysisData.profile.name} ↔ {edgeItem.targetName} ({edgeItem.targetCode}): 共同
                              {edgeItem.overlapDays}次, 条件共现率P(B|A)={formatPercent(edgeItem.conditionalProbability)}
                            </title>
                          </line>
                        ))}

                        {graphLayout.neighborNodes.map((neighborItem) => (
                          <g
                            key={`node-${neighborItem.code}`}
                            className="linkage-graph-node linkage-graph-node-neighbor"
                            tabIndex={0}
                            role="button"
                            aria-label={`切换中心股票为${neighborItem.name}`}
                            onClick={() => handleSelectStock(neighborItem.code)}
                            onKeyDown={(keyboardEvent) => {
                              if (keyboardEvent.key === 'Enter' || keyboardEvent.key === ' ') {
                                handleSelectStock(neighborItem.code)
                              }
                            }}
                          >
                            <circle
                              cx={neighborItem.coordX}
                              cy={neighborItem.coordY}
                              r={neighborItem.radius}
                            />
                            <text x={neighborItem.coordX} y={neighborItem.coordY - 5}>
                              {neighborItem.name.slice(0, 4)}
                            </text>
                            <text
                              x={neighborItem.coordX}
                              y={neighborItem.coordY + 8}
                              style={{ fontSize: '9px', fill: 'var(--muted)' }}
                            >
                              {neighborItem.code}
                            </text>
                            <title>
                              点击切换中心股票：{neighborItem.name} ({neighborItem.code}) · 共同
                              {neighborItem.overlapDays}次
                            </title>
                          </g>
                        ))}

                        {graphLayout.centerNode && (
                          <g
                            className="linkage-graph-node linkage-graph-node-center"
                            tabIndex={0}
                            role="img"
                            aria-label={`中心股票${graphLayout.centerNode.name}`}
                          >
                            <circle
                              cx={graphLayout.centerNode.coordX}
                              cy={graphLayout.centerNode.coordY}
                              r={graphLayout.centerNode.radius}
                            />
                            <text x={graphLayout.centerNode.coordX} y={graphLayout.centerNode.coordY - 6}>
                              {graphLayout.centerNode.name.slice(0, 4)}
                            </text>
                            <text
                              x={graphLayout.centerNode.coordX}
                              y={graphLayout.centerNode.coordY + 9}
                              style={{ fontSize: '10px', fill: '#ffffff' }}
                            >
                              {graphLayout.centerNode.code}
                            </text>
                          </g>
                        )}
                      </svg>
                      <div className="linkage-graph-hint">
                        口径：{EVENT_LABELS[eventType]}。点击外围邻居或聚焦后按 Enter/空格可切换中心股票；连线粗细按共同事件次数映射，不表示先后或因果。
                      </div>
                    </>
                  )}
                </div>

                <div className="linkage-table-context">
                  当前关系口径：{EVENT_LABELS[eventType]}，这是全市场同日共振，不是同花顺题材聚类。{EVENT_SCOPE_NOTES[eventType]}{' '}
                  P(B|A) = 共同事件日 / 中心股票在该口径下的事件日；基准频率按完整样本日 N 计算。
                </div>

                <div className="linkage-table-wrap">
                  <table
                    className="linkage-table"
                    aria-label={`${EVENT_LABELS[eventType]}关联股票共现关系表`}
                  >
                    <thead>
                      <tr>
                        <th>关联股票</th>
                        <th>
                          <span className="linkage-column-label">
                            共同次数
                            <MetricHint label="共同次数" formula="c" align="start">
                              A、B 在同一天发生同类事件的天数。先看它，再看比率；次数太少时，百分比和提升倍数容易失真。
                            </MetricHint>
                          </span>
                        </th>
                        <th>
                          <span className="linkage-column-label">
                            P(B|A) 条件共现率
                            <MetricHint label="P(B|A) 条件共现率" formula="c / a" align="start">
                              A 发生事件的日子里，B 也在同一天发生事件的比例。a 是 A 的事件天数，c 是共同事件天数。越高表示 A 一动，B 越常同步。
                            </MetricHint>
                          </span>
                        </th>
                        <th>
                          <span className="linkage-column-label">
                            P(A|B) 反向共现率
                            <MetricHint label="P(A|B) 反向共现率" formula="c / b" align="start">
                              B 发生事件的日子里，A 也在同一天发生事件的比例。b 是 B 的事件天数。它和 P(B|A) 分别回答“A 动时 B 在不在”和“B 动时 A 在不在”。
                            </MetricHint>
                          </span>
                        </th>
                        <th>
                          <span className="linkage-column-label">
                            提升倍数 (Lift)
                            <MetricHint label="提升倍数 (Lift)" formula="(c / a) / (b / N) = cN / (ab)" align="start">
                              B 在 A 事件日的出现率，是 B 平时基准出现率 b/N 的多少倍。大于 1 表示比平时更常同日出现，等于 1 接近独立，小于 1 表示反而更少同时出现。Lift 对调 A、B 结果不变，本身不分方向。
                            </MetricHint>
                          </span>
                        </th>
                        <th>
                          <span className="linkage-column-label">
                            Jaccard
                            <MetricHint label="Jaccard" formula="c / (a + b - c)" align="start">
                              A、B 事件日集合的交集除以并集，取值 0 到 1。越高表示两组事件日重合得越多，但它是集合相似度，不是条件概率。
                            </MetricHint>
                          </span>
                        </th>
                        <th>
                          <span className="linkage-column-label">
                            热度折算共现
                            <MetricHint label="热度折算共现" formula="Σ 1 / max(1, 当天同池股票数 - 1)" align="start">
                              每个共同事件日按当天同池股票数降权，减少全面普涨或普跌日对联动强度的放大。它是加权共现分，不是次数；共同次数高但集中在少数普涨日时，这一列可能不高。
                            </MetricHint>
                          </span>
                        </th>
                        <th>
                          <span className="linkage-column-label">
                            样本充分度
                            <MetricHint label="样本充分度" formula="N ≥ 20 且 c ≥ 3" align="start">
                              完整样本日至少有 20 天，且共同事件至少 3 次，才会标为充足。这是展示门槛，不代表统计显著性，也不证明因果。
                            </MetricHint>
                          </span>
                        </th>
                        <th>共同发生日期</th>
                        <th>操作</th>
                      </tr>
                    </thead>
                    <tbody>
                      {paginatedLinks.paginatedItems.length === 0 && (
                        <tr>
                          <td colSpan={10} style={{ textAlign: 'center', padding: '18px' }} className="muted">
                            暂无符合条件的关联股票记录
                          </td>
                        </tr>
                      )}
                      {paginatedLinks.paginatedItems.map((linkCandidate) => (
                        <tr key={linkCandidate.code}>
                          <td>
                            <strong>{linkCandidate.name}</strong>
                            <small>{linkCandidate.code} · {linkCandidate.market_type}</small>
                          </td>
                          <td>
                            <strong>{linkCandidate.overlap_days} 次</strong>
                            <small>对方事件 {linkCandidate.peer_days} 天</small>
                          </td>
                          <td>{formatPercent(linkCandidate.conditional_probability)}</td>
                          <td>{formatPercent(linkCandidate.reverse_probability)}</td>
                          <td>{formatNumber(linkCandidate.lift, 2)}</td>
                          <td>{formatNumber(linkCandidate.jaccard, 3)}</td>
                          <td>{formatNumber(linkCandidate.heat_adjusted_overlap, 2)}</td>
                          <td>
                            {linkCandidate.sample_sufficient ? (
                              <span className="linkage-tag-success">充足 (N≥20且≥3次)</span>
                            ) : (
                              <span className="linkage-tag-warning">样本不足</span>
                            )}
                          </td>
                          <td>
                            <details>
                              <summary title="展开查看全部共同交易日">
                                {linkCandidate.shared_dates.length} 个交易日
                              </summary>
                              <div className="linkage-shared-dates-box">
                                {linkCandidate.shared_dates.join('、')}
                              </div>
                            </details>
                          </td>
                          <td>
                            <button
                              type="button"
                              className="linkage-switch-btn"
                              onClick={() => handleSelectStock(linkCandidate.code)}
                            >
                              设为中心
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>

                {paginatedLinks.totalPages > 1 && (
                  <div className="linkage-pagination">
                    <button
                      type="button"
                      className="linkage-pagination-btn"
                      disabled={paginatedLinks.currentPage <= 1}
                      onClick={() => setRelationPage(paginatedLinks.currentPage - 1)}
                      title="上一页"
                      aria-label="关联股票上一页"
                    >
                      <ChevronLeft size={14} />
                    </button>
                    <span>
                      {paginatedLinks.currentPage} / {paginatedLinks.totalPages} 页
                    </span>
                    <button
                      type="button"
                      className="linkage-pagination-btn"
                      disabled={paginatedLinks.currentPage >= paginatedLinks.totalPages}
                      onClick={() => setRelationPage(paginatedLinks.currentPage + 1)}
                      title="下一页"
                      aria-label="关联股票下一页"
                    >
                      <ChevronRight size={14} />
                    </button>
                  </div>
                )}
              </section>

              <section className="linkage-panel" aria-labelledby="linkage-profile-heading">
                <div className="linkage-panel-head">
                  <div>
                    <h2 id="linkage-profile-heading">
                      股性画像：{profileData.name} ({profileData.code})
                    </h2>
                    <span>
                      基于窗口内三池均完整采集的样本日统计；板型、开板与回封指标仅统计涨停侧，封板率按当前窗口重新计算。
                    </span>
                  </div>
                </div>

                <div className="linkage-metrics-grid">
                  <div className="linkage-metric-box">
                    <span className="linkage-metric-title">触涨停 / 涨停 / 炸板 / 跌停日</span>
                    <strong className="linkage-metric-num">
                      {profileData.touch_days} / {profileData.seal_days} / {profileData.broken_days} /{' '}
                      {profileData.down_days} 天
                    </strong>
                    <span className="linkage-metric-sub">
                      三池独立日终记录；最近出现于 {profileData.last_seen}
                    </span>
                  </div>

                  <div className="linkage-metric-box">
                    <span className="linkage-metric-title">本地封板率</span>
                    <strong className="linkage-metric-num">
                      {formatPercent(profileData.seal_rate)}
                    </strong>
                    <span className="linkage-metric-sub">
                      收盘涨停日 / 触及涨停日
                    </span>
                  </div>

                  <div className="linkage-metric-box">
                    <span className="linkage-metric-title">样本触涨停频率</span>
                    <strong className="linkage-metric-num">
                      {formatPercent(profileData.event_frequency)}
                    </strong>
                    <span className="linkage-metric-sub">
                      触及涨停日 / 完整样本日 N
                    </span>
                  </div>

                  <div className="linkage-metric-box">
                    <span className="linkage-metric-title">样本跌停频率</span>
                    <strong className="linkage-metric-num">
                      {formatPercent(profileData.down_frequency)}
                    </strong>
                    <span className="linkage-metric-sub">
                      收盘跌停日 / 完整样本日 N（跌停不进入上涨侧分母）
                    </span>
                  </div>

                  <div className="linkage-metric-box">
                    <span className="linkage-metric-title">触涨停后收盘跌停</span>
                    <strong className="linkage-metric-num">{profileData.up_to_down_days} 天</strong>
                    <span className="linkage-metric-sub">
                      同日同时出现在炸板池与跌停池；不改变触板日与封板率分母
                    </span>
                  </div>

                  <div className="linkage-metric-box">
                    <span className="linkage-metric-title">已知曾开板 / 回封天数</span>
                    <strong className="linkage-metric-num">
                      {profileData.opened_days} / {profileData.recovered_days} 天
                    </strong>
                    <span className="linkage-metric-sub">
                      回封率 {formatPercent(profileData.recovery_rate)}（炸板池 + 收盘涨停且开板&gt;0）
                    </span>
                  </div>

                  <div className="linkage-metric-box">
                    <span className="linkage-metric-title">平均开板次数</span>
                    <strong className="linkage-metric-num">
                      {profileData.mean_open_count !== null
                        ? `${formatNumber(profileData.mean_open_count, 1)} 次`
                        : '--'}
                    </strong>
                    <span className="linkage-metric-sub">
                      未知开板数单列：{profileData.open_count_unknown_days} 天（未知不补0）
                    </span>
                  </div>

                  <div className="linkage-metric-box">
                    <span className="linkage-metric-title">首次触板时刻中位数</span>
                    <strong className="linkage-metric-num">
                      {formatTimeText(profileData.median_first_time)}
                    </strong>
                    <span className="linkage-metric-sub">
                      有时间戳样本：{profileData.first_time_samples} 个
                    </span>
                  </div>

                  <div className="linkage-metric-box">
                    <span className="linkage-metric-title">首次跌停时刻中位数</span>
                    <strong className="linkage-metric-num">
                      {formatTimeText(profileData.median_first_down_time)}
                    </strong>
                    <span className="linkage-metric-sub">
                      仅统计有真实跌停时间的记录：{formatNumber(profileData.first_down_time_samples)}
                      {' '}个（不混用涨停时间）
                    </span>
                  </div>
                </div>

                <div className="linkage-distribution-grid">
                  <div className="linkage-dist-card">
                    <div className="linkage-dist-head">首次触板时段分布</div>
                    {FIXED_TIME_BUCKET_LABELS.map((bucketLabel) => {
                      const matchedBucket = profileData.first_time_buckets?.find(
                        (bucketCandidate) => bucketCandidate.label === bucketLabel,
                      )
                      const bucketCount = matchedBucket?.count ?? 0
                      const totalBucketSamples = Math.max(1, profileData.first_time_samples)
                      const percentWidth = Math.min(100, Math.round((bucketCount / totalBucketSamples) * 100))
                      return (
                        <div key={bucketLabel} className="linkage-dist-row">
                          <span className="linkage-dist-label">{bucketLabel}</span>
                          <div className="linkage-dist-progress">
                            <div
                              className="linkage-dist-progress-bar"
                              style={{ width: `${percentWidth}%` }}
                            />
                          </div>
                          <span className="linkage-dist-count">{bucketCount} 次</span>
                        </div>
                      )
                    })}
                  </div>

                  <div className="linkage-dist-card">
                    <div className="linkage-dist-head">有源板型分布</div>
                    {(!profileData.limit_types || profileData.limit_types.length === 0) && (
                      <span className="muted" style={{ fontSize: '11px' }}>
                        暂无有源板型细分标签
                      </span>
                    )}
                    {profileData.limit_types?.map((typeCandidate) => {
                      const typeCount = typeCandidate.count
                      const totalTouchDays = Math.max(1, profileData.touch_days)
                      const percentWidth = Math.min(100, Math.round((typeCount / totalTouchDays) * 100))
                      return (
                        <div key={typeCandidate.label} className="linkage-dist-row">
                          <span className="linkage-dist-label">{typeCandidate.label || '常规板'}</span>
                          <div className="linkage-dist-progress">
                            <div
                              className="linkage-dist-progress-bar"
                              style={{ width: `${percentWidth}%`, background: 'var(--orange, #e07a31)' }}
                            />
                          </div>
                          <span className="linkage-dist-count">{typeCount} 次</span>
                        </div>
                      )
                    })}
                  </div>
                </div>
              </section>

              <section className="linkage-panel" aria-labelledby="linkage-events-heading">
                <div className="linkage-panel-head">
                  <div>
                    <h2 id="linkage-events-heading">
                      历史事件明细：{profileData.name} ({profileData.code})
                    </h2>
                    <span>
                      共 {eventList.length} 条记录（{eventPoolSummary}）；涨跌停形态保留源标签，不推断连板序列。
                    </span>
                  </div>
                </div>

                <div className="linkage-table-context">
                  涨停、炸板、跌停为独立的日终池记录；同一天可同时出现炸板与跌停两行。时间列会按记录所在池明确标出涨跌停两侧：上涨侧用涨停时间，跌停侧只用真实跌停时间，缺失显示“--”。
                </div>

                <div className="linkage-table-wrap">
                  <table className="linkage-table" aria-label="涨停、炸板与跌停历史事件明细表">
                    <thead>
                      <tr>
                        <th>交易日期</th>
                        <th>收盘状态</th>
                        <th>开板次数</th>
                        <th>首次触及所列涨/跌停</th>
                        <th>末次触及所列涨/跌停</th>
                        <th>形态 / 板型</th>
                        <th>源标签（几天几板）</th>
                        <th>涨跌幅</th>
                        <th>换手率</th>
                        <th>涨停 / 跌停原因</th>
                      </tr>
                    </thead>
                    <tbody>
                      {paginatedEvents.paginatedItems.length === 0 && (
                        <tr>
                          <td colSpan={10} style={{ textAlign: 'center', padding: '18px' }} className="muted">
                            暂无历史事件明细
                          </td>
                        </tr>
                      )}
                      {paginatedEvents.paginatedItems.map((eventItem) => {
                        const poolStatus = formatPoolStatus(eventItem.pool)
                        const touchTimes = eventTouchTimes(eventItem)
                        return (
                          <tr key={formatEventRowKey(eventItem)}>
                            <td>
                              <strong>{eventItem.trade_date}</strong>
                            </td>
                            <td>
                              <span className={`linkage-pool-tag ${poolStatus.className}`}>
                                {poolStatus.label}
                              </span>
                            </td>
                            <td>{formatOpenCountDisplay(eventItem.open_count)}</td>
                            <td title={`${touchTimes.firstLabel}（${touchTimes.side === 'down' ? '跌停侧' : '涨停侧'}）`}>
                              <span className={`linkage-touch-side ${touchTimes.side}`}>
                                {touchTimes.side === 'down' ? '跌停侧' : '涨停侧'}
                              </span>
                              {formatTimeText(touchTimes.first)}
                            </td>
                            <td title={`${touchTimes.lastLabel}（${touchTimes.side === 'down' ? '跌停侧' : '涨停侧'}）`}>
                              <span className={`linkage-touch-side ${touchTimes.side}`}>
                                {touchTimes.side === 'down' ? '跌停侧' : '涨停侧'}
                              </span>
                              {formatTimeText(touchTimes.last)}
                            </td>
                            <td>{eventItem.limit_type || '--'}</td>
                            <td>
                              <strong>{eventItem.high_days || '--'}</strong>
                            </td>
                            <td>{formatPercentPoint(eventItem.change_percent)}</td>
                            <td>{formatPercentPoint(eventItem.turnover_rate)}</td>
                            <td>
                              <span title={eventItem.reason || '--'}>
                                {eventItem.reason || '--'}
                              </span>
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>

                {paginatedEvents.totalPages > 1 && (
                  <div className="linkage-pagination">
                    <button
                      type="button"
                      className="linkage-pagination-btn"
                      disabled={paginatedEvents.currentPage <= 1}
                      onClick={() => setEventPage(paginatedEvents.currentPage - 1)}
                      title="上一页"
                      aria-label="历史明细上一页"
                    >
                      <ChevronLeft size={14} />
                    </button>
                    <span>
                      {paginatedEvents.currentPage} / {paginatedEvents.totalPages} 页
                    </span>
                    <button
                      type="button"
                      className="linkage-pagination-btn"
                      disabled={paginatedEvents.currentPage >= paginatedEvents.totalPages}
                      onClick={() => setEventPage(paginatedEvents.currentPage + 1)}
                      title="下一页"
                      aria-label="历史明细下一页"
                    >
                      <ChevronRight size={14} />
                    </button>
                  </div>
                )}
              </section>

              <details className="linkage-auxiliary" aria-labelledby="linkage-auxiliary-heading">
                <summary>
                  <span>
                    <strong id="linkage-auxiliary-heading">题材与连板补充</strong>
                    <small>同花顺涨停简图分类、连板梯队与市场摘要，辅助核对，不是全量成份</small>
                  </span>
                  <span className="linkage-auxiliary-action">展开</span>
                </summary>
                <div className="linkage-auxiliary-body">
                  <ThsFocusPanel
                    tradeDate={analysisData.coverage.latest_date || asOfDate}
                    scope={marketScope}
                    stockCode={selectedStockCode}
                    authRequired={authRequired}
                    nested
                  />
                </div>
              </details>
            </>
          )}
        </>
      )}

      {todayCoreOpen && (
        <TodayCorePanel
          authRequired={authRequired}
          defaultDays={lookbackDays}
          defaultScope={marketScope}
          onClose={() => setTodayCoreOpen(false)}
          returnFocusRef={todayCoreReturnRef}
          onOpenStock={(code, name) => {
            setTodayCoreOpen(false)
            onContextChange({ ...context, stockCode: code, stockName: name })
          }}
          onOpenQuality={onOpenQuality}
        />
      )}
    </section>
  )
}

function CoverageSummary({
  coverage,
  days,
  totalStocks,
  requestedAsOf,
  onUseLatestDate,
}: {
  coverage: LimitUpCoverage
  days: LimitUpDay[]
  totalStocks: number | null
  requestedAsOf: string
  onUseLatestDate?: () => void
}) {
  const recentDays = [...days].sort((dayFirst, daySecond) =>
    daySecond.trade_date.localeCompare(dayFirst.trade_date),
  ).slice(0, 5)

  return (
    <div className="linkage-coverage-strip" aria-label="数据采集与样本覆盖说明">
      <div className="linkage-coverage-cell">
        <span className="linkage-coverage-label">实际样本覆盖区间</span>
        <span className="linkage-coverage-value">
          {coverage.first_date && coverage.latest_date
            ? `${coverage.first_date} ~ ${coverage.latest_date}`
            : '暂无完整采集样本'}
        </span>
        {coverage.latest_date && requestedAsOf && coverage.latest_date < requestedAsOf && (
          <small className="linkage-coverage-note">
            请求截止 {requestedAsOf}，最近完整样本为 {coverage.latest_date}
          </small>
        )}
        {onUseLatestDate && (
          <button type="button" className="linkage-coverage-action" onClick={onUseLatestDate}>
            对齐最近完整样本日
          </button>
        )}
      </div>

      <div className="linkage-coverage-cell">
        <span className="linkage-coverage-label">完整采集样本日 (N)</span>
        <span className="linkage-coverage-value">
          <strong>{coverage.observed_days}</strong> 天
          <small className="muted" style={{ marginLeft: '4px' }}>
            / 回看 {coverage.lookback_days} 自然日
          </small>
        </span>
      </div>

      <div className="linkage-coverage-cell">
        <span className="linkage-coverage-label">最近完整样本日三池计数</span>
        {recentDays.length === 0 ? (
          <span className="linkage-coverage-value">
            {totalStocks === null ? '未读取概览' : '--'}
          </span>
        ) : (
          <details className="linkage-day-details">
            <summary>
              涨停 {recentDays[0].sealed_count} · 炸板 {recentDays[0].broken_count} · 跌停{' '}
              {recentDays[0].down_count} · {recentDays[0].trade_date}
            </summary>
            <div className="linkage-day-list">
              {recentDays.map((dayRecord) => (
                <div key={dayRecord.trade_date} className="linkage-day-row">
                  <span>{dayRecord.trade_date}</span>
                  <span>
                    涨停 {dayRecord.sealed_count} · 炸板 {dayRecord.broken_count} · 跌停{' '}
                    {dayRecord.down_count}
                  </span>
                </div>
              ))}
            </div>
          </details>
        )}
        <small className="linkage-coverage-note">
          {totalStocks === null ? '展开候选股票后读取三池计数' : '仅列出三池全部完成的日期'}
        </small>
      </div>

      <div className="linkage-coverage-cell">
        <span className="linkage-coverage-label">样本充足度</span>
        <div>
          {coverage.sample_sufficient ? (
            <span className="linkage-tag-success">
              <Check size={12} />
              充足 (≥{coverage.minimum_samples}天)
            </span>
          ) : (
            <span className="linkage-tag-warning">
              <AlertTriangle size={12} />
              样本不足 (&lt;{coverage.minimum_samples}天)
            </span>
          )}
        </div>
      </div>

      <div className="linkage-coverage-cell">
        <span className="linkage-coverage-label">出现标的总数</span>
        <span className="linkage-coverage-value">
          {totalStocks === null ? (
            <span className="muted">未读取概览</span>
          ) : (
            <>
              <strong>{totalStocks}</strong> 只个股
            </>
          )}
        </span>
        <small className="linkage-coverage-note">
          {totalStocks === null ? '展开候选股票后读取' : '含只在跌停池出现的股票'}
        </small>
      </div>
    </div>
  )
}
