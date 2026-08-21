import { useState } from 'react'
import { KeyRound } from 'lucide-react'
import { setToken } from './auth'

export function TokenGate({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  return (
    <main className="app-shell">
      <section className="panel-section token-gate">
        <div className="empty-state"><KeyRound size={28} /><h2>需要访问令牌</h2><p>请输入后端 SHADOWFLOW_API_TOKEN。令牌仅保存在当前浏览器标签页。</p></div>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            if (!value.trim()) { setError('请输入访问令牌'); return }
            setToken(value)
            onAuthenticated()
          }}
        >
          <input autoFocus type="password" value={value} onChange={(event) => setValue(event.target.value)} placeholder="API access token" />
          <button className="primary" type="submit">解锁</button>
        </form>
        {error && <p className="error-text">{error}</p>}
      </section>
    </main>
  )
}
