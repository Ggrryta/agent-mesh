// cursor.ts：inbox cursor 持久化。
//
// 重启后能从上次停下的地方继续，避免重复处理事件（重复处理 = 重复 LLM 调用 = 钱）。
//
// 存储路径：${stateDir}/${agentID}/cursor
// 格式：单行文本，存最大 inbox event id（int64）。
// 读不到 / 解析失败：从 0 开始（首次启动）。

import { mkdir, readFile, writeFile, rename } from 'node:fs/promises'
import { dirname } from 'node:path'
import type { Logger } from '../log.ts'

export class CursorStore {
  constructor(
    private path: string,
    private log: Logger,
  ) {}

  async load(): Promise<number> {
    try {
      const txt = await readFile(this.path, 'utf-8')
      const n = parseInt(txt.trim(), 10)
      if (Number.isNaN(n) || n < 0) {
        this.log.warn('cursor file unreadable, starting from 0', { path: this.path, raw: txt })
        return 0
      }
      this.log.info('cursor loaded', { path: this.path, value: n })
      return n
    } catch (e: any) {
      if (e?.code === 'ENOENT') {
        this.log.info('no cursor file yet, starting from 0', { path: this.path })
        return 0
      }
      this.log.warn('cursor load failed, starting from 0', { err: String(e) })
      return 0
    }
  }

  /**
   * 原子写：先写 .tmp，再 rename。避免 crash 时半写状态。
   */
  async save(value: number): Promise<void> {
    try {
      await mkdir(dirname(this.path), { recursive: true })
      const tmp = this.path + '.tmp'
      await writeFile(tmp, String(value), 'utf-8')
      await rename(tmp, this.path)
    } catch (e) {
      this.log.warn('cursor save failed', { err: String(e), value })
    }
  }
}
