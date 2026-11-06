import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Check, RefreshCw } from 'lucide-react'

import { api } from '../api/client'
import { LIMIT_UP_SCOPE_OPTIONS, limitUpQualityFailureNotice, limitUpQualityQueryKey, limitUpScopeLabel } from '../api/limitup'
import './limitup-quality.css'

const STATUS_LABELS: Record<string, string> = {
  disabled: '未启用自动归档',
  not_collected: '尚未采集',
  running: '正在采集',
  failed: '采集失败',
  complete: '三池完整',
}

function collectedTime(value: string) {
  return new Date(value).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false })
}

export function LimitUpArchivePanel({
  date,
  scope,
  onScopeChange,
}: {
  date: string
  scope: string
  onScopeChange: (scope: string) => void
}) {
  const query = useQuery({
    queryKey: limitUpQualityQueryKey(date, scope),
    queryFn: async ({ signal }) => (await api.limitUpQuality(date, scope || undefined, signal)).data,
    enabled: Boolean(date),
    refetchInterval: false,
    refetchOnWindowFocus: false,
  })
  const quality = query.data
  const failureNotice = limitUpQualityFailureNotice(quality)

  return (
    <section className="event-archive-quality" aria-label="涨跌停事件归档质量">
      <div className="event-archive-heading">
        <div>
          <span className="eyebrow">盘后归档 · 同花顺</span>
          <h2>涨跌停事件</h2>
        </div>
        <div className="event-archive-actions">
          <label className="event-archive-scope">
            <span>市场范围</span>
            <select value={scope} onChange={(event) => onScopeChange(event.target.value)}>
              {LIMIT_UP_SCOPE_OPTIONS.map((option) => (
                <option key={option.value || 'configured'} value={option.value}>{option.label}</option>
              ))}
            </select>
          </label>
          <span className={quality?.complete ? 'ok-label' : 'warn-label'}>
            {quality?.complete ? <Check size={14} /> : <AlertTriangle size={14} />}
            {quality ? STATUS_LABELS[quality.status] : query.isLoading ? '正在读取' : '等待选择日期'}
          </span>
          <button
            type="button"
            className="icon-button"
            title="重新读取事件归档状态"
            aria-label="重新读取事件归档状态"
            disabled={!date || query.isFetching}
            onClick={() => void query.refetch()}
          >
            <RefreshCw size={14} className={query.isFetching ? 'spin' : ''} />
          </button>
        </div>
      </div>

      {query.isError ? (
        <p role="alert">事件归档状态读取失败：{query.error.message}</p>
      ) : quality && (
        <>
          <div className="event-archive-stats">
            <div><span>涨停</span><strong>{quality.day?.sealed_count ?? '--'}</strong></div>
            <div><span>炸板</span><strong>{quality.day?.broken_count ?? '--'}</strong></div>
            <div><span>跌停</span><strong>{quality.day?.down_count ?? '--'}</strong></div>
            <div><span>触涨停</span><strong>{quality.day?.touched_count ?? '--'}</strong></div>
            <div>
              <span>统计口径</span>
              <strong>{limitUpScopeLabel(quality.scope)}</strong>
              <small>{quality.scope}</small>
            </div>
          </div>
          <p className="event-archive-detail">
            {quality.complete && quality.day ? `已采集 ${collectedTime(quality.day.fetched_at)}，三池完整发布。` : quality.status === 'disabled' ? '自动事件归档当前关闭；首次历史回补仍可由手动命令执行。' : quality.status === 'failed' ? `最近一次失败：${quality.last_run?.error_message || '未提供错误原因'}。` : '等待盘后事件归档完成。'}
            {quality.latest_trade_date ? ` 最近完整日期：${quality.latest_trade_date}。` : ''}
          </p>
          {failureNotice && quality.complete && <p className="event-archive-failure" role="alert">{failureNotice}</p>}
        </>
      )}
    </section>
  )
}
