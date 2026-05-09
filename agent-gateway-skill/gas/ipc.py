"""
GAS IPC Server —— Unix domain socket 接收 a2a-bus MCP server 的调用

协议:行分隔 JSON,每行一个请求或响应。
请求格式:
  {"id": <any>, "agent_id": "...", "method": "send_to"|"reply"|"close_task"|"list_friends"|"get_task", "params": {...}}
响应格式:
  {"id": <any>, "result": <any>}     成功
  {"id": <any>, "error": {"code": int, "message": str}}   错误

GAS 监听的 socket 路径通过环境变量 GAS_IPC_SOCKET 传给 a2a-bus 子进程。
每个 connection 绑定到一个 agent_id(由 a2a-bus 首条消息声明,对应 Agent Core 的身份)。
"""
from __future__ import annotations

import asyncio
import json
import pathlib
from typing import Any, Awaitable, Callable, Optional

from gas.log import get_logger

log = get_logger("gas.ipc")

# RPC handler 接口:给 (agent_id, method, params) 返回 result 或抛异常
RPCHandler = Callable[[str, str, dict], Awaitable[Any]]


class IPCServer:
    def __init__(self, socket_path: pathlib.Path, handler: RPCHandler):
        self.socket_path = socket_path
        self.handler = handler
        self._server: Optional[asyncio.AbstractServer] = None

    async def start(self):
        self.socket_path.parent.mkdir(parents=True, exist_ok=True)
        # 清理残留 socket 文件
        try:
            if self.socket_path.exists():
                self.socket_path.unlink()
        except Exception:
            pass
        self._server = await asyncio.start_unix_server(
            self._handle_conn, path=str(self.socket_path))
        log.info("ipc listening", extra={"sock": str(self.socket_path)})

    async def stop(self):
        if self._server:
            self._server.close()
            await self._server.wait_closed()
        try:
            if self.socket_path.exists():
                self.socket_path.unlink()
        except Exception:
            pass

    async def _handle_conn(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
        peer = id(writer)
        log.info("ipc accept", extra={"peer": peer})
        try:
            while True:
                line = await reader.readline()
                if not line:
                    return
                try:
                    req = json.loads(line)
                except Exception as e:
                    await self._write(writer, {"error": {"code": -32700, "message": f"parse err: {e}"}})
                    continue

                rid = req.get("id")
                agent_id = req.get("agent_id") or ""
                method = req.get("method") or ""
                params = req.get("params") or {}

                try:
                    result = await self.handler(agent_id, method, params)
                    await self._write(writer, {"id": rid, "result": result})
                except IPCMethodError as e:
                    await self._write(writer, {"id": rid, "error": {"code": e.code, "message": e.message}})
                except Exception as e:
                    log.exception("ipc handler error", extra={"peer": peer, "method": method})
                    await self._write(writer, {"id": rid, "error": {"code": -32000, "message": str(e)}})
        except (asyncio.CancelledError, ConnectionResetError):
            pass
        finally:
            try:
                writer.close()
                await writer.wait_closed()
            except Exception:
                pass
            log.info("ipc close", extra={"peer": peer})

    async def _write(self, writer: asyncio.StreamWriter, obj: dict):
        try:
            writer.write((json.dumps(obj, ensure_ascii=False) + "\n").encode())
            await writer.drain()
        except Exception:
            pass


class IPCMethodError(Exception):
    def __init__(self, code: int, message: str):
        self.code = code
        self.message = message
        super().__init__(message)


def default_socket_path(data_dir: pathlib.Path, agent_id: str) -> pathlib.Path:
    """每个 agent 一个 socket 文件,避免互相干扰

    macOS Unix socket 路径长度限制 104 字节。优先用 /tmp/gas-sockets/,
    仅在那里不可写时才回落到 data_dir。
    """
    fallback = data_dir / "sockets"
    tmp = pathlib.Path("/tmp/gas-sockets")
    candidates = [tmp, fallback]
    for base in candidates:
        try:
            base.mkdir(parents=True, exist_ok=True)
            # 测试可写
            probe = base / ".probe"
            probe.touch()
            probe.unlink(missing_ok=True)
            p = base / f"a.{agent_id[:16]}.sock"
            # macOS 限 104,用短路径
            if len(str(p).encode()) < 100:
                return p
        except Exception:
            continue
    # 最后保底:直接 /tmp
    return pathlib.Path(f"/tmp/a.{agent_id[:16]}.sock")
