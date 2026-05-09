#!/usr/bin/env python3
"""agent_feed.py —— 查看 agent 的最近活动(默认快照模式,非实时)

用法:
  agent_feed.py alice              最近 20 条
  agent_feed.py alice --tail 50    最近 50 条
  agent_feed.py alice --since 42   seq > 42 的记录
"""
from __future__ import annotations

import argparse
import json
import sys
import time

import ensure_daemon
from _common import DaemonError, die, http_call


ICONS = {
    "user_instruct": "🧑",
    "incoming":      "⬇️ ",
    "outgoing":      "⬆️ ",
    "tool_call":     "🔧",
    "thinking":      "💭",
    "log":           "📝",
    "status":        "ℹ️ ",
    "error":         "⚠️ ",
}


def _preview(data: dict, limit: int = 150) -> str:
    if not isinstance(data, dict):
        return str(data)[:limit]
    for k in ("text", "content", "event"):
        v = data.get(k)
        if isinstance(v, str):
            s = v.replace("\n", " ").strip()
            return s[:limit - 1] + "…" if len(s) > limit else s
    if "parts" in data and isinstance(data["parts"], list):
        texts = [p.get("text", "") for p in data["parts"] if isinstance(p, dict)]
        s = " ".join(t for t in texts if t).replace("\n", " ").strip()
        if s:
            return s[:limit - 1] + "…" if len(s) > limit else s
    s = json.dumps(data, ensure_ascii=False)
    return s[:limit - 1] + "…" if len(s) > limit else s


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("agent_id")
    ap.add_argument("--tail", type=int, default=20, help="显示最近 N 条")
    ap.add_argument("--since", type=int, default=0, help="seq > SINCE")
    args = ap.parse_args()

    st = ensure_daemon.status()
    if not st["health"]:
        print(f"(daemon 未运行,无 {args.agent_id} 的记录)")
        return 0

    try:
        data = http_call("GET",
                         f"/control/agents/{args.agent_id}/feed?since={args.since}&limit=500")
    except DaemonError as e:
        if e.status == 404:
            die(f"agent {args.agent_id} 未配置")
        die(f"查询失败: {e}")

    items = data.get("items") or []
    if not items:
        print(f"({args.agent_id} 暂无活动记录)")
        return 0

    # tail
    items = items[-args.tail:]
    for e in items:
        ts = e.get("ts_ms", 0) / 1000
        t = time.strftime("%H:%M:%S", time.localtime(ts))
        k = e.get("kind", "?")
        icon = ICONS.get(k, "•")
        preview = _preview(e.get("data", {}))
        print(f"[{t}] {icon} #{e.get('seq', '?'):<4} {k:<14} {preview}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
