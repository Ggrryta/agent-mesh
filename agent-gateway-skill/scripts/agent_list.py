#!/usr/bin/env python3
"""agent_list.py —— 列出本机所有 agent"""
from __future__ import annotations

import sys

import ensure_daemon
from _common import DaemonError, die, http_call


def main():
    st = ensure_daemon.status()
    if not st["alive"] and not st["health"]:
        print("(daemon 未运行,没有 agent)")
        return 0

    try:
        data = http_call("GET", "/control/agents")
    except DaemonError as e:
        die(f"查询失败: {e}")
    items = data.get("agents") or []
    if not items:
        print("(尚未创建 agent)")
        return 0

    fmt = "{:<20}  {:<12}  {:<10}  {:<40}"
    print(fmt.format("agent_id", "host", "state", "workspace"))
    print("-" * 90)
    for a in items:
        print(fmt.format(
            a.get("id", "")[:20],
            a.get("host", "")[:12],
            a.get("state", "")[:10],
            a.get("workspace_dir", "")[:40],
        ))
    return 0


if __name__ == "__main__":
    sys.exit(main())
