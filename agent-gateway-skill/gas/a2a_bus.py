"""
a2a-bus MCP server (stdio)

这是一个独立可执行的 Python 脚本,被 `claude -p --mcp-config` 拉起作为 MCP server。
它把 MCP tools/call 请求转成 GAS IPC 调用,返回结果。

协议:
  - stdin/stdout:MCP over stdio (JSON-RPC 2.0)
  - Unix socket:与 GAS daemon 通信

环境变量:
  GAS_IPC_SOCKET      Unix socket 路径(必须)
  GAS_AGENT_ID        当前 agent 的 ID(必须)

工具清单:
  send_to(agent_id, content)          发新消息给指定 agent,返回 {task_id, seq}
  reply(task_id, content)              回复现有 task
  close_task(task_id)                  关闭 task
  list_friends()                       当前好友列表
  get_task(task_id)                    task 详情 + 消息历史

所有 content 参数:字符串,会被包装为 parts=[{kind:"text",text:content}]。
"""
from __future__ import annotations

import json
import os
import socket
import sys
import threading
import time


AGENT_ID = os.environ.get("GAS_AGENT_ID", "")
SOCK_PATH = os.environ.get("GAS_IPC_SOCKET", "")


def log_stderr(msg: str):
    sys.stderr.write(f"[a2a-bus {AGENT_ID}] {msg}\n")
    sys.stderr.flush()


def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


# ── IPC client (sync, for MCP request thread) ────────────────

class IPCClient:
    def __init__(self, path: str):
        self.path = path
        self._sock = None
        self._buf = b""
        self._lock = threading.Lock()
        self._next_id = 1

    def _ensure(self):
        if self._sock is None:
            s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            s.connect(self.path)
            self._sock = s

    def _reset(self):
        try:
            if self._sock:
                self._sock.close()
        except Exception:
            pass
        self._sock = None
        self._buf = b""

    def call(self, method: str, params: dict, timeout: float = 30.0) -> dict:
        """同步调用。失败抛异常。返回 result 字段。"""
        with self._lock:
            attempts = 0
            while True:
                attempts += 1
                try:
                    self._ensure()
                    rid = self._next_id
                    self._next_id += 1
                    req = {"id": rid, "agent_id": AGENT_ID, "method": method, "params": params}
                    self._sock.sendall((json.dumps(req) + "\n").encode())
                    self._sock.settimeout(timeout)
                    # 读一行
                    while b"\n" not in self._buf:
                        chunk = self._sock.recv(8192)
                        if not chunk:
                            raise ConnectionError("ipc closed")
                        self._buf += chunk
                    line, self._buf = self._buf.split(b"\n", 1)
                    resp = json.loads(line)
                    if "error" in resp:
                        err = resp["error"]
                        raise RuntimeError(f"[{err.get('code')}] {err.get('message')}")
                    return resp.get("result")
                except (ConnectionError, OSError, socket.timeout):
                    self._reset()
                    if attempts >= 2:
                        raise
                    time.sleep(0.2)


client = IPCClient(SOCK_PATH)


# ── MCP tool definitions ───────────────────────────────────

TOOLS = [
    {
        "name": "send_to",
        "description": "Send a message to another agent (starts a new task). Returns {task_id, seq, message_id}.",
        "inputSchema": {
            "type": "object",
            "required": ["agent_id", "content"],
            "properties": {
                "agent_id": {"type": "string", "description": "Target agent_id"},
                "content": {"type": "string", "description": "Message content (text)"},
                "title": {"type": "string", "description": "Optional task title"},
            },
        },
    },
    {
        "name": "reply",
        "description": "Reply to an existing task. Appends a message.",
        "inputSchema": {
            "type": "object",
            "required": ["task_id", "content"],
            "properties": {
                "task_id": {"type": "string"},
                "content": {"type": "string"},
            },
        },
    },
    {
        "name": "close_task",
        "description": "Close an existing task.",
        "inputSchema": {
            "type": "object",
            "required": ["task_id"],
            "properties": {"task_id": {"type": "string"}},
        },
    },
    {
        "name": "list_friends",
        "description": "List accepted friends (available agents you can send to).",
        "inputSchema": {"type": "object", "properties": {}},
    },
    {
        "name": "get_task",
        "description": "Get task details including message history.",
        "inputSchema": {
            "type": "object",
            "required": ["task_id"],
            "properties": {"task_id": {"type": "string"}},
        },
    },
]


def wrap_text(text: str) -> dict:
    return {"content": [{"type": "text", "text": text}]}


def handle_tool_call(name: str, args: dict) -> dict:
    """调 IPC 转成 MCP content。"""
    try:
        if name == "send_to":
            result = client.call("send_to", args)
            return wrap_text(json.dumps(result, ensure_ascii=False))
        if name == "reply":
            result = client.call("reply", args)
            return wrap_text(json.dumps(result, ensure_ascii=False))
        if name == "close_task":
            result = client.call("close_task", args)
            return wrap_text(json.dumps(result, ensure_ascii=False))
        if name == "list_friends":
            result = client.call("list_friends", args or {})
            return wrap_text(json.dumps(result, ensure_ascii=False))
        if name == "get_task":
            result = client.call("get_task", args)
            return wrap_text(json.dumps(result, ensure_ascii=False))
        return {"content": [{"type": "text", "text": f"unknown tool: {name}"}], "isError": True}
    except Exception as e:
        log_stderr(f"tool {name} failed: {e}")
        return {"content": [{"type": "text", "text": f"error: {e}"}], "isError": True}


def main():
    if not AGENT_ID:
        log_stderr("GAS_AGENT_ID env var required")
        sys.exit(2)
    if not SOCK_PATH:
        log_stderr("GAS_IPC_SOCKET env var required")
        sys.exit(2)

    for raw in sys.stdin:
        raw = raw.strip()
        if not raw:
            continue
        try:
            req = json.loads(raw)
        except Exception as e:
            log_stderr(f"parse err: {e}")
            continue
        method = req.get("method")
        rid = req.get("id")
        if method == "initialize":
            send({"jsonrpc": "2.0", "id": rid, "result": {
                "protocolVersion": "2024-11-05",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "a2a-bus", "version": "0.1"},
            }})
        elif method == "notifications/initialized":
            pass
        elif method == "tools/list":
            send({"jsonrpc": "2.0", "id": rid, "result": {"tools": TOOLS}})
        elif method == "tools/call":
            params = req.get("params") or {}
            name = params.get("name")
            args = params.get("arguments") or {}
            result = handle_tool_call(name, args)
            send({"jsonrpc": "2.0", "id": rid, "result": result})
        elif method in ("resources/list", "prompts/list"):
            key = method.split("/")[0]
            send({"jsonrpc": "2.0", "id": rid, "result": {key: []}})
        else:
            if rid is not None:
                send({"jsonrpc": "2.0", "id": rid, "error": {"code": -32601, "message": f"method not found: {method}"}})


if __name__ == "__main__":
    main()
