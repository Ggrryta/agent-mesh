import React, { useEffect, useState } from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import './index.css'
import { bootstrapApiClient, currentToken } from './api/client'
import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import Agents from './pages/Agents'
import Tasks from './pages/Tasks'
import Friends from './pages/Friends'
import Groups from './pages/Groups'
import TeamDetail from './pages/TeamDetail'
import Feed from './pages/Feed'
import Settings from './pages/Settings'
import Market from './pages/Market'

// 鉴权检查：只看 localStorage 里有没有 user JWT。
// 实际是否有效 → 第一次发请求时由 axios interceptor 处理（401 自动清 + 跳 /login）。
function PrivateRoute({ children }: { children: React.ReactNode }) {
  if (!currentToken()) return <Navigate to="/login" replace />
  return <>{children}</>
}

function Boot({ children }: { children: React.ReactNode }) {
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [errMsg, setErrMsg] = useState('')

  useEffect(() => {
    let cancelled = false
    bootstrapApiClient()
      .then(() => {
        if (!cancelled) setState('ready')
      })
      .catch((e) => {
        if (cancelled) return
        setErrMsg(e?.message ?? String(e))
        setState('error')
      })
    return () => {
      cancelled = true
    }
  }, [])

  if (state === 'loading') {
    return (
      <div className="min-h-screen flex items-center justify-center text-sm text-muted-foreground">
        Loading...
      </div>
    )
  }
  if (state === 'error') {
    return (
      <div className="min-h-screen flex items-center justify-center p-8">
        <div className="max-w-md text-sm text-destructive">
          <p className="font-semibold mb-2">Cannot connect to local meshd</p>
          <p className="text-muted-foreground">{errMsg}</p>
          <p className="mt-4 text-muted-foreground">
            Try: <code className="px-1 py-0.5 bg-gray-100 rounded">agent-meshd start</code> in your terminal, then refresh.
          </p>
        </div>
      </div>
    )
  }
  return <>{children}</>
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <Boot>
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<PrivateRoute><Dashboard /></PrivateRoute>} />
        <Route path="/agents" element={<PrivateRoute><Agents /></PrivateRoute>} />
        <Route path="/tasks" element={<PrivateRoute><Tasks /></PrivateRoute>} />
        <Route path="/friends" element={<PrivateRoute><Friends /></PrivateRoute>} />
        <Route path="/groups" element={<PrivateRoute><Groups /></PrivateRoute>} />
        <Route path="/groups/:groupId" element={<PrivateRoute><TeamDetail /></PrivateRoute>} />
        <Route path="/feed" element={<PrivateRoute><Feed /></PrivateRoute>} />
        <Route path="/market" element={<PrivateRoute><Market /></PrivateRoute>} />
        <Route path="/settings" element={<PrivateRoute><Settings /></PrivateRoute>} />
      </Routes>
    </BrowserRouter>
  </Boot>
)
