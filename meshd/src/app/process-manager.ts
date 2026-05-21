// app/process-manager.ts：本地后台进程管理。
//
// 用 detached spawn 把 meshd serve 跑成后台进程；用 runtime.json 里的 pid
// 跟踪它（pid 是 daemon 自己写进去的），stop 命令读 pid 然后 SIGTERM。
//
// 没有用 launchd / systemd 这种系统服务管理，理由：
//   - 用户要的是"简单方式启动"，不是开机自启
//   - 开机自启在桌面场景下经常被嫌弃（占资源、跳到后台不知道）
//   - 我们的二进制本身就是单文件，平台之间一致行为最重要
//
// 不做的：
//   - 进程崩了不自动拉起（用户主动启动，崩了用户自己看 logs）
//   - 多实例支持（一个用户一个 meshd 就够了）

import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'
import { mkdir, open, readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'

import { RuntimeInfoStore } from '../state/runtime-info.ts'
import type { Logger } from '../log.ts'

export interface StartOpts {
  /** meshd 二进制路径，默认 process.execPath */
  binaryPath?: string
  /** state dir，pid / runtime.json / 日志的根目录 */
  stateDir: string
  /** 透传给 daemon 的 env */
  env: Record<string, string>
  /** 等多少毫秒确认 daemon 已起来。默认 3000 */
  startupTimeoutMs?: number
}

export interface ProcessStatus {
  /** runtime.json 是否存在 */
  pidFilePresent: boolean
  /** 文件里的 pid（不一定还活着） */
  pid?: number
  /** kill -0 探测：进程仍在 */
  alive: boolean
  /** 真实监听端口（来自 runtime.json） */
  port?: number
}

/**
 * 启动 daemon 后台进程。已经在跑则返回 already_running。
 *
 * 使用 detached + unref 让父进程立即退出而不影响子进程。
 * stdout / stderr 重定向到 ${stateDir}/logs/meshd.{out,err}.log
 */
export async function start(opts: StartOpts, log: Logger): Promise<{ status: 'started' | 'already_running'; pid: number; port?: number }> {
  const existing = await checkStatus(opts.stateDir)
  if (existing.alive) {
    return { status: 'already_running', pid: existing.pid!, port: existing.port }
  }

  const logDir = join(opts.stateDir, 'logs')
  await mkdir(logDir, { recursive: true })
  const outPath = join(logDir, 'meshd.out.log')
  const errPath = join(logDir, 'meshd.err.log')
  const outFd = await open(outPath, 'a')
  const errFd = await open(errPath, 'a')

  const binary = opts.binaryPath ?? process.execPath
  log.info('spawning daemon', { binary, log_dir: logDir })

  const child = spawn(binary, ['run'], {
    detached: true,
    env: { ...process.env, ...opts.env },
    stdio: ['ignore', outFd.fd, errFd.fd],
  })
  child.unref()
  // 关闭父进程持有的句柄，daemon 那边继承的还在
  await outFd.close()
  await errFd.close()

  if (typeof child.pid !== 'number') {
    throw new Error('spawn failed: no pid')
  }

  // 等 runtime.json 出现（daemon 监听后会写）
  const timeout = opts.startupTimeoutMs ?? 3000
  const deadline = Date.now() + timeout
  const store = new RuntimeInfoStore(opts.stateDir, log)
  while (Date.now() < deadline) {
    await sleep(100)
    const info = await store.read()
    if (info && info.pid === child.pid) {
      return { status: 'started', pid: info.pid, port: info.port }
    }
  }

  // 没等到 → 看看进程还活着吗，给个有用的错误
  if (!isAlive(child.pid)) {
    throw new Error(`daemon exited before becoming ready; check ${errPath}`)
  }
  throw new Error(`daemon did not write runtime.json within ${timeout}ms; check ${outPath} / ${errPath}`)
}

/**
 * 停止 daemon。读 runtime.json 拿 pid，发 SIGTERM，等它退出。
 */
export async function stop(stateDir: string, log: Logger, timeoutMs = 5000): Promise<{ status: 'stopped' | 'not_running'; pid?: number }> {
  const status = await checkStatus(stateDir)
  if (!status.alive || !status.pid) {
    return { status: 'not_running', pid: status.pid }
  }
  log.info('sending SIGTERM', { pid: status.pid })
  try {
    process.kill(status.pid, 'SIGTERM')
  } catch (e: any) {
    if (e?.code === 'ESRCH') {
      return { status: 'not_running', pid: status.pid }
    }
    throw e
  }

  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (!isAlive(status.pid)) {
      return { status: 'stopped', pid: status.pid }
    }
    await sleep(100)
  }

  log.warn('SIGTERM timeout, sending SIGKILL', { pid: status.pid })
  try {
    process.kill(status.pid, 'SIGKILL')
  } catch {
    // ignore
  }
  return { status: 'stopped', pid: status.pid }
}

/** 探活：runtime.json + kill -0。 */
export async function checkStatus(stateDir: string): Promise<ProcessStatus> {
  const path = join(stateDir, 'runtime.json')
  if (!existsSync(path)) {
    return { pidFilePresent: false, alive: false }
  }
  let info: { pid: number; port: number } | null = null
  try {
    const raw = await readFile(path, 'utf-8')
    info = JSON.parse(raw) as { pid: number; port: number }
  } catch {
    return { pidFilePresent: true, alive: false }
  }
  return {
    pidFilePresent: true,
    pid: info.pid,
    port: info.port,
    alive: isAlive(info.pid),
  }
}

function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch (e: any) {
    return e?.code === 'EPERM' // 权限问题但进程存在
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
}

/** 给 logs 子命令用：返回日志文件路径。 */
export function logPaths(stateDir: string): { out: string; err: string } {
  return {
    out: join(stateDir, 'logs', 'meshd.out.log'),
    err: join(stateDir, 'logs', 'meshd.err.log'),
  }
}

// 防 unused import 警告（dirname 是给以后扩展留的）
void dirname
