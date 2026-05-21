// Login.tsx —— 用户名/密码登录 + 注册。
//
// 直接调 Gateway /v1/admin/auth/login（或 /register），拿 user JWT 存 localStorage。
// 浏览器跟 meshd 的"本机访问 cookie"是另一层，与用户身份无关。

import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import api, { setSession } from '../api/client'

export default function Login() {
  const navigate = useNavigate()
  const [mode, setMode] = useState<'login' | 'register'>('login')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy || !username || !password) return
    setBusy(true)
    setErr('')
    try {
      const path = mode === 'login' ? '/auth/login' : '/auth/register'
      const res = await api.post<{ token: string; uid: number; username: string }>(path, { username, password })
      setSession(res.data.token, res.data.uid, res.data.username)
      navigate('/', { replace: true })
    } catch (e: any) {
      setErr(e?.response?.data?.message ?? `${mode} failed`)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-slate-900 via-slate-800 to-slate-900">
      <div className="w-full max-w-md p-8 space-y-6 bg-card rounded-xl shadow-2xl border border-border">
        <div className="text-center">
          <h1 className="text-3xl font-bold tracking-tight text-foreground">Agent Mesh</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            {mode === 'login' ? 'Sign in to your account' : 'Create your account'}
          </p>
        </div>
        <form onSubmit={submit} className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-foreground mb-1">Username</label>
            <input
              className="w-full px-3 py-2 rounded-md border border-input bg-background text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring"
              placeholder="your name"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              autoFocus
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-foreground mb-1">Password</label>
            <input
              type="password"
              className="w-full px-3 py-2 rounded-md border border-input bg-background text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring"
              placeholder="••••••••"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
            />
          </div>
          {err && (
            <div className="p-3 rounded-md bg-destructive/10 text-destructive text-sm">{err}</div>
          )}
          <button
            type="submit"
            disabled={busy}
            className="w-full py-2.5 px-4 rounded-md bg-primary text-primary-foreground font-medium hover:bg-primary/90 transition-colors disabled:opacity-60"
          >
            {busy ? 'Working...' : mode === 'login' ? 'Sign In' : 'Register'}
          </button>
        </form>
        <p className="text-center text-sm text-muted-foreground">
          <button
            onClick={() => { setMode(mode === 'login' ? 'register' : 'login'); setErr('') }}
            className="text-primary hover:underline"
          >
            {mode === 'login' ? "Don't have an account? Register" : 'Already have an account? Sign in'}
          </button>
        </p>
      </div>
    </div>
  )
}
