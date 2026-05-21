import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, Search, UserPlus, Check, Users, Globe, Bell } from 'lucide-react'
import api from '../api/client'

interface MarketAgent {
  agent_id: string
  name: string
  description: string
  skills: string[]
}

interface Friend {
  id: number
  from_agent_id: string
  to_agent_id: string
  status: string
}

/** 给 incoming 列表用：除了 friend 信息外，还知道这个请求是发给我哪个 agent 的。 */
interface IncomingRequest extends Friend {
  to_my_agent: string
}

interface MyAgent {
  agent_id: string
  name: string
  kind: string
}

export default function Friends() {
  const [market, setMarket] = useState<MarketAgent[]>([])
  const [friends, setFriends] = useState<Friend[]>([])
  /** incoming requests 是"所有 my agent 收到的"汇总，不依赖 actingAs */
  const [incoming, setIncoming] = useState<IncomingRequest[]>([])
  /** 用户当前所有 normal agent，给"Acting as" 选择器用 */
  const [myAgents, setMyAgents] = useState<MyAgent[]>([])
  /** 当前以哪个 agent 视角操作（发好友请求 / 看好友列表） */
  const [actingAs, setActingAs] = useState('')
  const [search, setSearch] = useState('')
  const [err, setErr] = useState('')
  const navigate = useNavigate()

  // 调试：每次 render 打印 incoming 状态
  console.log('[Friends] render, incoming.length =', incoming.length, 'actingAs =', actingAs)

  useEffect(() => { void loadMyAgents() }, [])
  // myAgents 加载完后汇总所有 incoming（每个 agent 都查一次再合并）
  useEffect(() => {
    if (myAgents.length > 0) void loadAllIncoming(myAgents)
  }, [myAgents])
  // actingAs 切换时只需要刷 friends 列表（incoming 已经汇总好了）
  useEffect(() => {
    if (actingAs) void loadFriends(actingAs)
  }, [actingAs])

  const loadMyAgents = async () => {
    try {
      const res = await api.get<{ agents: MyAgent[] }>('/users/me/agents')
      const list = (res.data.agents || []).filter((a) => a.kind !== 'virtual-user')
      setMyAgents(list)
      if (list.length > 0 && !actingAs) {
        setActingAs(list[0].agent_id)
      }
    } catch (e: any) {
      setErr(e?.response?.data?.message ?? 'Failed to load your agents')
    }
  }

  /** 把 my 全部 normal agent 的 incoming requests 汇总到一处。 */
  const loadAllIncoming = async (agents: MyAgent[]) => {
    console.log('[Friends] loadAllIncoming start', agents.map((a) => a.agent_id))
    const all: IncomingRequest[] = []
    for (const a of agents) {
      try {
        const res = await api.get(`/agents/${a.agent_id}/friends/incoming`)
        // 后端字段名是 "incoming"（admin handler.go handleListIncoming）
        const arr: Friend[] = res.data.incoming || []
        console.log(`[Friends] incoming for ${a.agent_id}:`, arr.length, 'items')
        for (const f of arr) all.push({ ...f, to_my_agent: a.agent_id })
      } catch (e) {
        console.warn(`[Friends] incoming for ${a.agent_id} failed`, e)
      }
    }
    console.log('[Friends] loadAllIncoming done, setIncoming with', all.length, 'items')
    setIncoming(all)
  }

  const loadFriends = async (agentId: string) => {
    try {
      // 显式只拉 accepted —— pending / rejected 不应该出现在 "My Friends" 里
      const res = await api.get(`/agents/${agentId}/friends?status=accepted`)
      setFriends(res.data.friends || [])
    } catch {
      // 切换 agent 时 race，忽略
    }
  }

  const loadIncoming = async (agentId: string) => {
    console.log('[Friends] loadIncoming for', agentId)
    try {
      const res = await api.get(`/agents/${agentId}/friends/incoming`)
      const arr: Friend[] = res.data.incoming || []
      console.log(`[Friends] loadIncoming(${agentId}) got`, arr.length, 'items')
      setIncoming((prev) => {
        const others = prev.filter((p) => p.to_my_agent !== agentId)
        const next = [...others, ...arr.map((f) => ({ ...f, to_my_agent: agentId }))]
        console.log(`[Friends] setIncoming via loadIncoming: ${prev.length} → ${next.length}`)
        return next
      })
    } catch (e) {
      console.warn(`[Friends] loadIncoming(${agentId}) failed`, e)
    }
  }

  const searchMarket = async () => {
    setErr('')
    try {
      const res = await api.get(`/market/agents?search=${encodeURIComponent(search)}`)
      setMarket(res.data.agents || [])
    } catch (e: any) {
      setErr(e?.response?.data?.message ?? 'Search failed')
    }
  }

  const sendRequest = async (toAgent: string) => {
    console.log('[Friends] sendRequest', { from: actingAs, to: toAgent })
    if (!actingAs) {
      setErr('Select one of your agents first')
      return
    }
    setErr('')
    try {
      const res = await api.post('/friends', { from_agent_id: actingAs, to_agent_id: toAgent, reason: 'mesh collaboration' })
      console.log('[Friends] sendRequest ok', res.data)
      await loadFriends(actingAs)
      // 也要刷 incoming —— 收件方那个 agent 现在有一条新 pending
      await loadIncoming(toAgent)
    } catch (e: any) {
      console.error('[Friends] sendRequest failed', e?.response?.status, e?.response?.data)
      setErr(e?.response?.data?.message ?? 'Failed to send friend request')
    }
  }

  const acceptRequest = async (req: IncomingRequest) => {
    console.log('[Friends] acceptRequest fired', req)
    setErr('')
    try {
      const res = await api.post(`/friends/${req.id}/accept`)
      console.log('[Friends] accept ok', res.data)
      // 刷该请求归属的 agent 的 incoming + 当前视角的 friends
      await loadIncoming(req.to_my_agent)
      if (actingAs) await loadFriends(actingAs)
    } catch (e: any) {
      console.error('[Friends] accept failed', e)
      setErr(e?.response?.data?.message ?? 'Failed to accept request')
    }
  }

  return (
    <div className="min-h-screen bg-gray-50/50 p-8">
      <div className="max-w-5xl mx-auto">
        {/* Header */}
        <div className="flex items-center justify-between gap-3 mb-6">
          <div className="flex items-center gap-3">
            <button onClick={() => navigate('/')} className="p-2 rounded-lg hover:bg-white border border-transparent hover:border-border transition-all">
              <ArrowLeft className="h-4 w-4 text-gray-500" />
            </button>
            <div>
              <h2 className="text-xl font-bold text-foreground">Friends & Market</h2>
              <p className="text-sm text-muted-foreground">Discover agents and manage connections</p>
            </div>
          </div>
          {/* Acting-as 选择器：好友请求 / 列表 / incoming 都从这个 agent 视角操作 */}
          {myAgents.length > 0 && (
            <div className="flex items-center gap-2 text-sm">
              <span className="text-muted-foreground">Acting as</span>
              <select
                value={actingAs}
                onChange={(e) => setActingAs(e.target.value)}
                className="px-3 py-1.5 rounded-md border border-input bg-white font-mono text-xs focus:outline-none focus:ring-2 focus:ring-ring"
              >
                {myAgents.map((a) => (
                  <option key={a.agent_id} value={a.agent_id}>
                    {a.agent_id}
                  </option>
                ))}
              </select>
            </div>
          )}
        </div>

        {myAgents.length === 0 && (
          <div className="mb-4 p-3 rounded-md bg-amber-50 text-amber-900 text-sm">
            You don't have any normal agent yet. Create one in <button onClick={() => navigate('/agents')} className="underline">Agents</button> first.
          </div>
        )}
        {err && (
          <div className="mb-4 p-3 rounded-md bg-destructive/10 text-destructive text-sm flex items-center justify-between gap-3">
            <span>{err}</span>
            <button onClick={() => setErr('')} className="text-xs px-2 py-1 rounded hover:bg-destructive/20">dismiss</button>
          </div>
        )}

        <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
          {/* Market search */}
          <div className="lg:col-span-2 rounded-xl border border-border bg-white shadow-sm overflow-hidden">
            <div className="px-5 py-4 border-b border-border flex items-center gap-2">
              <Globe className="h-4 w-4 text-muted-foreground" />
              <h3 className="text-sm font-semibold text-foreground">Agent Market</h3>
            </div>
            <div className="p-5">
              <div className="flex gap-2 mb-4">
                <input
                  placeholder="Search agents by name..."
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && !e.nativeEvent.isComposing && searchMarket()}
                  className="flex-1 px-3 py-2.5 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                />
                <button
                  onClick={searchMarket}
                  className="flex items-center gap-2 px-4 py-2.5 rounded-lg bg-gray-900 text-white text-sm font-medium hover:bg-gray-800 transition-colors"
                >
                  <Search className="h-4 w-4" />
                  Search
                </button>
              </div>
              <div className="space-y-2">
                {market.map((a) => {
                  // 已经是好友 / 是 actingAs 自己 → 不显示 Add
                  // 同 owner 的别的 agent 仍然可以加（用户想让自己两个 agent 协作就需要）
                  const isSelf = a.agent_id === actingAs
                  const alreadyFriend = friends.some(
                    (f) =>
                      (f.from_agent_id === actingAs && f.to_agent_id === a.agent_id) ||
                      (f.to_agent_id === actingAs && f.from_agent_id === a.agent_id),
                  )
                  return (
                  <div key={a.agent_id} className="flex items-center justify-between p-3 rounded-lg border border-border hover:bg-gray-50 transition-colors">
                    <div className="flex items-center gap-3">
                      <div className="w-9 h-9 rounded-lg bg-violet-50 flex items-center justify-center">
                        <Users className="h-4 w-4 text-violet-600" />
                      </div>
                      <div>
                        <p className="text-sm font-medium text-foreground">{a.name}</p>
                        <p className="text-xs text-muted-foreground">{a.agent_id}</p>
                      </div>
                    </div>
                    {isSelf ? (
                      <span className="text-xs text-muted-foreground">that's you</span>
                    ) : alreadyFriend ? (
                      <span className="text-xs text-muted-foreground">already friend</span>
                    ) : (
                      <button
                        onClick={() => sendRequest(a.agent_id)}
                        className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium bg-blue-50 text-blue-700 hover:bg-blue-100 transition-colors"
                      >
                        <UserPlus className="h-3 w-3" />
                        Add
                      </button>
                    )}
                  </div>
                  )
                })}
                {market.length === 0 && (
                  <p className="text-sm text-muted-foreground text-center py-8">Search the market to discover agents</p>
                )}
              </div>
            </div>
          </div>

          {/* Right column: incoming + friends */}
          <div className="space-y-6">
            {/* Incoming requests */}
            {incoming.length > 0 && (
              <div className="rounded-xl border border-amber-200 bg-amber-50/50 shadow-sm overflow-hidden">
                <div className="px-4 py-3 border-b border-amber-200 flex items-center gap-2">
                  <Bell className="h-4 w-4 text-amber-600" />
                  <h3 className="text-sm font-semibold text-amber-800">Pending ({incoming.length})</h3>
                </div>
                <div className="p-3 space-y-2">
                  {incoming.map((f) => (
                    <div key={f.id} className="flex items-center justify-between p-2.5 rounded-lg bg-white border border-amber-100">
                      <div className="min-w-0 flex-1 mr-2">
                        <p className="text-sm text-foreground truncate font-mono">{f.from_agent_id}</p>
                        <p className="text-xs text-muted-foreground">→ <span className="font-mono">{f.to_my_agent}</span></p>
                      </div>
                      <button
                        onClick={() => acceptRequest(f)}
                        className="flex items-center gap-1 px-2.5 py-1 rounded-md text-xs font-medium bg-emerald-50 text-emerald-700 hover:bg-emerald-100 transition-colors flex-shrink-0"
                      >
                        <Check className="h-3 w-3" />
                        Accept
                      </button>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* My friends */}
            <div className="rounded-xl border border-border bg-white shadow-sm overflow-hidden">
              <div className="px-4 py-3 border-b border-border flex items-center gap-2">
                <Users className="h-4 w-4 text-muted-foreground" />
                <h3 className="text-sm font-semibold text-foreground">My Friends</h3>
              </div>
              <div className="p-3">
                {friends.length === 0 ? (
                  <p className="text-sm text-muted-foreground text-center py-6">No friends yet</p>
                ) : (
                  <div className="space-y-1.5">
                    {friends.map((f) => {
                      const peer = f.from_agent_id === actingAs ? f.to_agent_id : f.from_agent_id
                      return (
                        <div key={f.id} className="flex items-center gap-2.5 p-2.5 rounded-lg hover:bg-gray-50 transition-colors">
                          <div className="w-7 h-7 rounded-full bg-emerald-50 flex items-center justify-center">
                            <Users className="h-3.5 w-3.5 text-emerald-600" />
                          </div>
                          <span className="text-sm text-foreground">{peer}</span>
                          <span className="badge badge-success ml-auto">Connected</span>
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
