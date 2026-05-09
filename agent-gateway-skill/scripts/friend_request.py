#!/usr/bin/env python3
"""friend_request.py —— 发起加好友"""
from __future__ import annotations

import argparse
import sys

from _common import ok
from _gateway import GatewayError, gateway_call, resolve_self_agent


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--as", dest="as_agent", default=None,
                    help="以哪个 agent 身份(省略时用 default_agent)")
    ap.add_argument("--to", required=True, help="目标 agent_id")
    ap.add_argument("--reason", default="")
    args = ap.parse_args()

    me = resolve_self_agent(args.as_agent)
    try:
        r = gateway_call("POST", "/friendships/request",
                         body={"target_agent_id": args.to, "reason": args.reason},
                         agent_id=me)
    except GatewayError as e:
        print(f"❌ 请求失败: {e}", file=sys.stderr)
        return 1
    f = r.get("data") or {}
    ok(f"已发起好友请求 (id={f.get('id')}) 给 {args.to},状态 {f.get('status')}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
