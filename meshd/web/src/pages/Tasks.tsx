import { useEffect, useState, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, Send, MessageSquare, Clock, CheckCircle2, XCircle, Loader2, Bot, User, ChevronRight } from 'lucide-react'
import api, { currentToken } from '../api/client'
import { FeedSocket, type FeedEvent } from '../ws/feed'

interface TaskItem {
  task_id: string
  context_id: string
  from: string
  to: string
  status: string
  created_at?: string
}

interface MessageItem {
  message_id: string
  role: string
  parts: { kind: string; text?: string }[]
  created_at: string
}

export default function Tasks() {
  const [tasks, setTasks] = useState<TaskItem[]>([])
  const [agents, setAgents] = useState<{agent_id: string, name: string}[]>([])
  const [friends, setFriends] = useState<string[]>([])
  const [toAgent, setToAgent] = useState('')
  const [message, setMessage] = useState('')
  const [sending, setSending] = useState(false)
  const [selectedTask, setSelectedTask] = useState<TaskItem | null>(null)
  const [messages, setMessages] = useState<MessageItem[]>([])
  const [historyTruncated, setHistoryTruncated] = useState(false)
  const [replyText, setReplyText] = useState('')
  const isTerminal = selectedTask?.status === 'completed' || selectedTask?.status === 'failed' || selectedTask?.status === 'canceled' || selectedTask?.status === 'rejected'
  /** 顶层错误条：用户主动触发的操作失败时显示。轮询类后台请求失败不放进来。 */
  const [err, setErr] = useState('')

  // axios 错误格式化成用户能看的短文字
  function fmtErr(e: any, fallback: string): string {
    return (e && e.response && e.response.data && (e.response.data.message || e.response.data.error)) || (e && e.message) || fallback
  }
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const messagesScrollRef = useRef<HTMLDivElement>(null)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const feedRef = useRef<FeedSocket | null>(null)
  const navigate = useNavigate()

  useEffect(() => {
    loadAgents()
    loadRecentTasks()

    // WebSocket 实时推送：收到事件时增量更新，替代 3 秒轮询
    const gatewayUrl = api.defaults.baseURL?.replace('/v1/admin', '') || ''
    const token = currentToken()
    if (gatewayUrl && token) {
      const feed = new FeedSocket(gatewayUrl)
      feedRef.current = feed
      feed.onEvent((ev: FeedEvent) => {
        // 新 task 创建 → 刷新 task 列表
        if (ev.type === 'task_created') {
          loadRecentTasks()
          return
        }
        // task 状态变更 → 刷新 task 列表 + 当前 task 状态
        if (ev.type === 'task_transition') {
          loadRecentTasks()
          return
        }
        // 新消息 → 如果属于当前选中 task 的 context，追加到 messages
        if (ev.type === 'task_message' && ev.payload) {
          setSelectedTask((current) => {
            if (!current) return current
            // 匹配条件：task_id 直接匹配，或同 context（协作链路内的 sibling task）
            const isRelevant = ev.task_id === current.task_id ||
              (current.context_id && ev.payload?.context_id === current.context_id)
            if (isRelevant) {
              const msg = ev.payload as MessageItem
              if (msg.message_id) {
                setMessages((prev) => {
                  if (prev.some((m) => m.message_id === msg.message_id)) return prev
                  return [...prev, msg]
                })
              }
            }
            return current
          })
        }
      })
      feed.connect(token)
    }

    return () => {
      if (pollRef.current) clearInterval(pollRef.current)
      feedRef.current?.close()
    }
  }, [])

  // 自动滚到底部：用 IntersectionObserver 跟踪 messagesEndRef 是否在视口里。
  // 在视口 = 用户已经看到底部 → 新消息自动滚；不在视口 = 用户在审计历史 → 不动。
  // 比"算 distance"鲁棒：不依赖某个 DOM 容器是不是真在滚动（外层页面 body 滚也算）。
  const isAtBottomRef = useRef(true)
  useEffect(() => {
    const sentinel = messagesEndRef.current
    if (!sentinel) return
    const obs = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          isAtBottomRef.current = e.isIntersecting
          console.log('[Tasks] sentinel visibility:', e.isIntersecting)
        }
      },
      { threshold: 0 }, // 任何一像素可见就算"在底部"
    )
    obs.observe(sentinel)
    return () => obs.disconnect()
  }, [selectedTask?.task_id])

  useEffect(() => {
    console.log('[Tasks] messages changed, isAtBottom=', isAtBottomRef.current)
    if (isAtBottomRef.current) {
      messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [messages])

  const loadAgents = async () => {
    try {
      const res = await api.get('/users/me/agents')
      const myAgents = res.data.agents || []
      setAgents(myAgents)
      if (myAgents.length > 0) {
        const fRes = await api.get(`/agents/${myAgents[0].agent_id}/friends`)
        const friendList = (fRes.data.friends || []).map((f: any) =>
          f.from_agent_id === myAgents[0].agent_id ? f.to_agent_id : f.from_agent_id
        )
        setFriends(friendList)
      }
    } catch (e) {
      // 初次加载，失败时记 console，让用户能在页面看到 "no agents/friends" 也行
      console.warn('loadAgents failed', e)
    }
  }

  const loadRecentTasks = async () => {
    console.log('[Tasks] loadRecentTasks')
    try {
      const res = await api.get('/tasks?context_id=*')
      console.log('[Tasks] task list:', res.data.tasks?.length ?? 0, 'tasks', res.data.tasks)
      setTasks(res.data.tasks || [])
    } catch (e) {
      console.warn('loadRecentTasks failed', e)
    }
  }

  const submitTask = async () => {
    if (!toAgent || !message) return
    setSending(true)
    setErr('')
    const msgId = `msg-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
    try {
      const res = await api.post('/tasks', {
        to_agent_id: toAgent,
        message: { message_id: msgId, parts: [{ kind: 'text', text: message }] },
      })
      setMessage('')
      const newTask: TaskItem = {
        task_id: res.data.task_id,
        context_id: res.data.context_id || res.data.task_id,
        from: `virtual-user-${localStorage.getItem('uid')}`,
        to: toAgent,
        status: 'submitted',
      }
      setTasks(prev => [newTask, ...prev])
      selectTask(newTask)
    } catch (e: any) {
      setErr(fmtErr(e, 'Failed to submit task'))
    }
    setSending(false)
  }

  const selectTask = async (task: TaskItem) => {
    // 重要：先停掉旧 task 的 polling，避免它在 setMessages 之后竞态把消息覆盖回老 task
    if (pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
    setSelectedTask(task)
    setMessages([]) // 清掉上个 task 的消息，避免一闪而过的混合状态
    setHistoryTruncated(false)
    await loadMessages(task.task_id)
    // 切 task 后立即跳到底部一次（不平滑），不靠"近底部才滚"的条件
    requestAnimationFrame(() => {
      messagesEndRef.current?.scrollIntoView({ behavior: 'auto' })
    })
    pollRef.current = setInterval(() => loadMessages(task.task_id), 30000) // 30s fallback（WebSocket 正常时靠实时推送）
  }

  const loadMessages = async (taskId: string) => {
    console.log('[Tasks] loadMessages', taskId)
    try {
      // 先拉当前 task 的信息（含 context_id）
      const res = await api.get(`/tasks/${taskId}?include=history`)
      const contextId = res.data.context_id || taskId
      let allMessages: MessageItem[] = res.data.history || []
      const truncated = !!res.data.history_truncated

      // 拉同 context 下的其他 task 的 history（协作链路合并视图）
      // 比如你→alice 和 alice→bob 共享同一个 context
      try {
        const ctxRes = await api.get(`/tasks?context_id=${encodeURIComponent(contextId)}`)
        const siblingTasks: TaskItem[] = ctxRes.data.tasks || []
        for (const t of siblingTasks) {
          if (t.task_id === taskId) continue // 已经拉过
          try {
            const sibRes = await api.get(`/tasks/${t.task_id}?include=history`)
            const sibHist: MessageItem[] = sibRes.data.history || []
            allMessages = [...allMessages, ...sibHist]
          } catch {
            // 单个 sibling 失败不影响整体
          }
        }
      } catch {
        // context 查询失败，只显示当前 task 的消息
      }

      // 按时间排序（合并多个 task 的消息后需要重排）
      allMessages.sort((a, b) => {
        const ta = a.created_at ? new Date(a.created_at).getTime() : 0
        const tb = b.created_at ? new Date(b.created_at).getTime() : 0
        return ta - tb
      })

      console.log(`[Tasks] context ${contextId}: ${allMessages.length} messages total, truncated=${truncated}`)
      setMessages(allMessages)
      setHistoryTruncated(truncated)
      if (res.data.status && selectedTask) {
        setSelectedTask(prev => prev ? { ...prev, status: res.data.status } : prev)
      }
    } catch (e) {
      console.warn('loadMessages failed', e)
    }
  }

  const sendReply = async () => {
    if (!replyText || !selectedTask) return
    setErr('')
    const msgId = `msg-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
    try {
      await api.post(`/tasks/${selectedTask.task_id}/messages`, {
        message_id: msgId,
        parts: [{ kind: 'text', text: replyText }],
      })
      setReplyText('')
      loadMessages(selectedTask.task_id)
    } catch (e: any) {
      setErr(fmtErr(e, 'Failed to send message'))
    }
  }

  const [cleanupDays, setCleanupDays] = useState(3)
  const [showCleanupDialog, setShowCleanupDialog] = useState(false)

  const handleCleanup = () => {
    setShowCleanupDialog(true)
  }

  const doCleanup = async () => {
    try {
      const res = await api.delete(`/tasks/cleanup?before_days=${cleanupDays}`)
      const deleted = res.data?.deleted ?? 0
      alert(`Cleaned up ${deleted} task(s).`)
      setShowCleanupDialog(false)
      loadRecentTasks()
      if (selectedTask) {
        setSelectedTask(null)
        setMessages([])
      }
    } catch (e: any) {
      alert(`Cleanup failed: ${e?.response?.data?.message || e.message}`)
    }
  }

  const deleteTask = async (taskId: string) => {
    if (!confirm('Delete this task and all its messages?')) return
    try {
      await api.delete(`/tasks/${taskId}`)
      setTasks(prev => prev.filter(t => t.task_id !== taskId))
      if (selectedTask?.task_id === taskId) {
        setSelectedTask(null)
        setMessages([])
      }
    } catch (e: any) {
      alert(`Delete failed: ${e?.response?.data?.message || e.message}`)
    }
  }

  const statusIcon = (status: string) => {
    switch (status) {
      case 'completed': return <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
      case 'failed': return <XCircle className="h-3.5 w-3.5 text-red-500" />
      case 'working': return <Loader2 className="h-3.5 w-3.5 text-blue-500 animate-spin" />
      default: return <Clock className="h-3.5 w-3.5 text-amber-500" />
    }
  }

  const statusBadge = (status: string) => {
    switch (status) {
      case 'completed': return <span className="badge badge-success">Completed</span>
      case 'failed': return <span className="badge badge-danger">Failed</span>
      case 'working': return <span className="badge badge-info">Working</span>
      case 'submitted': return <span className="badge badge-warning">Submitted</span>
      default: return <span className="badge badge-neutral">{status}</span>
    }
  }

  const getMessageText = (msg: MessageItem) => {
    return msg.parts?.map(p => p.text || '').join('') || ''
  }

  return (
    <div className="min-h-screen bg-gray-50/50 p-8">
      <div className="max-w-6xl mx-auto">
        {/* Header */}
        <div className="flex items-center gap-3 mb-6">
          <button onClick={() => navigate('/')} className="p-2 rounded-lg hover:bg-white border border-transparent hover:border-border transition-all">
            <ArrowLeft className="h-4 w-4 text-gray-500" />
          </button>
          <div>
            <h2 className="text-xl font-bold text-foreground">Tasks</h2>
            <p className="text-sm text-muted-foreground">Send commands and view conversations</p>
          </div>
        </div>

        {err && (
          <div className="mb-4 p-3 rounded-md bg-destructive/10 text-destructive text-sm flex items-center justify-between gap-3">
            <span>{err}</span>
            <button onClick={() => setErr('')} className="text-xs px-2 py-1 rounded hover:bg-destructive/20">dismiss</button>
          </div>
        )}

        {/* Send command card */}
        <div className="mb-6 p-5 rounded-xl border border-border bg-white shadow-sm">
          <div className="flex items-center gap-2 mb-4">
            <div className="w-8 h-8 rounded-lg bg-gradient-to-br from-emerald-500 to-emerald-600 flex items-center justify-center">
              <Send className="h-4 w-4 text-white" />
            </div>
            <h3 className="text-sm font-semibold text-foreground">New Command</h3>
          </div>
          <div className="flex gap-3">
            <select
              value={toAgent}
              onChange={(e) => setToAgent(e.target.value)}
              className="w-52 px-3 py-2.5 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring appearance-none"
            >
              <option value="">Select agent...</option>
              {friends.map(f => <option key={f} value={f}>{f}</option>)}
              {agents.map(a => <option key={a.agent_id} value={a.agent_id}>{a.name} (me)</option>)}
            </select>
            <input
              placeholder="Type your command..."
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && !e.nativeEvent.isComposing && submitTask()}
              className="flex-1 px-3 py-2.5 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring"
            />
            <button
              onClick={submitTask}
              disabled={sending || !toAgent || !message}
              className="flex items-center gap-2 px-5 py-2.5 rounded-lg bg-gradient-to-r from-emerald-600 to-green-600 text-white text-sm font-medium shadow-sm shadow-emerald-200 hover:shadow-md disabled:opacity-50 disabled:cursor-not-allowed transition-all"
            >
              {sending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />}
              Send
            </button>
          </div>
        </div>

        {/* Main area: task list + conversation */}
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
          {/* Task list */}
          <div className="rounded-xl border border-border bg-white shadow-sm overflow-hidden">
            <div className="px-4 py-3 border-b border-border flex items-center gap-2">
              <MessageSquare className="h-4 w-4 text-muted-foreground" />
              <h3 className="text-sm font-semibold text-foreground flex-1">Recent Tasks</h3>
              <button
                onClick={handleCleanup}
                className="text-xs px-2 py-1 rounded bg-red-50 text-red-600 hover:bg-red-100 border border-red-200 transition-colors"
                title="Clean up completed tasks"
              >
                Clean up
              </button>
            </div>
            <div className="max-h-[500px] overflow-y-auto divide-y divide-border">
              {tasks.length === 0 ? (
                <p className="text-sm text-muted-foreground text-center py-8">No tasks yet</p>
              ) : (
                tasks.map(t => (
                  <div key={t.task_id} className={`flex items-center gap-1 hover:bg-gray-50 transition-colors ${
                    selectedTask?.task_id === t.task_id ? 'bg-blue-50/50 border-l-2 border-l-blue-500' : ''
                  }`}>
                    <button
                      onClick={() => selectTask(t)}
                      className="flex-1 text-left px-4 py-3 flex items-center gap-3 min-w-0"
                    >
                      {statusIcon(t.status)}
                      <div className="flex-1 min-w-0">
                        <p className="text-sm font-medium text-foreground truncate">{t.to}</p>
                        <p className="text-xs text-muted-foreground font-mono truncate">{t.task_id.slice(0, 12)}...</p>
                      </div>
                      <ChevronRight className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                    </button>
                    <button
                      onClick={(e) => { e.stopPropagation(); deleteTask(t.task_id) }}
                      className="p-1.5 mr-2 rounded hover:bg-red-50 text-muted-foreground hover:text-red-500 transition-colors shrink-0"
                      title="Delete this task"
                    >
                      <XCircle className="h-3.5 w-3.5" />
                    </button>
                  </div>
                ))
              )}
            </div>
          </div>

          {/* Conversation view —— 用 calc 把高度锁住，确保内层 messages 区真能滚 */}
          <div
            className="lg:col-span-2 rounded-xl border border-border bg-white shadow-sm overflow-hidden flex flex-col"
            style={{ height: 'calc(100vh - 240px)', minHeight: 500 }}
          >
            {selectedTask ? (
              <>
                {/* Task header */}
                <div className="px-5 py-3 border-b border-border flex items-center justify-between bg-gray-50/50">
                  <div className="flex items-center gap-3">
                    <Bot className="h-4 w-4 text-blue-600" />
                    <div>
                      <p className="text-sm font-semibold text-foreground">{selectedTask.to}</p>
                      <p className="text-xs text-muted-foreground font-mono">{selectedTask.task_id}</p>
                    </div>
                  </div>
                  {statusBadge(selectedTask.status)}
                </div>

                {/* Messages */}
                <div ref={messagesScrollRef} className="flex-1 overflow-y-auto p-5 space-y-4">
                  {historyTruncated && (
                    <div className="px-3 py-2 rounded-md bg-amber-50 border border-amber-200 text-xs text-amber-800">
                      Showing the latest {messages.length} messages. Earlier messages were truncated for performance — pagination is on the roadmap.
                    </div>
                  )}
                  {messages.map(msg => {
                    // A2A 协议：role='user' = task 的 from 发的；role='agent' = to 发的
                    // 不要把 user-role 一律标成"You"——只有 from 真的是 virtual-user-{我} 时才是"你"
                    // 否则就显示具体的 agent_id（agent 之间互发时 from 是 agent，标"You"是错的）
                    const senderID = msg.role === 'user' ? selectedTask.from : selectedTask.to
                    const myUID = localStorage.getItem('uid')
                    const isMeHuman = senderID === `virtual-user-${myUID}`
                    const senderLabel = isMeHuman ? 'You' : senderID
                    const isFromSide = msg.role === 'user' // 决定头像颜色（蓝=发起方）
                    return (
                      <div key={msg.message_id} className="flex gap-3">
                        <div className={`w-8 h-8 rounded-full flex items-center justify-center shrink-0 ${
                          isFromSide ? 'bg-blue-100' : 'bg-emerald-100'
                        }`}>
                          {isMeHuman
                            ? <User className="h-4 w-4 text-blue-600" />
                            : <Bot className={`h-4 w-4 ${isFromSide ? 'text-blue-600' : 'text-emerald-600'}`} />
                          }
                        </div>
                        <div className="flex-1">
                          <div className="flex items-center gap-2 mb-1">
                            <span className="text-xs font-medium text-foreground">{senderLabel}</span>
                            <span className="text-xs text-muted-foreground">
                              {msg.created_at ? new Date(msg.created_at).toLocaleTimeString() : ''}
                            </span>
                          </div>
                          <div className="p-3 rounded-lg bg-gray-50 border border-border">
                            <p className="text-sm text-foreground whitespace-pre-wrap">{getMessageText(msg)}</p>
                          </div>
                        </div>
                      </div>
                    )
                  })}
                  <div ref={messagesEndRef} />
                </div>

                {/* Reply input */}
                <div className="px-5 py-3 border-t border-border">
                  <div className="flex gap-2">
                    <input
                      placeholder={isTerminal ? 'Task is closed' : 'Reply to this task...'}
                      value={replyText}
                      onChange={(e) => setReplyText(e.target.value)}
                      onKeyDown={(e) => e.key === 'Enter' && !e.nativeEvent.isComposing && sendReply()}
                      disabled={isTerminal}
                      className="flex-1 px-3 py-2.5 rounded-lg border border-input bg-background text-sm focus:outline-none focus:ring-2 focus:ring-ring disabled:opacity-50 disabled:cursor-not-allowed"
                    />
                    <button
                      onClick={sendReply}
                      disabled={!replyText || isTerminal}
                      className="px-4 py-2.5 rounded-lg bg-foreground text-background text-sm font-medium hover:bg-foreground/90 disabled:opacity-30 transition-colors"
                    >
                      <Send className="h-4 w-4" />
                    </button>
                  </div>
                </div>
              </>
            ) : (
              <div className="flex-1 flex items-center justify-center">
                <div className="text-center">
                  <MessageSquare className="h-10 w-10 text-muted-foreground/30 mx-auto mb-3" />
                  <p className="text-sm text-muted-foreground">Select a task to view conversation</p>
                  <p className="text-xs text-muted-foreground mt-1">Or send a new command above</p>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Cleanup dialog */}
      {showCleanupDialog && (
        <div className="fixed inset-0 bg-black/40 flex items-center justify-center z-50" onClick={() => setShowCleanupDialog(false)}>
          <div className="bg-white rounded-xl p-6 shadow-xl max-w-sm w-full mx-4" onClick={(e) => e.stopPropagation()}>
            <h3 className="text-lg font-semibold mb-3">Clean up tasks</h3>
            <p className="text-sm text-muted-foreground mb-4">
              Delete completed/failed tasks older than:
            </p>
            <div className="flex items-center gap-3 mb-5">
              <select
                value={cleanupDays}
                onChange={(e) => setCleanupDays(Number(e.target.value))}
                className="border border-border rounded-lg px-3 py-2 text-sm"
              >
                <option value={1}>1 day</option>
                <option value={3}>3 days</option>
                <option value={7}>7 days</option>
                <option value={14}>14 days</option>
                <option value={30}>30 days</option>
              </select>
              <span className="text-sm text-muted-foreground">ago</span>
            </div>
            <div className="flex gap-3 justify-end">
              <button onClick={() => setShowCleanupDialog(false)} className="px-4 py-2 text-sm rounded-lg border border-border hover:bg-gray-50">
                Cancel
              </button>
              <button onClick={doCleanup} className="px-4 py-2 text-sm rounded-lg bg-red-600 text-white hover:bg-red-700">
                Delete
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
