// fan-out.ts：1:N 广播 + 回复汇总收集器。
//
// 设计：
//   - Agent A 调 mesh_broadcast → 创建 N 个 child task → 注册 FanOutGroup
//   - inbox 事件进来时，runtime 先调 tryAbsorb 看是否属于某个 group 的 child
//   - 属于 → 缓冲回复，不喂 SDK；满足 policy 或超时 → resolve，生成汇总 prompt
//   - 汇总 prompt 通过 onResolved 回调交给 runtime 喂 SDK（1 次推理）
//
// 持久化：每次状态变更后 atomic write JSON，crash 后 restore 能恢复。

import { writeFile, readFile, mkdir } from 'node:fs/promises'
import { dirname } from 'node:path'
import type { Logger } from '../log.ts'

// ── Types ──

export type FanOutPolicy = 'wait_all' | 'wait_any' | string // string covers wait_n:K

export interface FanOutReply {
  agentID: string
  taskID: string
  text: string
  receivedAt: number
}

export interface FanOutGroup {
  fanOutID: string
  originalText: string
  policy: FanOutPolicy
  deadlineMs: number
  childTasks: Map<string, string> // taskID → agentID
  replies: Map<string, FanOutReply> // taskID → reply
  resolved: boolean
}

/** JSON-serializable form (no Map) */
interface FanOutGroupPersisted {
  fanOutID: string
  originalText: string
  policy: string
  deadlineMs: number
  childTasks: Record<string, string>
  replies: Record<string, FanOutReply>
  resolved: boolean
}

interface PersistFile {
  version: 1
  groups: FanOutGroupPersisted[]
}

// ── Collector ──

export class FanOutCollector {
  private groups = new Map<string, FanOutGroup>()
  /** Reverse index: child taskID → fanOutID for O(1) lookup */
  private taskIndex = new Map<string, string>()
  /** Active timeout handles */
  private timers = new Map<string, NodeJS.Timeout>()

  constructor(
    private persistPath: string,
    private onResolved: (group: FanOutGroup) => void,
    private log: Logger,
  ) {}

  /** Load persisted groups on startup; re-arm timeouts for active ones. */
  async restore(): Promise<void> {
    try {
      const raw = await readFile(this.persistPath, 'utf-8')
      const data: PersistFile = JSON.parse(raw)
      if (data.version !== 1) return

      for (const pg of data.groups) {
        if (pg.resolved) continue // already done, skip
        const group: FanOutGroup = {
          fanOutID: pg.fanOutID,
          originalText: pg.originalText,
          policy: pg.policy as FanOutPolicy,
          deadlineMs: pg.deadlineMs,
          childTasks: new Map(Object.entries(pg.childTasks)),
          replies: new Map(Object.entries(pg.replies)),
          resolved: false,
        }
        this.groups.set(group.fanOutID, group)
        for (const taskID of group.childTasks.keys()) {
          this.taskIndex.set(taskID, group.fanOutID)
        }

        // If past deadline, resolve immediately with partial results
        if (Date.now() >= group.deadlineMs) {
          this.resolve(group.fanOutID)
        } else {
          this.armTimeout(group)
        }
      }
      if (this.groups.size > 0) {
        this.log.info('fan-out: restored groups', { count: this.groups.size })
      }
    } catch {
      // File doesn't exist or parse error — fresh start
    }
  }

  /** Register a new fan-out group. Called by mesh_broadcast tool. */
  async register(group: FanOutGroup): Promise<void> {
    this.groups.set(group.fanOutID, group)
    for (const taskID of group.childTasks.keys()) {
      this.taskIndex.set(taskID, group.fanOutID)
    }
    this.armTimeout(group)
    await this.persist()
    this.log.info('fan-out: registered', {
      fan_out_id: group.fanOutID,
      targets: group.childTasks.size,
      policy: group.policy,
      timeout_ms: group.deadlineMs - Date.now(),
    })
  }

  /**
   * Try to absorb an inbox event's reply.
   * Returns:
   *   'absorbed' — reply buffered, policy not yet satisfied
   *   'resolved' — reply buffered AND policy satisfied (onResolved will fire)
   *   'not_mine' — this taskID doesn't belong to any active group
   */
  tryAbsorb(taskID: string, text: string, fromAgentID: string): 'absorbed' | 'resolved' | 'not_mine' {
    const fanOutID = this.taskIndex.get(taskID)
    if (!fanOutID) return 'not_mine'

    const group = this.groups.get(fanOutID)
    if (!group || group.resolved) return 'not_mine'

    // Only store first reply per child task
    if (group.replies.has(taskID)) return 'absorbed'

    group.replies.set(taskID, {
      agentID: fromAgentID || group.childTasks.get(taskID) || 'unknown',
      taskID,
      text,
      receivedAt: Date.now(),
    })

    this.log.debug('fan-out: reply absorbed', {
      fan_out_id: fanOutID,
      from: fromAgentID,
      received: group.replies.size,
      total: group.childTasks.size,
    })

    if (this.isPolicySatisfied(group)) {
      this.resolve(fanOutID)
      return 'resolved'
    }

    // Persist updated replies
    void this.persist()
    return 'absorbed'
  }

  /** Check for expired groups. Called after each pollLoop iteration. */
  sweepExpired(): void {
    const now = Date.now()
    for (const [id, group] of this.groups) {
      if (!group.resolved && now >= group.deadlineMs) {
        this.log.info('fan-out: timeout sweep resolving', { fan_out_id: id })
        this.resolve(id)
      }
    }
  }

  /** Build the aggregated prompt for a resolved group. */
  formatAggregatedPrompt(group: FanOutGroup): string {
    const targets = group.childTasks.size
    const lines: string[] = [
      `Your broadcast to ${targets} agent${targets > 1 ? 's' : ''} has been collected.`,
      `Original message: "${group.originalText.length > 200 ? group.originalText.slice(0, 200) + '...' : group.originalText}"`,
      `Policy: ${group.policy}`,
      ``,
      `--- Responses ---`,
    ]

    let emptyCount = 0
    const startTime = group.deadlineMs - 120_000 // approximate (default timeout)
    for (const [taskID, agentID] of group.childTasks) {
      const reply = group.replies.get(taskID)
      if (reply) {
        const elapsed = ((reply.receivedAt - startTime) / 1000).toFixed(0)
        const text = reply.text.trim()
        if (text.length < 10) {
          // 空回复或异常短回复 → 标记为可能失败
          emptyCount++
          lines.push(`[${agentID}] (replied in ~${elapsed}s): ⚠️ 回复异常短（${text.length} 字符），可能执行失败`)
          if (text) lines.push(text)
        } else {
          lines.push(`[${agentID}] (replied in ~${elapsed}s):`)
          lines.push(reply.text)
        }
        lines.push('')
      } else {
        lines.push(`[${agentID}] (task ${taskID}): ⏱ NO REPLY (timed out)`)
        lines.push('')
      }
    }

    const received = group.replies.size
    const timedOut = received < targets
    lines.push(`--- Summary ---`)
    lines.push(`Received: ${received}/${targets}${timedOut ? ' (timeout reached for remaining)' : ' (all replied)'}`)
    if (emptyCount > 0) {
      lines.push(`⚠️ ${emptyCount} 个回复异常短，可能是执行失败。建议确认或重试。`)
    }
    lines.push(``)
    lines.push(`Analyze all responses and decide your next action.`)
    return lines.join('\n')
  }

  // ── Private ──

  private isPolicySatisfied(group: FanOutGroup): boolean {
    const total = group.childTasks.size
    const received = group.replies.size
    if (group.policy === 'wait_all') return received >= total
    if (group.policy === 'wait_any') return received >= 1
    const match = group.policy.match(/^wait_n:(\d+)$/)
    if (match) return received >= parseInt(match[1], 10)
    return received >= total // fallback
  }

  private resolve(fanOutID: string): void {
    const group = this.groups.get(fanOutID)
    if (!group || group.resolved) return

    group.resolved = true

    // Clear timer
    const timer = this.timers.get(fanOutID)
    if (timer) {
      clearTimeout(timer)
      this.timers.delete(fanOutID)
    }

    // Clean up index
    for (const taskID of group.childTasks.keys()) {
      this.taskIndex.delete(taskID)
    }

    // Notify runtime
    this.onResolved(group)

    // Persist final state then remove from memory
    void this.persist().then(() => {
      this.groups.delete(fanOutID)
    })
  }

  private armTimeout(group: FanOutGroup): void {
    const remaining = group.deadlineMs - Date.now()
    if (remaining <= 0) {
      this.resolve(group.fanOutID)
      return
    }
    const timer = setTimeout(() => {
      this.log.info('fan-out: timeout fired', { fan_out_id: group.fanOutID })
      this.resolve(group.fanOutID)
    }, remaining)
    this.timers.set(group.fanOutID, timer)
  }

  private async persist(): Promise<void> {
    const data: PersistFile = {
      version: 1,
      groups: Array.from(this.groups.values())
        .filter((g) => !g.resolved)
        .map((g) => ({
          fanOutID: g.fanOutID,
          originalText: g.originalText,
          policy: g.policy,
          deadlineMs: g.deadlineMs,
          childTasks: Object.fromEntries(g.childTasks),
          replies: Object.fromEntries(g.replies),
          resolved: g.resolved,
        })),
    }
    try {
      await mkdir(dirname(this.persistPath), { recursive: true })
      const tmp = this.persistPath + '.tmp'
      await writeFile(tmp, JSON.stringify(data, null, 2))
      const { rename } = await import('node:fs/promises')
      await rename(tmp, this.persistPath)
    } catch (e) {
      this.log.warn('fan-out: persist failed', { err: String(e) })
    }
  }
}
