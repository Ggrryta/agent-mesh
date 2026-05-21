// secrets.ts：跨平台密钥存储抽象。
//
// 用途：保存 agent 的 API Key / 用户 JWT 等敏感凭证，避免明文写 state.json。
//
// 平台策略：
//   - macOS：用 /usr/bin/security 调系统 Keychain（零依赖）
//   - 其他平台：fallback 到 ~/.agent-mesh/secrets.enc 加密文件
//                 用机器 ID + 用户名派生 AES key，做基础混淆
//                 （注意：不是真正的 hardware-backed 安全，只是防止 cat 直接看到）
//
// 接口稳定，未来 Linux 加 secret-tool / Windows 加 wincred 都不影响调用方。

import { spawn } from 'node:child_process'
import { createCipheriv, createDecipheriv, createHash, randomBytes, scryptSync } from 'node:crypto'
import { mkdir, readFile, writeFile, unlink } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { hostname, userInfo, platform } from 'node:os'
import { dirname, join } from 'node:path'

/** Keychain 服务名前缀。Keychain 里所有项都属于这个 service。 */
const SERVICE = 'co.agent-mesh.meshd'

export interface SecretStore {
  /** 写入或覆盖。account 唯一标识一条 secret（如 `api_key:alice`、`user_jwt`）。 */
  set(account: string, value: string): Promise<void>
  /** 不存在返回 null。 */
  get(account: string): Promise<string | null>
  /** 不存在静默返回。 */
  delete(account: string): Promise<void>
}

export function makeSecretStore(stateDir: string): SecretStore {
  if (platform() === 'darwin') {
    return new MacKeychain()
  }
  return new EncryptedFileStore(stateDir)
}

// ─── macOS Keychain ─────────────────────────────────────────────────────

class MacKeychain implements SecretStore {
  async set(account: string, value: string): Promise<void> {
    // 先尝试更新；不存在则添加。security add-generic-password 用 -U 直接覆盖
    await runSecurity([
      'add-generic-password',
      '-a', account,
      '-s', SERVICE,
      '-w', value,
      '-U', // update if exists
    ])
  }

  async get(account: string): Promise<string | null> {
    try {
      const out = await runSecurity([
        'find-generic-password',
        '-a', account,
        '-s', SERVICE,
        '-w', // 只输出明文
      ])
      return out.trim()
    } catch {
      return null
    }
  }

  async delete(account: string): Promise<void> {
    try {
      await runSecurity([
        'delete-generic-password',
        '-a', account,
        '-s', SERVICE,
      ])
    } catch {
      // 不存在就算了
    }
  }
}

function runSecurity(args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = spawn('/usr/bin/security', args, { stdio: ['ignore', 'pipe', 'pipe'] })
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', (d) => (stdout += d.toString()))
    child.stderr.on('data', (d) => (stderr += d.toString()))
    child.on('close', (code) => {
      if (code === 0) return resolve(stdout)
      reject(new Error(`security exit ${code}: ${stderr.trim() || stdout.trim()}`))
    })
    child.on('error', reject)
  })
}

// ─── 加密文件 fallback ─────────────────────────────────────────────────
//
// 文件结构：JSON map account → ciphertext。
// AES-256-GCM；key 用 scrypt(machine_id + user) 派生。
//
// 不是高安全方案，但能挡住"cat 看到明文" + "用户复制 ~/.agent-mesh 到别人机器"
// 这两类常见误用。真正硬安全要等接 secret-tool / wincred。

const FILENAME = 'secrets.enc'

interface SecretsFile {
  v: number
  // map: account → { iv: hex, tag: hex, ct: hex }
  data: Record<string, { iv: string; tag: string; ct: string }>
}

class EncryptedFileStore implements SecretStore {
  private path: string
  private key: Buffer

  constructor(stateDir: string) {
    this.path = join(stateDir, FILENAME)
    // 用 hostname + 用户名做盐源，scrypt 派生 32 字节 key。
    // 不强壮，但跨同机器多进程一致；移到别的机器 / 别的用户解不开。
    const salt = createHash('sha256').update(`agent-mesh|${hostname()}|${userInfo().username}`).digest()
    this.key = scryptSync('agent-mesh-meshd-static', salt, 32)
  }

  async set(account: string, value: string): Promise<void> {
    const file = await this.read()
    const iv = randomBytes(12)
    const cipher = createCipheriv('aes-256-gcm', this.key, iv)
    const ct = Buffer.concat([cipher.update(value, 'utf8'), cipher.final()])
    const tag = cipher.getAuthTag()
    file.data[account] = { iv: iv.toString('hex'), tag: tag.toString('hex'), ct: ct.toString('hex') }
    await this.write(file)
  }

  async get(account: string): Promise<string | null> {
    const file = await this.read()
    const rec = file.data[account]
    if (!rec) return null
    try {
      const decipher = createDecipheriv('aes-256-gcm', this.key, Buffer.from(rec.iv, 'hex'))
      decipher.setAuthTag(Buffer.from(rec.tag, 'hex'))
      const pt = Buffer.concat([decipher.update(Buffer.from(rec.ct, 'hex')), decipher.final()])
      return pt.toString('utf8')
    } catch {
      // key 不对（换机器了 / 用户名变了）→ 视为不存在，避免抛错阻塞 meshd
      return null
    }
  }

  async delete(account: string): Promise<void> {
    const file = await this.read()
    if (!(account in file.data)) return
    delete file.data[account]
    await this.write(file)
  }

  private async read(): Promise<SecretsFile> {
    if (!existsSync(this.path)) return { v: 1, data: {} }
    try {
      const raw = await readFile(this.path, 'utf-8')
      const parsed = JSON.parse(raw) as SecretsFile
      if (parsed.v !== 1 || typeof parsed.data !== 'object') throw new Error('bad shape')
      return parsed
    } catch {
      // 损坏 → 清掉重建。这里宁可丢失再让用户重新登录，也不要阻塞启动。
      try { await unlink(this.path) } catch {}
      return { v: 1, data: {} }
    }
  }

  private async write(file: SecretsFile): Promise<void> {
    await mkdir(dirname(this.path), { recursive: true })
    const tmp = this.path + '.tmp'
    await writeFile(tmp, JSON.stringify(file), { mode: 0o600 })
    // atomic rename
    const { rename } = await import('node:fs/promises')
    await rename(tmp, this.path)
  }
}

// ─── account 命名约定 ────────────────────────────────────────────────────

/** API Key 用 `api_key:{agentID}`。 */
export function apiKeyAccount(agentID: string): string {
  return `api_key:${agentID}`
}

/** 用户级 JWT 用 `user_jwt`（meshd 单用户假设）。 */
export const USER_JWT_ACCOUNT = 'user_jwt'
