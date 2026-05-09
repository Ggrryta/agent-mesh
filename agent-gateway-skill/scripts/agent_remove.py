#!/usr/bin/env python3
"""agent_remove.py —— 从本机 daemon 移除 agent 配置(要求先 offline)"""
from __future__ import annotations

import argparse
import sys

import ensure_daemon
from _common import DaemonError, die, http_call, ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("agent_id")
    args = ap.parse_args()

    ensure_daemon.ensure()
    try:
        http_call("DELETE", f"/control/agents/{args.agent_id}")
    except DaemonError as e:
        if e.status == 404:
            die(f"agent {args.agent_id} 不存在")
        if e.status == 409:
            die(f"{args.agent_id} 还在线,请先 '下线 {args.agent_id}'")
        die(f"删除失败: {e}")
    ok(f"agent {args.agent_id} 已从本机移除")
    print("提示:该操作只清理本机配置,Gateway 侧的 agent 记录仍保留。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
