// client.ts：Gateway HTTP API 包装。
// 等价于 Go GAS 的 internal/gateway/client.go。
//
// 所有方法自动带 Authorization: Bearer <jwt>。
// 错误处理保持简单：4xx/5xx 都抛 Error，调用方自己判断是否重试。

import type { AuthManager } from './auth.ts'
import type { Logger } from '../log.ts'

export interface InboxEvent {
  id: number
  agent_id: string
  kind: 'message' | 'artifact' | 'transition' | 'timeline_update'
  task_id: string
  ref_id?: string
  payload: any
  enqueued_at: string
  delivered_at?: string
}

export interface AgentMe {
  agent_id: string
  name: string
  description?: string
  version?: string
  system_prompt?: string
  agent_card?: any
}

export interface SubmitTaskInput {
  task_id?: string
  context_id?: string
  to_agent_id: string
  message: {
    message_id: string
    parts: Array<{ kind: 'text'; text: string } | { kind: 'data'; data: any }>
    preview?: string
  }
}

export interface AppendMessageInput {
  message_id: string
  parts: Array<{ kind: 'text'; text: string } | { kind: 'data'; data: any }>
  preview?: string
}

export class GatewayClient {
  constructor(
    private gatewayURL: string,
    private auth: AuthManager,
    private log: Logger,
  ) {}

  async getMe(): Promise<AgentMe> {
    return this.json('GET', '/v1/mesh/agents/me')
  }

  async heartbeat(agentID: string): Promise<{ gas_instance_id?: string }> {
    const res = await this.fetch('POST', `/v1/mesh/agents/${encodeURIComponent(agentID)}/heartbeat`)
    const data = (await res.json()) as { gas_instance_id?: string }
    return data
  }

  /**
   * 下线 agent。用于 graceful shutdown 时主动通知 Gateway 清除在线状态。
   * 使用原始 API Key 鉴权（shutdown 时 JWT refresh 已停止，JWT 可能过期）。
   */
  async deregister(agentID: string, gasInstanceID: string, apiKey: string): Promise<void> {
    const url = `${this.gatewayURL}/v1/mesh/agents/${encodeURIComponent(agentID)}/online`
    const res = await fetch(url, {
      method: 'DELETE',
      headers: {
        Authorization: `Bearer ${apiKey}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ gas_instance_id: gasInstanceID }),
    })
    // 200 = success (including already-offline idempotent case)
    // 409 = another instance is online, not us — safe to ignore
    if (res.status === 409 || res.ok) {
      return
    }
    const txt = await res.text()
    throw new Error(`deregister: ${res.status} ${txt}`)
  }

  /**
   * 长轮询拉 inbox 事件。wait 秒内有新事件立即返回，否则超时返回空数组。
   */
  async pollInbox(sinceID: number, waitSec: number, limit = 50): Promise<{ events: InboxEvent[]; max_id: number }> {
    const url = `/v1/mesh/inbox?since=${sinceID}&wait=${waitSec}&limit=${limit}`
    return this.json('GET', url)
  }

  async submitTask(input: SubmitTaskInput): Promise<{ task_id: string; context_id: string; status: string }> {
    return this.json('POST', '/v1/mesh/tasks', input)
  }

  async appendMessage(taskID: string, input: AppendMessageInput): Promise<any> {
    return this.json('POST', `/v1/mesh/tasks/${encodeURIComponent(taskID)}/messages`, input)
  }

  async transition(taskID: string, toState: string, statusMessage?: string): Promise<any> {
    return this.json('POST', `/v1/mesh/tasks/${encodeURIComponent(taskID)}/transition`, {
      to_state: toState,
      status_message: statusMessage,
    })
  }

  async getRoster(groupID: string): Promise<{ roster: any[] }> {
    return this.json('GET', `/v1/mesh/groups/${encodeURIComponent(groupID)}/roster`)
  }

  /** 列出自己（agent 视角）的好友。可选 status 默认 accepted。 */
  async listMyFriends(status?: string): Promise<{ friends: Array<{ friend_agent_id: string; friendship_id: number; status: string }> }> {
    const q = status ? `?status=${encodeURIComponent(status)}` : ''
    return this.json('GET', `/v1/mesh/agents/me/friends${q}`)
  }

  /** 列出自己（agent 视角）参与的所有群组 ID。 */
  async listMyGroups(): Promise<{ group_ids: string[] }> {
    return this.json('GET', '/v1/mesh/agents/me/groups')
  }

  async notifyGroup(groupID: string, text: string): Promise<{ notified: number }> {
    return this.json('POST', `/v1/mesh/groups/${encodeURIComponent(groupID)}/notify`, { text })
  }

  async getAgentProfile(agentID: string): Promise<{
    agent_id: string
    name: string
    headline: string
    description: string
    status: string
    skills?: { name: string; description: string; tags?: string[] }[]
  }> {
    return this.json('GET', `/v1/mesh/agents/${encodeURIComponent(agentID)}/profile`)
  }

  async getTimeline(contextID: string, sinceID = 0, limit = 100): Promise<{ entries: any[] }> {
    return this.json('GET', `/v1/mesh/tasks/context/${encodeURIComponent(contextID)}/timeline?since=${sinceID}&limit=${limit}`)
  }

  // ─── 内部 helpers ─────────────────────────────────────────────────

  private async json<T>(method: string, path: string, body?: any): Promise<T> {
    const res = await this.fetch(method, path, body)
    return (await res.json()) as T
  }

  private async fetch(method: string, path: string, body?: any): Promise<Response> {
    const url = `${this.gatewayURL}${path}`
    const init: RequestInit = {
      method,
      headers: {
        Authorization: `Bearer ${this.auth.token()}`,
        ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      },
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    }
    const res = await fetch(url, init)
    if (!res.ok) {
      const txt = await res.text()
      this.log.warn('gateway non-2xx', { method, path, status: res.status })
      throw new Error(`gateway ${method} ${path}: ${res.status} ${txt}`)
    }
    return res
  }
}
