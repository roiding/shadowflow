import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Activity, AlertTriangle, BarChart3, CalendarDays, Check, ChevronDown, ChevronLeft, ChevronRight, Crosshair, Download, Gauge, Info, LineChart, RefreshCw, Search, Server, Table2, Wifi, WifiOff } from 'lucide-react'
import { api } from './api/client'
import { getToken, UNAUTHORIZED_EVENT } from './auth'
import { TokenGate } from './TokenGate'
import type { BoardStockQuote, FocusResult, FocusScanRequest, RankRecord, RankType, SystemStatus } from './api/types'
import { continuousMetricValues } from './continuousSeries'
import { FocusView } from './views/FocusView'
import { QualityView } from './views/QualityView'

type BoardType = Exclude<RankType, 'stock'>
type View = 'monitor' | 'focus' | 'history' | 'stocks' | 'quality'
type Metric = 'dark_money' | 'regular_money' | 'main_money_inflow' | 'dark_activity' | 'dark_inflow_ratio' | 'change_pct' | 'rank' | 'up_count'
type SortDirection = 'asc' | 'desc'
type SortState<Key extends string> = { key: Key; direction: SortDirection }
type ConstituentSortKey = 'stock_name' | 'stock_code' | 'dark_rank' | 'dark_money' | 'main_money_inflow' | 'dark_activity' | 'latest_price' | 'change_pct' | 'turnover'

const MONITOR_PAGE_SIZE = 25
const CONSTITUENT_PAGE_SIZE = 25

const BOARD_LABELS: Record<BoardType, string> = { industry: '行业', concept: '概念' }
const METRIC_LABELS: Record<Metric, string> = {
  dark_money: '暗盘资金估算', regular_money: '明盘资金', main_money_inflow: '主力净流入',
  dark_activity: '暗盘活跃度', dark_inflow_ratio: '暗盘流入占比', change_pct: '涨跌幅', rank: '榜单排名', up_count: '上涨家数',
}
const METRICS: Metric[] = ['dark_money', 'regular_money', 'main_money_inflow', 'dark_activity', 'dark_inflow_ratio', 'change_pct', 'rank', 'up_count']
const RESEARCH_METRICS: Metric[] = ['dark_money', 'regular_money', 'main_money_inflow']


const DEFAULT_FOCUS_REQUEST: FocusScanRequest = {
  as_of: '', consecutive_days: 3, concept_match: 'all', stock_match: 'all',
  concept_conditions: [
    { field: 'turnover', operator: 'gt', value: 50_000_000_000 },
    { field: 'turnover_rate', operator: 'gt', value: 0.03 },
    { field: 'change_pct', operator: 'between', value: 0.01, max_value: 0.06 },
    { field: 'control_coefficient', operator: 'between', value: 1.5, max_value: 6 },
  ],
  stock_conditions: [
    { field: 'turnover', operator: 'gt', value: 200_000_000 },
    { field: 'turnover_rate', operator: 'gt', value: 0.03 },
    { field: 'change_pct', operator: 'between', value: 0.01, max_value: 0.06 },
    { field: 'control_coefficient', operator: 'between', value: 1.5, max_value: 6 },
  ],
  stock_scope: { main_board_only: true, exclude_st: true, require_qualified_concepts: true },
}

function localDate(date = new Date()) {
  const parts = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(date)
  return `${parts.find((p) => p.type === 'year')?.value}-${parts.find((p) => p.type === 'month')?.value}-${parts.find((p) => p.type === 'day')?.value}`
}

function formatTime(value?: string) {
  if (!value) return '--'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value.slice(11, 16) : date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false, timeZone: 'Asia/Shanghai' })
}

function shanghaiDateTimeKey(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value.slice(0, 16).replace(' ', 'T')
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', hour12: false,
  }).formatToParts(date)
  const part = (type: string) => parts.find((item) => item.type === type)?.value ?? ''
  return `${part('year')}-${part('month')}-${part('day')}T${part('hour')}:${part('minute')}`
}

function shanghaiTime(value: string) {
  return shanghaiDateTimeKey(value).slice(11, 16)
}

// Interval detection must use the Shanghai wall clock, not the browser's
// local minutes, and an unparseable timestamp (NaN) must not masquerade as
// one-minute evidence — that would stretch an archived 48-point series over
// a 240-slot grid and render an almost-empty chart.
function isFiveMinuteSeries(series: RankRecord[]) {
  return !series.some((item) => {
    const minute = Number(shanghaiTime(item.snapshot_at).slice(3))
    return Number.isFinite(minute) && minute % 5 !== 0
  })
}

function formatNumber(value: number, digits = 0) {
  if (!Number.isFinite(value)) return '--'
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: digits, minimumFractionDigits: digits }).format(value)
}

function formatMoney(value: number) {
  if (!Number.isFinite(value)) return '--'
  const abs = Math.abs(value)
  const unit = abs >= 100000000 ? '亿' : abs >= 10000 ? '万' : ''
  const divisor = unit === '亿' ? 100000000 : unit === '万' ? 10000 : 1
  return `${value < 0 ? '-' : ''}${formatNumber(abs / divisor, unit ? 2 : 0)}${unit}`
}

function metricValue(record: RankRecord, metric: Metric) {
  return record[metric]
}

function signedClass(value: number) { return value > 0 ? 'positive' : value < 0 ? 'negative' : 'neutral' }

function metricAvailable(record: RankRecord, metric: Metric) {
  if (['dark_money', 'regular_money', 'main_money_inflow'].includes(metric)) return record.money_available
  if (['dark_activity', 'dark_inflow_ratio', 'rank', 'up_count'].includes(metric)) return record.rank > 0
  return true
}

function jitterInterval(base: number) {
  const factor = base * 0.2
  return Math.round(base - factor + Math.random() * factor * 2)
}

function App() {
  const [authRequired, setAuthRequired] = useState(false)
  const queryClient = useQueryClient()

  useEffect(() => {
    const listener = () => setAuthRequired(true)
    window.addEventListener(UNAUTHORIZED_EVENT, listener)
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, listener)
  }, [])
  const [view, setView] = useState<View>('monitor')
  const [boardType, setBoardType] = useState<BoardType>('industry')
  const [selectedCode, setSelectedCode] = useState('')
  const [mobilePane, setMobilePane] = useState<'ranks' | 'trend'>('ranks')
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<SortState<keyof RankRecord>>({ key: 'rank', direction: 'asc' })
  const [metric, setMetric] = useState<Metric>('dark_money')
  const [secondaryMetric, setSecondaryMetric] = useState<Metric | 'none'>('main_money_inflow')
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [refreshSeconds, setRefreshSeconds] = useState(60)
  const [qualityDate, setQualityDate] = useState('')
  const [focusRequest, setFocusRequest] = useState<FocusScanRequest>(DEFAULT_FOCUS_REQUEST)
  const [historyFrom, setHistoryFrom] = useState(localDate(new Date(Date.now() - 30 * 86400000)))
  const [historyTo, setHistoryTo] = useState(localDate())
  const [historyDate, setHistoryDate] = useState('')
  const [historyAt, setHistoryAt] = useState('15:00')
  const [historyCode, setHistoryCode] = useState('')
  const [stockDate, setStockDate] = useState('')
  const [stockQuery, setStockQuery] = useState('')
  const [stockSort, setStockSort] = useState<SortState<keyof RankRecord>>({ key: 'rank', direction: 'asc' })
  const [stockPage, setStockPage] = useState(1)
  const [debouncedStockQuery, setDebouncedStockQuery] = useState('')

  const refreshInterval = useMemo(() => autoRefresh ? jitterInterval(refreshSeconds * 1000) : false, [autoRefresh, refreshSeconds])
  const statusQuery = useQuery({ queryKey: ['system-status'], queryFn: async () => (await api.status()).data as SystemStatus, refetchInterval: refreshInterval, refetchIntervalInBackground: false })
  const latestTradingDay = statusQuery.data?.latest_trading_day ?? ''
  const rankQuery = useQuery({ queryKey: ['latest', boardType], queryFn: async () => { const started = performance.now(); const result = await api.latest(boardType); return { records: result.data ?? [], requestMs: Math.round(performance.now() - started) } }, refetchInterval: refreshInterval, refetchIntervalInBackground: false })
  const records = useMemo(() => rankQuery.data?.records ?? [], [rankQuery.data?.records])
  const selected = records.find((item) => item.code === selectedCode) ?? records[0]
  const selectedId = selected?.code ?? ''
  const monitorDate = selected?.trade_date ?? records[0]?.trade_date ?? latestTradingDay
  const staleSnapshot = Boolean(records.length && latestTradingDay && monitorDate !== latestTradingDay)
  const nonTradingToday = statusQuery.data?.trading_day === false
  const intradayQuery = useQuery({
    queryKey: ['intraday', boardType, selectedId, monitorDate],
    queryFn: async () => (await api.intraday(boardType, selectedId, monitorDate)).data ?? [],
    enabled: Boolean(selectedId && monitorDate) && view === 'monitor',
    refetchInterval: refreshInterval, refetchIntervalInBackground: false,
  })
  const boardQuotesQuery = useQuery({
    queryKey: ['board-quotes', boardType, selectedId, monitorDate],
    queryFn: async () => api.boardQuotes(boardType, selectedId, monitorDate),
    enabled: Boolean(selectedId && monitorDate) && view === 'monitor',
    refetchInterval: refreshInterval, refetchIntervalInBackground: false,
  })
  const trendQuery = useQuery({
    queryKey: ['trend', boardType, historyCode, historyFrom, historyTo],
    queryFn: async () => (await api.trend(boardType, historyCode, historyFrom, historyTo)).data ?? [],
    enabled: Boolean(historyCode && historyFrom && historyTo) && view === 'history',
  })
  const historyRanksQuery = useQuery({
    queryKey: ['history-ranks', boardType, historyDate, historyAt],
    queryFn: async () => (await api.rankAt(boardType, historyDate, historyAt)).data ?? [],
    enabled: view === 'history' && Boolean(historyDate),
  })
  const historyTradingDaysQuery = useQuery({
    queryKey: ['history-trading-days', historyFrom, historyTo],
    queryFn: async () => (await api.tradingDays(historyFrom, historyTo)).data ?? [],
    enabled: view === 'history' && Boolean(historyFrom && historyTo),
  })
  const historyStartDate = historyTradingDaysQuery.data?.[0] ?? ''
  const historyStartRanksQuery = useQuery({
    queryKey: ['history-start-ranks', boardType, historyStartDate, historyAt],
    queryFn: async () => (await api.rankAt(boardType, historyStartDate, historyAt)).data ?? [],
    enabled: view === 'history' && Boolean(historyStartDate),
  })
  const stocksQuery = useQuery({
    queryKey: ['daily-close', stockDate, debouncedStockQuery, stockSort.key, stockSort.direction, stockPage],
    queryFn: async () => api.dailyClose('stock', stockDate, debouncedStockQuery, stockSort.key, stockSort.direction, stockPage, 100),
    enabled: view === 'stocks' && Boolean(stockDate),
    placeholderData: (previous) => previous,
  })
  const qualityQuery = useQuery({ queryKey: ['quality', qualityDate], queryFn: async () => api.quality(qualityDate), enabled: view === 'quality' && Boolean(qualityDate) })
  const runsQuery = useQuery({ queryKey: ['runs', qualityDate], queryFn: async () => (await api.runs(qualityDate)).data ?? [], enabled: view === 'quality' && Boolean(qualityDate) })
  const focusScan = useMutation({
    mutationFn: async (request: FocusScanRequest) => (await api.focusScan(request)).data as FocusResult,
  })

  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible') void queryClient.invalidateQueries()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [queryClient])

  useEffect(() => {
    if (!latestTradingDay) return
    setStockDate((current) => current || latestTradingDay)
    setQualityDate((current) => current || latestTradingDay)
    setHistoryDate((current) => current || latestTradingDay)
    setFocusRequest((current) => current.as_of ? current : { ...current, as_of: latestTradingDay })
  }, [latestTradingDay])

  useEffect(() => {
    if (records.length && !records.some((item) => item.code === selectedCode)) setSelectedCode(records[0].code)
  }, [records, selectedCode])

  useEffect(() => {
    const available = historyRanksQuery.data ?? []
    if (!available.length || !historyStartDate || !historyStartRanksQuery.data) return
    if (historyCode && available.some((item) => item.code === historyCode)) return
    const startCodes = new Set((historyStartRanksQuery.data ?? []).map((item) => item.code))
    const common = available.find((item) => startCodes.has(item.code))
    setHistoryCode((common ?? available[0]).code)
  }, [historyRanksQuery.data, historyStartDate, historyStartRanksQuery.data, historyCode])

  useEffect(() => {
    const timer = window.setTimeout(() => { setDebouncedStockQuery(stockQuery.trim()); setStockPage(1) }, 300)
    return () => window.clearTimeout(timer)
  }, [stockQuery])

  const visibleRecords = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    return records.filter((item) => !normalized || item.name.toLowerCase().includes(normalized) || item.code.toLowerCase().includes(normalized)).sort((a, b) => {
      const left = a[sort.key] as string | number
      const right = b[sort.key] as string | number
      const result = typeof left === 'string' ? left.localeCompare(String(right), 'zh-CN') : Number(left) - Number(right)
      return sort.direction === 'asc' ? result : -result
    })
  }, [records, query, sort])

  const onSort = (key: keyof RankRecord) => setSort((current) => ({ key, direction: current.key === key && current.direction === 'asc' ? 'desc' : 'asc' }))
  const refreshAll = () => { void rankQuery.refetch(); void statusQuery.refetch(); if (selectedId) { void intradayQuery.refetch(); void boardQuotesQuery.refetch() } }
  const historicalSelected = (historyRanksQuery.data ?? []).find((item) => item.code === historyCode) ?? historyRanksQuery.data?.[0]
  const stockRecords = stocksQuery.data?.data ?? []
  const stockMeta = stocksQuery.data?.meta
  const onStockSort = (key: keyof RankRecord) => { setStockSort((current) => ({ key, direction: current.key === key && current.direction === 'asc' ? 'desc' : 'asc' })); setStockPage(1) }

  if (authRequired && !getToken()) {
    return <TokenGate onAuthenticated={() => { setAuthRequired(false); void queryClient.invalidateQueries() }} />
  }

  return <div className="app-shell">
    <header className="topbar">
      <div className="brand-lockup"><div className="brand-mark"><Activity size={18} /></div><div><strong>ShadowFlow 暗流</strong><span>板块资金监控台</span></div></div>
      <div className="topbar-actions">
        <MarketStatus status={statusQuery.data} />
        <div className={`live-chip ${staleSnapshot ? 'stale' : ''}`}><span className={`live-dot ${rankQuery.isFetching ? 'is-fetching' : ''}`} />{records.length ? `${nonTradingToday ? '最近交易日' : staleSnapshot ? '最近可用' : '最新'} ${(nonTradingToday || staleSnapshot) ? `${monitorDate} ` : ''}${formatTime(records[0].snapshot_at)}` : '等待数据'}</div>
        <label className="refresh-control"><RefreshCw size={14} /><select value={refreshSeconds} onChange={(event) => setRefreshSeconds(Number(event.target.value))}><option value={30}>30 秒</option><option value={60}>60 秒</option><option value={300}>5 分钟</option></select></label>
        <button className={`icon-button ${autoRefresh ? 'active' : ''}`} title="切换自动刷新" onClick={() => setAutoRefresh((value) => !value)}>{autoRefresh ? <Wifi size={17} /> : <WifiOff size={17} />}</button>
        <button className="icon-button" title="立即刷新" onClick={refreshAll} disabled={rankQuery.isFetching}><RefreshCw size={17} className={rankQuery.isFetching ? 'spin' : ''} /></button>
      </div>
    </header>

    <main className="main-content">
      <div className="view-tabs" role="tablist">
        <button className={view === 'monitor' ? 'selected' : ''} onClick={() => setView('monitor')}><Gauge size={16} />今日监控</button>
        <button className={view === 'focus' ? 'selected' : ''} onClick={() => setView('focus')}><Crosshair size={16} />动态筛选</button>
        <button className={view === 'history' ? 'selected' : ''} onClick={() => setView('history')}><LineChart size={16} />历史回看</button>
        <button className={view === 'stocks' ? 'selected' : ''} onClick={() => setView('stocks')}><Table2 size={16} />收盘个股</button>
        <button className={view === 'quality' ? 'selected' : ''} onClick={() => setView('quality')}><Server size={16} />采集质量</button>
      </div>
      {view === 'monitor' && <MonitorView boardType={boardType} setBoardType={setBoardType} records={visibleRecords} allRecords={records} selected={selected} selectedCode={selectedId} setSelectedCode={setSelectedCode} query={query} setQuery={setQuery} onSort={onSort} sort={sort} metric={metric} setMetric={setMetric} secondaryMetric={secondaryMetric} setSecondaryMetric={setSecondaryMetric} series={intradayQuery.data ?? []} loading={intradayQuery.isLoading} rankError={rankQuery.error} seriesError={intradayQuery.error} requestMs={rankQuery.data?.requestMs} status={statusQuery.data} tradeDate={monitorDate} staleSnapshot={staleSnapshot} mobilePane={mobilePane} setMobilePane={setMobilePane} stocks={boardQuotesQuery.data?.data ?? []} stocksLoading={boardQuotesQuery.isLoading || boardQuotesQuery.isFetching} stocksError={boardQuotesQuery.error} quoteMeta={boardQuotesQuery.data?.meta} />}
      {view === 'focus' && <FocusView request={focusRequest} onScan={(value) => { setFocusRequest(value); focusScan.mutate(value) }} result={focusScan.data} loading={focusScan.isPending} error={focusScan.error} />}
      {view === 'history' && <HistoryView boardType={boardType} setBoardType={setBoardType} selected={historicalSelected} historyRanks={historyRanksQuery.data ?? []} historyCode={historyCode} setHistoryCode={setHistoryCode} historyDate={historyDate} setHistoryDate={setHistoryDate} historyAt={historyAt} setHistoryAt={setHistoryAt} metric={metric} setMetric={setMetric} secondaryMetric={secondaryMetric} setSecondaryMetric={setSecondaryMetric} series={trendQuery.data ?? []} loading={trendQuery.isLoading || historyRanksQuery.isLoading} error={trendQuery.error ?? historyRanksQuery.error} from={historyFrom} to={historyTo} setFrom={setHistoryFrom} setTo={setHistoryTo} />}
      {view === 'stocks' && <StockView date={stockDate} setDate={(value) => { setStockDate(value); setStockPage(1) }} records={stockRecords} total={stockMeta?.total ?? 0} query={stockQuery} setQuery={setStockQuery} sort={stockSort} onSort={onStockSort} page={stockPage} pages={stockMeta?.pages ?? 0} setPage={setStockPage} loading={stocksQuery.isLoading || stocksQuery.isFetching} error={stocksQuery.error} />}
      {view === 'quality' && <QualityView date={qualityDate} setDate={setQualityDate} quality={(qualityQuery.data?.data ?? []).filter((item): item is NonNullable<typeof item> & { rank_type: BoardType } => item.rank_type !== 'stock')} stockQuality={qualityQuery.data?.meta?.stock_archive} manifest={qualityQuery.data?.meta?.archive_manifest} runs={runsQuery.data ?? []} loading={qualityQuery.isLoading || runsQuery.isLoading} error={qualityQuery.error ?? runsQuery.error} />}
    </main>
  </div>
}

function MarketStatus({ status }: { status?: SystemStatus }) {
  const labels: Record<NonNullable<SystemStatus['market_status']>, string> = { pre_open: '盘前', open: '交易中', lunch_break: '午间休市', closed: '已收市' }
  return <div className={`market-status ${status?.market_status ?? 'closed'}`}><span className="status-dot" />{status ? labels[status.market_status] : '连接中'}</div>
}

type MonitorProps = {
	  boardType: BoardType; setBoardType: (value: BoardType) => void; records: RankRecord[]; allRecords: RankRecord[]; selected?: RankRecord; selectedCode: string; setSelectedCode: (value: string) => void; query: string; setQuery: (value: string) => void; onSort: (key: keyof RankRecord) => void; sort: SortState<keyof RankRecord>; metric: Metric; setMetric: (value: Metric) => void; secondaryMetric: Metric | 'none'; setSecondaryMetric: (value: Metric | 'none') => void; series: RankRecord[]; loading: boolean; rankError: Error | null; seriesError: Error | null; requestMs?: number; status?: SystemStatus; tradeDate: string; staleSnapshot: boolean; mobilePane: 'ranks' | 'trend'; setMobilePane: (value: 'ranks' | 'trend') => void; stocks: BoardStockQuote[]; stocksLoading: boolean; stocksError: Error | null; quoteMeta?: { as_of: string; quote_source: string; quote_available: boolean; quoted_count?: number; quote_error?: string; quote_status: string; stale: boolean; cache_age_ms?: number; dark_data_available: boolean; dark_data_count: number }
}

function MonitorView(props: MonitorProps) {
	  const { boardType, setBoardType, records, allRecords, selected, selectedCode, setSelectedCode, query, setQuery, onSort, sort, metric, setMetric, secondaryMetric, setSecondaryMetric, series, loading, rankError, seriesError, requestMs, status, tradeDate, staleSnapshot, mobilePane, setMobilePane, stocks, stocksLoading, stocksError, quoteMeta } = props
  const nonTradingDay = status?.trading_day === false
  const [page, setPage] = useState(1)
  const pages = Math.ceil(records.length / MONITOR_PAGE_SIZE)
  const pageRecords = records.slice((page - 1) * MONITOR_PAGE_SIZE, page * MONITOR_PAGE_SIZE)
  useEffect(() => setPage(1), [boardType, query, sort.key, sort.direction])
  useEffect(() => setPage((current) => Math.max(1, Math.min(current, pages || 1))), [pages])
  return <section className="workspace-grid">
    <div className="mobile-pane-tabs"><button className={mobilePane === 'ranks' ? 'active' : ''} onClick={() => setMobilePane('ranks')}><Table2 size={14} />榜单</button><button className={mobilePane === 'trend' ? 'active' : ''} onClick={() => setMobilePane('trend')}><LineChart size={14} />趋势</button></div>
    <section className={`rank-panel panel-section ${mobilePane === 'trend' ? 'mobile-hidden' : ''}`}>
      <div className="section-heading"><div><p className="eyebrow">实时榜单</p><h1>{BOARD_LABELS[boardType]}暗盘榜</h1></div><span className="record-count">{records.length}/{allRecords.length || '--'} 条</span></div>
      {nonTradingDay && tradeDate && <InlineNotice kind={staleSnapshot ? 'warning' : 'info'} text={staleSnapshot ? `今天不是交易日，最近交易日为 ${status.latest_trading_day}；当前仅有 ${tradeDate} 的快照。` : `今天不是交易日，当前展示 ${tradeDate} 的最近交易日数据。`} />}
      {!nonTradingDay && staleSnapshot && <InlineNotice kind="warning" text={`当前展示 ${tradeDate} 的最近可用快照，并非今日数据。`} />}
      <div className="segmented"><button className={boardType === 'industry' ? 'active' : ''} onClick={() => setBoardType('industry')}>行业</button><button className={boardType === 'concept' ? 'active' : ''} onClick={() => setBoardType('concept')}>概念</button></div>
      <label className="search-field"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称或代码" /><kbd>/</kbd></label>
      {rankError && <InlineNotice kind="error" text="榜单读取失败，请检查后端服务。" />}
      <div className="table-wrap"><table className="rank-table"><thead><tr><SortHead label="排名" sortKey="rank" sort={sort} onSort={onSort} /><SortHead label="板块" sortKey="name" sort={sort} onSort={onSort} /><SortHead label="代码" sortKey="code" sort={sort} onSort={onSort} /><SortHead label="暗盘资金" sortKey="dark_money" sort={sort} onSort={onSort} /><SortHead label="主力净流入" sortKey="main_money_inflow" sort={sort} onSort={onSort} /><SortHead label="涨跌" sortKey="change_pct" sort={sort} onSort={onSort} /><SortHead label="活跃度" sortKey="dark_activity" sort={sort} onSort={onSort} /></tr></thead><tbody>{pageRecords.map((record) => <tr key={record.code} className={selectedCode === record.code ? 'selected' : ''} onClick={() => { setSelectedCode(record.code); setMobilePane('trend') }}><td><span className={`rank-number rank-${record.rank}`}>{record.rank}</span></td><td><strong>{record.name || '未命名'}</strong><small>{record.leader_name ? `领涨 ${record.leader_name}` : '板块'}</small></td><td className="muted">{record.code}</td><td className={signedClass(record.dark_money)}>{formatMoney(record.dark_money)}</td><td className={signedClass(record.main_money_inflow)}>{formatMoney(record.main_money_inflow)}</td><td className={signedClass(record.change_pct)}>{record.change_pct > 0 ? '+' : ''}{formatNumber(record.change_pct * 100, 2)}%</td><td>{formatNumber(record.dark_activity * 100, 2)}%</td></tr>)}</tbody></table>{!records.length && <EmptyState icon={<Table2 size={22} />} title="暂无榜单数据" detail="后端将在交易时段采集完整行业和概念榜单。" />}</div>
      {pages > 1 && <Pagination page={page} pages={pages} setPage={setPage} />}
      <div className="table-footer"><span><Check size={14} />来源排名保持原始顺序</span><span>{requestMs !== undefined ? `${requestMs} ms · ` : ''}筛选后 {records.length} 条</span></div>
    </section>
    <section className={`trend-panel panel-section ${mobilePane === 'ranks' ? 'mobile-hidden' : ''}`}>
	  <div className="section-heading trend-heading"><div><p className="eyebrow">分钟序列</p><h2>{selected?.name ?? '选择一个板块'}</h2><span className="subline">{selected ? `${selected.code} · ${BOARD_LABELS[boardType]} · ${tradeDate} 采集至 ${formatTime(series.at(-1)?.snapshot_at ?? selected.snapshot_at)}${series.at(-1) && shanghaiTime(series.at(-1)!.snapshot_at) === '15:00' ? '（日终快照）' : ''}` : '点击左侧榜单查看当日连续序列'}</span></div><ExportLink href={selected ? api.exportURL(boardType, selected.code, tradeDate, tradeDate) : undefined} title="导出研究数据"><Download size={15} />导出</ExportLink></div>
      <MetricToolbar metric={metric} setMetric={setMetric} secondaryMetric={secondaryMetric} setSecondaryMetric={setSecondaryMetric} />
      {seriesError && <InlineNotice kind="error" text="分钟序列读取失败，请稍后重试。" />}
	      <Chart series={series} metric={metric} secondaryMetric={secondaryMetric} loading={loading} emptyLabel={seriesError ? '分钟序列读取失败' : '选择板块后加载分钟数据'} />
	      <TrendStats selected={selected} series={series} status={status} />
	      <ConstituentPanel board={selected} boardType={boardType} tradeDate={tradeDate} stocks={stocks} loading={stocksLoading} error={stocksError} quoteMeta={quoteMeta} />
	    </section>
  </section>
}

function ConstituentPanel({ board, boardType, tradeDate, stocks, loading, error, quoteMeta }: { board?: RankRecord; boardType: BoardType; tradeDate: string; stocks: BoardStockQuote[]; loading: boolean; error: Error | null; quoteMeta?: MonitorProps['quoteMeta'] }) {
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<SortState<ConstituentSortKey>>({ key: 'dark_rank', direction: 'asc' })
  const [page, setPage] = useState(1)
  const visibleStocks = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    return stocks
      .filter((stock) => !normalized || stock.stock_name.toLowerCase().includes(normalized) || stock.stock_code.includes(normalized))
      .sort((left, right) => {
        const darkField = ['dark_rank', 'dark_money', 'main_money_inflow', 'dark_activity'].includes(sort.key)
        if (darkField && left.dark_data_available !== right.dark_data_available) return left.dark_data_available ? -1 : 1
        const quoteField = ['latest_price', 'change_pct', 'turnover'].includes(sort.key)
        if (quoteField && left.quote_available !== right.quote_available) return left.quote_available ? -1 : 1
        const leftValue = left[sort.key]
        const rightValue = right[sort.key]
        const compared = typeof leftValue === 'string'
          ? leftValue.localeCompare(String(rightValue), 'zh-CN')
          : Number(leftValue) - Number(rightValue)
        return sort.direction === 'asc' ? compared : -compared
      })
  }, [stocks, query, sort])
  const pages = Math.ceil(visibleStocks.length / CONSTITUENT_PAGE_SIZE)
  const pageStocks = visibleStocks.slice((page - 1) * CONSTITUENT_PAGE_SIZE, page * CONSTITUENT_PAGE_SIZE)
  useEffect(() => setPage(1), [board?.code, query, sort.key, sort.direction])
  useEffect(() => setPage((current) => Math.max(1, Math.min(current, pages || 1))), [pages])
  const onSort = (key: ConstituentSortKey) => setSort((current) => ({ key, direction: current.key === key && current.direction === 'asc' ? 'desc' : 'asc' }))
  const quoteTime = stocks.find((stock) => stock.quote_available && stock.quote_time)?.quote_time
  const quoteReady = Boolean(quoteMeta?.quote_available && stocks.some((stock) => stock.quote_available))
  const darkReady = Boolean(quoteMeta?.dark_data_available && stocks.some((stock) => stock.dark_data_available))
  const badgeLabel = quoteReady && darkReady ? '暗盘榜 + 行情' : darkReady ? '暗盘榜已关联' : quoteReady ? `行情 ${quoteTime ? formatTime(quoteTime) : '已更新'}` : '仅成分关系'
  return <section className="constituent-panel">
    <div className="constituent-heading"><div><p className="eyebrow">级联成分股</p><h3>{board?.name ?? '选择一个板块'} · 个股</h3><span className="subline">{board ? `${board.code} · ${BOARD_LABELS[boardType]} · ${tradeDate} 归属` : '选择板块后加载成分股'}</span></div><span className={`quote-badge ${quoteReady || darkReady ? 'available' : ''}`}>{badgeLabel}</span></div>
    {quoteMeta?.quote_error && <InlineNotice kind="warning" text="实时行情暂不可用，当前仍保留成分股关系。" />}
    {error && <InlineNotice kind="error" text="成分股读取失败，请稍后重试。" />}
    <label className="constituent-search"><Search size={14} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索成分股名称或代码" /></label>
    <div className={`constituent-table-wrap ${loading ? 'is-loading' : ''}`}>
      {loading && !stocks.length ? <div className="loading-block">正在读取成分股行情…</div> : <table className="constituent-table"><thead><tr><SortHead label="股票" sortKey="stock_name" sort={sort} onSort={onSort} /><SortHead label="代码" sortKey="stock_code" sort={sort} onSort={onSort} /><SortHead label="暗盘排名" sortKey="dark_rank" sort={sort} onSort={onSort} /><SortHead label="暗盘资金" sortKey="dark_money" sort={sort} onSort={onSort} /><SortHead label="主力净流入" sortKey="main_money_inflow" sort={sort} onSort={onSort} /><SortHead label="活跃度" sortKey="dark_activity" sort={sort} onSort={onSort} /><SortHead label="最新价" sortKey="latest_price" sort={sort} onSort={onSort} /><SortHead label="涨跌" sortKey="change_pct" sort={sort} onSort={onSort} /><SortHead label="成交额" sortKey="turnover" sort={sort} onSort={onSort} /></tr></thead><tbody>{pageStocks.map((stock) => <tr key={stock.stock_code}><td><strong>{stock.stock_name || '未命名'}</strong></td><td className="stock-code">{stock.stock_code}</td><td>{stock.dark_data_available ? stock.dark_rank : '--'}</td><td className={stock.dark_data_available ? signedClass(stock.dark_money) : 'muted'}>{stock.dark_data_available ? formatMoney(stock.dark_money) : '--'}</td><td className={stock.dark_data_available ? signedClass(stock.main_money_inflow) : 'muted'}>{stock.dark_data_available ? formatMoney(stock.main_money_inflow) : '--'}</td><td>{stock.dark_data_available ? `${formatNumber(stock.dark_activity * 100, 2)}%` : '--'}</td><td>{stock.quote_available ? formatNumber(stock.latest_price, 2) : '--'}</td><td className={stock.quote_available ? signedClass(stock.change_pct) : 'muted'}>{stock.quote_available ? `${stock.change_pct > 0 ? '+' : ''}${formatNumber(stock.change_pct * 100, 2)}%` : '--'}</td><td>{stock.quote_available ? formatMoney(stock.turnover) : '--'}</td></tr>)}</tbody></table>}
      {!loading && !visibleStocks.length && <EmptyState icon={<Table2 size={19} />} title="暂无成分股" detail={board ? '当前截面日没有可展示的归属关系。' : '选择板块后查看成分股。'} />}
    </div>
    {pages > 1 && <Pagination page={page} pages={pages} setPage={setPage} compact />}
    <div className="constituent-footer"><span>{visibleStocks.length}{query ? ` / ${stocks.length}` : ''} 只</span><span>{darkReady ? `暗盘榜 ${quoteMeta?.dark_data_count ?? 0} 只 · 活跃度 = |暗盘资金| / 成交额` : quoteReady ? '行情来自东方财富最新快照' : '行情服务未返回数据'}</span></div>
  </section>
}

function SortHead<Key extends string>({ label, sortKey, sort, onSort }: { label: string; sortKey: Key; sort: SortState<Key>; onSort: (key: Key) => void }) {
  const active = sort.key === sortKey
  return <th aria-sort={active ? (sort.direction === 'asc' ? 'ascending' : 'descending') : 'none'}><button className={`sort-button ${active ? 'active' : ''}`} onClick={() => onSort(sortKey)}>{label}<ChevronDown size={13} className={active && sort.direction === 'asc' ? 'flipped' : ''} /></button></th>
}

function Pagination({ page, pages, setPage, compact = false }: { page: number; pages: number; setPage: (value: number) => void; compact?: boolean }) {
  return <nav className={`pagination ${compact ? 'compact' : ''}`} aria-label="分页">
    <button type="button" title="上一页" aria-label="上一页" disabled={page <= 1} onClick={() => setPage(page - 1)}><ChevronLeft size={15} /></button>
    <span>第 {page} / {pages} 页</span>
    <button type="button" title="下一页" aria-label="下一页" disabled={page >= pages} onClick={() => setPage(page + 1)}><ChevronRight size={15} /></button>
  </nav>
}

function MetricToolbar({ metric, setMetric, secondaryMetric, setSecondaryMetric, metrics = METRICS }: { metric: Metric; setMetric: (value: Metric) => void; secondaryMetric: Metric | 'none'; setSecondaryMetric: (value: Metric | 'none') => void; metrics?: Metric[] }) {
  // Switching the primary metric onto the current overlay must demote the
  // overlay: otherwise the overlay <select> holds a value absent from its
  // option list (rendered blank) and the chart draws two identical series.
  const changeMetric = (value: Metric) => {
    setMetric(value)
    if (secondaryMetric === value) setSecondaryMetric('none')
  }
  return <div className="metric-toolbar"><label><span>主指标</span><select value={metric} onChange={(event) => changeMetric(event.target.value as Metric)}>{metrics.map((item) => <option key={item} value={item}>{METRIC_LABELS[item]}</option>)}</select></label><label><span>叠加指标</span><select value={secondaryMetric} onChange={(event) => setSecondaryMetric(event.target.value as Metric | 'none')}><option value="none">不叠加</option>{metrics.filter((item) => item !== metric).map((item) => <option key={item} value={item}>{METRIC_LABELS[item]}</option>)}</select></label></div>
}

function Chart({ series, metric, secondaryMetric, loading, emptyLabel }: { series: RankRecord[]; metric: Metric; secondaryMetric: Metric | 'none'; loading: boolean; emptyLabel: string }) {
  const [element, setElement] = useState<HTMLDivElement | null>(null)
  useEffect(() => {
	if (!element || !series.length) return
	let disposed = false
	let cleanup = () => {}
	const chartValue = (record: RankRecord, selectedMetric: Metric) => {
	  if (!metricAvailable(record, selectedMetric)) return null
	  return ['change_pct', 'dark_inflow_ratio', 'dark_activity'].includes(selectedMetric) ? metricValue(record, selectedMetric) * 100 : metricValue(record, selectedMetric)
	}
	const intervalMinutes = isFiveMinuteSeries(series) ? 5 : 1
	const points = completeTimeline(series, intervalMinutes)
	const primaryValues = continuousMetricValues(points, metric, chartValue)
	const secondaryValues = secondaryMetric === 'none' ? [] : continuousMetricValues(points, secondaryMetric, chartValue)
	const sameUnit = secondaryMetric !== 'none' && (['change_pct', 'dark_inflow_ratio', 'dark_activity'].includes(metric) === ['change_pct', 'dark_inflow_ratio', 'dark_activity'].includes(secondaryMetric))
	void import('./chartRuntime').then(({ createLineChart }) => {
	  if (disposed) return
	  cleanup = createLineChart(element, { points, primaryValues, secondaryValues, metric, secondaryMetric, sameUnit })
	})
	return () => { disposed = true; cleanup() }
  }, [element, series, metric, secondaryMetric])
  return <div className="chart-frame">{loading && <div className="chart-overlay">正在读取分钟序列…</div>}{!loading && !series.length && <EmptyState icon={<BarChart3 size={24} />} title={emptyLabel} detail="盘中数据按分钟积累；盘后保留 48 个五分钟资金点。" />}{series.length > 0 && <div className="chart-canvas" ref={setElement} />}</div>
}

function completeTimeline(series: RankRecord[], intervalMinutes: number) {
  const byTime = new Map(series.map((item) => [shanghaiDateTimeKey(item.snapshot_at), item]))
  const tradeDates = [...new Set(series.map((item) => item.trade_date))]
  const multiDay = tradeDates.length > 1
  const points: Array<{ label: string; record: RankRecord | null }> = []
  const hasDailyClose = series.some((item) => shanghaiTime(item.snapshot_at) === '15:00')
  const ranges = intervalMinutes === 1 ? [['09:31', '11:30'], ['13:01', '15:00']] : [['09:35', '11:30'], ['13:05', hasDailyClose ? '15:00' : '14:55']]
  for (const date of tradeDates) {
    for (const [start, end] of ranges) {
      let current = new Date(`${date}T${start}:00+08:00`)
      const finish = new Date(`${date}T${end}:00+08:00`)
      while (current <= finish) {
        const key = current.toLocaleString('sv-SE', { timeZone: 'Asia/Shanghai', hour12: false }).slice(0, 16)
        const time = key.slice(11, 16)
        points.push({ label: multiDay ? `${key.slice(5, 10)} ${time}` : time, record: byTime.get(key.replace(' ', 'T')) ?? null })
        current = new Date(current.getTime() + intervalMinutes * 60000)
      }
    }
  }
  return points
}

function TrendStats({ selected, series, status }: { selected?: RankRecord; series: RankRecord[]; status?: SystemStatus }) {
  const first = series[0]
  const last = series.at(-1)
  const delta = first && last ? metricValue(last, 'dark_money') - metricValue(first, 'dark_money') : 0
	const fiveMinuteSeries = series.length > 0 && isFiveMinuteSeries(series)
	const expectedPoints = fiveMinuteSeries ? 48 : 240
  return <div className="stat-strip"><div><span>当前排名</span><strong>{selected?.rank ?? '--'}</strong></div><div><span>暗盘资金</span><strong className={signedClass(selected?.dark_money ?? 0)}>{selected ? formatMoney(selected.dark_money) : '--'}</strong></div><div><span>日内变化</span><strong className={signedClass(delta)}>{series.length ? `${delta > 0 ? '+' : ''}${formatMoney(delta)}` : '--'}</strong></div><div><span>有效点数</span><strong>{series.length || '--'}<small> / {expectedPoints}</small></strong></div><div><span>市场状态</span><strong>{status?.market_status === 'open' ? '交易中' : status?.market_status === 'lunch_break' ? '午休' : '已收市'}</strong></div></div>
}

function HistoryView({ boardType, setBoardType, selected, historyRanks, historyCode, setHistoryCode, historyDate, setHistoryDate, historyAt, setHistoryAt, metric, setMetric, secondaryMetric, setSecondaryMetric, series, loading, error, from, to, setFrom, setTo }: { boardType: BoardType; setBoardType: (value: BoardType) => void; selected?: RankRecord; historyRanks: RankRecord[]; historyCode: string; setHistoryCode: (value: string) => void; historyDate: string; setHistoryDate: (value: string) => void; historyAt: string; setHistoryAt: (value: string) => void; metric: Metric; setMetric: (value: Metric) => void; secondaryMetric: Metric | 'none'; setSecondaryMetric: (value: Metric | 'none') => void; series: RankRecord[]; loading: boolean; error: Error | null; from: string; to: string; setFrom: (value: string) => void; setTo: (value: string) => void }) {
	useEffect(() => {
		if (!RESEARCH_METRICS.includes(metric)) setMetric('dark_money')
		if (secondaryMetric !== 'none' && !RESEARCH_METRICS.includes(secondaryMetric)) {
			// Pick a research metric that differs from the primary; hardcoding one
			// could collide with it and duplicate the series in the chart.
			setSecondaryMetric(RESEARCH_METRICS.find((item) => item !== metric) ?? 'none')
		}
	}, [metric, secondaryMetric, setMetric, setSecondaryMetric])
	  return <section className="history-page panel-section"><div className="section-heading"><div><p className="eyebrow">研究数据</p><h1>板块趋势回看</h1><span className="subline">多日累计资金以前一交易日收盘为基准连续增减；单日原值保留在 tooltip 中。</span></div><div className="section-actions"><ExportLink href={selected ? api.exportURL(boardType, selected.code, from, to) : undefined}><Download size={15} />研究序列</ExportLink><ExportLink href={api.dailyCloseExportURL(historyDate)}><Download size={15} />日终三榜</ExportLink></div></div><div className="history-controls"><label>榜单<select value={boardType} onChange={(event) => setBoardType(event.target.value as BoardType)}><option value="industry">行业</option><option value="concept">概念</option></select></label><label>交易日<input type="date" value={historyDate} onChange={(event) => setHistoryDate(event.target.value)} /></label><label>快照<select value={historyAt} onChange={(event) => setHistoryAt(event.target.value)}><option value="09:35">09:35</option><option value="10:00">10:00</option><option value="11:30">11:30</option><option value="13:05">13:05</option><option value="14:00">14:00</option><option value="15:00">15:00（日终截面）</option></select></label><label>板块<select value={historyCode} onChange={(event) => setHistoryCode(event.target.value)}><option value="">选择板块</option>{historyRanks.map((item) => <option key={item.code} value={item.code}>{item.name} ({item.code})</option>)}</select></label><label>趋势起点<input type="date" value={from} onChange={(event) => setFrom(event.target.value)} /></label><label>趋势终点<input type="date" value={to} onChange={(event) => setTo(event.target.value)} /></label><span className="history-hint">{selected ? `${historyAt === '15:00' ? '日终截面' : '资金点'}排名 ${selected.rank > 0 ? selected.rank : '--'} · ${selected.money_available ? formatMoney(selected.dark_money) : '--'}` : '请选择已归档交易日和板块'}</span></div>{error && <InlineNotice kind="error" text="历史数据读取失败。" />}<MetricToolbar metric={metric} setMetric={setMetric} secondaryMetric={secondaryMetric} setSecondaryMetric={setSecondaryMetric} metrics={RESEARCH_METRICS} /><Chart series={series} metric={metric} secondaryMetric={secondaryMetric} loading={loading} emptyLabel="暂无历史研究数据" /><div className="history-summary"><span><CalendarDays size={15} />{from} 至 {to}</span><span>{series.length} 个盘后修订五分钟资金点</span></div></section>
}

function StockView({ date, setDate, records, total, query, setQuery, sort, onSort, page, pages, setPage, loading, error }: { date: string; setDate: (value: string) => void; records: RankRecord[]; total: number; query: string; setQuery: (value: string) => void; sort: SortState<keyof RankRecord>; onSort: (key: keyof RankRecord) => void; page: number; pages: number; setPage: (value: number) => void; loading: boolean; error: Error | null }) {
	return <section className="stocks-page panel-section">
	  <div className="section-heading"><div><p className="eyebrow">日级快照</p><h1>收盘个股榜</h1><span className="subline">同日沉淀个股暗盘榜、OHLC、前收盘、成交额和换手率。</span></div><div className="section-actions"><ExportLink href={api.dailyCloseExportURL(date)}><Download size={15} />导出日终三榜</ExportLink><label className="date-picker"><CalendarDays size={15} /><input type="date" value={date} onChange={(event) => setDate(event.target.value)} /></label></div></div>
	  <div className="stocks-toolbar"><label className="search-field"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索股票名称或代码" /></label><span>共 {total || '--'} 条</span></div>
	  {error && <InlineNotice kind="error" text="收盘榜读取失败。" />}
	  <div className="stock-table-wrap">{loading && !records.length ? <div className="loading-block">正在读取收盘榜…</div> : <table className={`stock-table ${loading ? 'is-loading' : ''}`}>
	    <thead><tr><SortHead label="排名" sortKey="rank" sort={sort} onSort={onSort} /><SortHead label="股票" sortKey="name" sort={sort} onSort={onSort} /><SortHead label="代码" sortKey="code" sort={sort} onSort={onSort} /><SortHead label="收盘" sortKey="close_price" sort={sort} onSort={onSort} /><SortHead label="涨跌幅" sortKey="change_pct" sort={sort} onSort={onSort} /><SortHead label="开盘" sortKey="open_price" sort={sort} onSort={onSort} /><SortHead label="最高" sortKey="high_price" sort={sort} onSort={onSort} /><SortHead label="最低" sortKey="low_price" sort={sort} onSort={onSort} /><SortHead label="前收" sortKey="previous_close" sort={sort} onSort={onSort} /><SortHead label="换手率" sortKey="turnover_rate" sort={sort} onSort={onSort} /><SortHead label="成交额" sortKey="turnover" sort={sort} onSort={onSort} /><SortHead label="暗盘资金" sortKey="dark_money" sort={sort} onSort={onSort} /><SortHead label="主力净流入" sortKey="main_money_inflow" sort={sort} onSort={onSort} /><SortHead label="活跃度" sortKey="dark_activity" sort={sort} onSort={onSort} /></tr></thead>
	    <tbody>{records.map((record) => <tr key={record.code}><td><span className="rank-number">{record.rank}</span></td><td><strong>{record.name || '未命名'}</strong></td><td className="stock-code">{record.code}</td><td>{record.quote_available ? formatNumber(record.close_price, 2) : '--'}</td><td className={signedClass(record.change_pct)}>{record.change_pct > 0 ? '+' : ''}{formatNumber(record.change_pct * 100, 2)}%</td><td>{record.quote_available ? formatNumber(record.open_price, 2) : '--'}</td><td>{record.quote_available ? formatNumber(record.high_price, 2) : '--'}</td><td>{record.quote_available ? formatNumber(record.low_price, 2) : '--'}</td><td>{record.previous_close > 0 ? formatNumber(record.previous_close, 2) : '--'}</td><td>{record.quote_available ? `${formatNumber(record.turnover_rate * 100, 2)}%` : '--'}</td><td>{record.quote_available ? formatMoney(record.turnover) : '--'}</td><td className={signedClass(record.dark_money)}>{formatMoney(record.dark_money)}</td><td className={signedClass(record.main_money_inflow)}>{formatMoney(record.main_money_inflow)}</td><td>{formatNumber(record.dark_activity * 100, 2)}%</td></tr>)}</tbody>
	  </table>}{!loading && !records.length && <EmptyState icon={<Table2 size={22} />} title="暂无收盘榜" detail="选择一个已完成个股收盘采集的交易日。" />}</div>
	  {pages > 0 && <Pagination page={page} pages={pages} setPage={setPage} />}
	</section>
}

function ExportLink({ href, title, children }: { href?: string; title?: string; children: React.ReactNode }) {
  const [error, setError] = useState('')
  // Deliberately not a real hyperlink: an href would let middle-click, "open
  // in new tab" or "copy link" bypass the token-bearing fetch and land on a
  // bare 401 page (or navigate the SPA away). Running the download even
  // without a stored token lets the 401 trigger the Token Gate properly.
  const run = () => {
    if (!href) return
    setError('')
    api.download(href).catch((downloadError: unknown) => setError(downloadError instanceof Error ? downloadError.message : '导出失败'))
  }
  return (
    <a
      className="export-link"
      role="button"
      tabIndex={href ? 0 : -1}
      aria-disabled={!href}
      title={error || title}
      onClick={(event) => {
        event.preventDefault()
        run()
      }}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          run()
        }
      }}
    >
      {children}
    </a>
  )
}

function InlineNotice({ kind, text }: { kind: 'error' | 'info' | 'warning'; text: string }) { return <div className={`inline-notice ${kind}`}>{kind === 'info' ? <Info size={15} /> : <AlertTriangle size={15} />}{text}</div> }
function EmptyState({ icon, title, detail }: { icon: React.ReactNode; title: string; detail: string }) { return <div className="empty-state">{icon}<strong>{title}</strong><span>{detail}</span></div> }

export default App
