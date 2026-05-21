// Settings.tsx —— 用户 / meshd 综合设置页。
//
// 显示：
//   - 当前登录用户（uid + 用户名，从 Gateway /users/me 拿）
//   - meshd 版本 / uptime / 在跑 instance 数（从 /api/health）
//   - Logout 按钮：调 /api/auth/logout 清 keychain user_jwt → 跳 /login
//
// 不在这里：停止 daemon。停 daemon 是命令行的事（agent-meshd stop），
// 也不能从浏览器随便停（你停了 UI 自己也就不可用了）。

import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, LogOut, Activity, User } from 'lucide-react'
import api, { meshdApi, clearSession } from '../api/client'

interface UserMe {
  uid: number
  username: string
  virtual_user_agent_id?: string
}

interface Health {
  status: string
  version: string
  uptime_ms: number
  instance_count: number
}

export default function Settings() {
  const navigate = useNavigate()
  const [me, setMe] = useState<UserMe | null>(null)
  const [health, setHealth] = useState<Health | null>(null)
  const [loggingOut, setLoggingOut] = useState(false)

  useEffect(() => {
    void loadMe()
    void loadHealth()
    const id = setInterval(loadHealth, 5000)
    return () => clearInterval(id)
  }, [])

  const loadMe = async () => {
    try {
      const res = await api.get<UserMe>('/users/me')
      setMe(res.data)
    } catch {
      // 401 已经被 axios interceptor 跳转
    }
  }

  const loadHealth = async () => {
    try {
      const res = await meshdApi.get<Health>('/health')
      setHealth(res.data)
    } catch {
      // ignore
    }
  }

  const onLogout = () => {
    if (loggingOut) return
    setLoggingOut(true)
    clearSession()
    navigate('/login', { replace: true })
  }

  return (
    <div className="min-h-screen bg-gray-50/50 p-8">
      <div className="max-w-3xl mx-auto">
        <div className="flex items-center gap-3 mb-6">
          <button onClick={() => navigate('/')} className="p-2 rounded-md hover:bg-gray-100">
            <ArrowLeft className="h-4 w-4 text-gray-500" />
          </button>
          <h1 className="text-2xl font-bold text-foreground">Settings</h1>
        </div>

        {/* Account */}
        <section className="rounded-xl border border-border bg-white shadow-sm mb-4">
          <div className="px-5 py-4 border-b border-border flex items-center gap-2">
            <User className="h-4 w-4 text-blue-600" />
            <h2 className="text-sm font-semibold text-foreground">Account</h2>
          </div>
          <div className="px-5 py-4 space-y-2 text-sm">
            <Row label="User ID" value={me ? String(me.uid) : '—'} />
            <Row label="Username" value={me?.username ?? '—'} />
            <Row label="Virtual user agent" value={me?.virtual_user_agent_id ?? '—'} mono />
          </div>
          <div className="px-5 py-4 border-t border-border">
            <button
              onClick={onLogout}
              disabled={loggingOut}
              className="inline-flex items-center gap-1.5 px-3 py-2 rounded-md text-sm font-medium text-red-700 bg-red-50 hover:bg-red-100 disabled:opacity-60 transition-colors"
            >
              <LogOut className="h-4 w-4" />
              {loggingOut ? 'Logging out...' : 'Sign out'}
            </button>
            <p className="text-xs text-muted-foreground mt-2">
              Signing out clears the locally cached user token. Running agents on this device will stop receiving Gateway calls until you sign in again.
            </p>
          </div>
        </section>

        {/* meshd status */}
        <section className="rounded-xl border border-border bg-white shadow-sm">
          <div className="px-5 py-4 border-b border-border flex items-center gap-2">
            <Activity className="h-4 w-4 text-emerald-600" />
            <h2 className="text-sm font-semibold text-foreground">Local daemon (meshd)</h2>
          </div>
          <div className="px-5 py-4 space-y-2 text-sm">
            <Row label="Version" value={health?.version ?? '—'} />
            <Row label="Status" value={health?.status ?? '—'} />
            <Row label="Uptime" value={health ? formatUptime(health.uptime_ms) : '—'} />
            <Row label="Running instances" value={health ? String(health.instance_count) : '—'} />
          </div>
          <div className="px-5 py-4 border-t border-border text-xs text-muted-foreground">
            <p>To stop the daemon, run <code className="px-1 py-0.5 bg-gray-100 rounded font-mono">agent-meshd stop</code> in a terminal.</p>
          </div>
        </section>
      </div>
    </div>
  )
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex justify-between gap-4">
      <span className="text-muted-foreground">{label}</span>
      <span className={mono ? 'font-mono text-foreground' : 'text-foreground'}>{value}</span>
    </div>
  )
}

function formatUptime(ms: number): string {
  if (ms < 60_000) return `${Math.floor(ms / 1000)}s`
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m`
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h`
  return `${Math.floor(ms / 86_400_000)}d`
}
