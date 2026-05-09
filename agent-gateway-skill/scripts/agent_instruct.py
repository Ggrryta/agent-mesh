#!/usr/bin/env python3
"""agent_instruct.py —— 向 agent 下发一条指令(作为 user_input 注入)"""
from __future__ import annotations

import argparse
import sys

import ensure_daemon
from _common import DaemonError, die, http_call, ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("agent_id")
    ap.add_argument("text", nargs="+", help="指令文本(可带空格)")
    args = ap.parse_args()

    ensure_daemon.ensure()
    text = " ".join(args.text)
    try:
        http_call("POST", f"/control/agents/{args.agent_id}/instruct",
                  body={"text": text}, timeout=15)
    except DaemonError as e:
        if e.status == 409:
            die(f"agent {args.agent_id} 未上线。请先 '上线 {args.agent_id}'")
        if e.status == 404:
            die(f"agent {args.agent_id} 未配置")
        die(f"下发失败: {e}")
    ok(f"已下发给 {args.agent_id}: {text}")
    print("提示:使用 '查看 {0} 最近活动' 跟踪执行".format(args.agent_id))
    return 0


if __name__ == "__main__":
    sys.exit(main())
