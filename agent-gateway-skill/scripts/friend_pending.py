#!/usr/bin/env python3
"""friend_pending.py —— 查看收到的加好友请求"""
from __future__ import annotations

import argparse
import sys

from _gateway import GatewayError, gateway_call, resolve_self_agent


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--as", dest="as_agent", default=None)
    args = ap.parse_args()

    me = resolve_self_agent(args.as_agent)
    try:
        r = gateway_call("GET", "/friendships/pending", agent_id=me)
    except GatewayError as e:
        print(f"❌ 查询失败: {e}", file=sys.stderr); return 1
    items = r.get("data") or []
    if not items:
        print("(无待处理请求)")
        return 0
    fmt = "{:<6}  {:<20}  {:<30}"
    print(fmt.format("id", "来自", "理由"))
    print("-" * 60)
    for f in items:
        print(fmt.format(str(f.get("id", "")),
                         f.get("initiator", "")[:20],
                         (f.get("reason") or "")[:30]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
