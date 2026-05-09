#!/usr/bin/env python3
"""
init.py —— 初始化 skill 配置

用户意图对应:
  "接入 Agent Gateway 地址 <url>"       → --gateway <url>
  "我的 API Key 是 agw_xxx"             → --api-key agw_xxx
  "设置 agent 身份为 <id>"              → --default-agent <id>
  "显示当前配置"                          → --show

不直接拉起 daemon。daemon 在首次 agent_online 时才起。
"""
from __future__ import annotations

import argparse
import sys

from _common import load_skill_config, ok, print_json, save_skill_config


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--gateway", help="Gateway 公开 URL,如 https://gateway.corp")
    ap.add_argument("--api-key", help="用户账号的 API Key,agw_ 前缀")
    ap.add_argument("--default-agent", help="默认 agent 身份(后续命令可省略 --as)")
    ap.add_argument("--control-api-port", type=int, help="daemon HTTP 端口(默认 7789)")
    ap.add_argument("--show", action="store_true", help="显示当前配置")
    args = ap.parse_args()

    if args.show:
        cfg = load_skill_config()
        if not cfg:
            print("(尚未初始化)")
            return 0
        # 隐藏 key 大部分
        if "api_key" in cfg and cfg["api_key"]:
            cfg = {**cfg, "api_key": cfg["api_key"][:8] + "..."}
        print_json(cfg)
        return 0

    if not any([args.gateway, args.api_key, args.default_agent, args.control_api_port]):
        ap.print_help()
        print("\n请至少提供 --gateway / --api-key / --default-agent 之一", file=sys.stderr)
        return 1

    save_skill_config(
        gateway_url=args.gateway.rstrip("/") if args.gateway else None,
        api_key=args.api_key,
        default_agent=args.default_agent,
        control_api_port=args.control_api_port,
    )
    cfg = load_skill_config()
    parts = []
    if args.gateway:
        parts.append(f"gateway={args.gateway}")
    if args.api_key:
        parts.append(f"api_key={args.api_key[:8]}...")
    if args.default_agent:
        parts.append(f"default_agent={args.default_agent}")
    if args.control_api_port:
        parts.append(f"control_api_port={args.control_api_port}")
    ok(f"配置已更新: {', '.join(parts)}")

    # 提示后续步骤
    if not cfg.get("api_key") and args.gateway:
        print(f"\n下一步:访问 {args.gateway} 注册账号并生成 API Key,"
              f"然后告诉 Claude: '我的 API Key 是 agw_xxx'")
    elif cfg.get("gateway_url") and cfg.get("api_key") and not cfg.get("default_agent"):
        print("\n下一步:告诉 Claude '创建 agent <name>,工作目录 <dir>'")
    return 0


if __name__ == "__main__":
    sys.exit(main())
