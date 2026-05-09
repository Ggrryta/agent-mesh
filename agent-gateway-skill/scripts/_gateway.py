#!/usr/bin/env python3
"""
_gateway.py —— 直接调 Gateway 的辅助

好友关系管理不需要经过 GAS daemon,直接 HTTP 调 Gateway REST。
"""
from __future__ import annotations

import json
import sys
import urllib.error
import urllib.request
from typing import Any

from _common import DaemonError as _DErr, die, load_skill_config


class GatewayError(_DErr):
    pass


def gateway_call(method: str, path: str, body: Any = None,
                 agent_id: str | None = None, api_key: str | None = None,
                 timeout: float = 15.0) -> dict[str, Any]:
    cfg = load_skill_config()
    base = cfg.get("gateway_url")
    if not base:
        die("skill 未初始化。请先告诉 Claude '接入 Agent Gateway 地址 <url>'")
    key = api_key or cfg.get("api_key")
    if not key:
        die("没有 API Key,请先告诉 Claude '我的 API Key 是 agw_xxx'")

    url = base.rstrip("/") + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json")
    if agent_id:
        req.add_header("X-Agent-ID", agent_id)
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        raise GatewayError(e.code, body) from e
    except urllib.error.URLError as e:
        raise GatewayError(0, f"cannot reach gateway at {url}: {e}") from e
    raw = resp.read().decode("utf-8", errors="replace")
    return json.loads(raw) if raw else {}


def resolve_self_agent(cli_arg: str | None) -> str:
    """从 --as 参数或 default_agent 获取当前操作的 agent 身份"""
    if cli_arg:
        return cli_arg
    cfg = load_skill_config()
    dflt = cfg.get("default_agent")
    if not dflt:
        die("未指定 --as,也没有默认 agent。请在初始化时 '设置默认 agent 为 <id>',"
            "或命令行显式传入 --as")
    return dflt
