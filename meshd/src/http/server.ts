// http/server.ts：meshd 本机 HTTP server。
//
// 监听 127.0.0.1:7878（仅 loopback）。
//
// 鉴权模型（重要）：
//   meshd 不维护"用户登录态"——用户对 Gateway 的鉴权完全在浏览器侧（user JWT 存
//   localStorage），跟 meshd 无关。meshd 只持有"agent 级 API Key"用来跑 worker。
//
//   meshd 自己的 :7878 鉴权是**本机访问令牌**，仅防本机其他用户/其他 origin 偶然
//   访问，不是用户身份：
//     - 浏览器同源访问 / 资源时，meshd 自动在响应里 set-cookie，让浏览器无感拿到
//     - /api/* 校验 cookie，错误时清掉 cookie 返 401
//
// 路由：
//   GET  /api/health                      —— 健康检查（免鉴权）
//   GET  /api/config                      —— 给浏览器拿 gateway_url
//   GET  /api/instances                   —— 列实例
//   POST /api/instances/:id/start         —— body 必带 api_key（首次）；之后复用 keychain
//   POST /api/instances/:id/stop          —— 停 worker

import { Hono } from 'hono'
import { setCookie, getCookie } from 'hono/cookie'

import type { AgentManager } from '../agent/manager.ts'
import type { StateStore } from '../state/state.ts'
import type { Logger } from '../log.ts'
import type { SecretStore } from '../keychain/secrets.ts'
import { apiKeyAccount } from '../keychain/secrets.ts'
import { embedded } from './embedded.ts'

export interface HttpServerOpts {
  manager: AgentManager
  state: StateStore
  secrets: SecretStore
  /** Gateway 地址（仅给浏览器通过 /api/config 读取，meshd 自己不再代理 Gateway） */
  gatewayURL: string
  log: Logger
  /** meshd 版本，写到 /api/health */
  version: string
  /** 本机鉴权 token */
  authToken: string
}

const COOKIE_NAME = 'meshd_auth'
const HEADER_NAME = 'X-Auth-Token'
const QUERY_NAME = 't'

export function buildApp(opts: HttpServerOpts): Hono {
  const app = new Hono()

  // ─── 全局 cookie 维护 ──────────────────────────────────────────────
  // 非 /api/* 路径（即 SPA 资源）：无条件刷新 cookie 到当前 token，确保
  // 浏览器刷新或 meshd 重启后访问根路径自动恢复 cookie。
  // /api/* 路径：仅兼容 ?t=token 启动场景。
  app.use('*', async (c, next) => {
    const isAPI = c.req.path.startsWith('/api/')
    if (!isAPI) {
      const cur = getCookie(c, COOKIE_NAME)
      if (cur !== opts.authToken) {
        setCookie(c, COOKIE_NAME, opts.authToken, {
          httpOnly: true,
          sameSite: 'Strict',
          path: '/',
        })
      }
    } else if (c.req.query(QUERY_NAME) === opts.authToken && !getCookie(c, COOKIE_NAME)) {
      setCookie(c, COOKIE_NAME, opts.authToken, {
        httpOnly: true,
        sameSite: 'Strict',
        path: '/',
      })
    }
    return next()
  })

  // ─── /api/* 鉴权 ────────────────────────────────────────────────────
  app.use('/api/*', async (c, next) => {
    // 免鉴权端点：health（CLI status 用）、config（浏览器 bootstrap 用）
    if (c.req.path === '/api/health' || c.req.path === '/api/config') {
      return next()
    }
    const provided = readToken(c)
    if (provided !== opts.authToken) {
      // 失效 cookie 主动清掉，让浏览器下次直接命中 SPA 路径的"无条件刷新"
      const had = !!getCookie(c, COOKIE_NAME)
      if (had) {
        setCookie(c, COOKIE_NAME, '', {
          httpOnly: true,
          sameSite: 'Strict',
          path: '/',
          maxAge: 0,
        })
      }
      return c.json({ error: 'unauthorized', stale_cookie: had }, 401)
    }
    return next()
  })

  // ─── /api/health ────────────────────────────────────────────────────
  app.get('/api/health', (c) =>
    c.json({
      status: 'ok',
      version: opts.version,
      uptime_ms: Math.round(process.uptime() * 1000),
      instance_count: opts.manager.list().length,
    }),
  )

  // ─── /api/config ────────────────────────────────────────────────────
  // 浏览器启动时读这个，拿到 Gateway URL，之后所有业务请求直接打 Gateway。
  app.get('/api/config', (c) =>
    c.json({
      gateway_url: opts.gatewayURL,
      version: opts.version,
    }),
  )

  // ─── /api/instances ─────────────────────────────────────────────────
  app.get('/api/instances', async (c) => {
    const running = new Map(opts.manager.list().map((i) => [i.agent_id, i]))
    const persisted = await opts.state.load()

    type Item = {
      agent_id: string
      bound: boolean
      running: boolean
      auto_start: boolean
      started_at?: number
      uptime_ms?: number
    }
    const items: Item[] = []
    const seen = new Set<string>()
    for (const inst of persisted.instances) {
      seen.add(inst.agent_id)
      const r = running.get(inst.agent_id)
      items.push({
        agent_id: inst.agent_id,
        bound: true,
        running: !!r,
        auto_start: inst.auto_start,
        started_at: r?.started_at,
        uptime_ms: r?.uptime_ms,
      })
    }
    for (const r of running.values()) {
      if (seen.has(r.agent_id)) continue
      items.push({
        agent_id: r.agent_id,
        bound: false,
        running: true,
        auto_start: false,
        started_at: r.started_at,
        uptime_ms: r.uptime_ms,
      })
    }
    return c.json({ instances: items })
  })

  // ─── start / stop instance ──────────────────────────────────────────
  // 启动 worker 的凭证来源：
  //   1. body.api_key 显式传（首次绑定时浏览器先调 Gateway 签 raw_key 再 POST 过来）
  //   2. keychain 里已存的（再次启动）
  //   都没有 → 400，提示 caller 必须先签 key
  app.post('/api/instances/:agentID/start', async (c) => {
    const agentID = c.req.param('agentID')
    if (!agentID) {
      return c.json({ error: 'agent_id required' }, 400)
    }
    const body = await safeJSON(c)
    const apiKeyFromBody = typeof body?.api_key === 'string' ? body.api_key : ''
    const autoStart = body?.auto_start !== false

    let apiKey = apiKeyFromBody
    if (!apiKey) {
      const stored = await opts.secrets.get(apiKeyAccount(agentID))
      if (stored) apiKey = stored
    }
    if (!apiKey) {
      return c.json(
        {
          error:
            'api_key required: first start must be triggered by browser after issuing a key on Gateway',
        },
        400,
      )
    }

    try {
      const result = await opts.manager.start({ agentID, apiKey })
      if (apiKeyFromBody) {
        await opts.secrets.set(apiKeyAccount(agentID), apiKey)
      }
      await opts.state.upsertInstance({
        agent_id: agentID,
        auto_start: autoStart,
        last_started_at: Date.now(),
      })
      return c.json({ status: result.status, agent_id: agentID })
    } catch (e) {
      opts.log.warn('start failed', { agent_id: agentID, err: String(e) })
      return c.json({ error: String(e) }, 500)
    }
  })

  app.post('/api/instances/:agentID/stop', async (c) => {
    const agentID = c.req.param('agentID')
    if (!agentID) {
      return c.json({ error: 'agent_id required' }, 400)
    }
    const body = await safeJSON(c)
    const forget = body?.forget === true
    try {
      const result = await opts.manager.stop(agentID)
      if (forget) {
        await opts.state.removeInstance(agentID)
        await opts.secrets.delete(apiKeyAccount(agentID))
      }
      return c.json({ status: result.status, agent_id: agentID, forgotten: forget })
    } catch (e) {
      opts.log.warn('stop failed', { agent_id: agentID, err: String(e) })
      return c.json({ error: String(e) }, 500)
    }
  })

  // ─── 静态资源（SPA） ──────────────────────────────────────────────
  const decoded = new Map<string, { body: Uint8Array; contentType: string; etag: string }>()
  for (const [path, rec] of embedded) {
    const buf = Buffer.from(rec.body_b64, 'base64')
    const body = new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength)
    decoded.set(path, {
      body,
      contentType: rec.content_type,
      etag: `"${body.length.toString(16)}-${simpleHash(rec.body_b64).toString(16)}"`,
    })
  }

  // ─── 文件系统浏览 API（工作目录选择器用）───────────────────────
  app.get('/api/fs/browse', async (c) => {
    const { readdir, stat } = await import('node:fs/promises')
    const { homedir } = await import('node:os')
    const path = await import('node:path')

    let dir = c.req.query('path') || homedir()
    // 安全：规范化路径，防止 .. 穿越
    dir = path.resolve(dir)

    try {
      const entries = await readdir(dir, { withFileTypes: true })
      const dirs = entries
        .filter(e => e.isDirectory() && !e.name.startsWith('.'))
        .map(e => ({
          name: e.name,
          path: path.join(dir, e.name),
        }))
        .sort((a, b) => a.name.localeCompare(b.name))

      return c.json({
        current: dir,
        parent: path.dirname(dir) !== dir ? path.dirname(dir) : null,
        directories: dirs,
      })
    } catch (e: any) {
      return c.json({ current: dir, parent: path.dirname(dir), directories: [], error: e.message }, 400)
    }
  })

  app.get('*', (c) => {
    if (decoded.size === 0) {
      return c.text(
        'agent-meshd was built without an embedded UI.\n' +
          'run: cd web && bun install && bun run build && cd .. && bun run web:embed && bun run build',
        503,
        { 'Content-Type': 'text/plain; charset=utf-8' },
      )
    }
    const url = new URL(c.req.url)
    let pathname = url.pathname || '/'
    if (pathname === '/') pathname = '/index.html'

    let entry = decoded.get(pathname)
    if (!entry) entry = decoded.get('/index.html')
    if (!entry) return c.text('not found', 404)

    const ifNoneMatch = c.req.header('if-none-match')
    if (ifNoneMatch && ifNoneMatch === entry.etag) {
      const h = new Headers({ ETag: entry.etag })
      mergeCookieFromCtx(c, h)
      return new Response(null, { status: 304, headers: h })
    }

    const headers = new Headers({
      'Content-Type': entry.contentType,
      ETag: entry.etag,
      'Cache-Control': pathname === '/index.html' ? 'no-cache' : 'public, max-age=31536000, immutable',
    })
    mergeCookieFromCtx(c, headers)
    return new Response(entry.body, { status: 200, headers })
  })

  return app
}

async function safeJSON(c: any): Promise<any> {
  try {
    return await c.req.json()
  } catch {
    return undefined
  }
}

function readToken(c: any): string {
  const fromCookie = getCookie(c, COOKIE_NAME)
  if (fromCookie) return fromCookie
  const fromHeader = c.req.header(HEADER_NAME)
  if (fromHeader) return fromHeader
  const fromQuery = c.req.query(QUERY_NAME)
  if (fromQuery) return fromQuery
  return ''
}

function simpleHash(s: string): number {
  let h = 5381
  const sample = s.length > 2048 ? s.slice(0, 1024) + s.slice(-1024) : s
  for (let i = 0; i < sample.length; i++) {
    h = (h * 33) ^ sample.charCodeAt(i)
  }
  return h >>> 0
}

function mergeCookieFromCtx(c: any, headers: Headers): void {
  const existing = c.res?.headers as Headers | undefined
  if (!existing) return
  const cookies: string[] = (existing as any).getSetCookie?.() ?? []
  for (const v of cookies) {
    headers.append('set-cookie', v)
  }
}
