#!/usr/bin/env python3
"""friend_list.py —— 列出已建立的好友"""
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
        r = gateway_call("GET", "/friendships", agent_id=me)
    except GatewayError as e:
        print(f"❌ 查询失败: {e}", file=sys.stderr)
        return 1
    items = r.get("data") or []
    if not items:
        print(f"({me} 还没有好友)")
        return 0
    fmt = "{:<6}  {:<24}  {:<20}"
    print(fmt.format("id", "好友", "建立时间"))
    print("-" * 60)
    for f in items:
        print(fmt.format(str(f.get("id", "")),
                         f.get("friend", "")[:24],
                         (f.get("accepted_at") or "")[:20]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
