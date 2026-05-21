// manager.ts：meshd 内的 agent 实例管理器。
//
// 一个 meshd 进程内可以同时跑多个 AgentRuntime（每个对应一个 agent_id）。
// AgentManager 负责：
//   - 维护 Map<agentID, RunningInstance>
//   - 启动：建 AuthManager + GatewayClient + heartbeat + AgentRuntime，spawn poll loop
//   - 停止：runtime.stop() + 清理定时器
//   - 列出当前在跑的实例（给 /api/instances 用）
//
// 不在这一层做的事：
//   - keychain（M1.2）：apiKey 现在是参数传入，后续从 keychain 读
//   - state.json 持久化（M1.1.d 在 index.ts 启动恢复时驱动 manager.start）
//   - Gateway 互斥锁（M3）：现在两台机器同时起同一 agent 不会被拒绝

import { join } from 'node:path'

import { GatewayClient } from '../gateway/client.ts'
import { AuthManager } from '../gateway/auth.ts'
import { AgentRuntime } from './runtime.ts'
import { startHeartbeat } from './heartbeat.ts'
import type { HeartbeatHandle } from './heartbeat.ts'
import { makeLogger } from '../log.ts'
import type { Logger } from '../log.ts'

interface RunningInstance {
  agentID: string
  apiKey: string
  startedAt: number
  runtime: AgentRuntime
  auth: AuthManager
  client: GatewayClient
  refreshTimer: NodeJS.Timeout
  heartbeat: HeartbeatHandle
  /** poll loop 的 promise；stop 时 await 它结束以释放资源。 */
  loopPromise: Promise<void>
}

export interface AgentManagerOpts {
  gatewayURL: string
  stateDir: string
  model: string
  pollWaitSec: number
  kafkaBrokers?: string // 非空时 agent worker 用 Kafka consumer 替代长轮询
  log: Logger
}

export interface InstanceSummary {
  agent_id: string
  started_at: number
  uptime_ms: number
}

export interface StartInstanceInput {
  agentID: string
  apiKey: string
}

export class AgentManager {
  private instances = new Map<string, RunningInstance>()

  constructor(private opts: AgentManagerOpts) {}

  list(): InstanceSummary[] {
    const now = Date.now()
    const out: InstanceSummary[] = []
    for (const inst of this.instances.values()) {
      out.push({
        agent_id: inst.agentID,
        started_at: inst.startedAt,
        uptime_ms: now - inst.startedAt,
      })
    }
    return out
  }

  isRunning(agentID: string): boolean {
    return this.instances.has(agentID)
  }

  /**
   * 启动一个 agent worker。已经在跑则返回 already_running。
   *
   * 流程：
   *   1. AuthManager.bootstrap() —— 用 apiKey 换第一把 JWT；失败立刻抛
   *   2. 构造 GatewayClient + 启动心跳 + 启动 refresh loop
   *   3. 构造 AgentRuntime，启动 poll loop（不 await，让它后台跑）
   *   4. 注册到 instances map
   *
   * 任意一步失败都会清理已经创建的资源，保持原子性。
   */
  async start(input: StartInstanceInput): Promise<{ status: 'started' | 'already_running' }> {
    if (this.instances.has(input.agentID)) {
      return { status: 'already_running' }
    }

    const log = makeLogger(`agent:${input.agentID}`)
    const authLog = makeLogger(`auth:${input.agentID}`)
    const gwLog = makeLogger(`gw:${input.agentID}`)
    const hbLog = makeLogger(`heartbeat:${input.agentID}`)

    const auth = new AuthManager(this.opts.gatewayURL, input.apiKey, input.agentID, authLog)
    await auth.bootstrap()

    const refreshTimer = auth.startRefreshLoop()
    const client = new GatewayClient(this.opts.gatewayURL, auth, gwLog)
    const heartbeat = startHeartbeat(client, input.agentID, 30, hbLog)

    const cursorPath = join(this.opts.stateDir, 'cursor', input.agentID)
    const runtime = new AgentRuntime({
      client,
      agentID: input.agentID,
      model: this.opts.model,
      pollWaitSec: this.opts.pollWaitSec,
      cursorPath,
      kafkaBrokers: this.opts.kafkaBrokers,
      log,
    })

    // 后台跑 runtime.start()。它内部会一直 poll 到 stop() 调用为止。
    // 任何未捕获错误这里收尾一下，让 manager 看到异常退出。
    const loopPromise = runtime.start().catch((e) => {
      log.error('runtime exited with error', { err: String(e) })
    })

    this.instances.set(input.agentID, {
      agentID: input.agentID,
      apiKey: input.apiKey,
      startedAt: Date.now(),
      runtime,
      auth,
      client,
      refreshTimer,
      heartbeat,
      loopPromise,
    })

    this.opts.log.info('instance started', { agent_id: input.agentID })
    return { status: 'started' }
  }

  /**
   * 停止 agent worker。返回 not_running 表示本来就没在跑。
   * 先向 Gateway 发 deregister 主动下线，再清理本地资源。
   */
  async stop(agentID: string): Promise<{ status: 'stopped' | 'not_running' }> {
    const inst = this.instances.get(agentID)
    if (!inst) {
      return { status: 'not_running' }
    }

    // 主动下线：best-effort，5s 超时，500 重试一次（间隔 1.5s）。
    // 即使彻底失败，gateway 侧 health check 会在探活超时后自动标记 offline。
    if (inst.heartbeat.gasInstanceID) {
      await this.deregister(agentID, inst.apiKey, inst.heartbeat.gasInstanceID)
    }

    inst.runtime.stop()
    clearInterval(inst.refreshTimer)
    clearInterval(inst.heartbeat.timer)
    // poll loop 会在下一次 wait 结束时检查 stopped 标志退出。
    // 等它结束防止资源泄露（pending fetch、cursor 写一半等）。
    await inst.loopPromise
    this.instances.delete(agentID)
    this.opts.log.info('instance stopped', { agent_id: agentID })
    return { status: 'stopped' }
  }

  /** 关闭所有实例，meshd shutdown 时调用。 */
  async stopAll(): Promise<void> {
    const ids = Array.from(this.instances.keys())
    await Promise.all(ids.map((id) => this.stop(id)))
  }

  /**
   * Best-effort deregister：DELETE /v1/mesh/agents/:id/online
   * - 5s timeout（正常情况 gateway 处理很快，超时大概率是网络问题）
   * - 500 重试一次，间隔 1.5s
   * - 失败不阻塞 shutdown，gateway health check 会兜底
   */
  private async deregister(agentID: string, apiKey: string, gasInstanceID: string): Promise<void> {
    const url = `${this.opts.gatewayURL}/v1/mesh/agents/${encodeURIComponent(agentID)}/online`
    const headers = {
      Authorization: `Bearer ${apiKey}`,
      'Content-Type': 'application/json',
    }
    const body = JSON.stringify({ gas_instance_id: gasInstanceID })

    const attempt = async (): Promise<Response> => {
      const ctrl = new AbortController()
      const timer = setTimeout(() => ctrl.abort(), 5000)
      try {
        return await fetch(url, { method: 'DELETE', headers, body, signal: ctrl.signal })
      } finally {
        clearTimeout(timer)
      }
    }

    try {
      let res = await attempt()
      // 500 重试一次
      if (res.status >= 500) {
        this.opts.log.warn('deregister got 5xx, retrying in 1.5s', { agent_id: agentID, status: res.status })
        await new Promise((r) => setTimeout(r, 1500))
        res = await attempt()
      }
      if (res.ok || res.status === 409) {
        this.opts.log.info('deregistered from gateway', { agent_id: agentID })
      } else {
        this.opts.log.warn('deregister non-ok', { agent_id: agentID, status: res.status })
      }
    } catch (e) {
      this.opts.log.warn('deregister failed, health check will expire it', { agent_id: agentID, err: String(e) })
    }
  }
}
