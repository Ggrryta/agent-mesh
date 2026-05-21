// runtime-info.ts：meshd 运行期信息文件。
//
// 路径：${stateDir}/runtime.json
// mode 0600（只有当前用户能读），meshd 启动时生成、退出时清理（best-effort）。
//
// 内容：
//   - port：实际监听端口（占用时会 +1 重试）
//   - auth_token：本机 API 鉴权随机 token，长度 32 字节 hex
//   - pid：meshd 进程 pid
//
// 用途：
//   - CLI（agent-mesh open / status）读它知道 meshd 在哪、怎么认证
//   - meshd 自己读它做 token 校验
//
// 注意：token 持久化是为了"用户从两个 shell 都能用 CLI"，不是高安全方案。
// 真正防范是文件 mode + 进程隔离。攻击者已经在你机器上时这一层无效。

import { mkdir, readFile, rename, unlink, writeFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { randomBytes } from 'node:crypto'
import { dirname, join } from 'node:path'

import type { Logger } from '../log.ts'

export interface RuntimeInfo {
  port: number
  auth_token: string
  pid: number
  started_at: number
}

export class RuntimeInfoStore {
  private path: string

  constructor(stateDir: string, private log: Logger) {
    this.path = join(stateDir, 'runtime.json')
  }

  async write(info: RuntimeInfo): Promise<void> {
    await mkdir(dirname(this.path), { recursive: true })
    const tmp = this.path + '.tmp'
    await writeFile(tmp, JSON.stringify(info, null, 2), { mode: 0o600 })
    await rename(tmp, this.path)
  }

  async read(): Promise<RuntimeInfo | null> {
    if (!existsSync(this.path)) return null
    try {
      const raw = await readFile(this.path, 'utf-8')
      return JSON.parse(raw) as RuntimeInfo
    } catch (e) {
      this.log.warn('runtime.json read failed', { err: String(e) })
      return null
    }
  }

  async clear(): Promise<void> {
    try {
      await unlink(this.path)
    } catch {
      // 不存在就算了
    }
  }
}

export function generateAuthToken(): string {
  return randomBytes(32).toString('hex')
}
