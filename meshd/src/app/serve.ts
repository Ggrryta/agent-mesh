// app/serve.ts：meshd serve 子命令的核心。
//
// 启动顺序：
//   1. 加载 meshd 配置
//   2. 把 Anthropic 凭证写到 process.env，供 Claude Agent SDK 读取
//   3. 起 SecretStore（macOS Keychain / 加密文件 fallback）
//   4. 起 AgentManager
//   5. 读 state.json，对每个 auto_start 的 agent 自动启动 worker（从 keychain 取 api_key）
//   6. 生成本机鉴权 token，写 runtime.json（mode 0600）
//   7. 起 HTTP server
//
// meshd 不再持有"用户登录态"——用户对 Gateway 的鉴权完全由浏览器侧处理。
// meshd 只关心：启停 agent worker + 持有 agent 级 API Key + 给浏览器拿 gateway_url。

import { loadConfig } from '../config/config.ts'
import { setLogLevel, makeLogger } from '../log.ts'
import { AgentManager } from '../agent/manager.ts'
import { StateStore } from '../state/state.ts'
import { RuntimeInfoStore, generateAuthToken } from '../state/runtime-info.ts'
import { buildApp } from '../http/server.ts'
import { makeSecretStore, apiKeyAccount, USER_JWT_ACCOUNT } from '../keychain/secrets.ts'

export async function serve(version: string): Promise<void> {
  const cfg = loadConfig()
  setLogLevel(cfg.logLevel)

  if (!process.env.ANTHROPIC_AUTH_TOKEN && !process.env.ANTHROPIC_API_KEY) {
    process.env.ANTHROPIC_AUTH_TOKEN = cfg.anthropicAuthToken
  }
  if (cfg.anthropicBaseURL && !process.env.ANTHROPIC_BASE_URL) {
    process.env.ANTHROPIC_BASE_URL = cfg.anthropicBaseURL
  }

  const log = makeLogger('meshd')
  log.info('starting', {
    version,
    gateway: cfg.gatewayURL,
    listen: `${cfg.host}:${cfg.port}`,
    via_custom_base: !!process.env.ANTHROPIC_BASE_URL,
  })

  const state = new StateStore(cfg.stateDir, makeLogger('state'))
  const runtimeInfo = new RuntimeInfoStore(cfg.stateDir, makeLogger('runtime-info'))
  const secrets = makeSecretStore(cfg.stateDir)
  const manager = new AgentManager({
    gatewayURL: cfg.gatewayURL,
    stateDir: cfg.stateDir,
    model: cfg.model,
    pollWaitSec: cfg.pollWaitSec,
    kafkaBrokers: process.env.KAFKA_BROKERS || undefined,
    log: makeLogger('manager'),
  })

  // 一次性清理：旧版本曾把 user_jwt 也存进 keychain，新模型下不再需要。
  // 重启时清掉，避免遗留凭证。
  await secrets.delete(USER_JWT_ACCOUNT).catch(() => {})

  // 启动恢复：把上次记下来的 auto_start agent 拉起来
  const initial = await state.load()
  let migrated = false
  for (const inst of initial.instances) {
    if (!inst.auto_start) continue
    let apiKey: string | null = null
    if (inst.api_key_ephemeral) {
      // 兼容 M1.1 残留：把 state.json 里的 api_key 迁到 keychain
      apiKey = inst.api_key_ephemeral
      await secrets.set(apiKeyAccount(inst.agent_id), apiKey)
      delete inst.api_key_ephemeral
      migrated = true
      log.info('migrated api_key_ephemeral to keychain', { agent_id: inst.agent_id })
    } else {
      apiKey = await secrets.get(apiKeyAccount(inst.agent_id))
    }

    if (!apiKey) {
      log.warn('skip restore: no api_key in keychain', { agent_id: inst.agent_id })
      continue
    }
    try {
      await manager.start({ agentID: inst.agent_id, apiKey })
    } catch (e) {
      log.warn('restore failed', { agent_id: inst.agent_id, err: String(e) })
    }
  }
  if (migrated) {
    await state.save(initial)
  }

  const authToken = generateAuthToken()
  const app = buildApp({
    manager,
    state,
    secrets,
    gatewayURL: cfg.gatewayURL,
    log: makeLogger('http'),
    version,
    authToken,
  })

  const server = Bun.serve({
    hostname: cfg.host,
    port: cfg.port,
    fetch: app.fetch,
  })

  const actualPort = server.port ?? cfg.port
  await runtimeInfo.write({
    port: actualPort,
    auth_token: authToken,
    pid: process.pid,
    started_at: Date.now(),
  })
  log.info('meshd ready', {
    url: `http://${server.hostname}:${actualPort}`,
    runtime_info: 'written',
  })

  const shutdown = async (sig: string) => {
    log.info('shutting down', { signal: sig })
    try {
      await manager.stopAll()
    } catch (e) {
      log.warn('stopAll error', { err: String(e) })
    }
    server.stop()
    await runtimeInfo.clear()
    setTimeout(() => process.exit(0), 200)
  }
  process.on('SIGINT', () => void shutdown('SIGINT'))
  process.on('SIGTERM', () => void shutdown('SIGTERM'))
}
