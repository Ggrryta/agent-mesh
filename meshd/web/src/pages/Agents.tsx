import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Bot, Plus, Power, ArrowLeft, X, Save, Edit3, Code2, Zap, Sparkles, FolderOpen } from 'lucide-react'
import FolderPicker from '../components/FolderPicker'
import api, { meshdApi, currentUsername } from '../api/client'

interface Agent {
  agent_id: string
  name: string
  description?: string
  url?: string
  version?: string
  status: string
  kind: string
  system_prompt?: string
  workspace_path?: string
  agent_card?: any
}

interface Skill {
  skill_id?: string
  name: string
  description?: string
  tags?: string[]
  input_modes?: string[]
  output_modes?: string[]
}

/**
 * meshd 视角下的实例状态。从 GET /api/instances 来的。
 * - bound: 已绑定（state.json 有记录）
 * - running: worker 正在跑
 */
interface MeshdInstance {
  agent_id: string
  bound: boolean
  running: boolean
  auto_start: boolean
  started_at?: number
  uptime_ms?: number
}

type Tab = 'basic' | 'persona' | 'card' | 'skills'

export default function Agents() {
  const [agents, setAgents] = useState<Agent[]>([])
  const [instances, setInstances] = useState<Map<string, MeshdInstance>>(new Map())
  const [toggling, setToggling] = useState<Set<string>>(new Set())
  const [showCreate, setShowCreate] = useState(false)
  const [editing, setEditing] = useState<Agent | null>(null)
  const [tab, setTab] = useState<Tab>('basic')
  const [newAgentId, setNewAgentId] = useState('')
  const [newName, setNewName] = useState('')
  const navigate = useNavigate()

  // editor state
  const [form, setForm] = useState<Agent | null>(null)
  const [cardText, setCardText] = useState('')
  const [skills, setSkills] = useState<Skill[]>([])
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState('')
  const [folderPickerOpen, setFolderPickerOpen] = useState(false)

  useEffect(() => {
    loadAgents()
    loadInstances()
    // 实例状态有动态变化（worker 可能挂掉/启动），10s 轻量轮询
    const id = setInterval(loadInstances, 10000)
    return () => clearInterval(id)
  }, [])

  const loadAgents = async () => {
    const res = await api.get('/users/me/agents')
    setAgents(res.data.agents || [])
  }

  const loadInstances = async () => {
    try {
      const res = await meshdApi.get<{ instances: MeshdInstance[] }>('/instances')
      const m = new Map<string, MeshdInstance>()
      for (const inst of res.data.instances) m.set(inst.agent_id, inst)
      setInstances(m)
    } catch {
      // 单次失败忽略，下个轮询周期再试
    }
  }

  const toggleInstance = async (agentID: string, run: boolean) => {
    if (toggling.has(agentID)) return
    setToggling((s) => new Set(s).add(agentID))
    setErr('')
    try {
      if (run) {
        // 先看 meshd 这边是不是已绑定（即 keychain 里有 api_key）
        const inst = instances.get(agentID)
        const alreadyBound = !!inst?.bound

        let body: { api_key?: string } = {}
        if (!alreadyBound) {
          // 首次启动：浏览器先调 Gateway 签一把 raw_key（用户身份），
          // 再把它传给本机 meshd 启动 worker（meshd 会写 keychain）
          const keyRes = await api.post<{ raw_key: string }>('/users/me/api-keys', {
            label: `meshd:${agentID}`,
          })
          body = { api_key: keyRes.data.raw_key }
        }
        await meshdApi.post(`/instances/${agentID}/start`, body)
      } else {
        // 停止：保留 keychain key，下次再开 toggle 直接复用
        await meshdApi.post(`/instances/${agentID}/stop`, {})
      }
      await loadInstances()
    } catch (e: any) {
      const msg =
        e?.response?.data?.error ?? e?.response?.data?.message ?? `Failed to ${run ? 'start' : 'stop'} ${agentID}`
      setErr(msg)
    } finally {
      setToggling((s) => {
        const next = new Set(s)
        next.delete(agentID)
        return next
      })
    }
  }

  // ─── publish to market ───
  const [publishing, setPublishing] = useState<Agent | null>(null)
  const [pubTitle, setPubTitle] = useState('')
  const [pubSummary, setPubSummary] = useState('')
  const [pubTags, setPubTags] = useState('')
  const [pubBusy, setPubBusy] = useState(false)

  const openPublish = (a: Agent) => {
    setPublishing(a)
    setPubTitle(a.name)
    setPubSummary(a.description ?? '')
    setPubTags('')
    setErr('')
  }

  const closePublish = () => {
    if (pubBusy) return
    setPublishing(null)
  }

  const doPublish = async () => {
    if (!publishing || !pubTitle.trim()) return
    setPubBusy(true)
    setErr('')
    try {
      const tags = pubTags.split(',').map((t) => t.trim()).filter(Boolean)
      await api.post('/publications', {
        source_agent_id: publishing.agent_id,
        title: pubTitle.trim(),
        summary: pubSummary.trim(),
        tags,
      })
      setPublishing(null)
    } catch (e: any) {
      setErr(e.response?.data?.message || 'Failed to publish')
    } finally {
      setPubBusy(false)
    }
  }

  const createAgent = async () => {
    if (!newAgentId || !newName) return
    setErr('')
    try {
      await api.post('/users/me/agents', { agent_id: newAgentId, name: newName })
      setNewAgentId('')
      setNewName('')
      setShowCreate(false)
      await loadAgents()
    } catch (e: any) {
      const status = e?.response?.status
      const msg = e?.response?.data?.message ?? e?.message ?? 'Failed to create agent'
      if (status === 403 || status === 409) {
        // 这个 agent_id 已经被别人占了
        setErr(`agent_id "${newAgentId}" is taken — try a different name`)
      } else {
        setErr(msg)
      }
    }
  }

  const openEdit = async (a: Agent) => {
    setErr('')
    setTab('basic')
    try {
      const [agentRes, skillsRes] = await Promise.all([
        api.get(`/agents/${a.agent_id}`),
        api.get(`/agents/${a.agent_id}/skills`),
      ])
      const full = agentRes.data
      setEditing(full)
      setForm(full)
      setCardText(full.agent_card ? JSON.stringify(full.agent_card, null, 2) : '{}')
      setSkills(skillsRes.data.skills || [])
    } catch (e: any) {
      setErr(e.response?.data?.message || 'Failed to load')
    }
  }

  const closeEdit = () => {
    setEditing(null)
    setForm(null)
    setErr('')
  }

  const save = async () => {
    if (!form || !editing) return
    setSaving(true)
    setErr('')
    try {
      let cardObj: any = undefined
      if (cardText.trim() && cardText.trim() !== '{}') {
        try { cardObj = JSON.parse(cardText) } catch {
          setErr('AgentCard must be valid JSON')
          setSaving(false)
          return
        }
      }
      await api.post('/users/me/agents', {
        agent_id: editing.agent_id,
        name: form.name,
        description: form.description || '',
        url: form.url || '',
        version: form.version || '',
        system_prompt: form.system_prompt || '',
        workspace_path: form.workspace_path || '',
        agent_card: cardObj,
        skills: skills.length > 0 ? skills : undefined,
      })
      await loadAgents()
      closeEdit()
    } catch (e: any) {
      setErr(e.response?.data?.message || 'Save failed')
    }
    setSaving(false)
  }

  const drainAgent = async (id: string) => {
    await api.post(`/agents/${id}/drain`)
    loadAgents()
  }

  const addSkill = () => setSkills([...skills, { name: '', description: '' }])
  const removeSkill = (i: number) => setSkills(skills.filter((_, idx) => idx !== i))
  const updateSkill = (i: number, key: keyof Skill, value: any) => {
    const next = [...skills]; (next[i] as any)[key] = value; setSkills(next)
  }

  const statusBadge = (status: string) => {
    switch (status) {
      case 'active': return <span className="badge badge-success">Active</span>
      case 'draining': return <span className="badge badge-warning">Draining</span>
      case 'inactive': return <span className="badge badge-neutral">Inactive</span>
      default: return <span className="badge badge-neutral">{status}</span>
    }
  }

  const formatUptime = (ms: number): string => {
    if (ms < 60_000) return `${Math.floor(ms / 1000)}s`
    if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m`
    if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h`
    return `${Math.floor(ms / 86_400_000)}d`
  }

  return (
    <div className="min-h-screen bg-gray-50/50 p-8">
      <div className="max-w-5xl mx-auto">
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div className="flex items-center gap-3">
            <button onClick={() => navigate('/')} className="p-2 rounded-lg hover:bg-white border border-transparent hover:border-border transition-all">
              <ArrowLeft className="h-4 w-4 text-gray-500" />
            </button>
            <div>
              <h2 className="text-xl font-bold text-foreground">Agents</h2>
              <p className="text-sm text-muted-foreground">Manage your AI agents</p>
            </div>
          </div>
          <button
            onClick={() => setShowCreate(!showCreate)}
            className="flex items-center gap-2 px-4 py-2.5 rounded-lg bg-gradient-to-r from-blue-600 to-indigo-600 text-white text-sm font-medium shadow-sm shadow-blue-200 hover:shadow-md transition-all"
          >
            <Plus className="h-4 w-4" />
            New Agent
          </button>
        </div>

        {/* Create form */}
        {showCreate && (
          <div className="mb-6 p-5 rounded-xl border border-border bg-white shadow-sm">
            <h3 className="text-sm font-semibold text-foreground mb-3">Create Agent</h3>
            <p className="text-xs text-muted-foreground mb-3">Use the Edit panel to fill in AgentCard details and skills after creation.</p>
            <div className="flex gap-3">
              <input
                placeholder="Agent slug (e.g. bot)"
                value={newAgentId}
                onChange={(e) => setNewAgentId(e.target.value.toLowerCase())}
                className="w-60 px-3 py-2 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring font-mono"
              />
              <input
                placeholder="Display name"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && !e.nativeEvent.isComposing && createAgent()}
                className="flex-1 px-3 py-2 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
              />
              <button onClick={createAgent} className="px-4 py-2 rounded-lg bg-foreground text-background text-sm font-medium hover:bg-foreground/90 transition-colors">
                Create
              </button>
            </div>
            {newAgentId && currentUsername() && (
              <p className="mt-2 text-xs text-muted-foreground">
                Will be saved as: <span className="font-mono text-foreground">{newAgentId}@{currentUsername()}</span>
              </p>
            )}
            {err && (
              <div className="mt-3 p-2.5 rounded-md bg-destructive/10 text-destructive text-xs">{err}</div>
            )}
          </div>
        )}

        {/* Agent list */}
        <div className="rounded-xl border border-border bg-white shadow-sm overflow-hidden">
          <table className="w-full">
            <thead>
              <tr className="border-b border-border bg-gray-50/80">
                <th className="text-left px-5 py-3 text-xs font-semibold text-muted-foreground uppercase tracking-wider">Agent</th>
                <th className="text-left px-5 py-3 text-xs font-semibold text-muted-foreground uppercase tracking-wider">Status</th>
                <th className="text-left px-5 py-3 text-xs font-semibold text-muted-foreground uppercase tracking-wider">Kind</th>
                <th className="text-left px-5 py-3 text-xs font-semibold text-muted-foreground uppercase tracking-wider">Run on this device</th>
                <th className="text-right px-5 py-3 text-xs font-semibold text-muted-foreground uppercase tracking-wider">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {agents.map((a) => {
                const inst = instances.get(a.agent_id)
                const running = !!inst?.running
                const busy = toggling.has(a.agent_id)
                const isVirtualUser = a.kind === 'virtual-user'
                return (
                <tr key={a.agent_id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-5 py-4 cursor-pointer" onClick={() => openEdit(a)}>
                    <div className="flex items-center gap-3">
                      <div className="w-9 h-9 rounded-lg bg-blue-50 flex items-center justify-center">
                        <Bot className="h-4 w-4 text-blue-600" />
                      </div>
                      <div>
                        <p className="text-sm font-medium text-foreground">{a.name}</p>
                        <p className="text-xs text-muted-foreground font-mono">{a.agent_id}</p>
                      </div>
                    </div>
                  </td>
                  <td className="px-5 py-4">{statusBadge(a.status)}</td>
                  <td className="px-5 py-4"><span className="text-sm text-muted-foreground">{a.kind}</span></td>
                  <td className="px-5 py-4">
                    {isVirtualUser ? (
                      <span className="text-xs text-muted-foreground">—</span>
                    ) : (
                      <button
                        type="button"
                        onClick={() => toggleInstance(a.agent_id, !running)}
                        disabled={busy}
                        className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors disabled:opacity-50 ${
                          running ? 'bg-emerald-500' : 'bg-gray-300'
                        }`}
                        title={running ? 'Stop running on this device' : 'Start running on this device'}
                      >
                        <span
                          className={`inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform ${
                            running ? 'translate-x-6' : 'translate-x-1'
                          }`}
                        />
                      </button>
                    )}
                    {running && inst?.uptime_ms != null && (
                      <span className="ml-3 text-xs text-muted-foreground">{formatUptime(inst.uptime_ms)}</span>
                    )}
                  </td>
                  <td className="px-5 py-4 text-right">
                    <div className="inline-flex gap-2">
                      <button
                        onClick={() => openEdit(a)}
                        className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-blue-700 bg-blue-50 hover:bg-blue-100 transition-colors"
                      >
                        <Edit3 className="h-3 w-3" />
                        Edit
                      </button>
                      {!isVirtualUser && (
                        <button
                          onClick={() => openPublish(a)}
                          className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-purple-700 bg-purple-50 hover:bg-purple-100 transition-colors"
                          title="Publish to Market"
                        >
                          <Sparkles className="h-3 w-3" />
                          Publish
                        </button>
                      )}
                      {a.status === 'active' && a.kind !== 'virtual-user' && (
                        <button
                          onClick={() => drainAgent(a.agent_id)}
                          className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-amber-700 bg-amber-50 hover:bg-amber-100 transition-colors"
                        >
                          <Power className="h-3 w-3" />
                          Drain
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
                )
              })}
              {agents.length === 0 && (
                <tr><td colSpan={5} className="px-5 py-12 text-center text-sm text-muted-foreground">No agents yet. Create your first agent to get started.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* Edit drawer */}
      {editing && form && (
        <>
          <div className="fixed inset-0 bg-black/30 z-40" onClick={closeEdit} />
          <div className="fixed right-0 top-0 bottom-0 w-full max-w-xl bg-white shadow-2xl z-50 flex flex-col">
            {/* Header */}
            <div className="px-6 py-4 border-b border-border flex items-center justify-between">
              <div>
                <h3 className="text-base font-bold text-foreground">Edit Agent</h3>
                <p className="text-xs text-muted-foreground font-mono">{editing.agent_id}</p>
              </div>
              <button onClick={closeEdit} className="p-1.5 rounded-md hover:bg-gray-100 transition-colors">
                <X className="h-4 w-4 text-gray-500" />
              </button>
            </div>

            {/* Tabs */}
            <div className="border-b border-border flex px-6">
              {([
                { id: 'basic', label: 'Basic Info', icon: Bot, advanced: false },
                { id: 'persona', label: 'Persona', icon: Sparkles, advanced: false },
                { id: 'skills', label: 'Skills', icon: Zap, advanced: false },
                { id: 'card', label: 'AgentCard', icon: Code2, advanced: true },
              ] as const).map(t => {
                const Icon = t.icon
                const active = tab === t.id
                return (
                  <button
                    key={t.id}
                    onClick={() => setTab(t.id)}
                    className={`flex items-center gap-2 px-4 py-3 text-sm font-medium border-b-2 transition-colors ${
                      active
                        ? 'border-blue-600 text-blue-600'
                        : 'border-transparent text-muted-foreground hover:text-foreground'
                    }`}
                  >
                    <Icon className="h-3.5 w-3.5" />
                    {t.label}
                    {t.advanced && (
                      <span className="ml-1 text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-amber-100 text-amber-800 font-semibold">Adv</span>
                    )}
                  </button>
                )
              })}
            </div>

            {/* Body */}
            <div className="flex-1 overflow-y-auto p-6">
              {tab === 'basic' && (
                <div className="space-y-4">
                  <div className="rounded-lg border border-blue-200 bg-blue-50/50 p-3 text-xs text-blue-900">
                    These fields are used inside Agent Mesh — for the agent list, market discovery,
                    and routing. <strong>Required.</strong>
                  </div>
                  <div>
                    <label className="block text-xs font-medium text-foreground mb-1">Name</label>
                    <input
                      value={form.name}
                      onChange={(e) => setForm({ ...form, name: e.target.value })}
                      className="w-full px-3 py-2 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                    />
                  </div>
                  <div>
                    <label className="block text-xs font-medium text-foreground mb-1">Description</label>
                    <textarea
                      value={form.description || ''}
                      onChange={(e) => setForm({ ...form, description: e.target.value })}
                      rows={3}
                      className="w-full px-3 py-2 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring resize-none"
                    />
                  </div>
                  <div>
                    <label className="block text-xs font-medium text-foreground mb-1">Push URL (optional)</label>
                    <input
                      value={form.url || ''}
                      placeholder="https://agent.example.com"
                      onChange={(e) => setForm({ ...form, url: e.target.value })}
                      className="w-full px-3 py-2 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                    />
                    <p className="text-xs text-muted-foreground mt-1">
                      Optional. Set only if your agent has a reachable HTTP endpoint and wants Gateway to push events.
                      Agents behind NAT / serverless / CLI can leave this blank — they'll pull inbox instead.
                    </p>
                  </div>
                  <div>
                    <label className="block text-xs font-medium text-foreground mb-1">Version</label>
                    <input
                      value={form.version || ''}
                      placeholder="1.0.0"
                      onChange={(e) => setForm({ ...form, version: e.target.value })}
                      className="w-full px-3 py-2 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                    />
                  </div>
                </div>
              )}

              {tab === 'persona' && form && (
                <div className="space-y-4">
                  <div>
                    <div className="flex items-center justify-between mb-1.5">
                      <label className="block text-xs font-medium text-foreground">System Prompt</label>
                      <span className={`text-xs ${(form.system_prompt || '').length > 8000 ? 'text-red-600' : 'text-muted-foreground'}`}>
                        {(form.system_prompt || '').length} / 8192
                      </span>
                    </div>
                    <textarea
                      value={form.system_prompt || ''}
                      onChange={(e) => setForm({ ...form, system_prompt: e.target.value })}
                      rows={14}
                      placeholder="You are a research assistant. Always cite your sources. Respond concisely..."
                      className="w-full px-3 py-2 rounded-lg border border-input bg-background text-sm font-mono focus:outline-none focus:ring-2 focus:ring-ring resize-none"
                    />
                  </div>
                  <div className="rounded-lg border border-amber-200 bg-amber-50/50 p-3 text-xs text-amber-900">
                    <p className="font-semibold mb-1">How this works</p>
                    <ul className="space-y-1 list-disc list-inside text-amber-800/90">
                      <li>This prompt becomes the LLM's <code className="px-1 py-0.5 bg-white rounded">system</code> message — defines the agent's persona / role.</li>
                      <li>Agent's GAS daemon fetches it on startup. Changes apply on next agent restart.</li>
                      <li>Keep it focused: 1-2 paragraphs is usually enough. Long prompts inflate context cost.</li>
                    </ul>
                  </div>

                  {/* Workspace Path */}
                  <div className="space-y-2">
                    <label className="text-sm font-medium text-foreground">工作目录</label>
                    <div className="flex gap-2">
                      <input
                        value={form.workspace_path || ''}
                        onChange={(e) => setForm({ ...form, workspace_path: e.target.value })}
                        placeholder="/Users/bilibili/projects/my-project"
                        className="flex-1 px-3 py-2 rounded-lg border border-input bg-background text-sm font-mono focus:outline-none focus:ring-2 focus:ring-ring"
                      />
                      <button
                        type="button"
                        onClick={() => setFolderPickerOpen(true)}
                        className="px-3 py-2 rounded-lg border border-input hover:bg-muted transition-colors"
                        title="浏览文件夹"
                      >
                        <FolderOpen size={16} />
                      </button>
                    </div>
                    <p className="text-xs text-muted-foreground">Agent 的文件系统工作目录。Agent 会在此目录下读写文件。留空则使用默认目录。</p>
                  </div>

                  <FolderPicker
                    open={folderPickerOpen}
                    initial={form.workspace_path || undefined}
                    onSelect={(path) => setForm({ ...form, workspace_path: path })}
                    onClose={() => setFolderPickerOpen(false)}
                  />
                </div>
              )}

              {tab === 'card' && (
                <div className="space-y-3">
                  <div className="rounded-lg border border-amber-200 bg-amber-50/60 p-3 text-xs text-amber-900">
                    <p className="font-semibold mb-1">Advanced — for A2A protocol interoperability</p>
                    <p className="mb-2">
                      Agent Mesh runs a Gateway-mediated network: agents don't need a public URL —
                      they communicate through Gateway. <strong>You can leave this empty.</strong>
                    </p>
                    <p>
                      Fill the AgentCard JSON only if you want to expose this agent to the broader A2A ecosystem
                      (e.g., publish it via <code className="px-1 py-0.5 bg-white rounded">/.well-known/agent-card.json</code> for
                      external A2A clients to discover and call directly).
                    </p>
                  </div>
                  <div>
                    <div className="flex items-center justify-between mb-1.5">
                      <label className="block text-xs font-medium text-foreground">AgentCard JSON</label>
                      <button
                        onClick={() => {
                          const synth = {
                            name: form.name,
                            description: form.description || '',
                            version: form.version || '1.0.0',
                            url: form.url || '',
                            capabilities: { streaming: false, pushNotifications: !!form.url },
                            defaultInputModes: ['text/plain', 'application/json'],
                            defaultOutputModes: ['text/plain', 'application/json'],
                            skills: skills.map(s => ({
                              id: s.skill_id || s.name?.toLowerCase().replace(/\s+/g, '-'),
                              name: s.name,
                              description: s.description || '',
                              tags: s.tags || [],
                              inputModes: s.input_modes || [],
                              outputModes: s.output_modes || [],
                            })),
                          }
                          setCardText(JSON.stringify(synth, null, 2))
                        }}
                        className="text-xs text-blue-600 hover:underline"
                      >
                        Generate from Basic Info + Skills
                      </button>
                    </div>
                    <textarea
                      value={cardText}
                      onChange={(e) => setCardText(e.target.value)}
                      rows={20}
                      spellCheck={false}
                      placeholder={'{\n  // leave empty unless you need A2A interoperability\n}'}
                      className="w-full px-3 py-2 rounded-lg border border-input bg-gray-50 text-xs font-mono focus:outline-none focus:ring-2 focus:ring-ring resize-none"
                    />
                    <p className="text-xs text-muted-foreground mt-1">
                      Schema reference: <a href="https://a2a-protocol.org/latest/specification/" target="_blank" rel="noreferrer" className="text-blue-600 hover:underline">A2A AgentCard spec</a>
                    </p>
                  </div>
                </div>
              )}

              {tab === 'skills' && (
                <div className="space-y-3">
                  <div className="flex items-center justify-between">
                    <p className="text-xs text-muted-foreground">Declare what this agent can do. Skills are replaced entirely on save.</p>
                    <button
                      onClick={addSkill}
                      className="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-blue-50 text-blue-700 text-xs font-medium hover:bg-blue-100 transition-colors"
                    >
                      <Plus className="h-3 w-3" />
                      Add Skill
                    </button>
                  </div>
                  {skills.map((s, i) => (
                    <div key={i} className="p-3 rounded-lg border border-border bg-gray-50/50 space-y-2">
                      <div className="flex gap-2">
                        <input
                          placeholder="Skill name"
                          value={s.name}
                          onChange={(e) => updateSkill(i, 'name', e.target.value)}
                          className="flex-1 px-3 py-1.5 rounded-md border border-input bg-white text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                        />
                        <button
                          onClick={() => removeSkill(i)}
                          className="p-1.5 rounded-md text-red-600 hover:bg-red-50 transition-colors"
                        >
                          <X className="h-3.5 w-3.5" />
                        </button>
                      </div>
                      <textarea
                        placeholder="Description"
                        value={s.description || ''}
                        onChange={(e) => updateSkill(i, 'description', e.target.value)}
                        rows={2}
                        className="w-full px-3 py-1.5 rounded-md border border-input bg-white text-xs focus:outline-none focus:ring-2 focus:ring-ring resize-none"
                      />
                    </div>
                  ))}
                  {skills.length === 0 && (
                    <div className="text-center py-8 text-sm text-muted-foreground border border-dashed border-border rounded-lg">
                      No skills declared
                    </div>
                  )}
                </div>
              )}
            </div>

            {/* Footer */}
            <div className="px-6 py-4 border-t border-border flex items-center justify-between">
              {err ? <p className="text-xs text-red-600">{err}</p> : <span />}
              <div className="flex gap-2">
                <button
                  onClick={closeEdit}
                  className="px-4 py-2 rounded-lg text-sm text-foreground hover:bg-gray-100 transition-colors"
                >
                  Cancel
                </button>
                <button
                  onClick={save}
                  disabled={saving}
                  className="flex items-center gap-2 px-4 py-2 rounded-lg bg-gradient-to-r from-blue-600 to-indigo-600 text-white text-sm font-medium shadow-sm shadow-blue-200 hover:shadow-md disabled:opacity-50 transition-all"
                >
                  <Save className="h-4 w-4" />
                  {saving ? 'Saving...' : 'Save Changes'}
                </button>
              </div>
            </div>
          </div>
        </>
      )}

      {/* publish dialog */}
      {publishing && (
        <>
          <div className="fixed inset-0 bg-black/30 z-40" onClick={closePublish} />
          <div className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-full max-w-md bg-white shadow-2xl z-50 rounded-xl">
            <div className="px-5 py-4 border-b border-border flex items-center justify-between">
              <div>
                <h3 className="text-base font-bold text-foreground">Publish to Market</h3>
                <p className="text-xs text-muted-foreground font-mono">{publishing.agent_id}</p>
              </div>
              <button onClick={closePublish} className="p-1.5 rounded-md hover:bg-gray-100">
                <X className="h-4 w-4 text-gray-500" />
              </button>
            </div>
            <div className="px-5 py-4 space-y-3">
              <p className="text-xs text-muted-foreground">
                A snapshot of the system prompt will be shared. Other users can fork it to create their own copy.
              </p>
              <label className="block">
                <span className="block text-sm font-medium text-foreground mb-1">Title</span>
                <input
                  className="w-full px-3 py-2 rounded-md border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                  value={pubTitle}
                  onChange={(e) => setPubTitle(e.target.value)}
                  maxLength={120}
                />
              </label>
              <label className="block">
                <span className="block text-sm font-medium text-foreground mb-1">Summary</span>
                <textarea
                  className="w-full px-3 py-2 rounded-md border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                  rows={3}
                  value={pubSummary}
                  onChange={(e) => setPubSummary(e.target.value)}
                  maxLength={500}
                />
              </label>
              <label className="block">
                <span className="block text-sm font-medium text-foreground mb-1">Tags <span className="text-muted-foreground font-normal">(comma separated)</span></span>
                <input
                  className="w-full px-3 py-2 rounded-md border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                  value={pubTags}
                  onChange={(e) => setPubTags(e.target.value)}
                  placeholder="general, writing"
                />
              </label>
              {err && <div className="p-2 rounded-md bg-destructive/10 text-destructive text-xs">{err}</div>}
            </div>
            <div className="px-5 py-3 border-t border-border flex justify-end gap-2">
              <button onClick={closePublish} disabled={pubBusy} className="px-3 py-1.5 rounded-md text-sm text-gray-600 hover:bg-gray-100">Cancel</button>
              <button
                onClick={doPublish}
                disabled={pubBusy || !pubTitle.trim()}
                className="px-3 py-1.5 rounded-md text-sm text-white bg-purple-600 hover:bg-purple-700 disabled:opacity-60"
              >
                {pubBusy ? 'Publishing...' : 'Publish'}
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
