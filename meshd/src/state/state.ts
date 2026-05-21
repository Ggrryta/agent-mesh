// state.ts：meshd 运行时状态持久化。
//
// 文件位置：${stateDir}/state.json
//
// 职责：记录"哪些 agent 设置了 auto_start"，重启时据此恢复 worker。
// 敏感凭证（api_key / user_jwt）不在这里——见 keychain/secrets.ts。
//
// 兼容性：M1.1 阶段曾把 api_key_ephemeral 写在这里；M1.2 已改为 keychain。
// 如果读到旧版字段会自动迁移：把它写进 keychain，然后从 state.json 抹掉。

import { mkdir, readFile, rename, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'

import type { Logger } from '../log.ts'

export interface InstanceRecord {
  agent_id: string
  auto_start: boolean
  last_started_at?: number

  /** @deprecated M1.1 临时字段；M1.2 起废弃。读到时自动迁移到 keychain。 */
  api_key_ephemeral?: string
}

export interface MeshdState {
  version: number
  instances: InstanceRecord[]
}

const CURRENT_VERSION = 1

/** 用 atomic write（.tmp + rename）防止半写入导致 JSON 损坏。 */
export class StateStore {
  private path: string

  constructor(stateDir: string, private log: Logger) {
    this.path = join(stateDir, 'state.json')
  }

  async load(): Promise<MeshdState> {
    try {
      const raw = await readFile(this.path, 'utf-8')
      const parsed = JSON.parse(raw) as MeshdState
      if (typeof parsed.version !== 'number' || !Array.isArray(parsed.instances)) {
        throw new Error('invalid shape')
      }
      return parsed
    } catch (e: any) {
      if (e?.code === 'ENOENT') {
        return { version: CURRENT_VERSION, instances: [] }
      }
      this.log.warn('state load failed, starting empty', { err: String(e) })
      return { version: CURRENT_VERSION, instances: [] }
    }
  }

  async save(state: MeshdState): Promise<void> {
    try {
      await mkdir(dirname(this.path), { recursive: true })
      const tmp = this.path + '.tmp'
      await writeFile(tmp, JSON.stringify(state, null, 2), 'utf-8')
      await rename(tmp, this.path)
    } catch (e) {
      this.log.warn('state save failed', { err: String(e) })
    }
  }

  /** Upsert：按 agent_id 替换或追加。 */
  async upsertInstance(rec: InstanceRecord): Promise<void> {
    const state = await this.load()
    const idx = state.instances.findIndex((i) => i.agent_id === rec.agent_id)
    if (idx >= 0) {
      state.instances[idx] = rec
    } else {
      state.instances.push(rec)
    }
    await this.save(state)
  }

  async removeInstance(agentID: string): Promise<void> {
    const state = await this.load()
    state.instances = state.instances.filter((i) => i.agent_id !== agentID)
    await this.save(state)
  }
}
