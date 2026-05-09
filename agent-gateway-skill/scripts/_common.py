#!/usr/bin/env python3
"""
_common.py —— Skill 脚本共享库

职责:
  - 解析 skill 安装位置、配置路径
  - 提供 HTTP 调用 GAS daemon 的简易封装
  - 所有脚本首先 import 这里,统一错误格式

所有 skill 脚本都在 skill 自己的 venv 下运行(见 install.sh 和 ensure_daemon.py)。
"""
from __future__ import annotations

import json
import os
import pathlib
import sys
import urllib.error
import urllib.request
from typing import Any


# ── 路径 ────────────────────────────────────────────────────────

SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
SKILL_ROOT = SCRIPT_DIR.parent
VENV_DIR = SKILL_ROOT / ".venv"
DAEMON_PKG_ROOT = SKILL_ROOT  # 让 `python -c "import gas"` 能找到 gas/

# 用户数据目录(和旧 GAS 保持一致)
HOME_CONFIG_DIR = pathlib.Path(os.environ.get("GAS_CONFIG_DIR",
                                              "~/.agent-gateway")).expanduser()
PID_FILE = HOME_CONFIG_DIR / "daemon.pid"
DAEMON_LOG = HOME_CONFIG_DIR / "daemon.log"
SKILL_CONFIG = HOME_CONFIG_DIR / "skill.json"  # skill 自己的配置(区别于 config.yaml)


# ── skill 配置 ──────────────────────────────────────────────────

def load_skill_config() -> dict[str, Any]:
    if not SKILL_CONFIG.exists():
        return {}
    try:
        return json.loads(SKILL_CONFIG.read_text())
    except Exception:
        return {}


def save_skill_config(**kv) -> None:
    HOME_CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    cur = load_skill_config()
    for k, v in kv.items():
        if v is not None:
            cur[k] = v
    SKILL_CONFIG.write_text(json.dumps(cur, ensure_ascii=False, indent=2))


def get_default_control_url() -> str:
    cfg = load_skill_config()
    port = cfg.get("control_api_port", 7789)
    return f"http://127.0.0.1:{port}"


# ── HTTP 辅助 ───────────────────────────────────────────────────

class DaemonError(Exception):
    def __init__(self, status: int, body: str):
        self.status = status
        self.body = body
        super().__init__(f"daemon {status}: {body[:300]}")


def http_call(method: str, path: str, body: Any = None,
              timeout: float = 15.0) -> dict[str, Any]:
    url = get_default_control_url().rstrip("/") + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={"Content-Type": "application/json",
                                          "Accept": "application/json"})
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        raise DaemonError(e.code, body) from e
    except urllib.error.URLError as e:
        raise DaemonError(0, f"cannot reach daemon at {url}: {e}") from e
    raw = resp.read().decode("utf-8", errors="replace")
    return json.loads(raw) if raw else {}


# ── 输出辅助 ────────────────────────────────────────────────────

def die(msg: str, code: int = 1):
    print(f"❌ {msg}", file=sys.stderr)
    sys.exit(code)


def ok(msg: str):
    print(f"✅ {msg}")


def info(msg: str):
    print(f"ℹ️  {msg}")


def print_json(obj: Any):
    print(json.dumps(obj, ensure_ascii=False, indent=2))


def require_config(*required_keys: str) -> dict[str, Any]:
    cfg = load_skill_config()
    missing = [k for k in required_keys if not cfg.get(k)]
    if missing:
        die(f"skill 未初始化,缺少 {','.join(missing)}。请先告诉 Claude:"
            f"'初始化 Agent Gateway,地址 <url>' 或 '设置 API Key <key>'")
    return cfg
