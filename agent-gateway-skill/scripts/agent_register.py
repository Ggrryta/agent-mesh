#!/usr/bin/env python3
"""
agent_register.py —— 创建 / 注册新 agent

用户意图:
  "创建 agent alice-dev,工作目录 ~/projects/work"
    → agent_register.py alice-dev --workspace ~/projects/work

流程:
  1. 读 skill 配置(确保有 gateway_url + api_key)
  2. ensure_daemon(首次触发时会拉起后台)
  3. 通过 GAS 的 /control/agents POST,把 agent 加入本机 agents.yaml
  4. 不自动 online —— 给用户一步"上线"的明确动作

说明:
  本脚本不调 Gateway 的 /agents/register —— Gateway 侧创建由 Web 前端
  完成(或另行提供 register 脚本)。这里只管本机 agent 配置。
  (MVP 简化:假设用户已在 Gateway 创建好 agent,只差绑定到本机 daemon。)
"""
from __future__ import annotations

import argparse
import pathlib
import sys

import ensure_daemon
from _common import DaemonError, die, http_call, ok, require_config


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("agent_id", help="agent_id(须与 Gateway 上已注册的一致)")
    ap.add_argument("--host", default="claude-code", choices=["claude-code", "codex", "gemini"])
    ap.add_argument("--workspace", required=True, help="Agent Core 工作目录")
    ap.add_argument("--api-key", default=None,
                    help="账号的 API Key(agw_ 开头)。一个账号下所有 agent 共用这把。"
                         "不指定时用 skill 全局 api_key。")
    ap.add_argument("--auto-start", action="store_true",
                    help="daemon 启动时自动上线该 agent")
    ap.add_argument("--system-prompt-addition", default="",
                    help="追加到 agent system prompt")
    args = ap.parse_args()

    cfg = require_config("gateway_url")
    api_key = args.api_key or cfg.get("api_key")
    if not api_key:
        die("没有 API Key。请告诉 Claude '我的 API Key 是 agw_xxx' 先配置,"
            "或通过 --api-key 指定")

    ws = str(pathlib.Path(args.workspace).expanduser().resolve())

    # 确保 daemon 在跑
    ensure_daemon.ensure()

    payload = {
        "id": args.agent_id,
        "host": args.host,
        "api_key": api_key,
        "workspace_dir": ws,
        "auto_start": args.auto_start,
        "system_prompt_addition": args.system_prompt_addition,
    }
    try:
        http_call("POST", "/control/agents", body=payload)
    except DaemonError as e:
        if e.status == 409:
            die(f"agent {args.agent_id} 已存在。要重新配置请先 '删除 agent {args.agent_id}'")
        die(f"注册失败: {e}")

    ok(f"agent {args.agent_id} 已加入本机 daemon,工作目录 {ws}")
    print(f"\n下一步:告诉 Claude '上线 {args.agent_id}'")
    return 0


if __name__ == "__main__":
    sys.exit(main())
