#!/usr/bin/env python3
"""friend_action.py —— accept / reject / revoke 通用处理"""
from __future__ import annotations

import argparse
import sys

from _common import ok
from _gateway import GatewayError, gateway_call, resolve_self_agent


VERB_MAP = {"accept": "已接受", "reject": "已拒绝", "revoke": "已撤销"}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("action", choices=list(VERB_MAP))
    ap.add_argument("friendship_id", type=int)
    ap.add_argument("--as", dest="as_agent", default=None)
    args = ap.parse_args()

    me = resolve_self_agent(args.as_agent)
    try:
        gateway_call("POST", f"/friendships/{args.friendship_id}/{args.action}",
                     agent_id=me)
    except GatewayError as e:
        print(f"❌ {args.action} 失败: {e}", file=sys.stderr)
        return 1
    ok(f"{VERB_MAP[args.action]} 好友关系 {args.friendship_id}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
