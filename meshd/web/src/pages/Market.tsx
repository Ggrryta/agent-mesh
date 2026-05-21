// Market.tsx —— 市场：浏览别人发布的 agent 模板，一键 fork 到自己名下。
//
// 列表展示：title / summary / tags / download count / publisher。
// 卡片右上角：
//   - 自己的发布 → "Yours" 标签 + 删除按钮
//   - 别人的：未 fork → "Add to my agents" 按钮
//             已 fork → "Already added" 灰色按钮
//
// 点 "Add" 弹小框：让用户输入 new_agent_id（默认从 source_agent_id 拼一个建议值）。

import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, Search, Tag, Download, Trash2, Sparkles, X } from 'lucide-react'
import api from '../api/client'

interface Publication {
  id: number
  publisher_uid: number
  source_agent_id: string
  title: string
  summary: string
  system_prompt_template?: string
  category?: string
  tags: string[]
  download_count: number
  created_at: string
}

interface Subscription {
  id: number
  publication_id: number
  forked_agent_id: string
  created_at: string
}

interface UserMe {
  uid: number
}

export default function Market() {
  const navigate = useNavigate()
  const [pubs, setPubs] = useState<Publication[]>([])
  const [me, setMe] = useState<UserMe | null>(null)
  const [subs, setSubs] = useState<Subscription[]>([])
  const [search, setSearch] = useState('')
  const [forking, setForking] = useState<Publication | null>(null)
  const [newAgentId, setNewAgentId] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => { void loadAll() }, [])

  const loadAll = async () => {
    try {
      const [meRes, pubRes, subRes] = await Promise.all([
        api.get<UserMe>('/users/me'),
        api.get<{ publications: Publication[] }>('/publications'),
        api.get<{ subscriptions: Subscription[] }>('/users/me/subscriptions'),
      ])
      setMe(meRes.data)
      setPubs(pubRes.data.publications || [])
      setSubs(subRes.data.subscriptions || [])
    } catch (e: any) {
      setErr(e.response?.data?.message || 'Failed to load market')
    }
  }

  const subscribed = useMemo(() => {
    const m = new Set<number>()
    for (const s of subs) m.add(s.publication_id)
    return m
  }, [subs])

  const filtered = useMemo(() => {
    if (!search.trim()) return pubs
    const q = search.toLowerCase()
    return pubs.filter(p =>
      p.title.toLowerCase().includes(q) ||
      p.summary.toLowerCase().includes(q) ||
      p.tags.some(t => t.toLowerCase().includes(q))
    )
  }, [pubs, search])

  const openFork = (p: Publication) => {
    setNewAgentId(`${p.source_agent_id}-${(me?.uid ?? '').toString().slice(-3)}`)
    setForking(p)
    setErr('')
  }

  const doFork = async () => {
    if (!forking || !newAgentId.trim()) return
    setBusy(true)
    setErr('')
    try {
      await api.post(`/publications/${forking.id}/fork`, {
        new_agent_id: newAgentId.trim(),
        new_agent_name: forking.title,
      })
      setForking(null)
      await loadAll()
    } catch (e: any) {
      setErr(e.response?.data?.message || 'Fork failed')
    } finally {
      setBusy(false)
    }
  }

  const onDelete = async (p: Publication) => {
    if (!confirm(`Delete "${p.title}" from market?`)) return
    try {
      await api.delete(`/publications/${p.id}`)
      await loadAll()
    } catch (e: any) {
      setErr(e.response?.data?.message || 'Delete failed')
    }
  }

  return (
    <div className="min-h-screen bg-gray-50/50 p-8">
      <div className="max-w-6xl mx-auto">
        <div className="flex items-center gap-3 mb-6">
          <button onClick={() => navigate('/')} className="p-2 rounded-md hover:bg-gray-100">
            <ArrowLeft className="h-4 w-4 text-gray-500" />
          </button>
          <div>
            <h1 className="text-2xl font-bold text-foreground">Market</h1>
            <p className="text-sm text-muted-foreground">Browse and add agents shared by others</p>
          </div>
        </div>

        {/* search bar */}
        <div className="mb-6 flex items-center gap-3">
          <div className="flex-1 relative">
            <Search className="h-4 w-4 absolute left-3 top-3 text-gray-400" />
            <input
              className="w-full pl-9 pr-3 py-2 rounded-md border border-input bg-white text-sm focus:outline-none focus:ring-2 focus:ring-ring"
              placeholder="Search title / summary / tags"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
        </div>

        {err && (
          <div className="mb-4 p-3 rounded-md bg-destructive/10 text-destructive text-sm">{err}</div>
        )}

        {/* grid */}
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {filtered.map((p) => {
            const mine = me?.uid === p.publisher_uid
            const added = subscribed.has(p.id)
            return (
              <div key={p.id} className="rounded-xl border border-border bg-white p-5 shadow-sm flex flex-col">
                <div className="flex items-start justify-between gap-2 mb-2">
                  <div className="w-10 h-10 rounded-lg bg-purple-50 flex items-center justify-center">
                    <Sparkles className="h-5 w-5 text-purple-600" />
                  </div>
                  {mine && (
                    <span className="text-xs px-2 py-0.5 rounded-full bg-blue-50 text-blue-700">Yours</span>
                  )}
                </div>
                <h3 className="text-sm font-semibold text-foreground mb-1">{p.title}</h3>
                {p.summary && (
                  <p className="text-xs text-muted-foreground mb-3 line-clamp-3">{p.summary}</p>
                )}
                {p.tags.length > 0 && (
                  <div className="flex flex-wrap gap-1 mb-3">
                    {p.tags.map((t) => (
                      <span key={t} className="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded-full bg-gray-100 text-gray-700">
                        <Tag className="h-3 w-3" />
                        {t}
                      </span>
                    ))}
                  </div>
                )}
                <div className="flex items-center gap-3 text-xs text-muted-foreground mb-3">
                  <span className="inline-flex items-center gap-1"><Download className="h-3 w-3" />{p.download_count}</span>
                  {p.category && <span>· {p.category}</span>}
                </div>
                <div className="mt-auto flex items-center gap-2">
                  {mine ? (
                    <button
                      onClick={() => onDelete(p)}
                      className="flex-1 inline-flex items-center justify-center gap-1.5 px-3 py-2 rounded-md text-xs font-medium text-red-700 bg-red-50 hover:bg-red-100"
                    >
                      <Trash2 className="h-3 w-3" />
                      Delete
                    </button>
                  ) : added ? (
                    <button
                      disabled
                      className="flex-1 px-3 py-2 rounded-md text-xs font-medium text-gray-500 bg-gray-100 cursor-not-allowed"
                    >
                      Already added
                    </button>
                  ) : (
                    <button
                      onClick={() => openFork(p)}
                      className="flex-1 inline-flex items-center justify-center gap-1.5 px-3 py-2 rounded-md text-xs font-medium text-white bg-blue-600 hover:bg-blue-700"
                    >
                      <Sparkles className="h-3 w-3" />
                      Add to my agents
                    </button>
                  )}
                </div>
              </div>
            )
          })}
          {filtered.length === 0 && (
            <div className="col-span-full rounded-xl border border-dashed border-border bg-white p-12 text-center text-sm text-muted-foreground">
              {pubs.length === 0 ? 'No publications yet. Be the first one!' : 'No match for the search.'}
            </div>
          )}
        </div>
      </div>

      {/* fork dialog */}
      {forking && (
        <>
          <div className="fixed inset-0 bg-black/30 z-40" onClick={() => !busy && setForking(null)} />
          <div className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-full max-w-md bg-white shadow-2xl z-50 rounded-xl">
            <div className="px-5 py-4 border-b border-border flex items-center justify-between">
              <h3 className="text-base font-bold text-foreground">Add "{forking.title}"</h3>
              <button onClick={() => !busy && setForking(null)} className="p-1.5 rounded-md hover:bg-gray-100">
                <X className="h-4 w-4 text-gray-500" />
              </button>
            </div>
            <div className="px-5 py-4 space-y-3">
              <p className="text-sm text-muted-foreground">
                A new agent will be created in your account with the same system prompt template.
              </p>
              <label className="block">
                <span className="block text-sm font-medium text-foreground mb-1">New agent ID</span>
                <input
                  className="w-full px-3 py-2 rounded-md border border-input bg-background text-sm font-mono focus:outline-none focus:ring-2 focus:ring-ring"
                  value={newAgentId}
                  onChange={(e) => setNewAgentId(e.target.value)}
                  placeholder="my-agent"
                />
              </label>
              {err && <div className="p-2 rounded-md bg-destructive/10 text-destructive text-xs">{err}</div>}
            </div>
            <div className="px-5 py-3 border-t border-border flex justify-end gap-2">
              <button onClick={() => setForking(null)} disabled={busy} className="px-3 py-1.5 rounded-md text-sm text-gray-600 hover:bg-gray-100">Cancel</button>
              <button
                onClick={doFork}
                disabled={busy || !newAgentId.trim()}
                className="px-3 py-1.5 rounded-md text-sm text-white bg-blue-600 hover:bg-blue-700 disabled:opacity-60"
              >
                {busy ? 'Adding...' : 'Add'}
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
