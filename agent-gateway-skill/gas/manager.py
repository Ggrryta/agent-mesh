"""
AgentManager —— 管理本机所有 AgentRunner 的生命周期
"""
from __future__ import annotations

import asyncio
from dataclasses import dataclass, field
from enum import Enum
from typing import Optional

from gas.config import AgentConfig, AgentsFile, Config
from gas.events import InputEvent
from gas.feed_storage import FeedStorage
from gas.ipc import IPCMethodError, IPCServer, default_socket_path
from gas.log import get_logger
from gas.runner import AgentRunner

log = get_logger("gas.manager")


class AgentState(str, Enum):
    OFFLINE = "offline"
    STARTING = "starting"
    ONLINE = "online"
    STOPPING = "stopping"
    FAILED = "failed"


@dataclass
class AgentEntry:
    config: AgentConfig
    state: AgentState = AgentState.OFFLINE
    runner: Optional[AgentRunner] = None
    last_error: str = ""
    ipc: Optional[IPCServer] = None
    extras: dict = field(default_factory=dict)


class AgentManager:
    def __init__(self, cfg: Config, agents: AgentsFile, feed: FeedStorage):
        self.cfg = cfg
        self.feed = feed
        self._entries: dict[str, AgentEntry] = {}
        for a in agents.agents:
            self._entries[a.id] = AgentEntry(config=a)

    # ── 查询 ───────────────────────────────

    def list(self) -> list[AgentEntry]:
        return list(self._entries.values())

    def get(self, agent_id: str) -> Optional[AgentEntry]:
        return self._entries.get(agent_id)

    # ── 配置层增减(写盘由 daemon CLI 负责) ──

    def add(self, config: AgentConfig) -> AgentEntry:
        if config.id in self._entries:
            return self._entries[config.id]
        e = AgentEntry(config=config)
        self._entries[config.id] = e
        return e

    def remove(self, agent_id: str) -> bool:
        return self._entries.pop(agent_id, None) is not None

    # ── 运行时 ─────────────────────────────

    async def start_agent(self, agent_id: str) -> None:
        e = self._entries.get(agent_id)
        if e is None:
            raise ValueError(f"agent {agent_id} not configured")
        if e.state == AgentState.ONLINE:
            return
        if e.state == AgentState.STARTING:
            raise RuntimeError(f"agent {agent_id} is starting")

        data_dir = self.cfg.gas.expanded_data_dir()
        sock_path = default_socket_path(data_dir, agent_id)

        # 1. 启 IPC server,handler 暂时 placeholder,Runner 就绪后再替换
        runner: Optional[AgentRunner] = None

        async def handler(called_agent_id: str, method: str, params: dict):
            if runner is None:
                raise IPCMethodError(503, "runner not ready")
            if called_agent_id and called_agent_id != agent_id:
                raise IPCMethodError(403, f"ipc agent mismatch: {called_agent_id}")
            return await runner.handle_ipc(method, params)

        ipc = IPCServer(socket_path=sock_path, handler=handler)
        await ipc.start()

        e.state = AgentState.STARTING
        e.ipc = ipc
        try:
            runner = AgentRunner(
                config=e.config,
                gas_config=self.cfg,
                data_dir=data_dir,
                feed=self.feed,
                ipc_socket_path=sock_path,
            )
            await runner.start()
            e.runner = runner
            e.state = AgentState.ONLINE
            e.last_error = ""
            log.info("agent online", extra={"agent_id": agent_id})
        except Exception as ex:
            log.exception("agent start failed", extra={"agent_id": agent_id})
            e.state = AgentState.FAILED
            e.last_error = str(ex)
            try:
                await ipc.stop()
            except Exception:
                pass
            e.ipc = None
            raise

    async def stop_agent(self, agent_id: str) -> None:
        e = self._entries.get(agent_id)
        if e is None:
            return
        if e.state == AgentState.OFFLINE:
            return
        e.state = AgentState.STOPPING
        if e.runner:
            try:
                await e.runner.stop()
            except Exception as ex:
                log.warning("runner stop error", extra={"agent_id": agent_id, "err": str(ex)})
            e.runner = None
        if e.ipc:
            try:
                await e.ipc.stop()
            except Exception:
                pass
            e.ipc = None
        e.state = AgentState.OFFLINE
        log.info("agent offline", extra={"agent_id": agent_id})

    async def send_to_agent(self, agent_id: str, event: InputEvent) -> None:
        e = self._entries.get(agent_id)
        if e is None:
            raise ValueError(f"agent {agent_id} not found")
        if e.state != AgentState.ONLINE or e.runner is None:
            raise RuntimeError(f"agent {agent_id} not online")
        await e.runner.send_input(event)

    async def start_auto_agents(self) -> None:
        tasks = []
        for e in self._entries.values():
            if e.config.auto_start:
                tasks.append(self._safe_start(e.config.id))
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)

    async def _safe_start(self, agent_id: str):
        try:
            await self.start_agent(agent_id)
        except Exception as e:
            log.warning("auto_start failed", extra={"agent_id": agent_id, "err": str(e)})

    async def stop_all(self) -> None:
        ids = [a for a, e in self._entries.items() if e.state != AgentState.OFFLINE]
        if not ids:
            return
        log.info("stopping all agents", extra={"count": len(ids)})
        await asyncio.gather(*[self.stop_agent(a) for a in ids], return_exceptions=True)
