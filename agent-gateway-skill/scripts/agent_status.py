#!/usr/bin/env python3
"""agent_status.py —— 查 agent 状态"""
from __future__ import annotations

import argparse
import sys

import ensure_daemon
from _common import DaemonError, die, http_call, print_json


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("agent_id")
    args = ap.parse_args()

    st = ensure_daemon.status()
    if not st["health"]:
        print_json({"id": args.agent_id, "state": "offline",
                    "note": "daemon 未运行"})
        return 0
    try:
        data = http_call("GET", f"/control/agents/{args.agent_id}/status")
    except DaemonError as e:
        if e.status == 404:
            die(f"agent {args.agent_id} 未配置")
        die(f"查询失败: {e}")
    print_json(data)
    return 0


if __name__ == "__main__":
    sys.exit(main())
