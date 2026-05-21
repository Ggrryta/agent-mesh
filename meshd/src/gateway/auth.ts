// auth.ts：API Key → JWT 的获取和续签。
//
// Gateway 设计（详见 ADR 007/009）：
//   - POST /v1/mesh/auth/token
//     Header:  X-Api-Key: <raw>
//     Body:    {"agent_id":"..."}
//     Response: {token, expires_in, agent_id}
//   - 没有 refresh_token：token 过期时直接再次调 /auth/token 换新

import type { Logger } from '../log.ts'

interface IssueResp {
  token: string
  expires_in: number // seconds
  agent_id: string
}

export class AuthManager {
  private accessToken = ''
  private expiresAt = 0 // unix ms

  constructor(
    private gatewayURL: string,
    private apiKey: string,
    private agentID: string,
    private log: Logger,
  ) {}

  async bootstrap(): Promise<void> {
    await this.issue()
    this.log.info('auth bootstrapped', { agent_id: this.agentID })
  }

  /** 当前 access_token；调用前确保已 bootstrap。 */
  token(): string {
    if (!this.accessToken) {
      throw new Error('auth: not bootstrapped')
    }
    return this.accessToken
  }

  /**
   * 启动后台续签循环。每 30s 检查一次，离过期还剩 < 5 分钟时主动重新 issue。
   */
  startRefreshLoop(): NodeJS.Timeout {
    return setInterval(async () => {
      const remaining = this.expiresAt - Date.now()
      if (remaining < 5 * 60 * 1000) {
        try {
          await this.issue()
          this.log.debug('auth refreshed')
        } catch (e) {
          this.log.warn('auth refresh failed', { err: String(e) })
        }
      }
    }, 30 * 1000) as unknown as NodeJS.Timeout
  }

  /** 调 /mesh/auth/token 换新 JWT。bootstrap / refresh 都走这里。 */
  private async issue(): Promise<void> {
    const url = `${this.gatewayURL}/v1/mesh/auth/token`
    const res = await fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Api-Key': this.apiKey,
      },
      body: JSON.stringify({ agent_id: this.agentID }),
    })
    if (!res.ok) {
      const body = await res.text()
      throw new Error(`auth issue: ${res.status} ${body}`)
    }
    const data = (await res.json()) as IssueResp
    this.accessToken = data.token
    this.expiresAt = Date.now() + data.expires_in * 1000
  }
}
