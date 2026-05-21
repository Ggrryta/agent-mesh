// app/cli.ts：meshd 命令行子命令分发。
//
// 用户主要用：
//   start            后台启动 daemon
//   stop             停止 daemon
//   status           查看是否在跑
//   open             浏览器打开 UI（必要时自动 start）
//   logs             tail 一下日志
//
// 给守护场景（service manager / docker run）用的：
//   run              前台跑，绑定到当前 stdio
//
// 故意不做"开机自启"——用户每次开机自己 `agent-meshd start` 一次。
// 如果用户真要开机自启，可以自己挂 launchd plist / systemd unit / cron @reboot。

// CLI 子命令分发。
//
// 设计要点：
//   - run / start / install 这种"要拉起 daemon"的命令才调 loadConfig（需要 GATEWAY_URL 等）
//   - stop / status / open / logs 只跟 stateDir 打交道，单独读 STATE_DIR env，
//     避免要求用户在停服务/查状态时也设 GATEWAY_URL

import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'

import { loadConfig } from '../config/config.ts'
import { RuntimeInfoStore } from '../state/runtime-info.ts'
import { makeLogger, setLogLevel } from '../log.ts'
import { start, stop, checkStatus, logPaths } from './process-manager.ts'
import { serve } from './serve.ts'

/** 默认 state dir：~/.agent-mesh，可被 STATE_DIR env 覆盖。 */
function resolveStateDir(): string {
  return process.env.STATE_DIR ?? `${process.env.HOME}/.agent-mesh`
}

export async function runCLI(argv: string[], version: string): Promise<void> {
  const cmd = (argv[0] ?? 'help').toLowerCase()
  switch (cmd) {
    case 'run':
      return serve(version)
    case 'start':
      return cmdStart(argv.slice(1))
    case 'stop':
      return cmdStop()
    case 'restart':
      return cmdRestart(argv.slice(1))
    case 'status':
      return cmdStatus(version)
    case 'open':
      return cmdOpen(argv.slice(1))
    case 'logs':
      return cmdLogs(argv.slice(1))
    case '-h':
    case '--help':
    case 'help':
      printHelp(version)
      return
    case '-v':
    case '--version':
    case 'version':
      console.log(version)
      return
    default:
      console.error(`unknown command: ${cmd}`)
      printHelp(version)
      process.exit(2)
  }
}

function printHelp(version: string): void {
  process.stdout.write(`agent-meshd ${version}

Usage: agent-meshd <command> [args]

Common commands:
  start              Start the daemon in background
  stop               Stop the daemon
  restart            Stop then start
  status             Show daemon status
  open               Open the local UI in your browser (starts daemon if needed)
  logs [-f]          Tail the daemon logs (-f to follow)

Other:
  run                Run in foreground (for service managers / docker)
  version            Print version
  help               Print this message

Environment (required for start / run):
  GATEWAY_URL                e.g. http://localhost:8080
  ANTHROPIC_AUTH_TOKEN       or ANTHROPIC_API_KEY

Optional environment:
  ANTHROPIC_BASE_URL         company / proxy base URL
  MESHD_HOST                 default 127.0.0.1
  MESHD_PORT                 default 7878
  STATE_DIR                  default ~/.agent-mesh
  MODEL                      default claude-sonnet-4-5
  POLL_WAIT_SEC              default 20
  LOG_LEVEL                  debug | info | warn | error
`)
}

// ─── start / stop / status / restart ─────────────────────────────────────

function envForChild(): Record<string, string> {
  // 把 daemon 需要的 env 显式拷一份（不是必要的，process-manager 已经合并了
  // process.env，但显式列出来让 install 类场景容易复制粘贴）
  const cfg = loadConfig()
  const env: Record<string, string> = {
    GATEWAY_URL: cfg.gatewayURL,
    ANTHROPIC_AUTH_TOKEN: cfg.anthropicAuthToken,
    STATE_DIR: cfg.stateDir,
    MESHD_HOST: cfg.host,
    MESHD_PORT: String(cfg.port),
    MODEL: cfg.model,
    POLL_WAIT_SEC: String(cfg.pollWaitSec),
    LOG_LEVEL: cfg.logLevel,
  }
  if (cfg.anthropicBaseURL) {
    env.ANTHROPIC_BASE_URL = cfg.anthropicBaseURL
  }
  return env
}

async function cmdStart(_extra: string[]): Promise<void> {
  setLogLevel('info')
  const log = makeLogger('start')
  const cfg = loadConfig()
  const res = await start({ stateDir: cfg.stateDir, env: envForChild() }, log)
  if (res.status === 'already_running') {
    console.log(`agent-meshd is already running (pid ${res.pid}, port ${res.port}).`)
    console.log(`run 'agent-meshd open' to open the UI.`)
    return
  }
  console.log(`agent-meshd started (pid ${res.pid}, port ${res.port}).`)
  console.log(`run 'agent-meshd open' to open the UI, or 'agent-meshd logs -f' to tail logs.`)
}

async function cmdStop(): Promise<void> {
  setLogLevel('info')
  const log = makeLogger('stop')
  const res = await stop(resolveStateDir(), log)
  if (res.status === 'not_running') {
    console.log('agent-meshd is not running.')
    return
  }
  console.log(`agent-meshd stopped (was pid ${res.pid}).`)
}

async function cmdRestart(_extra: string[]): Promise<void> {
  await cmdStop()
  await cmdStart(_extra)
}

async function cmdStatus(version: string): Promise<void> {
  setLogLevel('error')
  const stateDir = resolveStateDir()
  const st = await checkStatus(stateDir)
  console.log(`agent-meshd ${version}`)
  if (!st.pidFilePresent) {
    console.log('status:    not running (no runtime.json)')
    return
  }
  if (!st.alive) {
    console.log(`status:    stale runtime.json (pid ${st.pid} not alive)`)
    console.log('(run "agent-meshd start" to start it)')
    return
  }

  console.log(`status:    running`)
  console.log(`pid:       ${st.pid}`)
  console.log(`port:      ${st.port}`)

  // 试 fetch /api/health 拿更详细信息
  try {
    const res = await fetch(`http://127.0.0.1:${st.port}/api/health`)
    if (res.ok) {
      const body = (await res.json()) as { version?: string; instance_count?: number; uptime_ms?: number }
      console.log(`uptime:    ${formatUptime(body.uptime_ms ?? 0)}`)
      console.log(`instances: ${body.instance_count}`)
    }
  } catch {
    // ignore：HTTP 可能还没起来或者 token 路径需要调（但 /api/health 是免认证的）
  }
}

function formatUptime(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h${m % 60}m`
}

// ─── open / logs ─────────────────────────────────────────────────────────

async function cmdOpen(_extra: string[]): Promise<void> {
  setLogLevel('info')
  const log = makeLogger('open')
  const stateDir = resolveStateDir()
  let st = await checkStatus(stateDir)

  if (!st.alive) {
    console.log('agent-meshd not running, starting it...')
    // 这一步需要 GATEWAY_URL / ANTHROPIC_AUTH_TOKEN，loadConfig 会 fail 提示用户去配
    const res = await start({ stateDir, env: envForChild() }, log)
    st = { pidFilePresent: true, pid: res.pid, port: res.port, alive: true }
  }

  // 拿 auth token 拼带认证的 URL
  const info = await new RuntimeInfoStore(stateDir, log).read()
  if (!info) {
    console.error('runtime.json missing after start; cannot open')
    process.exit(1)
  }
  const url = `http://127.0.0.1:${info.port}/?t=${info.auth_token}`
  console.log(`opening ${url.replace(info.auth_token, '<auth_token>')}`)
  const opener = process.platform === 'darwin' ? 'open' : process.platform === 'win32' ? 'start' : 'xdg-open'
  spawn(opener, [url], { stdio: 'ignore', detached: true }).unref()
}

async function cmdLogs(extra: string[]): Promise<void> {
  setLogLevel('error')
  const { out } = logPaths(resolveStateDir())
  if (!existsSync(out)) {
    console.error(`no log file yet: ${out}`)
    process.exit(1)
  }
  const follow = extra.includes('-f') || extra.includes('--follow')
  const args = follow ? ['-f', out] : ['-n', '200', out]
  const child = spawn('tail', args, { stdio: 'inherit' })
  child.on('exit', (code) => process.exit(code ?? 0))
}
