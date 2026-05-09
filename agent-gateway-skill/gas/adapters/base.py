"""Adapter 接口定义。M5 实现 ClaudeCodeAdapter。"""
from __future__ import annotations

import asyncio
from typing import Protocol, AsyncIterator

from gas.config import AgentConfig
from gas.events import InputEvent, OutputEvent


class AgentCoreAdapter(Protocol):
    """
    Agent Core 宿主适配器。每种 host (claude-code / codex / gemini) 各一个实现。

    所有方法都是 async,因为需要和子进程的 stdin/stdout 做异步 IO。
    """

    async def spawn(self, config: AgentConfig, mcp_config_path: str) -> asyncio.subprocess.Process:
        """启动 Agent Core 子进程,返回 Process 句柄。"""
        ...

    async def send_input(self, proc: asyncio.subprocess.Process, event: InputEvent) -> None:
        """把 InputEvent 格式化成宿主能识别的 stdin 输入。"""
        ...

    def parse_output(self, line: bytes) -> OutputEvent | None:
        """把宿主 stdout 一行输出解析成 OutputEvent。返回 None 表示忽略。"""
        ...

    async def graceful_stop(self, proc: asyncio.subprocess.Process) -> None:
        """优雅停机:关 stdin,等进程自己退出。"""
        ...


def get_adapter(host: str) -> AgentCoreAdapter:
    """按 host 名字返回对应 adapter。未实现的 host 抛 NotImplementedError。"""
    from gas.adapters.claude_code import ClaudeCodeAdapter
    if host == "claude-code":
        return ClaudeCodeAdapter()
    raise NotImplementedError(f"adapter for host {host!r} not implemented yet")


# iter helper
async def read_lines(stream: asyncio.StreamReader) -> AsyncIterator[bytes]:
    while True:
        line = await stream.readline()
        if not line:
            return
        yield line
