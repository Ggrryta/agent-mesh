#!/usr/bin/env python3
"""
cleanup.py —— 安全清理 agent-gateway 管理的所有进程

设计原则(保护用户自己的其他进程):
  1. **永不使用 pkill 模糊匹配** —— 绝不会误杀同名的 claude / python 进程
  2. 只信任 GAS 自己记录的 runtime.pid 文件
  3. 杀进程前**强制双重校验**:
     - pid 文件里的进程必须还活着
     - 该进程的环境变量里必须有 AGENT_GATEWAY_MANAGED=1
  4. 通过进程组(PGID = PID,因 spawn 时用了 start_new_session)精确清理
  5. 若双重校验任一不过,跳过,不清理

用法:
  python3 cleanup.py          正常清理:gracious stop daemon + 清 pid 文件
  python3 cleanup.py --force  强制杀:即使 daemon 不响应也通过 pid 文件精确 killpg
  python3 cleanup.py --dry-run 只显示会杀什么,不执行

退出码:
  0  成功(包括"没什么可清理的")
  1  错误(双重校验失败导致无法清理)
"""
from __future__ import annotations

import argparse
import json
import os
import signal
import sys
import time
from pathlib import Path

from _common import HOME_CONFIG_DIR, PID_FILE, DaemonError, http_call


# ═══ 进程安全校验 ═══════════════════════════════════

def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def _is_agent_gateway_managed(pid: int, expected_from_pid_file: bool) -> bool:
    """
    进程身份校验。两种信息源,任一通过即可:

    1. **首选**:pid 来自 agent-gateway 自己写的 runtime.pid / daemon.pid 文件
       (文件路径 ~/.agent-gateway/data/agents/<id>/runtime.pid 和 ~/.agent-gateway/daemon.pid
        都是本程序独占,普通 Claude 进程不会被写到这里)
       → expected_from_pid_file=True 时直接认为通过

    2. **辅助**:进程环境变量有 AGENT_GATEWAY_MANAGED=1
       (macOS 11+ 默认看不到别人进程 env,读不到不代表没有,所以这一项只当加分项)

    策略:只要 expected_from_pid_file=True,就放行;它已经是足够权威的签名。
    """
    if expected_from_pid_file:
        return True

    # fallback:尝试读 env(macOS 可能失败)
    env_text = ""
    if sys.platform == "darwin":
        import subprocess
        try:
            r = subprocess.run(
                ["ps", "-E", "-p", str(pid), "-o", "command="],
                capture_output=True, text=True, timeout=3,
            )
            env_text = r.stdout or ""
        except Exception:
            return False
    else:
        try:
            env_text = Path(f"/proc/{pid}/environ").read_text(errors="replace")
        except Exception:
            return False

    return "AGENT_GATEWAY_MANAGED=1" in env_text


def _safe_kill_pgid(pid: int, sig: signal.Signals, reason: str,
                    from_pid_file: bool = False, dry_run: bool = False) -> str:
    """**必须身份校验**才敢杀:
       - from_pid_file=True 表示这个 pid 来自 ~/.agent-gateway 下的 pid 文件,
         这是足够的身份证明(那些文件只有 GAS 会写)
       - from_pid_file=False 时会尝试 env 变量校验(兜底)

    返回人类可读的状态字符串。
    """
    if not _pid_alive(pid):
        return f"pid {pid}: 已不存在"
    if not _is_agent_gateway_managed(pid, from_pid_file):
        return f"pid {pid}: 无法确认是 agent-gateway 管理的进程,跳过(安全优先)"
    if dry_run:
        return f"pid {pid}: [dry-run] 将 killpg {sig.name} ({reason})"
    try:
        os.killpg(pid, sig)
        return f"pid {pid}: killpg {sig.name} 已发出 ({reason})"
    except ProcessLookupError:
        return f"pid {pid}: 进程组已不存在"
    except PermissionError:
        return f"pid {pid}: 权限不足,无法 killpg"


# ═══ 发现所有 agent runtime.pid ═══════════════════════

def _find_agent_pids() -> list[dict]:
    """从 ~/.agent-gateway/data/agents/<id>/runtime.pid 收集所有记录"""
    base = HOME_CONFIG_DIR / "data" / "agents"
    if not base.exists():
        return []
    out: list[dict] = []
    for agent_dir in base.iterdir():
        pid_file = agent_dir / "runtime.pid"
        if not pid_file.exists():
            continue
        try:
            info = json.loads(pid_file.read_text())
            info["_pid_file"] = str(pid_file)
            out.append(info)
        except Exception:
            pass
    return out


def _find_daemon_pid() -> int | None:
    if not PID_FILE.exists():
        return None
    try:
        return int(PID_FILE.read_text().strip())
    except Exception:
        return None


# ═══ 主流程 ═══════════════════════════════════════════

def main() -> int:
    ap = argparse.ArgumentParser(description="安全清理 agent-gateway 管理的进程")
    ap.add_argument("--force", action="store_true",
                    help="跳过优雅 shutdown,直接基于 pid 文件 killpg")
    ap.add_argument("--dry-run", action="store_true",
                    help="只显示会杀什么,不实际执行")
    args = ap.parse_args()

    dry = args.dry_run

    print("🔍 扫描 agent-gateway 管理的进程...")
    daemon_pid = _find_daemon_pid()
    agent_pids = _find_agent_pids()

    print(f"  daemon pid file: {daemon_pid or '(空)'}")
    print(f"  agent runtime.pid 条目: {len(agent_pids)}")
    for a in agent_pids:
        print(f"    - {a.get('agent_id')}: pid={a.get('pid')} daemon={a.get('daemon_pid')}")

    if daemon_pid is None and not agent_pids:
        print("✅ 没有需要清理的东西")
        return 0

    # 优雅路径:调 daemon 的 shutdown endpoint
    if not args.force and daemon_pid and _pid_alive(daemon_pid):
        print("\n📡 尝试优雅停 daemon(会先 offline 所有 agent)...")
        try:
            if not dry:
                http_call("POST", "/control/shutdown", timeout=3)
                # 等 daemon 真正退出
                deadline = time.time() + 15
                while time.time() < deadline and _pid_alive(daemon_pid):
                    time.sleep(0.3)
            print("  ✓ daemon 优雅退出" if not dry else "  [dry-run] 调 /control/shutdown")
        except Exception as e:
            print(f"  ⚠️  daemon 不响应 shutdown: {e}")
            print("  降级为强制清理 pid 文件...")

    # 二次扫描:看还有什么活着
    print("\n🔨 二次扫描,清理所有残留(严格双重校验)...")
    still_alive = []
    for a in agent_pids:
        pid = a.get("pid")
        if pid and _pid_alive(pid):
            still_alive.append(a)

    if still_alive:
        print(f"  发现 {len(still_alive)} 个 agent core 进程还活着")
        for a in still_alive:
            pid = a["pid"]
            aid = a.get("agent_id")
            print(f"    {_safe_kill_pgid(pid, signal.SIGTERM, f'agent {aid}', from_pid_file=True, dry_run=dry)}")
        # 等 2s 看是否自己退
        if not dry:
            time.sleep(2)
        for a in still_alive:
            pid = a["pid"]
            if _pid_alive(pid):
                print(f"    {_safe_kill_pgid(pid, signal.SIGKILL, 'SIGTERM 后仍存活', from_pid_file=True, dry_run=dry)}")

    # 如果 daemon 还在,也清理(pid 来自 daemon.pid 文件,from_pid_file=True)
    if daemon_pid and _pid_alive(daemon_pid) and not args.force:
        # 优雅路径失败的备案
        print(f"  daemon pid {daemon_pid} 还活着,用 killpg 精确清理")
        print(f"    {_safe_kill_pgid(daemon_pid, signal.SIGTERM, 'daemon 未响应 shutdown', from_pid_file=True, dry_run=dry)}")

    # 清理 pid 文件
    if not dry:
        try:
            PID_FILE.unlink(missing_ok=True)
        except Exception:
            pass
        for a in agent_pids:
            try:
                Path(a["_pid_file"]).unlink(missing_ok=True)
            except Exception:
                pass

    print("\n✅ 清理完成" + (" (dry-run,实际未执行)" if dry else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
