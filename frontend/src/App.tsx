import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Activity, AlertTriangle, BarChart3, CalendarDays, Check, ChevronDown, Download, Gauge, LineChart, RefreshCw, Search, Server, Table2, Wifi, WifiOff } from 'lucide-react'
import { api } from './api/client'
import type { CollectionRun, RankRecord, RankType, SystemStatus } from './api/types'

type BoardType = Exclude<RankType, 'stock'>
type View = 'monitor' | 'history' | 'stocks' | 'quality'
type Metric = 'dark_money' | 'regular_money' | 'main_money_inflow' | 'dark_activity' | 'dark_inflow_ratio' | 'change_pct' | 'rank' | 'up_count'

const BOARD_LABELS: Record<BoardType, string> = { industry: '行业', concept: '概念' }
const METRIC_LABELS: Record<Metric, string> = {
  dark_money: '暗盘资金估算', regular_money: '明盘资金', main_money_inflow: '主力净流入',
  dark_activity: '暗盘活跃度', dark_inflow_ratio: '暗盘流入占比', change_pct: '涨跌幅', rank: '榜单排名', up_count: '上涨家数',
}
const METRICS: Metric[] = ['dark_money', 'regular_money', 'main_money_inflow', 'dark_activity', 'dark_inflow_ratio', 'change_pct', 'rank', 'up_count']

function localDate(date = new Date()) {
  const parts = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(date)
  return `${parts.find((p) => p.type === 'year')?.value}-${parts.find((p) => p.type === 'month')?.value}-${parts.find((p) => p.type === 'day')?.value}`
}

function formatTime(value?: string) {
  if (!value) return '--'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value.slice(11, 16) : date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false, timeZone: 'Asia/Shanghai' })
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

function App() {
  const [view, setView] = useState<View>('monitor')
  const [boardType, setBoardType] = useState<BoardType>('industry')
  const [selectedCode, setSelectedCode] = useState('')
  const [mobilePane, setMobilePane] = useState<'ranks' | 'trend'>('ranks')
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<{ key: keyof RankRecord; direction: 'asc' | 'desc' }>({ key: 'rank', direction: 'asc' })
  const [metric, setMetric] = useState<Metric>('dark_money')
  const [secondaryMetric, setSecondaryMetric] = useState<Metric | 'none'>('main_money_inflow')
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [refreshSeconds, setRefreshSeconds] = useState(60)
  const [qualityDate, setQualityDate] = useState(localDate())
  const [historyFrom, setHistoryFrom] = useState(localDate(new Date(Date.now() - 30 * 86400000)))
  const [historyTo, setHistoryTo] = useState(localDate())
  const [historyDate, setHistoryDate] = useState(localDate(new Date(Date.now() - 86400000)))
  const [historyAt, setHistoryAt] = useState('15:00')
  const [historyCode, setHistoryCode] = useState('')
  const [stockDate, setStockDate] = useState(localDate())
  const [stockQuery, setStockQuery] = useState('')
  const [stockSort, setStockSort] = useState<{ key: keyof RankRecord; direction: 'asc' | 'desc' }>({ key: 'rank', direction: 'asc' })
  const [stockPage, setStockPage] = useState(1)
  const [debouncedStockQuery, setDebouncedStockQuery] = useState('')

  const refreshInterval = autoRefresh ? refreshSeconds * 1000 : false
  const statusQuery = useQuery({ queryKey: ['system-status'], queryFn: async () => (await api.status()).data as SystemStatus, refetchInterval: refreshInterval })
  const rankQuery = useQuery({ queryKey: ['latest', boardType], queryFn: async () => { const started = performance.now(); const result = await api.latest(boardType); return { records: result.data ?? [], requestMs: Math.round(performance.now() - started) } }, refetchInterval: refreshInterval })
  const records = useMemo(() => rankQuery.data?.records ?? [], [rankQuery.data?.records])
  const selected = records.find((item) => item.code === selectedCode) ?? records[0]
  const selectedId = selected?.code ?? ''
  const monitorDate = selected?.trade_date ?? records[0]?.trade_date ?? localDate()
  const staleSnapshot = Boolean(records.length && monitorDate !== localDate())
  const intradayQuery = useQuery({
    queryKey: ['intraday', boardType, selectedId, monitorDate],
    queryFn: async () => (await api.intraday(boardType, selectedId, monitorDate)).data ?? [],
    enabled: Boolean(selectedId) && view === 'monitor',
    refetchInterval: refreshInterval,
  })
  const trendQuery = useQuery({
    queryKey: ['trend', boardType, historyCode, historyFrom, historyTo],
    queryFn: async () => (await api.trend(boardType, historyCode, historyFrom, historyTo)).data ?? [],
    enabled: Boolean(historyCode) && view === 'history',
  })
  const historyRanksQuery = useQuery({
    queryKey: ['history-ranks', boardType, historyDate, historyAt],
    queryFn: async () => (await api.rankAt(boardType, historyDate, historyAt)).data ?? [],
    enabled: view === 'history' && Boolean(historyDate),
  })
  const stocksQuery = useQuery({
    queryKey: ['daily-close', stockDate, debouncedStockQuery, stockSort.key, stockSort.direction, stockPage],
    queryFn: async () => api.dailyClose('stock', stockDate, debouncedStockQuery, stockSort.key, stockSort.direction, stockPage, 100),
    enabled: view === 'stocks',
    placeholderData: (previous) => previous,
  })
  const qualityQuery = useQuery({ queryKey: ['quality', qualityDate], queryFn: async () => (await api.quality(qualityDate)).data ?? [], enabled: view === 'quality' })
  const runsQuery = useQuery({ queryKey: ['runs', qualityDate], queryFn: async () => (await api.runs(qualityDate)).data ?? [], enabled: view === 'quality' })

  useEffect(() => {
    if (records.length && !records.some((item) => item.code === selectedCode)) setSelectedCode(records[0].code)
  }, [records, selectedCode])

  useEffect(() => {
    const available = historyRanksQuery.data ?? []
    if (available.length && !available.some((item) => item.code === historyCode)) setHistoryCode(available[0].code)
  }, [historyRanksQuery.data, historyCode])

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
  const refreshAll = () => { void rankQuery.refetch(); void statusQuery.refetch(); if (selectedId) void intradayQuery.refetch() }
  const historicalSelected = (historyRanksQuery.data ?? []).find((item) => item.code === historyCode) ?? historyRanksQuery.data?.[0]
  const stockRecords = stocksQuery.data?.data ?? []
  const stockMeta = stocksQuery.data?.meta
  const onStockSort = (key: keyof RankRecord) => { setStockSort((current) => ({ key, direction: current.key === key && current.direction === 'asc' ? 'desc' : 'asc' })); setStockPage(1) }

  return <div className="app-shell">
    <header className="topbar">
      <div className="brand-lockup"><div className="brand-mark"><Activity size={18} /></div><div><strong>ShadowFlow 暗流</strong><span>板块资金监控台</span></div></div>
      <div className="topbar-actions">
        <MarketStatus status={statusQuery.data} />
        <div className={`live-chip ${staleSnapshot ? 'stale' : ''}`}><span className={`live-dot ${rankQuery.isFetching ? 'is-fetching' : ''}`} />{records.length ? `最新 ${staleSnapshot ? `${monitorDate} ` : ''}${formatTime(records[0].snapshot_at)}` : '等待数据'}</div>
        <label className="refresh-control"><RefreshCw size={14} /><select value={refreshSeconds} onChange={(event) => setRefreshSeconds(Number(event.target.value))}><option value={30}>30 秒</option><option value={60}>60 秒</option><option value={300}>5 分钟</option></select></label>
        <button className={`icon-button ${autoRefresh ? 'active' : ''}`} title="切换自动刷新" onClick={() => setAutoRefresh((value) => !value)}>{autoRefresh ? <Wifi size={17} /> : <WifiOff size={17} />}</button>
        <button className="icon-button" title="立即刷新" onClick={refreshAll} disabled={rankQuery.isFetching}><RefreshCw size={17} className={rankQuery.isFetching ? 'spin' : ''} /></button>
      </div>
    </header>

    <main className="main-content">
      <div className="view-tabs" role="tablist">
        <button className={view === 'monitor' ? 'selected' : ''} onClick={() => setView('monitor')}><Gauge size={16} />今日监控</button>
        <button className={view === 'history' ? 'selected' : ''} onClick={() => setView('history')}><LineChart size={16} />历史回看</button>
        <button className={view === 'stocks' ? 'selected' : ''} onClick={() => setView('stocks')}><Table2 size={16} />收盘个股</button>
        <button className={view === 'quality' ? 'selected' : ''} onClick={() => setView('quality')}><Server size={16} />采集质量</button>
      </div>
      {view === 'monitor' && <MonitorView boardType={boardType} setBoardType={setBoardType} records={visibleRecords} allRecords={records} selected={selected} selectedCode={selectedId} setSelectedCode={setSelectedCode} query={query} setQuery={setQuery} onSort={onSort} sort={sort} metric={metric} setMetric={setMetric} secondaryMetric={secondaryMetric} setSecondaryMetric={setSecondaryMetric} series={intradayQuery.data ?? []} loading={intradayQuery.isLoading} rankError={rankQuery.error} seriesError={intradayQuery.error} requestMs={rankQuery.data?.requestMs} status={statusQuery.data} tradeDate={monitorDate} staleSnapshot={staleSnapshot} mobilePane={mobilePane} setMobilePane={setMobilePane} />}
      {view === 'history' && <HistoryView boardType={boardType} setBoardType={setBoardType} selected={historicalSelected} historyRanks={historyRanksQuery.data ?? []} historyCode={historyCode} setHistoryCode={setHistoryCode} historyDate={historyDate} setHistoryDate={setHistoryDate} historyAt={historyAt} setHistoryAt={setHistoryAt} metric={metric} setMetric={setMetric} secondaryMetric={secondaryMetric} setSecondaryMetric={setSecondaryMetric} series={trendQuery.data ?? []} loading={trendQuery.isLoading || historyRanksQuery.isLoading} error={trendQuery.error ?? historyRanksQuery.error} from={historyFrom} to={historyTo} setFrom={setHistoryFrom} setTo={setHistoryTo} />}
      {view === 'stocks' && <StockView date={stockDate} setDate={(value) => { setStockDate(value); setStockPage(1) }} records={stockRecords} total={stockMeta?.total ?? 0} query={stockQuery} setQuery={setStockQuery} sort={stockSort} onSort={onStockSort} page={stockPage} pages={stockMeta?.pages ?? 0} setPage={setStockPage} loading={stocksQuery.isLoading || stocksQuery.isFetching} error={stocksQuery.error} />}
      {view === 'quality' && <QualityView date={qualityDate} setDate={setQualityDate} quality={(qualityQuery.data ?? []).filter((item): item is NonNullable<typeof item> & { rank_type: BoardType } => item.rank_type !== 'stock')} runs={runsQuery.data ?? []} loading={qualityQuery.isLoading || runsQuery.isLoading} error={qualityQuery.error ?? runsQuery.error} />}
    </main>
  </div>
}

function MarketStatus({ status }: { status?: SystemStatus }) {
  const labels: Record<NonNullable<SystemStatus['market_status']>, string> = { pre_open: '盘前', open: '交易中', lunch_break: '午间休市', closed: '已收市' }
  return <div className={`market-status ${status?.market_status ?? 'closed'}`}><span className="status-dot" />{status ? labels[status.market_status] : '连接中'}</div>
}

type MonitorProps = {
  boardType: BoardType; setBoardType: (value: BoardType) => void; records: RankRecord[]; allRecords: RankRecord[]; selected?: RankRecord; selectedCode: string; setSelectedCode: (value: string) => void; query: string; setQuery: (value: string) => void; onSort: (key: keyof RankRecord) => void; sort: { key: keyof RankRecord; direction: 'asc' | 'desc' }; metric: Metric; setMetric: (value: Metric) => void; secondaryMetric: Metric | 'none'; setSecondaryMetric: (value: Metric | 'none') => void; series: RankRecord[]; loading: boolean; rankError: Error | null; seriesError: Error | null; requestMs?: number; status?: SystemStatus; tradeDate: string; staleSnapshot: boolean; mobilePane: 'ranks' | 'trend'; setMobilePane: (value: 'ranks' | 'trend') => void
}

function MonitorView(props: MonitorProps) {
  const { boardType, setBoardType, records, allRecords, selected, selectedCode, setSelectedCode, query, setQuery, onSort, sort, metric, setMetric, secondaryMetric, setSecondaryMetric, series, loading, rankError, seriesError, requestMs, status, tradeDate, staleSnapshot, mobilePane, setMobilePane } = props
  return <section className="workspace-grid">
    <div className="mobile-pane-tabs"><button className={mobilePane === 'ranks' ? 'active' : ''} onClick={() => setMobilePane('ranks')}><Table2 size={14} />榜单</button><button className={mobilePane === 'trend' ? 'active' : ''} onClick={() => setMobilePane('trend')}><LineChart size={14} />趋势</button></div>
    <section className={`rank-panel panel-section ${mobilePane === 'trend' ? 'mobile-hidden' : ''}`}>
      <div className="section-heading"><div><p className="eyebrow">实时榜单</p><h1>{BOARD_LABELS[boardType]}暗盘榜</h1></div><span className="record-count">{records.length}/{allRecords.length || '--'} 条</span></div>
      {staleSnapshot && <InlineNotice kind="warning" text={`当前展示 ${tradeDate} 的最近可用快照，并非今日数据。`} />}
      <div className="segmented"><button className={boardType === 'industry' ? 'active' : ''} onClick={() => setBoardType('industry')}>行业</button><button className={boardType === 'concept' ? 'active' : ''} onClick={() => setBoardType('concept')}>概念</button></div>
      <label className="search-field"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称或代码" /><kbd>/</kbd></label>
      {rankError && <InlineNotice kind="error" text="榜单读取失败，请检查后端服务。" />}
      <div className="table-wrap"><table className="rank-table"><thead><tr><SortHead label="排名" sortKey="rank" sort={sort} onSort={onSort} /><th>板块</th><th>代码</th><SortHead label="暗盘资金" sortKey="dark_money" sort={sort} onSort={onSort} /><SortHead label="主力净流入" sortKey="main_money_inflow" sort={sort} onSort={onSort} /><SortHead label="涨跌" sortKey="change_pct" sort={sort} onSort={onSort} /><SortHead label="活跃度" sortKey="dark_activity" sort={sort} onSort={onSort} /></tr></thead><tbody>{records.map((record) => <tr key={record.code} className={selectedCode === record.code ? 'selected' : ''} onClick={() => { setSelectedCode(record.code); setMobilePane('trend') }}><td><span className={`rank-number rank-${record.rank}`}>{record.rank}</span></td><td><strong>{record.name || '未命名'}</strong><small>{record.leader_name ? `领涨 ${record.leader_name}` : '板块'}</small></td><td className="muted">{record.code}</td><td className={signedClass(record.dark_money)}>{formatMoney(record.dark_money)}</td><td className={signedClass(record.main_money_inflow)}>{formatMoney(record.main_money_inflow)}</td><td className={signedClass(record.change_pct)}>{record.change_pct > 0 ? '+' : ''}{formatNumber(record.change_pct * 100, 2)}%</td><td>{formatNumber(record.dark_activity * 100, 2)}%</td></tr>)}</tbody></table>{!records.length && <EmptyState icon={<Table2 size={22} />} title="暂无榜单数据" detail="后端将在交易时段采集完整行业和概念榜单。" />}</div>
      <div className="table-footer"><span><Check size={14} />来源排名保持原始顺序</span><span>{requestMs !== undefined ? `${requestMs} ms · ` : ''}筛选后 {records.length} 条</span></div>
    </section>
    <section className={`trend-panel panel-section ${mobilePane === 'ranks' ? 'mobile-hidden' : ''}`}>
	  <div className="section-heading trend-heading"><div><p className="eyebrow">分钟序列</p><h2>{selected?.name ?? '选择一个板块'}</h2><span className="subline">{selected ? `${selected.code} · ${BOARD_LABELS[boardType]} · ${tradeDate} 采集至 ${formatTime(series.at(-1)?.snapshot_at ?? selected.snapshot_at)}${series.at(-1)?.snapshot_at.slice(11, 16) === '15:00' ? '（日终快照）' : ''}` : '点击左侧榜单查看当日连续序列'}</span></div><a className="export-link" href={selected ? api.exportURL(boardType, selected.code, tradeDate, tradeDate) : undefined} title="导出研究数据"><Download size={15} />导出</a></div>
      <MetricToolbar metric={metric} setMetric={setMetric} secondaryMetric={secondaryMetric} setSecondaryMetric={setSecondaryMetric} />
      {seriesError && <InlineNotice kind="error" text="分钟序列读取失败，请稍后重试。" />}
      <Chart series={series} metric={metric} secondaryMetric={secondaryMetric} loading={loading} emptyLabel={seriesError ? '分钟序列读取失败' : '选择板块后加载分钟数据'} />
      <TrendStats selected={selected} series={series} status={status} />
    </section>
  </section>
}

function SortHead({ label, sortKey, sort, onSort }: { label: string; sortKey: keyof RankRecord; sort: { key: keyof RankRecord; direction: 'asc' | 'desc' }; onSort: (key: keyof RankRecord) => void }) {
  return <th><button className="sort-button" onClick={() => onSort(sortKey)}>{label}<ChevronDown size={13} className={sort.key === sortKey && sort.direction === 'desc' ? 'flipped' : ''} /></button></th>
}

function MetricToolbar({ metric, setMetric, secondaryMetric, setSecondaryMetric }: { metric: Metric; setMetric: (value: Metric) => void; secondaryMetric: Metric | 'none'; setSecondaryMetric: (value: Metric | 'none') => void }) {
  return <div className="metric-toolbar"><label><span>主指标</span><select value={metric} onChange={(event) => setMetric(event.target.value as Metric)}>{METRICS.map((item) => <option key={item} value={item}>{METRIC_LABELS[item]}</option>)}</select></label><label><span>叠加指标</span><select value={secondaryMetric} onChange={(event) => setSecondaryMetric(event.target.value as Metric | 'none')}><option value="none">不叠加</option>{METRICS.filter((item) => item !== metric).map((item) => <option key={item} value={item}>{METRIC_LABELS[item]}</option>)}</select></label></div>
}

function Chart({ series, metric, secondaryMetric, loading, emptyLabel }: { series: RankRecord[]; metric: Metric; secondaryMetric: Metric | 'none'; loading: boolean; emptyLabel: string }) {
  const [element, setElement] = useState<HTMLDivElement | null>(null)
  useEffect(() => {
	if (!element || !series.length) return
	let disposed = false
	let cleanup = () => {}
	const chartValue = (record: RankRecord, selectedMetric: Metric) => ['change_pct', 'dark_inflow_ratio', 'dark_activity'].includes(selectedMetric) ? metricValue(record, selectedMetric) * 100 : metricValue(record, selectedMetric)
	const intervalMinutes = series.some((item) => new Date(item.snapshot_at).getMinutes() % 5 !== 0) ? 1 : 5
	const points = completeTimeline(series, intervalMinutes)
	const primaryValues = points.map((item) => item.record ? chartValue(item.record, metric) : null)
	const secondaryValues = secondaryMetric === 'none' ? [] : points.map((item) => item.record ? chartValue(item.record, secondaryMetric) : null)
	const sameUnit = secondaryMetric !== 'none' && (['change_pct', 'dark_inflow_ratio', 'dark_activity'].includes(metric) === ['change_pct', 'dark_inflow_ratio', 'dark_activity'].includes(secondaryMetric))
	void import('./chartRuntime').then(({ createLineChart }) => {
	  if (disposed) return
	  cleanup = createLineChart(element, { points, primaryValues, secondaryValues, metric, secondaryMetric, sameUnit })
	})
	return () => { disposed = true; cleanup() }
  }, [element, series, metric, secondaryMetric])
  return <div className="chart-frame">{loading && <div className="chart-overlay">正在读取分钟序列…</div>}{!loading && !series.length && <EmptyState icon={<BarChart3 size={24} />} title={emptyLabel} detail="盘中数据按分钟积累；盘后保留 47 个研究点和独立 15:00 日终点。" />}{series.length > 0 && <div className="chart-canvas" ref={setElement} />}</div>
}

function completeTimeline(series: RankRecord[], intervalMinutes: number) {
  const byTime = new Map(series.map((item) => [item.snapshot_at.slice(0, 16), item]))
  const tradeDates = [...new Set(series.map((item) => item.trade_date))]
  const multiDay = tradeDates.length > 1
  const points: Array<{ label: string; record: RankRecord | null }> = []
  const hasDailyClose = series.some((item) => item.snapshot_at.slice(11, 16) === '15:00')
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
	const fiveMinuteSeries = series.length > 0 && series.every((item) => new Date(item.snapshot_at).getMinutes() % 5 === 0)
	const hasDailyClose = series.some((item) => item.snapshot_at.slice(11, 16) === '15:00')
	const expectedPoints = fiveMinuteSeries ? (hasDailyClose ? 48 : 47) : 240
  return <div className="stat-strip"><div><span>当前排名</span><strong>{selected?.rank ?? '--'}</strong></div><div><span>暗盘资金</span><strong className={signedClass(selected?.dark_money ?? 0)}>{selected ? formatMoney(selected.dark_money) : '--'}</strong></div><div><span>日内变化</span><strong className={signedClass(delta)}>{series.length ? `${delta > 0 ? '+' : ''}${formatMoney(delta)}` : '--'}</strong></div><div><span>有效点数</span><strong>{series.length || '--'}<small> / {expectedPoints}</small></strong></div><div><span>市场状态</span><strong>{status?.market_status === 'open' ? '交易中' : status?.market_status === 'lunch_break' ? '午休' : '已收市'}</strong></div></div>
}

function HistoryView({ boardType, setBoardType, selected, historyRanks, historyCode, setHistoryCode, historyDate, setHistoryDate, historyAt, setHistoryAt, metric, setMetric, secondaryMetric, setSecondaryMetric, series, loading, error, from, to, setFrom, setTo }: { boardType: BoardType; setBoardType: (value: BoardType) => void; selected?: RankRecord; historyRanks: RankRecord[]; historyCode: string; setHistoryCode: (value: string) => void; historyDate: string; setHistoryDate: (value: string) => void; historyAt: string; setHistoryAt: (value: string) => void; metric: Metric; setMetric: (value: Metric) => void; secondaryMetric: Metric | 'none'; setSecondaryMetric: (value: Metric | 'none') => void; series: RankRecord[]; loading: boolean; error: Error | null; from: string; to: string; setFrom: (value: string) => void; setTo: (value: string) => void }) {
	return <section className="history-page panel-section"><div className="section-heading"><div><p className="eyebrow">研究数据</p><h1>板块趋势回看</h1><span className="subline">盘中长期数据保存 47 个五分钟研究点；15:00 作为独立日终快照，可与个股收盘榜联合分析。</span></div><div className="section-actions"><a className="export-link" href={selected ? api.exportURL(boardType, selected.code, from, to) : undefined}><Download size={15} />研究序列</a><a className="export-link" href={api.dailyCloseExportURL(historyDate)}><Download size={15} />日终三榜</a></div></div><div className="history-controls"><label>榜单<select value={boardType} onChange={(event) => setBoardType(event.target.value as BoardType)}><option value="industry">行业</option><option value="concept">概念</option></select></label><label>交易日<input type="date" value={historyDate} onChange={(event) => setHistoryDate(event.target.value)} /></label><label>快照<select value={historyAt} onChange={(event) => setHistoryAt(event.target.value)}><option value="09:35">09:35</option><option value="10:00">10:00</option><option value="11:30">11:30</option><option value="13:05">13:05</option><option value="14:00">14:00</option><option value="15:00">15:00（日终快照）</option></select></label><label>板块<select value={historyCode} onChange={(event) => setHistoryCode(event.target.value)}><option value="">选择板块</option>{historyRanks.map((item) => <option key={item.code} value={item.code}>{item.name} ({item.code})</option>)}</select></label><label>趋势起点<input type="date" value={from} onChange={(event) => setFrom(event.target.value)} /></label><label>趋势终点<input type="date" value={to} onChange={(event) => setTo(event.target.value)} /></label><span className="history-hint">{selected ? `${historyAt === '15:00' ? '日终快照' : '研究快照'}排名 ${selected.rank} · ${formatMoney(selected.dark_money)}` : '请选择已归档交易日和板块'}</span></div>{error && <InlineNotice kind="error" text="历史数据读取失败。" />}<MetricToolbar metric={metric} setMetric={setMetric} secondaryMetric={secondaryMetric} setSecondaryMetric={setSecondaryMetric} /><Chart series={series} metric={metric} secondaryMetric={secondaryMetric} loading={loading} emptyLabel="暂无历史研究数据" /><div className="history-summary"><span><CalendarDays size={15} />{from} 至 {to}</span><span>{series.length} 个五分钟研究点（不含日终快照）</span></div></section>
}

function StockView({ date, setDate, records, total, query, setQuery, sort, onSort, page, pages, setPage, loading, error }: { date: string; setDate: (value: string) => void; records: RankRecord[]; total: number; query: string; setQuery: (value: string) => void; sort: { key: keyof RankRecord; direction: 'asc' | 'desc' }; onSort: (key: keyof RankRecord) => void; page: number; pages: number; setPage: (value: number) => void; loading: boolean; error: Error | null }) {
	return <section className="stocks-page panel-section"><div className="section-heading"><div><p className="eyebrow">日级快照</p><h1>收盘个股榜</h1><span className="subline">个股只保存收盘全量榜；同日 15:00 行业、概念快照可与其联合分析。</span></div><div className="section-actions"><a className="export-link" href={api.dailyCloseExportURL(date)}><Download size={15} />导出日终三榜</a><label className="date-picker"><CalendarDays size={15} /><input type="date" value={date} onChange={(event) => setDate(event.target.value)} /></label></div></div><div className="stocks-toolbar"><label className="search-field"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索股票名称或代码" /></label><span>共 {total || '--'} 条</span></div>{error && <InlineNotice kind="error" text="收盘榜读取失败。" />}<div className="stock-table-wrap">{loading && !records.length ? <div className="loading-block">正在读取收盘榜…</div> : <table className={`stock-table ${loading ? 'is-loading' : ''}`}><thead><tr><SortHead label="排名" sortKey="rank" sort={sort} onSort={onSort} /><th>股票</th><th>代码</th><SortHead label="暗盘资金" sortKey="dark_money" sort={sort} onSort={onSort} /><SortHead label="主力净流入" sortKey="main_money_inflow" sort={sort} onSort={onSort} /><SortHead label="涨跌幅" sortKey="change_pct" sort={sort} onSort={onSort} /></tr></thead><tbody>{records.map((record) => <tr key={record.code}><td><span className="rank-number">{record.rank}</span></td><td><strong>{record.name || '未命名'}</strong><small>{record.quote_time || '--'}</small></td><td className="muted">{record.code}</td><td className={signedClass(record.dark_money)}>{formatMoney(record.dark_money)}</td><td className={signedClass(record.main_money_inflow)}>{formatMoney(record.main_money_inflow)}</td><td className={signedClass(record.change_pct)}>{record.change_pct > 0 ? '+' : ''}{formatNumber(record.change_pct * 100, 2)}%</td></tr>)}</tbody></table>}{!loading && !records.length && <EmptyState icon={<Table2 size={22} />} title="暂无收盘榜" detail="选择一个已完成个股收盘采集的交易日。" />}</div>{pages > 0 && <div className="pagination"><button disabled={page <= 1} onClick={() => setPage(page - 1)}>上一页</button><span>第 {page} / {pages} 页</span><button disabled={page >= pages} onClick={() => setPage(page + 1)}>下一页</button></div>}</section>
}

function QualityView({ date, setDate, quality, runs, loading, error }: { date: string; setDate: (value: string) => void; quality: Array<{ rank_type: BoardType; expected_minutes: number; collected_minutes: number; expected_research_snapshots: number; collected_research_snapshots: number; expected_daily_close_snapshots: number; collected_daily_close_snapshots: number; missing_minutes: string[]; missing_research_snapshots: string[]; missing_daily_close_snapshots: string[] }>; runs: CollectionRun[]; loading: boolean; error: Error | null }) {
  return <section className="quality-page"><div className="section-heading"><div><p className="eyebrow">运行记录</p><h1>采集质量</h1><span className="subline">检查 240 个分钟点、47 个研究点、独立 15:00 日终点及个股收盘任务。</span></div><label className="date-picker"><CalendarDays size={15} /><input type="date" value={date} onChange={(event) => setDate(event.target.value)} /></label></div>{error ? <InlineNotice kind="error" text="采集质量读取失败，请检查后端服务后重试。" /> : loading ? <div className="loading-block">正在读取质量摘要…</div> : <><div className="quality-grid">{(['industry', 'concept'] as BoardType[]).map((type) => { const item = quality.find((row) => row.rank_type === type); const minutePct = item ? Math.round(item.collected_minutes / Math.max(1, item.expected_minutes) * 100) : 0; const researchPct = item ? Math.round(item.collected_research_snapshots / Math.max(1, item.expected_research_snapshots) * 100) : 0; const closeOK = item?.collected_daily_close_snapshots === item?.expected_daily_close_snapshots && Boolean(item?.expected_daily_close_snapshots); const allOK = minutePct === 100 && researchPct === 100 && closeOK; return <article className="quality-card" key={type}><div className="quality-card-head"><span className="type-tag">{BOARD_LABELS[type]}</span><span className={allOK ? 'ok-label' : 'warn-label'}>{allOK ? <Check size={14} /> : <AlertTriangle size={14} />}{minutePct}%</span></div><div className="quality-metric"><span>分钟采集</span><strong>{item?.collected_minutes ?? 0}<small> / {item?.expected_minutes ?? 240}</small></strong><div className="progress"><i style={{ width: `${Math.min(minutePct, 100)}%` }} /></div></div><div className="quality-metric"><span>研究沉淀</span><strong>{item?.collected_research_snapshots ?? 0}<small> / {item?.expected_research_snapshots ?? 47}</small></strong><div className="progress orange"><i style={{ width: `${Math.min(researchPct, 100)}%` }} /></div></div><div className="quality-metric"><span>15:00 日终</span><strong>{item?.collected_daily_close_snapshots ?? 0}<small> / {item?.expected_daily_close_snapshots ?? 1}</small></strong><div className="progress"><i style={{ width: closeOK ? '100%' : '0%' }} /></div></div><div className="missing-list">{item?.missing_daily_close_snapshots?.length ? '缺失 15:00 日终快照，分钟工作数据已保留' : item?.missing_minutes?.length ? `缺失分钟 ${item.missing_minutes.slice(0, 5).join('、')}${item.missing_minutes.length > 5 ? '…' : ''}` : '分钟、研究及日终时间点完整'}</div></article> })}</div><section className="runs-section"><div className="subsection-heading"><h2>最近运行</h2><span>{runs.length} 条</span></div><div className="run-table-wrap"><table className="run-table"><thead><tr><th>时间</th><th>类型</th><th>榜单</th><th>状态</th><th>记录数</th><th>耗时</th><th>错误</th></tr></thead><tbody>{runs.map((run) => <tr key={run.run_id}><td>{formatTime(run.started_at)}</td><td>{run.snapshot_kind === 'daily_close' ? '日终' : run.snapshot_kind === 'minute_work' ? '分钟' : run.snapshot_kind}</td><td>{run.rank_type === 'stock' ? '个股' : BOARD_LABELS[run.rank_type as BoardType]}</td><td><span className={`run-status ${run.status}`}><span />{run.status === 'success' ? '成功' : run.status === 'failed' ? '失败' : run.status}</span></td><td>{formatNumber(run.fetched_total)}</td><td>{formatNumber(run.duration_ms)} ms</td><td className="muted">{run.error_message || '--'}</td></tr>)}</tbody></table>{!runs.length && <EmptyState icon={<Server size={22} />} title="暂无运行记录" detail="选择一个已采集的交易日查看任务状态。" />}</div></section></>}</section>
}

function InlineNotice({ kind, text }: { kind: 'error' | 'info' | 'warning'; text: string }) { return <div className={`inline-notice ${kind}`}><AlertTriangle size={15} />{text}</div> }
function EmptyState({ icon, title, detail }: { icon: React.ReactNode; title: string; detail: string }) { return <div className="empty-state">{icon}<strong>{title}</strong><span>{detail}</span></div> }

export default App
