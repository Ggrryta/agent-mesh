import { useEffect, useState } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import {
  Bot, Users, MessageSquare, Network, LogOut, Radio,
  Inbox, ChevronRight, AlertCircle, UserPlus, PowerOff,
  Clock, CheckCircle2, XCircle, Loader2, Settings as SettingsIcon, Sparkles,
} from 'lucide-react'
import api, { clearSession } from '../api/client'

const navItems = [
  { path: '/', label: 'Dashboard', icon: Network },
  { path: '/agents', label: 'Agents', icon: Bot },
  { path: '/market', label: 'Market', icon: Sparkles },
  { path: '/tasks', label: 'Tasks', icon: MessageSquare },
  { path: '/feed', label: 'Live Feed', icon: Radio },
  { path: '/friends', label: 'Friends', icon: Users },
  { path: '/groups', label: 'Groups', icon: Users },
  { path: '/settings', label: 'Settings', icon: SettingsIcon },
]

interface DashSummary {
  active_agents: number
  total_agents: number
  primary_agent_id?: string
}

interface ActionItem {
  kind: string
  title: string
  detail?: string
  ref_id?: string
  created_at?: string
}

interface RecentTask {
  task_id: string
  from: string
  to: string
  status: string
  updated_at: string
}

interface DashboardData {
  summary: DashSummary
  action_items: ActionItem[]
  recent_tasks: RecentTask[]
}

export default function Dashboard() {
  const navigate = useNavigate()
  const location = useLocation()
  const [data, setData] = useState<DashboardData | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const load = () => {
      api.get('/users/me/dashboard')
        .then(res => setData(res.data))
        .catch(() => {})
        .finally(() => setLoading(false))
    }
    load()
    const t = setInterval(load, 15000)
    return () => clearInterval(t)
  }, [])

  const relTime = (iso?: string) => {
    if (!iso) return ''
    const ms = Date.now() - new Date(iso).getTime()
    if (ms < 60_000) return 'just now'
    if (ms < 3600_000) return `${Math.floor(ms / 60_000)}m ago`
    if (ms < 86400_000) return `${Math.floor(ms / 3600_000)}h ago`
    return `${Math.floor(ms / 86400_000)}d ago`
  }

  const actionIcon = (kind: string) => {
    switch (kind) {
      case 'friend_request': return <UserPlus className="h-4 w-4 text-blue-600" />
      case 'task_failed': return <XCircle className="h-4 w-4 text-red-600" />
      case 'agent_draining': return <PowerOff className="h-4 w-4 text-amber-600" />
      default: return <AlertCircle className="h-4 w-4 text-gray-500" />
    }
  }

  const actionRoute = (item: ActionItem) => {
    switch (item.kind) {
      case 'friend_request': return '/friends'
      case 'task_failed': return '/tasks'
      case 'agent_draining': return '/agents'
      default: return '/'
    }
  }

  const taskStatusIcon = (status: string) => {
    switch (status) {
      case 'completed': return <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
      case 'failed': return <XCircle className="h-3.5 w-3.5 text-red-500" />
      case 'working': return <Loader2 className="h-3.5 w-3.5 text-blue-500 animate-spin" />
      default: return <Clock className="h-3.5 w-3.5 text-amber-500" />
    }
  }

  return (
    <div className="min-h-screen bg-gray-50/50 flex">
      {/* Sidebar */}
      <aside className="w-64 bg-white border-r border-border flex flex-col shadow-sm">
        <div className="p-5 border-b border-border">
          <div className="flex items-center gap-2.5">
            <div className="w-8 h-8 rounded-lg bg-gradient-to-br from-blue-600 to-indigo-600 flex items-center justify-center">
              <Network className="h-4 w-4 text-white" />
            </div>
            <div>
              <h1 className="text-sm font-bold text-foreground">Agent Mesh</h1>
              <p className="text-[11px] text-muted-foreground">Control Console</p>
            </div>
          </div>
        </div>
        <nav className="flex-1 p-3 space-y-0.5">
          {navItems.map((item) => {
            const Icon = item.icon
            const active = location.pathname === item.path
            return (
              <button
                key={item.path}
                onClick={() => navigate(item.path)}
                className={`w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-all duration-150 ${
                  active
                    ? 'bg-blue-50 text-blue-700 shadow-sm'
                    : 'text-gray-600 hover:bg-gray-50 hover:text-gray-900'
                }`}
              >
                <Icon className={`h-4 w-4 ${active ? 'text-blue-600' : ''}`} />
                {item.label}
              </button>
            )
          })}
        </nav>
        <div className="p-3 border-t border-border">
          <button
            onClick={() => { clearSession(); navigate('/login') }}
            className="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium text-gray-500 hover:bg-red-50 hover:text-red-600 transition-all duration-150"
          >
            <LogOut className="h-4 w-4" />
            Logout
          </button>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 p-8 overflow-y-auto">
        {/* Header */}
        <div className="mb-6 flex items-end justify-between">
          <div>
            <h2 className="text-2xl font-bold text-foreground">Dashboard</h2>
            <p className="text-sm text-muted-foreground mt-1">
              {loading ? 'Loading…' : `${data?.summary.active_agents ?? 0} of ${data?.summary.total_agents ?? 0} agents online`}
              {data?.summary.primary_agent_id && (
                <span className="ml-2 inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md bg-emerald-50 text-emerald-700 text-xs">
                  <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse" />
                  Primary: {data.summary.primary_agent_id}
                </span>
              )}
            </p>
          </div>
        </div>

        {/* Three-column layout */}
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
          {/* Action Items */}
          <section className="rounded-xl border border-border bg-white shadow-sm overflow-hidden">
            <div className="px-5 py-4 border-b border-border bg-gradient-to-r from-amber-50 to-white flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Inbox className="h-4 w-4 text-amber-600" />
                <h3 className="text-sm font-semibold text-foreground">Needs Attention</h3>
              </div>
              {data && data.action_items.length > 0 && (
                <span className="badge badge-warning">{data.action_items.length}</span>
              )}
            </div>
            <div className="divide-y divide-border max-h-[480px] overflow-y-auto">
              {!data || data.action_items.length === 0 ? (
                <div className="px-5 py-12 text-center text-sm text-muted-foreground">
                  <CheckCircle2 className="h-8 w-8 mx-auto text-emerald-300 mb-2" />
                  All clear. Nothing pending.
                </div>
              ) : (
                data.action_items.map((item, i) => (
                  <button
                    key={i}
                    onClick={() => navigate(actionRoute(item))}
                    className="w-full px-5 py-3 hover:bg-gray-50 transition-colors text-left flex items-start gap-3"
                  >
                    <div className="mt-0.5">{actionIcon(item.kind)}</div>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium text-foreground">{item.title}</p>
                      {item.detail && <p className="text-xs text-muted-foreground truncate mt-0.5">{item.detail}</p>}
                      <p className="text-xs text-muted-foreground/70 mt-1">{relTime(item.created_at)}</p>
                    </div>
                    <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/50 mt-1" />
                  </button>
                ))
              )}
            </div>
          </section>

          {/* Continue Working — Recent Tasks */}
          <section className="lg:col-span-2 rounded-xl border border-border bg-white shadow-sm overflow-hidden">
            <div className="px-5 py-4 border-b border-border bg-gradient-to-r from-blue-50/50 to-white flex items-center justify-between">
              <div className="flex items-center gap-2">
                <MessageSquare className="h-4 w-4 text-blue-600" />
                <h3 className="text-sm font-semibold text-foreground">Continue Working</h3>
              </div>
              <button
                onClick={() => navigate('/tasks')}
                className="text-xs text-blue-600 hover:underline flex items-center gap-1"
              >
                View all
                <ChevronRight className="h-3 w-3" />
              </button>
            </div>
            <div className="divide-y divide-border max-h-[480px] overflow-y-auto">
              {!data || data.recent_tasks.length === 0 ? (
                <div className="px-5 py-12 text-center text-sm text-muted-foreground">
                  <MessageSquare className="h-8 w-8 mx-auto text-muted-foreground/30 mb-2" />
                  No recent tasks.
                  <button
                    onClick={() => navigate('/tasks')}
                    className="block mx-auto mt-3 text-xs text-blue-600 hover:underline"
                  >
                    Send your first command →
                  </button>
                </div>
              ) : (
                data.recent_tasks.map((t) => (
                  <button
                    key={t.task_id}
                    onClick={() => navigate('/tasks')}
                    className="w-full px-5 py-3.5 hover:bg-gray-50 transition-colors text-left flex items-center gap-3"
                  >
                    <div>{taskStatusIcon(t.status)}</div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-1.5 text-sm text-foreground">
                        <span className="font-medium truncate">{t.from}</span>
                        <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
                        <span className="font-medium truncate">{t.to}</span>
                      </div>
                      <p className="text-xs text-muted-foreground font-mono truncate mt-0.5">
                        {t.task_id}
                      </p>
                    </div>
                    <div className="flex flex-col items-end gap-1 shrink-0">
                      <span className={
                        t.status === 'completed' ? 'badge badge-success' :
                        t.status === 'failed' ? 'badge badge-danger' :
                        t.status === 'working' ? 'badge badge-info' :
                        'badge badge-warning'
                      }>{t.status}</span>
                      <span className="text-xs text-muted-foreground/70">{relTime(t.updated_at)}</span>
                    </div>
                  </button>
                ))
              )}
            </div>
          </section>
        </div>

        {/* Quick links footer */}
        <div className="mt-6 grid grid-cols-3 gap-4">
          <button onClick={() => navigate('/agents')} className="card-hover p-4 rounded-xl border border-border bg-white text-left flex items-center gap-3">
            <div className="w-9 h-9 rounded-lg bg-blue-50 flex items-center justify-center">
              <Bot className="h-4 w-4 text-blue-600" />
            </div>
            <div>
              <p className="text-sm font-medium text-foreground">Manage Agents</p>
              <p className="text-xs text-muted-foreground">Configure & deploy</p>
            </div>
          </button>
          <button onClick={() => navigate('/feed')} className="card-hover p-4 rounded-xl border border-border bg-white text-left flex items-center gap-3">
            <div className="w-9 h-9 rounded-lg bg-emerald-50 flex items-center justify-center">
              <Radio className="h-4 w-4 text-emerald-600" />
            </div>
            <div>
              <p className="text-sm font-medium text-foreground">Live Feed</p>
              <p className="text-xs text-muted-foreground">Real-time activity</p>
            </div>
          </button>
          <button onClick={() => navigate('/friends')} className="card-hover p-4 rounded-xl border border-border bg-white text-left flex items-center gap-3">
            <div className="w-9 h-9 rounded-lg bg-violet-50 flex items-center justify-center">
              <Users className="h-4 w-4 text-violet-600" />
            </div>
            <div>
              <p className="text-sm font-medium text-foreground">Network</p>
              <p className="text-xs text-muted-foreground">Friends & groups</p>
            </div>
          </button>
        </div>
      </main>
    </div>
  )
}
