#!/usr/bin/env python3
"""agent_offline.py —— 下线 agent

用法:
  agent_offline.py alice-dev      下线指定 agent
  agent_offline.py --all          下线本机所有 agent(daemon 继续跑)
"""
from __future__ import annotations

import argparse
import sys

import ensure_daemon
from _common import DaemonError, die, http_call, ok


def main():
    ap = argparse.ArgumentParser()
    group = ap.add_mutually_exclusive_group(required=True)
    group.add_argument("agent_id", nargs="?", help="要下线的 agent")
    group.add_argument("--all", action="store_true", help="下线所有 agent")
    args = ap.parse_args()

    st = ensure_daemon.status()
    if not st["health"]:
        print("(daemon 未运行,没有 agent 需要下线)")
        return 0

    if args.all:
        # 先列,再逐个 offline
        try:
            data = http_call("GET", "/control/agents")
        except DaemonError as e:
            die(f"查询失败: {e}")
        online_ids = [a["id"] for a in (data.get("agents") or [])
                      if a.get("state") in ("online", "starting")]
        if not online_ids:
            print("(没有在线 agent)")
            return 0
        for aid in online_ids:
            try:
                http_call("POST", f"/control/agents/{aid}/offline", timeout=15)
                ok(f"{aid} 已下线")
            except DaemonError as e:
                print(f"⚠️  {aid} 下线失败: {e}", file=sys.stderr)
        return 0

    try:
        http_call("POST", f"/control/agents/{args.agent_id}/offline", timeout=15)
    except DaemonError as e:
        if e.status == 404:
            die(f"agent {args.agent_id} 未配置")
        die(f"下线失败: {e}")
    ok(f"agent {args.agent_id} 已下线")
    return 0


if __name__ == "__main__":
    sys.exit(main())
