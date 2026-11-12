import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Check, Info, Layers, RefreshCw, Tag } from 'lucide-react'

import { api } from './api/client'
import {
  formatThsFocusPercent,
  hasThsFocusMarketValues,
  isThsFocusScope,
  isThsFocusTradeDate,
  selectedThsFocusBlocks,
  selectedThsFocusLadder,
  selectedThsFocusTabs,
  sortedThsFocusLadder,
  thsFocusCoverageNotice,
  thsFocusDateVerificationText,
  thsFocusDisplayLimitations,
  thsFocusLadderMismatch,
  thsFocusMarketSummary,
  thsFocusPayload,
  thsFocusQueryEnabled,
  thsFocusQueryKey,
  thsFocusResponseMatches,
  thsFocusScopeLabel,
  thsFocusSourceLimitation,
  type ThsFocusBlockHit,
  type ThsFocusLevel,
  type ThsFocusMarketSummary,
  type ThsFocusPayload,
  type ThsFocusTab,
  type ThsFocusTabHit,
} from './api/thsfocus'
import './ths-focus.css'

function formatCount(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '--'
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 0 }).format(value)
}

function formatDateTime(value: string | undefined): string {
  if (!value) return '--'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString('zh-CN', {
    timeZone: 'Asia/Shanghai',
    hour12: false,
  })
}

function changeClass(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value) || value === 0) return 'ths-focus-neutral'
  return value > 0 ? 'positive' : 'negative'
}

function statusView(input: {
  payload: ThsFocusPayload | undefined
  authRequired: boolean
  dateReady: boolean
  scopeReady: boolean
  loading: boolean
  fetching: boolean
  failed: boolean
}) {
  if (!input.dateReady) return { label: '等待日期', className: 'pending', icon: null }
  if (!input.scopeReady) return { label: '等待范围', className: 'pending', icon: null }
  if (input.authRequired) return { label: '需要令牌', className: 'pending', icon: null }
  if (input.failed) return { label: '读取失败', className: 'failed', icon: <AlertTriangle size={13} /> }
  if (input.loading) return { label: '读取中', className: 'pending', icon: null }
  if (input.fetching) return { label: '刷新中', className: 'pending', icon: null }
  if (input.payload?.status === 'not_collected') return { label: '尚未采集', className: 'pending', icon: <AlertTriangle size={13} /> }
  if (input.payload?.status === 'complete') return { label: '已归档', className: 'complete', icon: <Check size={13} /> }
  return { label: '等待数据', className: 'pending', icon: null }
}

export function ThsFocusPanel({
  tradeDate,
  scope,
  stockCode,
  authRequired,
  nested = false,
}: {
  tradeDate: string
  scope: string
  stockCode: string
  authRequired: boolean
  nested?: boolean
}) {
  const normalizedDate = tradeDate.trim()
  const normalizedScope = scope.trim()
  const normalizedStockCode = stockCode.trim()
  const dateReady = isThsFocusTradeDate(normalizedDate)
  const scopeReady = isThsFocusScope(normalizedScope)
  const queryEnabled = thsFocusQueryEnabled({
    tradeDate: normalizedDate,
    scope: normalizedScope,
    authRequired,
  })

  const query = useQuery({
    queryKey: thsFocusQueryKey(normalizedDate, normalizedScope),
    queryFn: async ({ signal }) => {
      const response = await api.thsFocus(normalizedDate, normalizedScope, signal)
      const payload = thsFocusPayload(response)
      if (!payload) throw new Error('复盘补充接口未返回有效数据')
      if (!thsFocusResponseMatches(payload, normalizedDate, normalizedScope)) {
        throw new Error('复盘补充响应的日期或市场范围与请求不一致')
      }
      return payload
    },
    enabled: queryEnabled,
    refetchInterval: false,
    refetchOnWindowFocus: false,
    retry: false,
  })

  const payload = authRequired ? undefined : query.data
  const snapshot = payload?.status === 'complete' ? payload.snapshot ?? undefined : undefined
  const selectedBlocks = selectedThsFocusBlocks(snapshot, normalizedStockCode)
  const selectedTabs = selectedThsFocusTabs(snapshot, normalizedStockCode)
  const selectedLadder = selectedThsFocusLadder(snapshot, normalizedStockCode)
  const selectedStockName = selectedTabs[0]?.stock.name ?? selectedBlocks[0]?.stock.name ?? selectedLadder?.stock.name
  const ladder = sortedThsFocusLadder(snapshot)
  const marketSummary = thsFocusMarketSummary(snapshot?.market, snapshot?.comparison)
  const status = statusView({
    payload,
    authRequired,
    dateReady,
    scopeReady,
    loading: query.isLoading,
    fetching: query.isFetching,
    failed: query.isError,
  })

  return (
    <section className={`ths-focus-panel ${nested ? 'nested' : ''}`} aria-labelledby="ths-focus-title">
      <header className="ths-focus-header">
        <div className="ths-focus-heading">
          <p className="eyebrow">辅助核对 · 同花顺源</p>
          <h2 id="ths-focus-title">题材与连板补充</h2>
          <p className="ths-focus-subline">
            {dateReady ? normalizedDate : '等待交易日'}
            {scopeReady ? ` · ${thsFocusScopeLabel(normalizedScope)}` : ' · 等待市场范围'}
            {' · 只读归档，不轮询、不触发采集'}
          </p>
        </div>
        <div className="ths-focus-header-actions">
          <span className={`ths-focus-status ${status.className}`}>
            {status.icon}
            {status.label}
          </span>
          <button
            type="button"
            className="icon-button"
            title="重新读取复盘补充归档"
            aria-label="重新读取复盘补充归档"
            disabled={!queryEnabled || query.isFetching}
            onClick={() => void query.refetch()}
          >
            <RefreshCw size={14} className={query.isFetching ? 'spin' : ''} />
          </button>
        </div>
      </header>

      <div className="ths-focus-body" aria-live="polite">
        {!dateReady && (
          <FocusState
            icon={<Info size={20} />}
            title="等待交易日"
            detail="确定交易日后再读取复盘补充，当前不会发起请求。"
          />
        )}

        {dateReady && !scopeReady && (
          <FocusState
            icon={<Info size={20} />}
            title="等待有效市场范围"
            detail="题材与连板梯队按市场范围隔离，请先选择受支持的市场范围。"
          />
        )}

        {dateReady && scopeReady && authRequired && (
          <FocusState
            icon={<Info size={20} />}
            title="需要访问令牌"
            detail="当前未发起复盘补充请求；完成鉴权后再读取只读归档。"
          />
        )}

        {queryEnabled && query.isLoading && (
          <div className="ths-focus-loading">
            <span className="ths-focus-loading-line" />
            <span className="ths-focus-loading-line short" />
            <span className="ths-focus-loading-line" />
          </div>
        )}

        {queryEnabled && query.isError && (
          <div className="ths-focus-state ths-focus-state-error" role="alert">
            <AlertTriangle size={20} />
            <strong>复盘补充数据读取失败</strong>
            <span>{query.error.message}</span>
            <button type="button" className="ths-focus-retry" onClick={() => void query.refetch()}>
              <RefreshCw size={13} />
              手动重试
            </button>
          </div>
        )}

        {queryEnabled && !query.isLoading && !query.isError && payload?.status === 'not_collected' && (
          <FocusState
            icon={<Info size={20} />}
            title={`${payload.trade_date} 尚未采集复盘补充`}
            detail="该交易日暂无复盘补充归档。当前只读，不会请求上游或把空结果当作已采集。"
          />
        )}

        {queryEnabled && !query.isLoading && !query.isError && payload?.status === 'complete' && !payload.snapshot && (
          <div className="ths-focus-state ths-focus-state-error" role="alert">
            <AlertTriangle size={20} />
            <strong>已标记归档但缺少内容</strong>
            <span>数据响应不完整，页面不补造题材、梯队或市场数据。</span>
          </div>
        )}

        {snapshot && (
          <>
            <div className={`ths-focus-date-alert ${snapshot.source_date_verified ? 'verified' : 'unverified'}`} role={snapshot.source_date_verified ? 'status' : 'alert'}>
              {snapshot.source_date_verified ? <Check size={17} /> : <AlertTriangle size={17} />}
              <div>
                <strong>{snapshot.source_date_verified ? '源日期已核对' : '源未回显交易日期'}</strong>
                <span>{thsFocusDateVerificationText(snapshot.source_date_verified)}</span>
              </div>
            </div>

            <div className="ths-focus-guidance">
              <Info size={15} />
              <span>{thsFocusCoverageNotice()}</span>
            </div>

            <div className="ths-focus-meta" aria-label="复盘补充来源信息">
              <span>来源 同花顺</span>
              <span>题材与梯队范围 {thsFocusScopeLabel(snapshot.scope)}</span>
              <span>{snapshot.tab_date_verified ? '涨停简图日期已核对' : '涨停简图分类未归档或日期未核对'}</span>
              <span>市场摘要独立口径</span>
              <span>采集 {formatDateTime(snapshot.fetched_at)}</span>
              <span>{snapshot.source_date_verified ? '日期回显已核对' : '日期未回显'}</span>
            </div>

            <SourceLimitations limitations={thsFocusDisplayLimitations(snapshot)} />

            <div className="ths-focus-grid">
              <SelectedStockSection
                stockCode={normalizedStockCode}
                selectedStockName={selectedStockName}
                selectedBlocks={selectedBlocks}
                selectedTabs={selectedTabs}
              />
              <MarketSummarySection marketSummary={marketSummary} scope={normalizedScope} />
              <LadderSection ladder={ladder} stockCode={normalizedStockCode} />
            </div>
            <ThemeTabsSection tabs={snapshot.tabs ?? []} stockCode={normalizedStockCode} />
          </>
        )}
      </div>
    </section>
  )
}

function SourceLimitations({ limitations }: { limitations: string[] }) {
  if (!limitations.length) {
    return (
      <div className="ths-focus-guidance">
        <Info size={15} />
        <span>源未附口径限制说明。仍以 Top 列表、源默认市场口径和非因果解释为准，不将其视为全量覆盖。</span>
      </div>
    )
  }
  return (
    <section className="ths-focus-limitations" aria-label="源口径与覆盖限制">
      <strong>源口径与覆盖限制（{limitations.length} 条）</strong>
      <ul>
        {limitations.map((limitation, index) => (
          <li key={`${index}-${limitation}`}>{limitation}</li>
        ))}
      </ul>
    </section>
  )
}

function FocusState({
  icon,
  title,
  detail,
}: {
  icon: React.ReactNode
  title: string
  detail: string
}) {
  return (
    <div className="ths-focus-state">
      {icon}
      <strong>{title}</strong>
      <span>{detail}</span>
    </div>
  )
}

function SelectedStockSection({
  stockCode,
  selectedStockName,
  selectedBlocks,
  selectedTabs,
}: {
  stockCode: string
  selectedStockName?: string
  selectedBlocks: ThsFocusBlockHit[]
  selectedTabs: ThsFocusTabHit[]
}) {
  return (
    <section className="ths-focus-card ths-focus-topics" aria-labelledby="ths-focus-topics-title">
      <div className="ths-focus-card-head">
        <div>
          <span className="ths-focus-card-eyebrow">选中股票</span>
          <h3 id="ths-focus-topics-title">{selectedStockName ?? (stockCode || '未选择股票')}</h3>
          <p>{stockCode ? `${stockCode} · 涨停简图分类命中` : '从联动页选择股票后查看命中题材'}</p>
        </div>
        <span className="ths-focus-count">{selectedTabs.length || selectedBlocks.length} 个题材命中</span>
      </div>

      {!stockCode && (
        <div className="ths-focus-inline-empty">
          <Tag size={17} />
          <span>尚未选择股票；连板梯队和源市场摘要仍可独立查看。</span>
        </div>
      )}

      {stockCode && selectedTabs.length === 0 && selectedBlocks.length === 0 && (
        <div className="ths-focus-inline-empty">
          <Tag size={17} />
          <span>所选股票未出现在涨停简图分类中。它可能仍出现在连板梯队，不能据此判断没有其它题材。</span>
        </div>
      )}

      {selectedTabs.length > 0 && (
        <div className="ths-focus-topic-list">
          {selectedTabs.map((hit) => (
            <article className="ths-focus-topic" key={`${hit.tab.name}-${hit.stock.code}`}>
              <div className="ths-focus-topic-head">
                <div>
                  <h4>{hit.tab.name}</h4>
                  <span>涨停简图分类</span>
                </div>
                <span className="ths-focus-source-order">源分类第 {hit.source_order} 项</span>
              </div>

              <div className="ths-focus-metric-row">
                <FocusMetric label="分类涨停股" value={`${formatCount(hit.tab.stocks.length)} 只`} />
                <FocusMetric label="源涨停标签" value={hit.stock.reason.trim() || '未提供'} />
              </div>

              <div className="ths-focus-reason">
                <span className="ths-focus-field-label">
                  <Tag size={13} />
                  题材催化
                </span>
                <strong>{hit.stock.abnormal_reason.trim() || '源未提供题材催化'}</strong>
                <small>分类与原因来自同花顺涨停简图，仅用于辅助解释，不代表已核验的因果。</small>
              </div>

              {hit.stock.detail_reason.trim() ? (
                <details className="ths-focus-reason-detail">
                  <summary>展开源说明原文（{hit.stock.detail_reason.trim().length.toLocaleString('zh-CN')} 字）</summary>
                  <p>上游原文可能包含 AI 自动汇总与免责声明；这里仅保留原文供核对，不作为事实复制或因果依据。</p>
                  <pre>{hit.stock.detail_reason}</pre>
                </details>
              ) : (
                <p className="ths-focus-missing-detail">源未提供说明原文。</p>
              )}
            </article>
          ))}
        </div>
      )}

      {selectedTabs.length === 0 && selectedBlocks.length > 0 && (
        <div className="ths-focus-topic-list">
          {selectedBlocks.map((hit) => (
            <article className="ths-focus-topic" key={hit.block.code}>
              <div className="ths-focus-topic-head">
                <div>
                  <h4>{hit.block.name}</h4>
                  <span>{hit.block.code}</span>
                </div>
                <span className="ths-focus-source-order">源列表第 {hit.source_order} 项</span>
              </div>

              <div className="ths-focus-metric-row">
                <FocusMetric label="板块涨幅" value={formatThsFocusPercent(hit.block.change)} valueClassName={changeClass(hit.block.change)} />
                <FocusMetric label="列出涨停股" value={`${formatCount(hit.block.limit_up_count)} 只`} />
                <FocusMetric label="个股涨幅" value={formatThsFocusPercent(hit.stock.change_percent)} valueClassName={changeClass(hit.stock.change_percent)} />
                <FocusMetric label="连板数" value={String(hit.stock.continue_num)} />
                <FocusMetric label="几天几板" value={hit.stock.high_days || '未提供'} />
              </div>

              <div className="ths-focus-reason">
                <span className="ths-focus-field-label">
                  <Tag size={13} />
                  源原因标签
                </span>
                <strong>{hit.stock.reason.trim() || '源未提供原因标签'}</strong>
                <small>题材与原因来自源标签，仅用于辅助解释，不代表已核验的因果。</small>
              </div>

              {hit.stock.reason_detail.trim() ? (
                <details className="ths-focus-reason-detail">
                  <summary>展开源说明原文（{hit.stock.reason_detail.trim().length.toLocaleString('zh-CN')} 字）</summary>
                  <p>上游原文可能包含 AI 自动汇总与免责声明；这里仅保留原文供核对，不作为事实复制或因果依据。</p>
                  <pre>{hit.stock.reason_detail}</pre>
                </details>
              ) : (
                <p className="ths-focus-missing-detail">源未提供说明原文。</p>
              )}
            </article>
          ))}
        </div>
      )}
    </section>
  )
}

function FocusMetric({
  label,
  value,
  valueClassName,
}: {
  label: string
  value: string
  valueClassName?: string
}) {
  return (
    <div className="ths-focus-metric">
      <span>{label}</span>
      <strong className={valueClassName}>{value}</strong>
    </div>
  )
}

function ThemeTabsSection({ tabs, stockCode }: { tabs: ThsFocusTab[]; stockCode: string }) {
  const uniqueStocks = new Set<string>()
  tabs.forEach((tab) => tab.stocks.forEach((stock) => uniqueStocks.add(stock.code)))
  return (
    <section className="ths-focus-card ths-focus-tabs" aria-labelledby="ths-focus-tabs-title">
      <div className="ths-focus-card-head">
        <div>
          <span className="ths-focus-card-eyebrow">涨停简图 · 每日源分类</span>
          <h3 id="ths-focus-tabs-title">题材分类与成员</h3>
          <p>来自 tab_list/all_tab_data，按源分类展开可见具体个股；block_top 仅作普通板块视图，不再冒充这里的主线分类。</p>
        </div>
        <span className="ths-focus-count">
          {tabs.length} 类 · {uniqueStocks.size} 只去重个股
        </span>
      </div>

      {tabs.length === 0 ? (
        <div className="ths-focus-inline-empty">
          <Tag size={17} />
          <span>该日归档没有涨停简图分类。旧版 block_top 快照不会被回填成这里的分类。</span>
        </div>
      ) : (
        <div className="ths-focus-tab-list">
          {tabs.map((tab) => {
            const containsSelected = tab.stocks.some((stock) => stock.code === stockCode)
            return (
              <details className={`ths-focus-tab-group ${containsSelected ? 'contains-selected' : ''}`} key={tab.name} open={containsSelected || undefined}>
                <summary>
                  <strong>{tab.name}</strong>
                  <span>{tab.stocks.length} 只</span>
                </summary>
                <div className="ths-focus-tab-stocks">
                  {tab.stocks.map((stock) => (
                    <div className={`ths-focus-tab-stock ${stock.code === stockCode ? 'selected' : ''}`} key={`${tab.name}-${stock.code}`}>
                      <div>
                        <strong>{stock.name}</strong>
                        <small>{stock.code}</small>
                      </div>
                      <span>{stock.abnormal_reason.trim() || stock.reason.trim() || '源未提供原因'}</span>
                    </div>
                  ))}
                </div>
              </details>
            )
          })}
        </div>
      )}
    </section>
  )
}

function LadderSection({ ladder, stockCode }: { ladder: ThsFocusLevel[]; stockCode: string }) {
  const stockCount = ladder.reduce((total, level) => total + level.stocks.length, 0)
  const mismatchCount = ladder.reduce((total, level) => (
    total + level.stocks.filter((stock) => thsFocusLadderMismatch(level.height, stock.continue_num)).length
  ), 0)

  return (
    <section className="ths-focus-card ths-focus-ladder" aria-labelledby="ths-focus-ladder-title">
      <div className="ths-focus-card-head">
        <div>
          <span className="ths-focus-card-eyebrow">按市场范围隔离</span>
          <h3 id="ths-focus-ladder-title">连板梯队</h3>
          <p>梯队高度与个股连板数均保留源字段，不用几天几板标签换算。</p>
        </div>
        <span className="ths-focus-count">
          {ladder.length} 档 · {stockCount} 只
          {ladder[0] ? ` · 所选范围最高 ${ladder[0].height} 板` : ''}
        </span>
      </div>

      {mismatchCount > 0 && (
        <div className="ths-focus-inline-warning">
          <AlertTriangle size={14} />
          <span>有 {mismatchCount} 个源项的个股连板数与梯队高度不一致，已原样标注。</span>
        </div>
      )}

      {ladder.length === 0 ? (
        <div className="ths-focus-inline-empty">
          <Layers size={17} />
          <span>源连板梯队为空，或该范围没有已归档梯队数据。</span>
        </div>
      ) : (
        <div className="ths-focus-ladder-list">
          {ladder.map((level) => {
            const containsSelected = level.stocks.some((stock) => stock.code === stockCode)
            return (
              <article className={`ths-focus-level ${containsSelected ? 'contains-selected' : ''}`} key={level.height}>
                <div className="ths-focus-level-head">
                  <strong>{level.height} 板</strong>
                  <span>源梯队高度</span>
                  <em>{level.stocks.length} 只</em>
                </div>
                <div className="ths-focus-level-stocks">
                  {level.stocks.map((stock) => {
                    const selected = stock.code === stockCode
                    const mismatch = thsFocusLadderMismatch(level.height, stock.continue_num)
                    return (
                      <div
                        className={`ths-focus-ladder-stock ${selected ? 'selected' : ''} ${mismatch ? 'mismatch' : ''}`}
                        key={`${level.height}-${stock.code}`}
                      >
                        <div>
                          <strong>{stock.name}</strong>
                          <small>{stock.code}</small>
                        </div>
                        <span>
                          {mismatch ? `源连板数 ${stock.continue_num} / 梯队 ${level.height}` : `${stock.continue_num} 连板`}
                        </span>
                      </div>
                    )
                  })}
                </div>
              </article>
            )
          })}
        </div>
      )}
    </section>
  )
}

function MarketSummarySection({
  marketSummary,
  scope,
}: {
  marketSummary: ThsFocusMarketSummary
  scope: string
}) {
  const hasValues = hasThsFocusMarketValues(marketSummary)
  return (
    <section className="ths-focus-card ths-focus-market" aria-labelledby="ths-focus-market-title">
      <div className="ths-focus-card-head">
        <div>
          <span className="ths-focus-card-eyebrow">独立口径</span>
          <h3 id="ths-focus-market-title">市场摘要</h3>
          <p>涨跌家数与最高板来自同花顺市场摘要及两日对比。</p>
        </div>
        <span className="ths-focus-count">非当前题材范围</span>
      </div>

      {hasValues ? (
        <div className="ths-focus-market-grid">
          <FocusMetric label="上涨家数" value={formatCount(marketSummary.rise)} />
          <FocusMetric label="下跌家数" value={formatCount(marketSummary.fall)} />
          <FocusMetric label="平盘家数" value={formatCount(marketSummary.flat)} />
          <FocusMetric label="涨停家数" value={formatCount(marketSummary.limitUp)} />
          <FocusMetric label="跌停家数" value={formatCount(marketSummary.limitDown)} />
          <FocusMetric label="今日最高板" value={marketSummary.todayMaxBoard === undefined ? '--' : `${marketSummary.todayMaxBoard} 板`} />
          <FocusMetric label="前日最高板" value={marketSummary.yesterdayMaxBoard === undefined ? '--' : `${marketSummary.yesterdayMaxBoard} 板`} />
        </div>
      ) : (
        <div className="ths-focus-inline-empty">
          <Info size={17} />
          <span>源市场摘要未提供可识别的涨跌家数或最高板字段；页面不推算替代值。</span>
        </div>
      )}

      <div className="ths-focus-market-dates">
        <span>对比今日 {marketSummary.todayDate ?? '源未回显'}</span>
        <span>对比前日 {marketSummary.yesterdayDate ?? '源未回显'}</span>
      </div>

      <p className="ths-focus-scope-note">{thsFocusSourceLimitation(scope)}</p>
    </section>
  )
}
