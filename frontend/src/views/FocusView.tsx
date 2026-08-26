import { useEffect, useState, type ReactNode } from 'react'
import { AlertTriangle, CalendarDays, Copy, Crosshair, Filter, Plus, Save, Search, Trash2, Upload } from 'lucide-react'

import type {
  FocusConceptCandidate,
  FocusCondition,
  FocusDailyMetric,
  FocusField,
  FocusMatchMode,
  FocusOperator,
  FocusRejection,
  FocusResult,
  FocusScanRequest,
  FocusStockCandidate,
} from '../api/types'

const FOCUS_TEMPLATE_KEY = 'shadowflow.focus.templates.v1'

type FocusTemplate = { name: string; request: FocusScanRequest }

const FOCUS_FIELDS: Array<{ field: FocusField; label: string; unit: string; factor: number; step: number }> = [
  { field: 'turnover', label: '成交额', unit: '亿元', factor: 100_000_000, step: 0.1 },
  { field: 'turnover_rate', label: '换手率', unit: '%', factor: 0.01, step: 0.1 },
  { field: 'change_pct', label: '涨跌幅', unit: '%', factor: 0.01, step: 0.1 },
  { field: 'control_coefficient', label: '控盘系数', unit: '%', factor: 1, step: 0.1 },
  { field: 'dark_money', label: '主力暗盘', unit: '亿元', factor: 100_000_000, step: 0.01 },
  { field: 'regular_money', label: '主力明盘', unit: '亿元', factor: 100_000_000, step: 0.01 },
  { field: 'main_money_inflow', label: '主力净流入', unit: '亿元', factor: 100_000_000, step: 0.01 },
  { field: 'dark_activity', label: '暗盘活跃度', unit: '%', factor: 0.01, step: 0.1 },
  { field: 'dark_inflow_ratio', label: '暗盘流入占比', unit: '%', factor: 0.01, step: 0.1 },
  { field: 'rank', label: '榜单排名', unit: '名', factor: 1, step: 1 },
  { field: 'close_price', label: '收盘价', unit: '元', factor: 1, step: 0.01 },
  { field: 'amplitude', label: '振幅', unit: '%', factor: 0.01, step: 0.1 },
  { field: 'volume', label: '成交量', unit: '股', factor: 1, step: 100 },
  { field: 'up_count', label: '上涨家数', unit: '家', factor: 1, step: 1 },
  { field: 'flat_count', label: '平盘家数', unit: '家', factor: 1, step: 1 },
  { field: 'down_count', label: '下跌家数', unit: '家', factor: 1, step: 1 },
]

const FOCUS_OPERATORS: Array<{ value: FocusOperator; label: string }> = [
  { value: 'gt', label: '大于' }, { value: 'gte', label: '大于等于' }, { value: 'lt', label: '小于' },
  { value: 'lte', label: '小于等于' }, { value: 'eq', label: '等于' }, { value: 'between', label: '区间' },
]

function loadFocusTemplates(): FocusTemplate[] {
  try {
    const value = JSON.parse(localStorage.getItem(FOCUS_TEMPLATE_KEY) ?? '[]') as FocusTemplate[]
    return Array.isArray(value) ? value.filter((item) => item?.name && item?.request) : []
  } catch {
    return []
  }
}

function focusRequestFromJSON(value: string): FocusScanRequest | null {
  try {
    const request = JSON.parse(value) as FocusScanRequest
    if (!request || !Array.isArray(request.concept_conditions) || !Array.isArray(request.stock_conditions) || !request.stock_scope) return null
    return request
  } catch {
    return null
  }
}

function isFocusStock(record: FocusConceptCandidate | FocusStockCandidate): record is FocusStockCandidate {
  return 'concepts' in record
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

function signedClass(value: number) {
  return value > 0 ? 'positive' : value < 0 ? 'negative' : 'neutral'
}

export function FocusView({ request, onScan, result, loading, error }: {
  request: FocusScanRequest
  onScan: (value: FocusScanRequest) => void
  result?: FocusResult
  loading: boolean
  error: Error | null
}) {
  const [draft, setDraft] = useState<FocusScanRequest>(request)
  const [mode, setMode] = useState<'concepts' | 'stocks'>('concepts')
  const [query, setQuery] = useState('')
  const [templates, setTemplates] = useState<FocusTemplate[]>(loadFocusTemplates)
  const [selectedTemplate, setSelectedTemplate] = useState('')
  const [templateNotice, setTemplateNotice] = useState('')
  useEffect(() => setDraft(request), [request])
  useEffect(() => localStorage.setItem(FOCUS_TEMPLATE_KEY, JSON.stringify(templates)), [templates])
  const normalized = query.trim().toLowerCase()
  const records: Array<FocusConceptCandidate | FocusStockCandidate> = mode === 'concepts' ? (result?.concepts ?? []) : (result?.stocks ?? [])
  const visible = records.filter((record) => !normalized || record.name.toLowerCase().includes(normalized) || record.code.toLowerCase().includes(normalized) || (isFocusStock(record) && record.concepts.some((concept) => concept.name.toLowerCase().includes(normalized))))
  const applied = result?.request ?? request
  const activeConditions = mode === 'concepts' ? applied.concept_conditions : applied.stock_conditions
  const displayFields = Array.from(new Set(activeConditions.map((condition) => condition.field)))
  if (!displayFields.length) displayFields.push('control_coefficient')
  const updateScope = (key: keyof FocusScanRequest['stock_scope'], value: boolean) => setDraft((current) => ({ ...current, stock_scope: { ...current.stock_scope, [key]: value } }))
  const flashTemplateNotice = (value: string) => { setTemplateNotice(value); window.setTimeout(() => setTemplateNotice(''), 1800) }
  const saveTemplate = () => {
    const name = window.prompt('模板名称', selectedTemplate || '')
    if (!name?.trim()) return
    const normalizedName = name.trim()
    setTemplates((current) => [...current.filter((item) => item.name !== normalizedName), { name: normalizedName, request: draft }].sort((left, right) => left.name.localeCompare(right.name, 'zh-CN')))
    setSelectedTemplate(normalizedName)
    flashTemplateNotice('已保存')
  }
  const loadTemplate = (name: string) => {
    setSelectedTemplate(name)
    const template = templates.find((item) => item.name === name)
    if (template) setDraft({ ...template.request, as_of: draft.as_of })
  }
  const deleteTemplate = () => {
    if (!selectedTemplate) return
    setTemplates((current) => current.filter((item) => item.name !== selectedTemplate))
    setSelectedTemplate('')
    flashTemplateNotice('已删除')
  }
  const copyTemplate = async () => {
    try {
      await navigator.clipboard.writeText(JSON.stringify(draft, null, 2))
      flashTemplateNotice('已复制')
    } catch {
      flashTemplateNotice('复制失败')
    }
  }
  const importTemplate = () => {
    const value = window.prompt('粘贴筛选参数 JSON')
    if (!value) return
    const imported = focusRequestFromJSON(value)
    if (!imported) {
      flashTemplateNotice('JSON 无效')
      return
    }
    setDraft({ ...imported, as_of: draft.as_of })
    flashTemplateNotice('已导入')
  }
  return <section className="focus-page panel-section">
    <div className="section-heading"><div><p className="eyebrow">逐日条件</p><h1>动态连续筛选</h1><span className="subline">{result?.ready ? `${result.trade_dates.join(' · ')} · 完整日终截面` : result ? `当前可用 ${result.trade_dates.length} / ${result.required_days} 个交易日` : '尚未执行筛选'}</span></div><div className="focus-run-controls"><div className="focus-template-controls"><select value={selectedTemplate} onChange={(event) => loadTemplate(event.target.value)}><option value="">筛选模板</option>{templates.map((template) => <option value={template.name} key={template.name}>{template.name}</option>)}</select><button className="icon-button" title="保存模板" onClick={saveTemplate}><Save size={15} /></button><button className="icon-button" title="删除模板" disabled={!selectedTemplate} onClick={deleteTemplate}><Trash2 size={15} /></button><button className="icon-button" title="复制筛选参数" onClick={() => void copyTemplate()}><Copy size={15} /></button><button className="icon-button" title="导入筛选参数" onClick={importTemplate}><Upload size={15} /></button>{templateNotice && <span>{templateNotice}</span>}</div><label className="focus-days"><span>连续</span><input type="number" min={1} max={60} value={draft.consecutive_days} onChange={(event) => setDraft((current) => ({ ...current, consecutive_days: Math.max(1, Math.min(60, Number(event.target.value) || 1)) }))} /><span>日</span></label><label className="date-picker"><CalendarDays size={15} /><input type="date" value={draft.as_of} onChange={(event) => setDraft((current) => ({ ...current, as_of: event.target.value }))} /></label><button className="focus-run-button" onClick={() => onScan(draft)} disabled={loading || !draft.as_of}><Filter size={15} />执行筛选</button></div></div>
    <div className="focus-builder">
      <RulePanel title="概念条件" match={draft.concept_match} conditions={draft.concept_conditions} setMatch={(concept_match) => setDraft((current) => ({ ...current, concept_match }))} setConditions={(concept_conditions) => setDraft((current) => ({ ...current, concept_conditions }))} />
      <RulePanel title="个股条件" match={draft.stock_match} conditions={draft.stock_conditions} setMatch={(stock_match) => setDraft((current) => ({ ...current, stock_match }))} setConditions={(stock_conditions) => setDraft((current) => ({ ...current, stock_conditions }))} />
      <div className="focus-scope"><strong>个股范围</strong><label><input type="checkbox" checked={draft.stock_scope.main_board_only} onChange={(event) => updateScope('main_board_only', event.target.checked)} />仅主板</label><label><input type="checkbox" checked={draft.stock_scope.exclude_st} onChange={(event) => updateScope('exclude_st', event.target.checked)} />排除 ST</label><label><input type="checkbox" checked={draft.stock_scope.require_qualified_concepts} onChange={(event) => updateScope('require_qualified_concepts', event.target.checked)} />仅命中概念成分股</label></div>
    </div>
    {error && <InlineNotice text={error.message || '动态筛选数据读取失败。'} />}
    {loading && !result && <div className="loading-block">正在计算筛选结果…</div>}
    {!loading && result && !result.ready && <div className="focus-not-ready"><Crosshair size={28} /><strong>日终数据尚不足 {result.required_days} 个完整交易日</strong><span>当前已累积 {result.trade_dates.length} / {result.required_days} 日{result.trade_dates.length ? `：${result.trade_dates.join('、')}` : ''}</span></div>}
    {result?.ready && <>
      <div className="focus-summary"><span><b>{result.concepts.length}</b>入选概念</span><span><b>{result.stocks.length}</b>入选个股</span><span><b>{result.stats.non_main_board_excluded}</b>非主板剔除</span><span><b>{result.stats.st_excluded}</b>ST 剔除</span></div>
      <div className="focus-toolbar"><div className="segmented"><button className={mode === 'concepts' ? 'active' : ''} onClick={() => setMode('concepts')}>概念 {result.concepts.length}</button><button className={mode === 'stocks' ? 'active' : ''} onClick={() => setMode('stocks')}>个股 {result.stocks.length}</button></div><label className="search-field"><Search size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称、代码或概念" /></label></div>
      <div className="focus-table-wrap"><table className="focus-table"><thead><tr><th>{mode === 'concepts' ? '概念板块' : applied.stock_scope.main_board_only ? '主板个股' : '个股'}</th>{mode === 'stocks' && <th>入选概念</th>}{displayFields.map((field) => <th key={field}>{focusField(field).label}</th>)}</tr></thead><tbody>{visible.map((record) => <tr key={record.code}><td><strong>{record.name}</strong><small>{record.code}</small></td>{mode === 'stocks' && <td className="focus-concepts">{isFocusStock(record) ? record.concepts.map((concept) => concept.name).join(' · ') || '--' : ''}</td>}{displayFields.map((field) => <FocusValues key={field} days={record.days} field={field} />)}</tr>)}</tbody></table>{!visible.length && <EmptyState icon={<Crosshair size={22} />} title="没有符合条件的记录" detail={`连续 ${applied.consecutive_days} 个交易日需逐日符合当前规则。`} />}</div>
      <FocusRejections rejections={result.rejections ?? []} truncated={result.rejections_truncated} />
    </>}
  </section>
}

function FocusRejections({ rejections, truncated }: { rejections: FocusRejection[]; truncated: boolean }) {
  if (!rejections.length) return null
  const reasonLabels: Record<FocusRejection['reason'], string> = {
    condition_failed: '条件未通过', non_main_board: '非主板', st_excluded: 'ST', missing_daily_close: '缺少日终行情',
  }
  return <details className="focus-rejections"><summary>未入选解释 <span>{rejections.length}{truncated ? '+' : ''}</span></summary><div className="focus-rejection-table-wrap"><table><thead><tr><th>标的</th><th>原因</th><th>失败日</th><th>条件</th><th>实际值</th></tr></thead><tbody>{rejections.map((item) => { const failed = item.evaluation?.conditions.find((condition) => !condition.passed); return <tr key={`${item.kind}-${item.code}`}><td><strong>{item.name}</strong><small>{item.code}</small></td><td>{reasonLabels[item.reason]}</td><td>{item.failed_date ?? '--'}</td><td>{failed ? focusField(failed.condition.field).label : '--'}</td><td>{failed ? failed.available ? formatFocusActual(failed.condition.field, failed.actual_value) : '数据不可用' : '--'}</td></tr> })}</tbody></table></div></details>
}

function RulePanel({ title, match, conditions, setMatch, setConditions }: {
  title: string
  match: FocusMatchMode
  conditions: FocusCondition[]
  setMatch: (value: FocusMatchMode) => void
  setConditions: (value: FocusCondition[]) => void
}) {
  const update = (index: number, condition: FocusCondition) => setConditions(conditions.map((item, itemIndex) => itemIndex === index ? condition : item))
  return <section className="rule-panel"><div className="rule-panel-head"><strong>{title}</strong><select value={match} onChange={(event) => setMatch(event.target.value as FocusMatchMode)}><option value="all">全部满足</option><option value="any">任一满足</option></select></div><div className="rule-list">{conditions.map((condition, index) => {
    const meta = focusField(condition.field)
    return <div className="rule-row" key={`${condition.field}-${index}`}><select value={condition.field} onChange={(event) => update(index, { field: event.target.value as FocusField, operator: condition.operator, value: 0, ...(condition.operator === 'between' ? { max_value: 0 } : {}) })}>{FOCUS_FIELDS.map((field) => <option value={field.field} key={field.field}>{field.label}</option>)}</select><select value={condition.operator} onChange={(event) => { const operator = event.target.value as FocusOperator; update(index, { ...condition, operator, ...(operator === 'between' ? { max_value: condition.max_value ?? condition.value } : { max_value: undefined }) }) }}>{FOCUS_OPERATORS.map((operator) => <option value={operator.value} key={operator.value}>{operator.label}</option>)}</select><ConditionValueInput value={condition.value} factor={meta.factor} unit={meta.unit} onCommit={(value) => update(index, { ...condition, value })} />{condition.operator === 'between' && <><i>至</i><ConditionValueInput value={condition.max_value ?? condition.value} factor={meta.factor} unit={meta.unit} onCommit={(value) => update(index, { ...condition, max_value: value })} /></>}<button className="rule-remove" title="删除条件" onClick={() => setConditions(conditions.filter((_, itemIndex) => itemIndex !== index))}><Trash2 size={15} /></button></div>
  })}</div><button className="rule-add" onClick={() => setConditions([...conditions, { field: 'turnover', operator: 'gt', value: 0 }])}><Plus size={14} />添加条件</button></section>
}

// A text input with a string intermediate state: <input type="number"> wipes
// transitional text like "3." (its value reads as ""), so typing 3.5 into a
// numeric field silently became 0.05 after the factor conversion — a wrong
// order of magnitude in the filter condition.
function ConditionValueInput({ value, factor, unit, onCommit }: { value: number; factor: number; unit: string; onCommit: (value: number) => void }) {
  const [raw, setRaw] = useState<string | null>(null)
  return <label className="rule-value">
    <input
      type="text"
      inputMode="decimal"
      value={raw ?? String(displayConditionValue(value, factor))}
      onChange={(event) => {
        const text = event.target.value
        setRaw(text)
        const parsed = Number(text)
        if (text !== '' && Number.isFinite(parsed)) onCommit(parsed * factor)
      }}
      onBlur={() => setRaw(null)}
    />
    <span>{unit}</span>
  </label>
}

function focusField(field: FocusField) {
  return FOCUS_FIELDS.find((item) => item.field === field) ?? FOCUS_FIELDS[0]
}

function displayConditionValue(value: number, factor: number) {
  return Number((value / factor).toFixed(6))
}

function formatFocusMetric(day: FocusDailyMetric, field: FocusField) {
  if (!focusMetricAvailable(day, field)) return '--'
  const value = day[field]
  const meta = focusField(field)
  if (['turnover', 'dark_money', 'regular_money', 'main_money_inflow'].includes(field)) return formatMoney(value)
  if (['turnover_rate', 'change_pct', 'dark_activity', 'dark_inflow_ratio', 'amplitude'].includes(field)) return `${value > 0 && field === 'change_pct' ? '+' : ''}${formatNumber(value * 100, 2)}%`
  if (field === 'control_coefficient') return `${formatNumber(value, 2)}%`
  if (field === 'close_price') return `${formatNumber(value, 2)}元`
  return `${formatNumber(value)}${meta.unit}`
}

function focusMetricAvailable(day: FocusDailyMetric, field: FocusField) {
  if (['control_coefficient', 'dark_money', 'regular_money', 'main_money_inflow'].includes(field)) return day.money_available
  if (['dark_activity', 'dark_inflow_ratio', 'rank', 'up_count', 'flat_count', 'down_count'].includes(field)) return day.rank_available
  return true
}

function formatFocusActual(field: FocusField, value: number) {
  const meta = focusField(field)
  if (['turnover', 'dark_money', 'regular_money', 'main_money_inflow'].includes(field)) return formatMoney(value)
  if (['turnover_rate', 'change_pct', 'dark_activity', 'dark_inflow_ratio', 'amplitude'].includes(field)) return `${formatNumber(value * 100, 2)}%`
  if (field === 'control_coefficient') return `${formatNumber(value, 2)}%`
  if (field === 'close_price') return `${formatNumber(value, 2)}元`
  return `${formatNumber(value)}${meta.unit}`
}

function FocusValues({ days, field }: { days: FocusDailyMetric[]; field: FocusField }) {
  const signed = ['change_pct', 'dark_money', 'regular_money', 'main_money_inflow'].includes(field)
  return <td><div className="focus-values" style={{ gridTemplateColumns: `repeat(${days.length}, minmax(64px, auto))` }}>{days.map((day) => <span key={day.trade_date}><small>{day.trade_date.slice(5)}</small><b className={signed ? signedClass(day[field]) : ''}>{formatFocusMetric(day, field)}</b></span>)}</div></td>
}

function InlineNotice({ text }: { text: string }) {
  return <div className="inline-notice error"><AlertTriangle size={15} />{text}</div>
}

function EmptyState({ icon, title, detail }: { icon: ReactNode; title: string; detail: string }) {
  return <div className="empty-state">{icon}<strong>{title}</strong><span>{detail}</span></div>
}
