// ws/feed.ts —— Gateway 实时事件 WebSocket 客户端。
//
// 连接 Gateway 的 /v1/admin/ws/feed 端点，接收用户名下所有 agent 的实时事件。
// 事件类型：task_message / task_transition / task_artifact / task_created

export class FeedSocket {
  private listeners: ((event: FeedEvent) => void)[] = []
  private ws: WebSocket | null = null
  private url: string
  private token: string = ''
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private closed = false

  constructor(gatewayUrl: string) {
    // gatewayUrl 形如 http://localhost:8080 → ws://localhost:8080/v1/admin/ws/feed
    const protocol = gatewayUrl.startsWith('https') ? 'wss' : 'ws'
    const host = gatewayUrl.replace(/^https?:\/\//, '')
    this.url = `${protocol}://${host}/v1/admin/ws/feed`
  }

  connect(token: string) {
    this.token = token
    this.closed = false
    this.doConnect()
  }

  private doConnect() {
    if (this.closed) return
    try {
      // 浏览器 WebSocket API 不支持自定义 header，用 query param 传 token
      this.ws = new WebSocket(`${this.url}?token=${encodeURIComponent(this.token)}`)
    } catch {
      this.scheduleReconnect()
      return
    }

    this.ws.onopen = () => {
      console.log('[FeedSocket] connected')
    }

    this.ws.onmessage = (ev) => {
      try {
        const data = JSON.parse(ev.data) as FeedEvent
        if (data.type === 'ping') return // keepalive
        for (const fn of this.listeners) fn(data)
      } catch {
        // ignore parse errors
      }
    }

    this.ws.onclose = () => {
      console.log('[FeedSocket] disconnected')
      this.scheduleReconnect()
    }

    this.ws.onerror = () => {
      this.ws?.close()
    }
  }

  private scheduleReconnect() {
    if (this.closed) return
    if (this.reconnectTimer) return
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.doConnect()
    }, 3000)
  }

  onEvent(fn: (event: FeedEvent) => void) {
    this.listeners.push(fn)
    return () => {
      this.listeners = this.listeners.filter((l) => l !== fn)
    }
  }

  close() {
    this.closed = true
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    this.ws?.close()
    this.ws = null
  }
}

export interface FeedEvent {
  type: string
  agent_id: string
  task_id: string
  payload: any
  timestamp: string
}
