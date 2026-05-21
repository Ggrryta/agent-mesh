// meshd 入口：分发到 CLI 子命令。
//
// `agent-meshd serve`      启动 daemon（默认）
// `agent-meshd install`    注册系统守护进程
// `agent-meshd uninstall`  反过来
// `agent-meshd status`     查看状态
// `agent-meshd open`       浏览器打开 UI

import { runCLI } from './app/cli.ts'

const VERSION = '0.2.0'

runCLI(process.argv.slice(2), VERSION).catch((e) => {
  console.error(JSON.stringify({ ts: new Date().toISOString(), level: 'fatal', err: String(e), stack: e?.stack }))
  process.exit(1)
})
