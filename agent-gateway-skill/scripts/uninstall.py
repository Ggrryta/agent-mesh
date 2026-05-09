#!/usr/bin/env python3
"""
uninstall.py —— 彻底卸载 agent-gateway skill

步骤:
  1. 停所有 agent + daemon
  2. 删 ~/.agent-gateway/(包含配置、数据、feed.db、pid)
  3. 提示用户手动删 Claude Code skill 目录

Gateway 侧 agent 记录默认保留(避免误删)。加 --purge-gateway 才彻底删除。
"""
from __future__ import annotations

import argparse
import shutil
import subprocess
import sys

from _common import HOME_CONFIG_DIR, SKILL_ROOT, ok
import daemon_stop


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--purge-gateway", action="store_true",
                    help="同时删除 Gateway 上的 agent 记录(危险!)")
    args = ap.parse_args()

    if args.purge_gateway:
        print("⚠️  --purge-gateway 目前未实现,默认只清本地。")

    # 停 daemon
    print("1. 停止后台服务...")
    daemon_stop.main()

    # 清数据
    print(f"\n2. 清理数据目录 {HOME_CONFIG_DIR} ...")
    if HOME_CONFIG_DIR.exists():
        shutil.rmtree(HOME_CONFIG_DIR, ignore_errors=True)
        print(f"  ✓ 已删除")
    else:
        print(f"  - 不存在,跳过")

    ok("本地卸载完成")
    print(f"\n提示:如需彻底删除 skill,请运行:")
    print(f"  rm -rf {SKILL_ROOT}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
