"""
进程安全防护单测

测试目标:验证 "永远不能误杀用户其他 claude 进程" 的防护机制不被代码演进破坏。
"""
import asyncio
import os
import signal

import pytest

from gas.adapters.claude_code import ClaudeCodeAdapter
from gas.config import AgentConfig


@pytest.mark.asyncio
async def test_adapter_spawn_uses_start_new_session(tmp_path):
    """adapter spawn 必须用 start_new_session=True,让子进程有独立进程组"""
    cfg = AgentConfig(
        id="test-isolation",
        host="claude-code",
        api_key="test",
        workspace_dir=str(tmp_path),
        extra_env={"GAS_CLAUDE_BIN": "/bin/sh"},  # 用 shell 避免真拉 claude
        extra_args=["-c", "sleep 30"],
    )
    # 假 mcp config
    mcp_cfg = str(tmp_path / "mcp.json")
    (tmp_path / "mcp.json").write_text('{"mcpServers": {}}')

    a = ClaudeCodeAdapter()
    proc = await a.spawn(cfg, mcp_cfg)
    try:
        pid = proc.pid
        # 独立进程组 → PGID == PID
        pgid = os.getpgid(pid)
        assert pgid == pid, f"子进程必须是进程组 leader,got pid={pid} pgid={pgid}"
    finally:
        await a.graceful_stop(proc)


@pytest.mark.asyncio
async def test_adapter_spawn_injects_agent_gateway_managed_env(tmp_path):
    """spawn 必须注入 AGENT_GATEWAY_MANAGED=1 环境变量"""
    cfg = AgentConfig(
        id="test-env",
        host="claude-code",
        api_key="test",
        workspace_dir=str(tmp_path),
        extra_env={"GAS_CLAUDE_BIN": "/bin/sh"},
        # 打印自己的环境到文件
        extra_args=["-c", f"env > {tmp_path}/env.out; sleep 10"],
    )
    (tmp_path / "mcp.json").write_text('{"mcpServers": {}}')

    a = ClaudeCodeAdapter()
    proc = await a.spawn(cfg, str(tmp_path / "mcp.json"))
    try:
        # 给 shell 一点时间写文件
        await asyncio.sleep(1.0)
        env_dump = (tmp_path / "env.out").read_text()
        assert "AGENT_GATEWAY_MANAGED=1" in env_dump
        assert "AGENT_GATEWAY_AGENT_ID=test-env" in env_dump
    finally:
        await a.graceful_stop(proc)


@pytest.mark.asyncio
async def test_graceful_stop_uses_killpg(tmp_path):
    """graceful_stop 必须通过 killpg 精确清理进程组,不能影响其他同名进程"""
    cfg = AgentConfig(
        id="test-killpg",
        host="claude-code",
        api_key="test",
        workspace_dir=str(tmp_path),
        extra_env={"GAS_CLAUDE_BIN": "/bin/sh"},
        # 起一个会 fork 子进程的 shell,验证子进程也被清理
        extra_args=["-c", "sleep 30 & sleep 30"],
    )
    (tmp_path / "mcp.json").write_text('{"mcpServers": {}}')

    a = ClaudeCodeAdapter()
    proc = await a.spawn(cfg, str(tmp_path / "mcp.json"))
    pid = proc.pid
    # 确认进程活着
    await asyncio.sleep(0.3)
    assert proc.returncode is None

    # graceful_stop 走到 killpg 路径(因为 shell 不会自己退)
    await a.graceful_stop(proc)

    # 进程应该已经退了
    assert proc.returncode is not None, "graceful_stop 后进程应已退出"


@pytest.mark.asyncio
async def test_runner_writes_runtime_pid_file(tmp_path):
    """Runner.start() 必须把 agent core pid 写入 runtime.pid 文件,
    供 cleanup.py 精确识别和清理。"""
    from gas.runner import AgentRunner
    from gas.feed_storage import FeedStorage
    from gas.config import Config, GatewayConfig, GASSection

    cfg = Config(
        gateway=GatewayConfig(url="http://localhost:1"),  # 故意不连真 gateway
        gas=GASSection(
            control_api_host="127.0.0.1",
            control_api_port=0,
            data_dir=str(tmp_path),
            instance_id="test",
        ),
    )
    agent_cfg = AgentConfig(
        id="test-runtime-pid",
        host="claude-code",
        api_key="test",
        workspace_dir=str(tmp_path),
        extra_env={"GAS_CLAUDE_BIN": "/bin/sh"},
        extra_args=["-c", "sleep 30"],
    )
    feed = FeedStorage(data_dir=tmp_path)
    sock_path = tmp_path / "ipc.sock"

    runner = AgentRunner(
        config=agent_cfg, gas_config=cfg, data_dir=tmp_path,
        feed=feed, ipc_socket_path=sock_path,
    )
    # 只跑到 _write_runtime_pid,不连 gateway
    runner._mcp_cfg_path = tmp_path / "mcp.json"
    runner._mcp_cfg_path.write_text('{"mcpServers": {}}')
    runner.proc = await runner.adapter.spawn(agent_cfg, str(runner._mcp_cfg_path))
    runner._started_at = 1234.5
    runner._write_runtime_pid()

    try:
        pid_file = tmp_path / "agents" / "test-runtime-pid" / "runtime.pid"
        assert pid_file.exists(), "runtime.pid 必须被写入"
        import json
        info = json.loads(pid_file.read_text())
        assert info["agent_id"] == "test-runtime-pid"
        assert info["pid"] == runner.proc.pid
        assert info["pgid"] == runner.proc.pid  # start_new_session → pgid == pid
        assert info["daemon_pid"] == os.getpid()
    finally:
        await runner.adapter.graceful_stop(runner.proc)
        runner._clear_runtime_pid()
        assert not pid_file.exists(), "stop 后应清 runtime.pid"
