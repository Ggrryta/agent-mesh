// meshd 启动配置。
//
// 区别于旧的 gas-ts：
//   - 旧版：一进程跑一个 agent，配置里就有 GAS_AGENT_ID / GAS_API_KEY
//   - 新版：meshd 管理多个 agent worker，配置里只有 meshd 自己的东西；
//          单个 agent 的 apiKey 在调 /api/instances/:id/start 时传入（之后 M1.2 改为从 keychain 读）
//
// 必填：GATEWAY_URL、ANTHROPIC_AUTH_TOKEN（或 ANTHROPIC_API_KEY）
// 可选：ANTHROPIC_BASE_URL、LOG_LEVEL、POLL_WAIT_SEC、MODEL、STATE_DIR、MESHD_HOST、MESHD_PORT

import { z } from 'zod'

const ConfigSchema = z.object({
  gatewayURL: z.string().url(),

  // Anthropic 凭证（所有 worker 共享）：
  //   - anthropicAuthToken：Bearer token
  //   - anthropicBaseURL：可选公司代理
  anthropicAuthToken: z.string().min(1),
  anthropicBaseURL: z.string().url().optional(),

  // meshd HTTP server。出于安全考虑只听 loopback。
  host: z.string().default('127.0.0.1'),
  port: z.number().int().min(1).max(65535).default(7878),

  // 全局默认值，可被单个 agent 启动时覆盖
  logLevel: z.enum(['debug', 'info', 'warn', 'error']).default('info'),
  pollWaitSec: z.number().int().min(1).max(30).default(20),
  model: z.string().default('claude-sonnet-4-5'),

  // 状态目录：state.json / cursor / 日志
  stateDir: z.string().default(`${process.env.HOME}/.agent-mesh`),
})

export type Config = z.infer<typeof ConfigSchema>

export function loadConfig(): Config {
  const authToken = process.env.ANTHROPIC_AUTH_TOKEN || process.env.ANTHROPIC_API_KEY

  return ConfigSchema.parse({
    gatewayURL: process.env.GATEWAY_URL,
    anthropicAuthToken: authToken,
    anthropicBaseURL: process.env.ANTHROPIC_BASE_URL,
    host: process.env.MESHD_HOST,
    port: process.env.MESHD_PORT ? parseInt(process.env.MESHD_PORT, 10) : undefined,
    logLevel: process.env.LOG_LEVEL,
    pollWaitSec: process.env.POLL_WAIT_SEC ? parseInt(process.env.POLL_WAIT_SEC, 10) : undefined,
    model: process.env.MODEL,
    stateDir: process.env.STATE_DIR,
  })
}
