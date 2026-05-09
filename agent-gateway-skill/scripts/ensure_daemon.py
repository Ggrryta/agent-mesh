#!/usr/bin/env python3
"""
ensure_daemon.py —— 确保 GAS daemon 在后台运行

所有需要和 daemon 交互的 skill 脚本,第一步都调此文件(直接 import + call).

工作流程:
  1. 读 PID file
  2. PID 存在且存活 → 什么都不做,返回
  3. PID 不存在或已死 → 用 skill 自带的 venv Python 拉起 daemon
     - nohup + setsid,完全脱离当前 shell
     - stdin/stdout/stderr 重定向到日志文件
     - 写入新 PID
  4. 轮询 daemon 健康端点,就绪后返回

脚本也支持 CLI 调用:
  python3 ensure_daemon.py          确保在跑
  python3 ensure_daemon.py --status 查状态不拉起
"""
from __future__ import annotations

import argparse
import os
import pathlib
import subprocess
import sys
import time
import urllib.error
import urllib.request

from _common import (
    DAEMON_LOG,
    HOME_CONFIG_DIR,
    PID_FILE,
    SKILL_ROOT,
    VENV_DIR,
    get_default_control_url,
    load_skill_config,
)


def _venv_python() -> str:
    """skill 自带 venv 的 Python。不存在时回落到 sys.executable"""
    for candidate in (VENV_DIR / "bin" / "python3", VENV_DIR / "bin" / "python",
                      VENV_DIR / "Scripts" / "python.exe"):
        if candidate.exists():
            return str(candidate)
    return sys.executable


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def _read_pid() -> int | None:
    if not PID_FILE.exists():
        return None
    try:
        pid = int(PID_FILE.read_text().strip())
    except (ValueError, OSError):
        return None
    if not _pid_alive(pid):
        return None
    return pid


def _health_ok(url: str, timeout: float = 1.0) -> bool:
    try:
        resp = urllib.request.urlopen(url.rstrip("/") + "/control/health", timeout=timeout)
        return resp.status == 200
    except Exception:
        return False


def _spawn_daemon() -> int:
    """nohup 拉起 daemon。返回新 PID。"""
    HOME_CONFIG_DIR.mkdir(parents=True, exist_ok=True)

    # 先写入 gas 配置(如果没有)
    config_yaml = HOME_CONFIG_DIR / "config.yaml"
    agents_yaml = HOME_CONFIG_DIR / "agents.yaml"
    cfg = load_skill_config()

    if not config_yaml.exists():
        port = cfg.get("control_api_port", 7789)
        gateway_url = cfg.get("gateway_url", "http://localhost:11556")
        config_yaml.write_text(
            f"gateway:\n  url: {gateway_url}\n"
            f"gas:\n  control_api_host: 127.0.0.1\n  control_api_port: {port}\n"
            f"  data_dir: {HOME_CONFIG_DIR / 'data'}\n  log_level: info\n"
        )
    if not agents_yaml.exists():
        agents_yaml.write_text("agents: []\n")

    py = _venv_python()
    env = {
        **os.environ,
        "PYTHONPATH": str(SKILL_ROOT),  # 让 import gas 生效
        "GAS_CONFIG_DIR": str(HOME_CONFIG_DIR),
        # 指纹:让 cleanup.py 能精确识别这是 agent-gateway 管理的进程,
        # 不会被误认为用户其他 python 进程。
        "AGENT_GATEWAY_MANAGED": "1",
        "AGENT_GATEWAY_ROLE": "daemon",
    }
    log_file = open(DAEMON_LOG, "ab")
    # setsid 脱离终端,nohup 行为由 shell 或 os.setsid 模拟
    kwargs = {
        "stdin": subprocess.DEVNULL,
        "stdout": log_file,
        "stderr": log_file,
        "env": env,
        "start_new_session": True,  # 等价 setsid,脱离父进程
    }
    proc = subprocess.Popen([py, "-m", "gas", "run"], **kwargs)
    PID_FILE.write_text(str(proc.pid))
    return proc.pid


def _check_skill_update_silent() -> str | None:
    """静默检查是否有 skill 新版。返回远端版本号(有更新)或 None(无更新/检查失败)。
    失败不影响 daemon 运行,纯提示用途。"""
    try:
        cfg = load_skill_config()
        gateway_url = cfg.get("gateway_url")
        if not gateway_url:
            return None
        # 读本地 VERSION
        vf = SKILL_ROOT / "VERSION"
        if not vf.exists():
            return None
        local = vf.read_text().strip()
        # 查远端
        url = gateway_url.rstrip("/") + "/skill/version"
        resp = urllib.request.urlopen(url, timeout=3.0)
        data = (lambda raw: __import__("json").loads(raw).get("data", {}))(resp.read().decode("utf-8"))
        remote = data.get("version", "")
        if remote and remote != local:
            return remote
    except Exception:
        pass
    return None


def ensure(timeout: float = 10.0) -> dict[str, object]:
    """确保 daemon 在跑。返回 {status, pid, url, update_available?}。"""
    url = get_default_control_url()

    pid = _read_pid()
    if pid is not None and _health_ok(url):
        result = {"status": "already_running", "pid": pid, "url": url}
        if rv := _check_skill_update_silent():
            result["update_available"] = rv
        return result

    # 没跑或 PID stale,起一个
    if pid is not None:
        # 进程活但 health 不通,给几秒再判断
        deadline = time.time() + 3
        while time.time() < deadline:
            if _health_ok(url):
                result = {"status": "already_running", "pid": pid, "url": url}
                if rv := _check_skill_update_silent():
                    result["update_available"] = rv
                return result
            time.sleep(0.2)
        # 仍然不通 → 视为挂掉,不去 kill(可能是用户开了别的),重新起
    pid = _spawn_daemon()

    # 等 ready
    deadline = time.time() + timeout
    while time.time() < deadline:
        if _health_ok(url):
            result = {"status": "started", "pid": pid, "url": url}
            if rv := _check_skill_update_silent():
                result["update_available"] = rv
            return result
        # 看看进程是否还活
        if not _pid_alive(pid):
            log_tail = ""
            try:
                log_tail = "\n".join(DAEMON_LOG.read_text().splitlines()[-20:])
            except Exception:
                pass
            raise RuntimeError(
                f"daemon spawned but died immediately (pid {pid}). log tail:\n{log_tail}")
        time.sleep(0.2)
    raise RuntimeError(f"daemon did not become healthy within {timeout}s")


def status() -> dict[str, object]:
    pid = _read_pid()
    url = get_default_control_url()
    return {
        "pid": pid,
        "alive": pid is not None and _pid_alive(pid),
        "health": _health_ok(url),
        "url": url,
    }


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--status", action="store_true")
    args = ap.parse_args()

    if args.status:
        import json as _json
        print(_json.dumps(status(), ensure_ascii=False, indent=2))
    else:
        r = ensure()
        print(f"✅ daemon {r['status']} (pid={r['pid']}, url={r['url']})")
        if r.get("update_available"):
            print(f"🔔 有 skill 新版可升级: {r['update_available']}")
            print(f"   执行 '升级 agent-gateway' 或 python3 scripts/self_update.py")
