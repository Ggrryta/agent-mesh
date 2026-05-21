// dedup.ts：消费者侧消息去重。
//
// 保证同一条消息（by message_id）只触发一次 LLM 推理。
// 即使 Kafka consumer 重复投递（crash 后 offset 回退），也不会重复处理。
//
// 实现：
//   - 内存 Set 做快速查重（O(1)）
//   - 磁盘文件持久化（crash 后恢复）
//   - 滑动窗口：只保留最近 N 条（防止无限增长）
//
// 持久化格式：每行一个 message_id，最新的在最后。
// 文件路径：~/.agent-mesh/cursor/dedup/{agentID}

import { readFile, writeFile, mkdir, rename } from 'node:fs/promises'
import { dirname } from 'node:path'
import type { Logger } from '../log.ts'

const MAX_WINDOW = 500 // 保留最近 500 条 ID（覆盖 Kafka autoCommit 5s 内的消息量）

export class DedupStore {
  private processed: Set<string>
  private ordered: string[] // 保持插入顺序，用于淘汰

  constructor(
    private path: string,
    private log: Logger,
  ) {
    this.processed = new Set()
    this.ordered = []
  }

  /** 启动时从磁盘恢复 */
  async load(): Promise<void> {
    try {
      const content = await readFile(this.path, 'utf-8')
      const ids = content.split('\n').filter(Boolean)
      // 只保留最近 MAX_WINDOW 条
      const recent = ids.slice(-MAX_WINDOW)
      this.ordered = recent
      this.processed = new Set(recent)
      this.log.info('dedup: loaded', { count: this.processed.size })
    } catch (e: any) {
      if (e?.code !== 'ENOENT') {
        this.log.warn('dedup: load failed', { err: String(e) })
      }
    }
  }

  /** 检查是否已处理过。true = 重复，应跳过 */
  has(messageId: string): boolean {
    return this.processed.has(messageId)
  }

  /** 标记为已处理 + 持久化 */
  async mark(messageId: string): Promise<void> {
    if (this.processed.has(messageId)) return

    this.processed.add(messageId)
    this.ordered.push(messageId)

    // 滑动窗口淘汰
    while (this.ordered.length > MAX_WINDOW) {
      const evicted = this.ordered.shift()!
      this.processed.delete(evicted)
    }

    // 持久化（原子写）
    await this.persist()
  }

  private async persist(): Promise<void> {
    try {
      await mkdir(dirname(this.path), { recursive: true })
      const tmp = this.path + '.tmp'
      await writeFile(tmp, this.ordered.join('\n') + '\n', 'utf-8')
      await rename(tmp, this.path)
    } catch (e) {
      this.log.warn('dedup: persist failed', { err: String(e) })
    }
  }
}
