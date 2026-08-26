import { useState } from 'react'
import { KeyRound } from 'lucide-react'
import { setToken } from './auth'
import { api } from './api/client'

export function TokenGate({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  return (
    <main className="app-shell">
      <section className="panel-section token-gate">
        <div className="empty-state"><KeyRound size={28} /><h2>需要访问令牌</h2><p>请输入后端 SHADOWFLOW_API_TOKEN。令牌仅保存在当前浏览器标签页。</p></div>
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            const candidate = value.trim()
            if (!candidate) { setError('请输入访问令牌'); return }
            setSubmitting(true)
            setError('')
            try {
              // Verify before persisting: writing sessionStorage first let a
              // concurrent background poll see the unverified token, unmount
              // the gate, and swallow the error message when it bounced.
              await api.validateToken(candidate)
              setToken(candidate)
              onAuthenticated()
            } catch (authError) {
              setError(authError instanceof Error ? authError.message : '访问令牌验证失败')
            } finally {
              setSubmitting(false)
            }
          }}
        >
          <input autoFocus type="password" value={value} disabled={submitting} onChange={(event) => setValue(event.target.value)} placeholder="API access token" />
          <button className="primary" type="submit" disabled={submitting}>{submitting ? '验证中…' : '解锁'}</button>
        </form>
        {error && <p className="error-text">{error}</p>}
      </section>
    </main>
  )
}
