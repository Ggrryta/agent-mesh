#!/usr/bin/env python3
"""agent_online.py —— 上线 agent(拉起 Agent Core)"""
from __future__ import annotations

import argparse
import sys

import ensure_daemon
from _common import DaemonError, die, http_call, ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("agent_id", help="要上线的 agent")
    args = ap.parse_args()

    # 确保 daemon 在跑
    r = ensure_daemon.ensure()
    if r["status"] == "started":
        print(f"ℹ️  后台服务已自动启动 (pid {r['pid']})")

    try:
        http_call("POST", f"/control/agents/{args.agent_id}/online", timeout=30)
    except DaemonError as e:
        if e.status == 404:
            die(f"agent {args.agent_id} 未配置。请先 '创建 agent {args.agent_id}'")
        die(f"上线失败: {e}")

    ok(f"agent {args.agent_id} 已上线")
    print("提示:告诉 Claude '告诉 <agent> 去做某事' 下发指令,"
          "或 '加 <other> 为好友' 建立协作关系")
    return 0


if __name__ == "__main__":
    sys.exit(main())
