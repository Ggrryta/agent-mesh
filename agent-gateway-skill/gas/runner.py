"""
AgentRunner —— 单个 agent 的完整运行时编排

整合:Adapter(Agent Core 子进程)+ GatewayClient + a2a-bus IPC handler + FeedStorage

事件流:
  入站  gateway SSE 事件 → _on_gateway_event → adapter.send_input → Agent Core stdin
  出站  Agent Core stdout → _stdout_reader → OutputEvent
         │
         ├─ send_message → GatewayClient.send_message + feed
         ├─ close_task   → GatewayClient.close_task + feed
         ├─ thinking/log → feed
         └─ turn_end     → feed

  IPC   a2a-bus 转发 tool call → _handle_ipc → GatewayClient 调用 → 返回给 tool
         (实际路径:Claude tool_use 输出 → adapter parse → send_message OutputEvent。
          所以 a2a-bus 主要处理 Agent Core 自己发起的 MCP 调用,我们通过 parse_output
          已经捕获到了这条路径。IPC 这里主要服务 list_friends / get_task 这种"问询"调用。)

关键实现:stdout 中 tool_use 的处理逻辑
   - Claude 调用 a2a-bus.send_to → adapter.parse_output 产出 send_message OutputEvent
   - 同时 a2a-bus MCP server 的 stdin 也会收到 tool/call,然后调 IPC
   - 两条路径会对同一个动作双重处理!

  解决:a2a-bus 脚本的 tool/call 是"真实会发往 GAS"的那一次。
        adapter 解析 stdout 只用于生成 feed 的审计记录,
        不直接调用 gateway。
  IPC handler 才是 gateway 调用的唯一入口。
"""
from __future__ import annotations

import asyncio
import json
import os
import pathlib
import tempfile
import time
from typing import Optional

from gas.adapters.base import AgentCoreAdapter, get_adapter
from gas.config import AgentConfig, Config
from gas.events import InputEvent, OutputEvent
from gas.feed_storage import FeedStorage
from gas.gateway_client import GatewayClient, GatewayError, GatewayInboxEvent
from gas.ipc import IPCMethodError
from gas.log import get_logger

log = get_logger("gas.runner")


class AgentRunner:
    def __init__(
        self,
        config: AgentConfig,
        gas_config: Config,
        data_dir: pathlib.Path,
        feed: FeedStorage,
        ipc_socket_path: pathlib.Path,
    ):
        self.config = config
        self.gas_config = gas_config
        self.data_dir = data_dir
        self.feed = feed
        self.ipc_socket_path = ipc_socket_path

        self.adapter: AgentCoreAdapter = get_adapter(config.host)
        self.proc: Optional[asyncio.subprocess.Process] = None
        self.gw: Optional[GatewayClient] = None

        self._stdout_task: Optional[asyncio.Task] = None
        self._stderr_task: Optional[asyncio.Task] = None
        self._wait_task: Optional[asyncio.Task] = None
        self._stopped = asyncio.Event()
        self._started_at: Optional[float] = None
        self._mcp_cfg_path: Optional[pathlib.Path] = None
        # 去重:Gateway 对同一条消息可能推 task_created + task_message 两个事件,
        # Runner 只给 Claude 投递一次。key = (task_id, message_id),无 message_id 时退化到 (task_id, seq)
        self._seen_incoming: set[tuple[str, str]] = set()
        # 防无限增长:超过阈值清掉一半(保留最近的)
        self._seen_incoming_max = 1024

    @property
    def started_at(self) -> Optional[float]:
        return self._started_at

    async def start(self):
        """1. 写 mcp-config   2. spawn Agent Core   3. 记录 runtime.pid   4. 连接 gateway   5. 起 stdout 读循环

        注:Agent Core(claude -p)冷启动需要 10-40s,期间输入会被 kernel 的 PIPE buffer 缓存,
        Claude 启动完成后会读到。所以 "start() 返回" 不代表 Claude 真就绪,但写给 stdin
        的消息不会丢。stdout 的 system.init 事件会通过 _on_agent_output 被记录到 feed,
        需要 "agent 真就绪" 信号的场景可以查 feed 里的 `event: ready` 条目。
        """
        self._mcp_cfg_path = self._write_mcp_config()
        self.proc = await self.adapter.spawn(self.config, str(self._mcp_cfg_path))
        self._started_at = time.time()
        # 记录 runtime.pid —— 用户/外部工具可基于此**精确**清理,不需要 pkill 模糊匹配。
        # 因为 adapter.spawn 用了 start_new_session=True,proc.pid 就是进程组 leader。
        self._write_runtime_pid()
        self._stdout_task = asyncio.create_task(self._stdout_loop(), name=f"stdout-{self.config.id}")
        self._stderr_task = asyncio.create_task(self._stderr_loop(), name=f"stderr-{self.config.id}")
        self._wait_task = asyncio.create_task(self._wait_loop(), name=f"wait-{self.config.id}")

        self.gw = GatewayClient(
            base_url=self.gas_config.gateway.url,
            agent_id=self.config.id,
            api_key=self.config.api_key,
            gas_instance_id=self.gas_config.gas.instance_id,
            on_event=self._on_gateway_event,
        )
        try:
            await self.gw.start()
        except Exception:
            await self._kill_proc()
            raise
        await self.feed.append(self.config.id, "status", {"state": "online", "ts": int(time.time() * 1000)})
        log.info("runner started", extra={"agent_id": self.config.id})

    async def stop(self):
        self._stopped.set()
        if self.gw:
            try:
                await self.gw.stop()
            except Exception:
                pass
        if self.proc:
            try:
                await self.adapter.graceful_stop(self.proc)
            except Exception:
                pass
        for t in (self._stdout_task, self._stderr_task, self._wait_task):
            if t:
                t.cancel()
                try:
                    await t
                except (asyncio.CancelledError, Exception):
                    pass
        await self.feed.append(self.config.id, "status", {"state": "offline"})
        # 清 runtime.pid(外部清理工具按此判断"还活着"而误清理)
        self._clear_runtime_pid()
        # 清理 mcp_config 临时目录
        if self._mcp_cfg_path and self._mcp_cfg_path.parent.exists():
            try:
                for f in self._mcp_cfg_path.parent.iterdir():
                    f.unlink(missing_ok=True)
                self._mcp_cfg_path.parent.rmdir()
            except Exception:
                pass
        log.info("runner stopped", extra={"agent_id": self.config.id})

    async def send_input(self, event: InputEvent):
        if not self.proc:
            raise RuntimeError("runner not started")
        if event.kind == "user_input":
            await self.feed.append(self.config.id, "user_instruct", {"text": event.data.get("text", "")})
        elif event.kind == "a2a_incoming":
            await self.feed.append(self.config.id, "incoming", event.data)
        await self.adapter.send_input(self.proc, event)

    # ── IPC handler (a2a-bus → gateway) ────────────────────

    async def handle_ipc(self, method: str, params: dict):
        """a2a-bus 把 MCP tool call 转到这里"""
        if self.gw is None:
            raise IPCMethodError(503, "gateway client not ready")

        if method == "send_to":
            target = params.get("agent_id")
            content = params.get("content", "")
            title = params.get("title", "")
            if not target or not content:
                raise IPCMethodError(400, "agent_id and content required")
            try:
                r = await self.gw.send_message(
                    target_agent_id=target, title=title,
                    parts=[{"kind": "text", "text": content}],
                )
            except GatewayError as e:
                raise IPCMethodError(e.status, e.body)
            await self.feed.append(self.config.id, "outgoing", {
                "target": target, "task_id": r.get("task_id"), "seq": r.get("seq"),
                "parts": [{"kind": "text", "text": content}],
            })
            return r

        if method == "reply":
            task_id = params.get("task_id")
            content = params.get("content", "")
            if not task_id or not content:
                raise IPCMethodError(400, "task_id and content required")
            try:
                r = await self.gw.send_message(
                    task_id=task_id,
                    parts=[{"kind": "text", "text": content}],
                )
            except GatewayError as e:
                raise IPCMethodError(e.status, e.body)
            await self.feed.append(self.config.id, "outgoing", {
                "task_id": task_id, "seq": r.get("seq"),
                "parts": [{"kind": "text", "text": content}],
            })
            return r

        if method == "close_task":
            task_id = params.get("task_id")
            if not task_id:
                raise IPCMethodError(400, "task_id required")
            try:
                await self.gw.close_task(task_id)
            except GatewayError as e:
                raise IPCMethodError(e.status, e.body)
            await self.feed.append(self.config.id, "status", {"event": "task_closed", "task_id": task_id})
            return {"status": "closed"}

        if method == "list_friends":
            try:
                friends = await self.gw.list_friends()
            except GatewayError as e:
                raise IPCMethodError(e.status, e.body)
            return {"friends": friends}

        if method == "get_task":
            # 本 MVP 未暴露 list/get,GatewayClient 可后续补。返回空占位。
            return {"error": "get_task not implemented in MVP"}

        raise IPCMethodError(-32601, f"method not found: {method}")

    # ── Gateway SSE → Agent Core stdin ─────────────────────

    async def _on_gateway_event(self, evt: GatewayInboxEvent):
        if evt.kind == "task_message" or evt.kind == "task_created":
            data = evt.data or {}
            task_id = str(data.get("task_id") or "")
            msg_id = str(data.get("message_id") or "")
            seq = str(data.get("seq") if data.get("seq") is not None else "")
            # 去重 key 优先用 message_id,退化到 (task_id, seq)
            dedup_key = (task_id, msg_id) if msg_id else (task_id, seq)
            if dedup_key in self._seen_incoming:
                log.info("runner: skip duplicate gateway event",
                         extra={"agent_id": self.config.id, "kind": evt.kind,
                                "task_id": task_id, "message_id": msg_id})
                return
            self._seen_incoming.add(dedup_key)
            # 防止 set 无限增长
            if len(self._seen_incoming) > self._seen_incoming_max:
                # 简化策略:清一半(实际生产应该用 LRU,这里暂用"清完整"保证正确性)
                self._seen_incoming.clear()
                self._seen_incoming.add(dedup_key)
            await self.send_input(InputEvent(kind="a2a_incoming", data=data))
        elif evt.kind == "task_closed":
            await self.feed.append(self.config.id, "status", {
                "event": "task_closed_by_peer", "task_id": (evt.data or {}).get("task_id"),
            })
        elif evt.kind in ("friend_request", "friend_accept", "friend_revoke"):
            await self.feed.append(self.config.id, "status", {
                "event": evt.kind, "data": evt.data,
            })

    # ── stdout / stderr loops ──────────────────────────────

    async def _stdout_loop(self):
        assert self.proc and self.proc.stdout
        while not self._stopped.is_set():
            line = await self.proc.stdout.readline()
            if not line:
                return
            out = self.adapter.parse_output(line)
            if out is None:
                continue
            await self._on_agent_output(out)

    async def _on_agent_output(self, out: OutputEvent):
        """
        Agent Core stdout 中看到的 tool_use 事件处理。

        为什么 send_message/close_task 这里直接调 gateway:
          真实 claude 的 tool_use 是"请求执行工具"。如果客户端是 Claude Code 交互界面,
          它会真的去调 MCP server 拿结果; 但我们的 --output-format stream-json 模式下,
          stdout 里的 tool_use 也是"意向声明"。Claude 内部会并发触发 MCP server 的
          tools/call,由 a2a-bus 负责调 gateway。

        为避免"同一个动作被 stdout 路径和 MCP 路径同时发两次",MVP 的设计是:
          - 真实 Claude:stdout 看到 tool_use → 只写 feed (tool_call)
                         MCP tools/call 到 a2a-bus → 真正调 gateway
          - 测试 / non-MCP 场景: 可通过 GAS_STDOUT_DISPATCH=1 在 stdout 路径直接调 gateway

        因此 feed 记录永远走 stdout 路径,gateway 调用根据环境变量选择路径。
        """
        import os
        stdout_dispatch = os.environ.get("GAS_STDOUT_DISPATCH") == "1"

        if out.kind == "send_message":
            await self.feed.append(self.config.id, "tool_call", {
                "tool": out.data.get("tool"), "input": out.data.get("input"),
            })
            if stdout_dispatch:
                await self._dispatch_send(out.data.get("tool", ""), out.data.get("input", {}))
        elif out.kind == "close_task":
            inp = out.data.get("input", {})
            await self.feed.append(self.config.id, "tool_call", {"tool": "close_task", "input": inp})
            if stdout_dispatch:
                try:
                    await self.handle_ipc("close_task", inp)
                except Exception as e:
                    log.warning("stdout close_task failed", extra={"err": str(e)})
        elif out.kind == "create_task":
            await self.feed.append(self.config.id, "tool_call", {"tool": "create_task", "input": out.data.get("input")})
        elif out.kind == "thinking":
            await self.feed.append(self.config.id, "thinking", out.data)
        elif out.kind == "log":
            await self.feed.append(self.config.id, "log", out.data)
        elif out.kind == "tool_call":
            await self.feed.append(self.config.id, "tool_call", out.data)
        elif out.kind == "turn_end":
            await self.feed.append(self.config.id, "status", {"event": "turn_end", **out.data})
        elif out.kind == "system_init":
            # Agent Core MCP 握手完成(诊断信号,记录到 feed 方便调试)
            await self.feed.append(self.config.id, "status",
                                    {"event": "ready", **out.data})

    async def _dispatch_send(self, tool: str, tool_input: dict):
        """stdout 路径下直接调 gateway(用于测试或 MCP 不可用环境)"""
        try:
            if tool == "send_to":
                await self.handle_ipc("send_to", tool_input)
            elif tool == "reply":
                await self.handle_ipc("reply", tool_input)
        except Exception as e:
            log.warning("stdout dispatch failed", extra={"tool": tool, "err": str(e)})

    async def _stderr_loop(self):
        assert self.proc and self.proc.stderr
        while not self._stopped.is_set():
            line = await self.proc.stderr.readline()
            if not line:
                return
            s = line.decode(errors="replace").rstrip()
            log.info("agent stderr", extra={"agent_id": self.config.id, "msg": s[:500]})

    async def _wait_loop(self):
        """等待 Agent Core 退出。自然退出时通知上层。"""
        assert self.proc
        await self.proc.wait()
        if not self._stopped.is_set():
            log.warning("agent core exited unexpectedly",
                        extra={"agent_id": self.config.id, "rc": self.proc.returncode})
            await self.feed.append(self.config.id, "error", {
                "event": "agent_core_exited", "returncode": self.proc.returncode,
            })

    async def _kill_proc(self):
        """start() 失败时的紧急清理,发 SIGKILL 到整个进程组(防止孤儿 MCP 子进程)"""
        if self.proc:
            try:
                import os
                import signal
                os.killpg(self.proc.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError, OSError):
                try:
                    self.proc.kill()
                except Exception:
                    pass
        self._clear_runtime_pid()

    def _runtime_pid_path(self) -> pathlib.Path:
        """每 agent 一个 runtime.pid,记录 Agent Core 子进程 PID(同时也是进程组 leader)"""
        p = self.data_dir / "agents" / self.config.id
        p.mkdir(parents=True, exist_ok=True)
        return p / "runtime.pid"

    def _write_runtime_pid(self) -> None:
        """写入 {"pid": ..., "pgid": ..., "started_at": ..., "binary": ...}"""
        if not self.proc:
            return
        import json as _json
        p = self._runtime_pid_path()
        payload = {
            "agent_id": self.config.id,
            "pid": self.proc.pid,
            "pgid": self.proc.pid,  # start_new_session=True → pgid == pid
            "started_at": self._started_at,
            "daemon_pid": os.getpid(),
        }
        try:
            p.write_text(_json.dumps(payload))
        except Exception as e:
            log.warning("write runtime.pid failed", extra={"agent_id": self.config.id, "err": str(e)})

    def _clear_runtime_pid(self) -> None:
        try:
            self._runtime_pid_path().unlink(missing_ok=True)
        except Exception:
            pass

    def _write_mcp_config(self) -> pathlib.Path:
        """为 Agent Core 生成 a2a-bus 的 MCP 配置"""
        tmp = pathlib.Path(tempfile.mkdtemp(prefix=f"gas-mcp-{self.config.id}-"))
        # 调 a2a-bus 脚本,用当前 python 解释器
        import sys
        import gas.a2a_bus as a2a_bus_mod
        a2a_bus_path = pathlib.Path(a2a_bus_mod.__file__).resolve()
        mcp_cfg = {
            "mcpServers": {
                "a2a-bus": {
                    "command": sys.executable,
                    "args": [str(a2a_bus_path)],
                    "env": {
                        "GAS_AGENT_ID": self.config.id,
                        "GAS_IPC_SOCKET": str(self.ipc_socket_path),
                    },
                }
            }
        }
        p = tmp / "mcp.json"
        p.write_text(json.dumps(mcp_cfg))
        return p
