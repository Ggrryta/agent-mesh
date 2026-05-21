import { useEffect, useState, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, Radio, Bot, ArrowRight, CheckCircle2, Zap, MessageSquare } from 'lucide-react'

interface FeedEvent {
  type: string
  agent_id: string
  task_id: string
  payload: any
  timestamp: string
}

export default function Feed() {
  const [events] = useState<FeedEvent[]>([])
  const [connected, setConnected] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const bottomRef = useRef<HTMLDivElement>(null)
  const navigate = useNavigate()

  useEffect(() => {
    // M2 阶段：meshd 还没做 /api/gateway/* 的 WebSocket 代理。
    // 暂时不连接，避免 ws 失败的红色控制台噪音。M3 补 WS 代理或换 SSE。
    setConnected(false)
    void wsRef
    return () => {}
  }, [])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [events])

  const eventIcon = (type: string) => {
    switch (type) {
      case 'task_created': return <Zap className="h-4 w-4 text-blue-500" />
      case 'task_message': return <MessageSquare className="h-4 w-4 text-emerald-500" />
      case 'task_transition': return <CheckCircle2 className="h-4 w-4 text-violet-500" />
      case 'task_artifact': return <Bot className="h-4 w-4 text-amber-500" />
      default: return <Radio className="h-4 w-4 text-gray-400" />
    }
  }

  const eventLabel = (type: string) => {
    switch (type) {
      case 'task_created': return 'New Task'
      case 'task_message': return 'Message'
      case 'task_transition': return 'Status Change'
      case 'task_artifact': return 'Artifact'
      case 'group_message': return 'Group Message'
      default: return type
    }
  }

  const eventBadge = (type: string) => {
    switch (type) {
      case 'task_created': return 'badge badge-info'
      case 'task_message': return 'badge badge-success'
      case 'task_transition': return 'badge badge-warning'
      case 'task_artifact': return 'badge badge-neutral'
      default: return 'badge badge-neutral'
    }
  }

  return (
    <div className="min-h-screen bg-gray-50/50 p-8">
      <div className="max-w-4xl mx-auto">
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div className="flex items-center gap-3">
            <button onClick={() => navigate('/')} className="p-2 rounded-lg hover:bg-white border border-transparent hover:border-border transition-all">
              <ArrowLeft className="h-4 w-4 text-gray-500" />
            </button>
            <div>
              <h2 className="text-xl font-bold text-foreground">Live Feed</h2>
              <p className="text-sm text-muted-foreground">Real-time agent collaboration activity</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <div className={`w-2 h-2 rounded-full ${connected ? 'bg-emerald-500 animate-pulse' : 'bg-red-400'}`} />
            <span className="text-xs text-muted-foreground">{connected ? 'Connected' : 'Reconnecting...'}</span>
          </div>
        </div>

        {/* Event stream */}
        <div className="rounded-xl border border-border bg-white shadow-sm overflow-hidden">
          <div className="px-5 py-3 border-b border-border bg-gray-50/80 flex items-center gap-2">
            <Radio className="h-4 w-4 text-muted-foreground" />
            <span className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Activity Stream</span>
            <span className="ml-auto text-xs text-muted-foreground">{events.length} events</span>
          </div>

          <div className="max-h-[600px] overflow-y-auto">
            {events.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-16">
                <div className="w-12 h-12 rounded-full bg-gray-100 flex items-center justify-center mb-3">
                  <Radio className="h-6 w-6 text-gray-300" />
                </div>
                <p className="text-sm text-muted-foreground">Waiting for agent activity...</p>
                <p className="text-xs text-muted-foreground mt-1">Events will appear here in real-time</p>
              </div>
            ) : (
              <div className="divide-y divide-border">
                {events.map((event, i) => (
                  <div key={i} className="px-5 py-3.5 hover:bg-gray-50/50 transition-colors flex items-start gap-3">
                    <div className="mt-0.5">{eventIcon(event.type)}</div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 mb-1">
                        <span className={eventBadge(event.type)}>{eventLabel(event.type)}</span>
                        <span className="text-xs text-muted-foreground">
                          {event.timestamp ? new Date(event.timestamp).toLocaleTimeString() : 'now'}
                        </span>
                      </div>
                      <div className="flex items-center gap-1.5 text-sm text-foreground">
                        <span className="font-medium">{event.agent_id}</span>
                        <ArrowRight className="h-3 w-3 text-muted-foreground" />
                        <span className="font-mono text-xs text-muted-foreground">{event.task_id?.slice(0, 12)}...</span>
                      </div>
                    </div>
                  </div>
                ))}
                <div ref={bottomRef} />
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
