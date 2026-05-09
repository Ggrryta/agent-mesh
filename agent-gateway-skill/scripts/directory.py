#!/usr/bin/env python3
"""directory.py —— 浏览 Gateway 全局 agent 目录"""
from __future__ import annotations

import argparse
import sys
import urllib.parse

from _gateway import GatewayError, gateway_call


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("keyword", nargs="?", default="")
    args = ap.parse_args()

    path = "/agents"
    if args.keyword:
        path += "?keyword=" + urllib.parse.quote(args.keyword)
    try:
        r = gateway_call("GET", path)
    except GatewayError as e:
        print(f"❌ 查询失败: {e}", file=sys.stderr)
        return 1

    data = r.get("data") or {}
    if isinstance(data, dict):
        items = data.get("items") or []
    else:
        items = data
    if not items:
        print("(目录为空)")
        return 0
    fmt = "{:<24}  {:<20}  {:<10}  {:<30}"
    print(fmt.format("agent_id", "名称", "投递", "描述"))
    print("-" * 90)
    for a in items:
        dmap = {0: "push", 1: "pull"}
        print(fmt.format(a.get("agent_id", "")[:24], (a.get("name") or "")[:20],
                         dmap.get(a.get("delivery_mode"), str(a.get("delivery_mode", ""))),
                         (a.get("description") or "")[:30]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
