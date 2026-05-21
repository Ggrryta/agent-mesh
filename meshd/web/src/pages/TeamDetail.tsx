import { useEffect, useState, useRef } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, Send, UserPlus, Sparkles, Activity, Crown } from 'lucide-react'
import api from '../api/client'
import AgentAvatar from '../components/AgentAvatar'

interface Skill {
  skill_id: string
  name: string
  description?: string
}

interface RosterMember {
  agent_id: string
  name: string
  description?: string
  role: string
  status: string
  skills?: Skill[]
}

export default function TeamDetail() {
  const { groupId } = useParams()
  const navigate = useNavigate()
  const [roster, setRoster] = useState<RosterMember[]>([])
  const [messages, setMessages] = useState<any[]>([])
  const [tasks, setTasks] = useState<any[]>([])
  const [commandModal, setCommandModal] = useState<RosterMember | null>(null)
  const [commandText, setCommandText] = useState('')
  const [sending, setSending] = useState(false)
  const [showAddMember, setShowAddMember] = useState(false)
  const [newMember, setNewMember] = useState('')
  const [myAgents, setMyAgents] = useState<{agent_id: string, name: string}[]>([])
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const messagesEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (groupId) {
      loadRoster()
      // 群组的 context_id 是 ctx-{group_id}
      const ctxId = `ctx-${groupId}`
      loadGroupMessages(ctxId)
      // 轮询刷新（30s fallback，WebSocket 正常时靠实时推送）
      pollRef.current = setInterval(() => loadGroupMessages(ctxId), 15000)
    }
    loadMyAgents()
    return () => {
      if (pollRef.current) clearInterval(pollRef.current)
      if (wsRef.current) wsRef.current.close()
    }
  }, [groupId])

  const loadMyAgents = async () => {
    try {
      const res = await api.get('/users/me/agents')
      const agents = (res.data.agents || []).filter((a: any) => a.kind !== 'virtual-user')
      setMyAgents(agents)
    } catch {}
  }

  /** 加载群组 context 下所有 task 的消息，合并成时间线 */
  const loadGroupMessages = async (ctxId: string) => {
    try {
      // 拉 context 下所有 task
      const tasksRes = await api.get(`/tasks?context_id=${encodeURIComponent(ctxId)}`)
      const taskList = tasksRes.data.tasks || []
      setTasks(taskList)

      // 拉每个 task 的 history（并行）
      const allMsgs: any[] = []
      await Promise.all(taskList.map(async (t: any) => {
        try {
          const res = await api.get(`/tasks/${t.task_id}?include=history`)
          const hist = res.data.history || []
          for (const msg of hist) {
            allMsgs.push({ ...msg, _task_id: t.task_id, _from: t.from, _to: t.to })
          }
        } catch {}
      }))

      // 按时间排序
      allMsgs.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
      setMessages(allMsgs)
    } catch {}
  }

  const loadRoster = async () => {
    try {
      const res = await api.get(`/groups/${groupId}/roster`)
      setRoster(res.data.roster || [])
    } catch {}
  }

  const addMember = async () => {
    if (!newMember) return
    try {
      await api.post(`/groups/${groupId}/members`, { agent_id: newMember })
      setNewMember('')
      setShowAddMember(false)
      loadRoster()
    } catch {}
  }

  const openCommand = (member: RosterMember) => {
    setCommandModal(member)
    setCommandText('')
  }

  const sendCommand = async () => {
    if (!commandModal || !commandText.trim()) return
    setSending(true)
    const msgId = `msg-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
    try {
      const ctxId = `ctx-${groupId}`
      await api.post('/tasks', {
        to_agent_id: commandModal.agent_id,
        context_id: ctxId,
        message: { message_id: msgId, parts: [{ kind: 'text', text: commandText }] },
      })
      setCommandModal(null)
      setCommandText('')
      // 刷新消息
      setTimeout(() => loadGroupMessages(ctxId), 1000)
    } catch {}
    setSending(false)
  }

  return (
    <div className="min-h-screen bg-gray-50/50">
      {/* Top bar */}
      <div className="px-8 py-4 bg-white border-b border-border flex items-center gap-3 sticky top-0 z-10">
        <button onClick={() => navigate('/groups')} className="p-2 rounded-lg hover:bg-gray-50 transition-colors">
          <ArrowLeft className="h-4 w-4 text-gray-500" />
        </button>
        <div className="flex items-center gap-2.5">
          <div className="w-9 h-9 rounded-xl bg-gradient-to-br from-violet-500 to-purple-600 flex items-center justify-center">
            <Sparkles className="h-4 w-4 text-white" />
          </div>
          <div>
            <h2 className="text-base font-bold text-foreground">{groupId}</h2>
            <p className="text-xs text-muted-foreground">{roster.length} agents collaborating</p>
          </div>
        </div>
      </div>

      <div className="flex h-[calc(100vh-73px)]">
        {/* Left panel: Team roster */}
        <div className="w-80 bg-white border-r border-border overflow-y-auto">
          <div className="p-4 border-b border-border flex items-center justify-between">
            <h3 className="text-sm font-semibold text-foreground">Team Members</h3>
            <button
              onClick={() => setShowAddMember(!showAddMember)}
              className="p-1.5 rounded-md hover:bg-gray-100 transition-colors"
            >
              <UserPlus className="h-3.5 w-3.5 text-gray-500" />
            </button>
          </div>

          {showAddMember && (
            <div className="p-3 border-b border-border bg-gray-50/50 flex gap-2">
              <select
                value={newMember}
                onChange={(e) => setNewMember(e.target.value)}
                className="flex-1 px-2.5 py-1.5 rounded-md border border-input bg-white text-xs focus:outline-none focus:ring-1 focus:ring-ring"
              >
                <option value="">Select agent...</option>
                {myAgents
                  .filter(a => !roster.some(r => r.agent_id === a.agent_id))
                  .map(a => (
                    <option key={a.agent_id} value={a.agent_id}>
                      {a.name || a.agent_id}
                    </option>
                  ))
                }
              </select>
              <button onClick={addMember} disabled={!newMember} className="px-3 py-1.5 rounded-md bg-violet-600 text-white text-xs font-medium hover:bg-violet-700 transition-colors disabled:opacity-50">
                Add
              </button>
            </div>
          )}

          <div className="p-3 space-y-2">
            {roster.map(member => (
              <MemberCard key={member.agent_id} member={member} onCommand={() => openCommand(member)} />
            ))}
            {roster.length === 0 && (
              <p className="text-xs text-muted-foreground text-center py-8">No members yet. Add agents to build your team.</p>
            )}
          </div>
        </div>

        {/* Right panel: Group conversation */}
        <div className="flex-1 flex flex-col overflow-hidden">
          {/* Header */}
          <div className="px-6 py-3 border-b border-border bg-white flex items-center justify-between">
            <div>
              <h3 className="text-sm font-semibold text-foreground">Group Conversation</h3>
              <p className="text-xs text-muted-foreground">{messages.length} messages across {tasks.length} task(s)</p>
            </div>
            <div className="flex items-center gap-2 px-3 py-1.5 rounded-full bg-white border border-border">
              <div className={`w-2 h-2 rounded-full ${messages.length > 0 ? 'bg-emerald-500 animate-pulse' : 'bg-gray-300'}`} />
              <span className="text-xs font-medium text-muted-foreground">
                {messages.length > 0 ? 'Active' : 'No activity'}
              </span>
            </div>
          </div>

          {/* Messages */}
          <div className="flex-1 overflow-y-auto p-5 space-y-3">
            {messages.length === 0 ? (
              <div className="flex flex-col items-center justify-center h-full text-center">
                <Activity className="h-10 w-10 text-muted-foreground mb-3" />
                <p className="text-sm text-muted-foreground">No messages yet. Assign a task to start collaboration.</p>
              </div>
            ) : (
              messages.map((msg, i) => {
                const sender = msg.from_agent_id || (msg.role === 'user' ? msg._from : msg._to) || 'unknown'
                const isVirtualUser = sender.startsWith('virtual-user-')
                const text = msg.parts?.map((p: any) => p.text).filter(Boolean).join('\n') || ''
                const time = msg.created_at ? new Date(msg.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : ''

                return (
                  <div key={msg.message_id || i} className="flex gap-3">
                    <div className="w-8 h-8 rounded-lg bg-blue-50 flex items-center justify-center shrink-0 mt-0.5">
                      {isVirtualUser
                        ? <span className="text-xs">👤</span>
                        : <span className="text-xs">🤖</span>
                      }
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 mb-1">
                        <span className="text-xs font-semibold text-foreground">{sender}</span>
                        <span className="text-[10px] text-muted-foreground">{time}</span>
                        <span className="text-[10px] text-muted-foreground font-mono ml-auto">{msg._task_id?.slice(0, 10)}</span>
                      </div>
                      <div className="text-sm text-foreground bg-gray-50 rounded-lg px-3 py-2 whitespace-pre-wrap break-words">
                        {text || <span className="italic text-muted-foreground">(empty)</span>}
                      </div>
                    </div>
                  </div>
                )
              })
            )}
            <div ref={messagesEndRef} />
          </div>
        </div>
      </div>

      {/* Command modal */}
      {commandModal && (
        <div className="fixed inset-0 bg-black/40 flex items-center justify-center z-50" onClick={() => setCommandModal(null)}>
          <div className="bg-white rounded-xl p-6 shadow-xl max-w-md w-full mx-4" onClick={(e) => e.stopPropagation()}>
            <h3 className="text-lg font-semibold mb-1">Assign Task to {commandModal.name || commandModal.agent_id}</h3>
            <p className="text-sm text-muted-foreground mb-4">Describe what you want this agent to do.</p>
            <textarea
              value={commandText}
              onChange={(e) => setCommandText(e.target.value)}
              placeholder="e.g. Review the latest PR and summarize findings..."
              className="w-full border border-input rounded-lg px-3 py-2 text-sm min-h-[100px] focus:outline-none focus:ring-1 focus:ring-ring resize-none"
            />
            <div className="flex gap-3 justify-end mt-4">
              <button onClick={() => setCommandModal(null)} className="px-4 py-2 text-sm rounded-lg border border-border hover:bg-gray-50">
                Cancel
              </button>
              <button
                onClick={sendCommand}
                disabled={sending || !commandText.trim()}
                className="px-4 py-2 text-sm rounded-lg bg-violet-600 text-white hover:bg-violet-700 disabled:opacity-50 flex items-center gap-2"
              >
                {sending && <span className="animate-spin">⏳</span>}
                <Send className="h-3.5 w-3.5" />
                Send
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function MemberCard({ member, onCommand }: { member: RosterMember; onCommand: () => void }) {
  const [expanded, setExpanded] = useState(false)
  const visibleSkills = expanded ? (member.skills || []) : (member.skills || []).slice(0, 3)
  const hasMore = (member.skills || []).length > 3

  return (
    <div className="p-3 rounded-xl border border-border bg-white hover:border-violet-200 hover:shadow-sm transition-all">
      <div className="flex items-start gap-3">
        <AgentAvatar name={member.name || member.agent_id} status={member.status} showStatus />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5 mb-0.5">
            <p className="text-sm font-semibold text-foreground truncate">{member.name || member.agent_id}</p>
            {member.role === 'owner' && <Crown className="h-3 w-3 text-amber-500 shrink-0" />}
          </div>
          {member.description && (
            <p className="text-xs text-muted-foreground line-clamp-2 mb-2">{member.description}</p>
          )}
          {visibleSkills.length > 0 && (
            <div className="flex flex-wrap gap-1 mb-2">
              {visibleSkills.map(s => (
                <span key={s.skill_id} className="px-1.5 py-0.5 rounded bg-violet-50 text-violet-700 text-[10px] font-medium" title={s.description}>
                  {s.name}
                </span>
              ))}
              {hasMore && !expanded && (
                <button onClick={() => setExpanded(true)} className="text-[10px] text-muted-foreground hover:text-foreground">
                  +{(member.skills || []).length - 3}
                </button>
              )}
            </div>
          )}
        </div>
      </div>
      <button
        onClick={onCommand}
        className="w-full mt-2 flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-md bg-gray-900 text-white text-xs font-medium hover:bg-gray-800 transition-colors"
      >
        <Send className="h-3 w-3" />
        Assign Task
      </button>
    </div>
  )
}

