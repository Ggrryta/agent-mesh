"""
GAS 配置加载

两份配置文件,都位于 ~/.agent-gateway/:
  - config.yaml  主配置(gateway 地址 / ControlAPI 端口 / 数据目录)
  - agents.yaml  每个 agent 的启动参数

环境变量 GAS_CONFIG_DIR 可改写目录,方便测试。
"""
from __future__ import annotations

import os
import pathlib
from dataclasses import dataclass, field
from typing import List, Optional

import yaml

DEFAULT_CONFIG_DIR = "~/.agent-gateway"


def config_dir() -> pathlib.Path:
    d = os.environ.get("GAS_CONFIG_DIR", DEFAULT_CONFIG_DIR)
    return pathlib.Path(d).expanduser()


@dataclass
class GatewayConfig:
    url: str = "http://localhost:11556"
    timeout_ms: int = 30000
    # API Key 可用来做 GAS → Gateway 的系统级调用(heartbeat 兜底等)。
    # 正常 per-agent 请求用 agent 自己的 key。
    system_api_key: Optional[str] = None


@dataclass
class GASSection:
    control_api_host: str = "127.0.0.1"
    control_api_port: int = 7789
    data_dir: str = "~/.agent-gateway/data"
    log_level: str = "info"
    # 本 GAS 实例的唯一 ID。config.yaml 留空则启动时生成并持久化
    instance_id: Optional[str] = None

    def expanded_data_dir(self) -> pathlib.Path:
        return pathlib.Path(self.data_dir).expanduser()


@dataclass
class Config:
    gateway: GatewayConfig = field(default_factory=GatewayConfig)
    gas: GASSection = field(default_factory=GASSection)


@dataclass
class AgentConfig:
    """单个 agent 的静态配置(来自 agents.yaml)"""
    id: str
    host: str                        # claude-code / codex / gemini ...
    api_key: str                     # 该 agent 的 Gateway API Key
    workspace_dir: str               # Agent Core 的 cwd
    auto_start: bool = False
    system_prompt_addition: str = "" # 追加到 agent 的 system prompt
    # 留给未来扩展:对 Agent Core 的命令行 / 环境变量覆盖
    extra_env: dict = field(default_factory=dict)
    extra_args: List[str] = field(default_factory=list)


@dataclass
class AgentsFile:
    agents: List[AgentConfig] = field(default_factory=list)


class ConfigError(Exception):
    pass


def load_config(path: Optional[pathlib.Path] = None) -> Config:
    """加载 config.yaml。文件不存在时返回默认配置。"""
    if path is None:
        path = config_dir() / "config.yaml"
    if not path.exists():
        return Config()
    try:
        raw = yaml.safe_load(path.read_text()) or {}
    except Exception as e:
        raise ConfigError(f"parse {path}: {e}") from e
    return _build_config(raw)


def load_agents(path: Optional[pathlib.Path] = None) -> AgentsFile:
    """加载 agents.yaml。文件不存在时返回空列表。"""
    if path is None:
        path = config_dir() / "agents.yaml"
    if not path.exists():
        return AgentsFile()
    try:
        raw = yaml.safe_load(path.read_text()) or {}
    except Exception as e:
        raise ConfigError(f"parse {path}: {e}") from e
    return _build_agents(raw)


def save_agents(agents: AgentsFile, path: Optional[pathlib.Path] = None):
    if path is None:
        d = config_dir()
        d.mkdir(parents=True, exist_ok=True)
        path = d / "agents.yaml"
    out = {
        "agents": [
            {
                "id": a.id,
                "host": a.host,
                "api_key": a.api_key,
                "workspace_dir": a.workspace_dir,
                "auto_start": a.auto_start,
                "system_prompt_addition": a.system_prompt_addition,
                "extra_env": a.extra_env,
                "extra_args": a.extra_args,
            }
            for a in agents.agents
        ]
    }
    path.write_text(yaml.safe_dump(out, allow_unicode=True, sort_keys=False))


def _build_config(raw: dict) -> Config:
    gw = raw.get("gateway") or {}
    gas = raw.get("gas") or {}
    return Config(
        gateway=GatewayConfig(
            url=gw.get("url", "http://localhost:11556"),
            timeout_ms=int(gw.get("timeout_ms", 30000)),
            system_api_key=gw.get("system_api_key"),
        ),
        gas=GASSection(
            control_api_host=gas.get("control_api_host", "127.0.0.1"),
            control_api_port=int(gas.get("control_api_port", 7789)),
            data_dir=gas.get("data_dir", "~/.agent-gateway/data"),
            log_level=gas.get("log_level", "info"),
            instance_id=gas.get("instance_id"),
        ),
    )


def _build_agents(raw: dict) -> AgentsFile:
    lst = raw.get("agents") or []
    agents = []
    for item in lst:
        if not item.get("id"):
            raise ConfigError("agents.yaml entry missing id")
        if not item.get("host"):
            raise ConfigError(f"agents.yaml entry {item['id']} missing host")
        if not item.get("api_key"):
            raise ConfigError(f"agents.yaml entry {item['id']} missing api_key")
        if not item.get("workspace_dir"):
            raise ConfigError(f"agents.yaml entry {item['id']} missing workspace_dir")
        agents.append(AgentConfig(
            id=item["id"],
            host=item["host"],
            api_key=item["api_key"],
            workspace_dir=item["workspace_dir"],
            auto_start=bool(item.get("auto_start", False)),
            system_prompt_addition=item.get("system_prompt_addition", ""),
            extra_env=dict(item.get("extra_env") or {}),
            extra_args=list(item.get("extra_args") or []),
        ))
    return AgentsFile(agents=agents)
