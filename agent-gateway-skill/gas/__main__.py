"""
GAS CLI 入口

用法:
    python -m gas run                  # 启动 daemon(前台)
    python -m gas agent add <id> --host claude-code --api-key ... --workspace ...
    python -m gas agent list
    python -m gas agent remove <id>
"""
from __future__ import annotations

import argparse
import asyncio
import sys

from gas import __version__
from gas.config import load_agents
from gas.daemon import agent_add, agent_remove, run_daemon


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="gas", description="Gateway Agent Server")
    p.add_argument("--version", action="version", version=f"gas {__version__}")
    sub = p.add_subparsers(dest="cmd")

    sub.add_parser("run", help="Run the GAS daemon in foreground")

    agent = sub.add_parser("agent", help="Manage local agents")
    asub = agent.add_subparsers(dest="agent_cmd")

    a_add = asub.add_parser("add", help="Add a new agent to agents.yaml")
    a_add.add_argument("id", help="Agent ID (must match Gateway registration)")
    a_add.add_argument("--host", required=True, choices=["claude-code", "codex", "gemini"])
    a_add.add_argument("--api-key", required=True, help="Gateway API key for this agent")
    a_add.add_argument("--workspace", required=True, help="Working directory for Agent Core")
    a_add.add_argument("--auto-start", action="store_true")
    a_add.add_argument("--system-prompt-addition", default="")

    asub.add_parser("list", help="List configured agents")

    a_rm = asub.add_parser("remove", help="Remove an agent from agents.yaml")
    a_rm.add_argument("id")

    return p


def cli(argv: list[str] | None = None) -> int:
    argv = argv if argv is not None else sys.argv[1:]
    args = _build_parser().parse_args(argv)

    if args.cmd == "run":
        return asyncio.run(run_daemon())

    if args.cmd == "agent":
        if args.agent_cmd == "add":
            try:
                a = agent_add(args.id, args.host, args.api_key, args.workspace,
                              args.auto_start, args.system_prompt_addition)
            except ValueError as e:
                print(f"error: {e}", file=sys.stderr)
                return 1
            print(f"added agent {a.id} host={a.host} workspace={a.workspace_dir}")
            return 0
        if args.agent_cmd == "list":
            agents = load_agents()
            if not agents.agents:
                print("(no agents configured)")
                return 0
            print(f"{'id':<24} {'host':<14} {'auto_start':<10} workspace_dir")
            for a in agents.agents:
                print(f"{a.id:<24} {a.host:<14} {str(a.auto_start):<10} {a.workspace_dir}")
            return 0
        if args.agent_cmd == "remove":
            if agent_remove(args.id):
                print(f"removed {args.id}")
                return 0
            print(f"agent {args.id} not found", file=sys.stderr)
            return 1

    _build_parser().print_help()
    return 1


if __name__ == "__main__":
    sys.exit(cli())
