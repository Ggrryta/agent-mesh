#!/usr/bin/env python3
"""daemon_stop.py —— 关闭后台 daemon(会先 offline 所有 agent)"""
from __future__ import annotations

import os
import sys
import time

import ensure_daemon
from _common import DaemonError, PID_FILE, http_call, ok


def _wait_gone(pid: int, timeout: float = 5.0) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            os.kill(pid, 0)
            time.sleep(0.1)
        except OSError:
            return True
    return False


def main():
    st = ensure_daemon.status()
    if not st["alive"] and not st["health"]:
        print("(daemon 未运行)")
        return 0

    # 先 offline 所有
    try:
        data = http_call("GET", "/control/agents", timeout=5)
        online_ids = [a["id"] for a in (data.get("agents") or [])
                      if a.get("state") in ("online", "starting")]
        for aid in online_ids:
            try:
                http_call("POST", f"/control/agents/{aid}/offline", timeout=15)
                print(f"  已下线 {aid}")
            except DaemonError as e:
                print(f"  ⚠️  {aid} 下线失败: {e}", file=sys.stderr)
    except DaemonError as e:
        print(f"  ⚠️  列出 agent 失败(继续停 daemon): {e}", file=sys.stderr)

    # 请求 daemon 自己退出
    try:
        http_call("POST", "/control/shutdown", timeout=3)
    except DaemonError as e:
        print(f"  ⚠️  shutdown 请求失败: {e}", file=sys.stderr)

    # 等进程消失
    pid = st["pid"]
    if pid and not _wait_gone(pid, timeout=8):
        print(f"  ⚠️  daemon(pid={pid}) 5s 未退出,发 SIGTERM", file=sys.stderr)
        try:
            os.kill(pid, 15)
        except OSError:
            pass
        if not _wait_gone(pid, timeout=3):
            try:
                os.kill(pid, 9)
            except OSError:
                pass

    try:
        PID_FILE.unlink(missing_ok=True)
    except Exception:
        pass

    ok("后台服务已停止")
    return 0


if __name__ == "__main__":
    sys.exit(main())
