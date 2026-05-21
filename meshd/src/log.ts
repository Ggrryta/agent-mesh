// 极简结构化 logger。生产可换 pino。
// MVP 阶段直接 console.log + JSON：足够了，避免引入大依赖。

type Level = 'debug' | 'info' | 'warn' | 'error'

const order: Record<Level, number> = { debug: 0, info: 1, warn: 2, error: 3 }

let minLevel: Level = 'info'

export function setLogLevel(l: Level) {
  minLevel = l
}

function emit(level: Level, msg: string, fields?: Record<string, unknown>) {
  if (order[level] < order[minLevel]) return
  const line = JSON.stringify({
    ts: new Date().toISOString(),
    level,
    msg,
    ...fields,
  })
  if (level === 'error' || level === 'warn') {
    process.stderr.write(line + '\n')
  } else {
    process.stdout.write(line + '\n')
  }
}

export const log = {
  debug: (msg: string, fields?: Record<string, unknown>) => emit('debug', msg, fields),
  info: (msg: string, fields?: Record<string, unknown>) => emit('info', msg, fields),
  warn: (msg: string, fields?: Record<string, unknown>) => emit('warn', msg, fields),
  error: (msg: string, fields?: Record<string, unknown>) => emit('error', msg, fields),
}

export function makeLogger(scope: string) {
  return {
    debug: (msg: string, f?: Record<string, unknown>) => log.debug(`[${scope}] ${msg}`, f),
    info: (msg: string, f?: Record<string, unknown>) => log.info(`[${scope}] ${msg}`, f),
    warn: (msg: string, f?: Record<string, unknown>) => log.warn(`[${scope}] ${msg}`, f),
    error: (msg: string, f?: Record<string, unknown>) => log.error(`[${scope}] ${msg}`, f),
  }
}

export type Logger = ReturnType<typeof makeLogger>
