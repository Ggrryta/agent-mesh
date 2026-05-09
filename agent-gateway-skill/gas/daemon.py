"""
GAS daemon 主循环。

职责:
  1. 初始化日志、加载配置、创建 AgentManager
  2. 启动 ControlAPI
  3. 启动 auto_start agent(M5 真启)
  4. 阻塞等待 SIGINT/SIGTERM
  5. 优雅退出:停所有 agent、关 ControlAPI
"""
from __future__ import annotations

import asyncio
import os
import signal
import uuid

from gas import __version__
from gas.config import config_dir, load_agents, load_config, save_agents
from gas.control_api import ControlAPI
from gas.feed_storage import FeedStorage
from gas.log import get_logger, init_logger
from gas.manager import AgentManager

log = get_logger("gas.daemon")


def _ensure_instance_id(cfg):
    if cfg.gas.instance_id:
        return cfg
    # 生成并写入 config.yaml
    cfg.gas.instance_id = str(uuid.uuid4())
    import yaml
    d = config_dir()
    d.mkdir(parents=True, exist_ok=True)
    p = d / "config.yaml"
    raw = {}
    if p.exists():
        try:
            raw = yaml.safe_load(p.read_text()) or {}
        except Exception:
            raw = {}
    raw.setdefault("gas", {})["instance_id"] = cfg.gas.instance_id
    p.write_text(yaml.safe_dump(raw, allow_unicode=True, sort_keys=False))
    log.info("generated instance_id", extra={"instance_id": cfg.gas.instance_id})
    return cfg


async def run_daemon() -> int:
    cfg = load_config()
    init_logger(cfg.gas.log_level)
    cfg = _ensure_instance_id(cfg)
    log.info("gas daemon starting",
             extra={"version": __version__, "instance_id": cfg.gas.instance_id,
                    "gateway": cfg.gateway.url})

    data_dir = cfg.gas.expanded_data_dir()
    data_dir.mkdir(parents=True, exist_ok=True)

    agents = load_agents()
    log.info("agents loaded", extra={"count": len(agents.agents)})

    feed = FeedStorage(data_dir=data_dir)
    manager = AgentManager(cfg, agents, feed)

    stop_event = asyncio.Event()

    def _request_shutdown():
        """由 HTTP /control/shutdown 触发,或由 SIGINT/SIGTERM 触发"""
        if not stop_event.is_set():
            stop_event.set()

    api = ControlAPI(cfg, manager, feed, shutdown_callback=_request_shutdown)
    await api.start()

    # 启动 auto_start agent(M5 真启)
    await manager.start_auto_agents()

    def _signal_stop():
        if not stop_event.is_set():
            log.info("signal received, stopping")
            stop_event.set()

    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(sig, _signal_stop)
        except NotImplementedError:
            # Windows 不支持 add_signal_handler,忽略
            pass

    log.info("gas daemon ready")
    await stop_event.wait()

    log.info("gas daemon shutting down")
    await manager.stop_all()
    await api.stop()
    feed.close()
    log.info("gas daemon stopped")
    return 0


# 工具函数:从命令行添加一个 agent(未来扩展成完整 CLI 子命令)
def agent_add(id_: str, host: str, api_key: str, workspace_dir: str,
              auto_start: bool = False, system_prompt_addition: str = ""):
    from gas.config import AgentConfig
    agents = load_agents()
    # 去重
    for a in agents.agents:
        if a.id == id_:
            raise ValueError(f"agent {id_} already exists")
    agents.agents.append(AgentConfig(
        id=id_,
        host=host,
        api_key=api_key,
        workspace_dir=os.path.abspath(os.path.expanduser(workspace_dir)),
        auto_start=auto_start,
        system_prompt_addition=system_prompt_addition,
    ))
    save_agents(agents)
    return agents.agents[-1]


def agent_remove(id_: str) -> bool:
    agents = load_agents()
    n = len(agents.agents)
    agents.agents = [a for a in agents.agents if a.id != id_]
    if len(agents.agents) == n:
        return False
    save_agents(agents)
    return True
