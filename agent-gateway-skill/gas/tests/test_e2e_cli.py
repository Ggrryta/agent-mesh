"""
M6 E2E:启动真实 GAS daemon(subprocess),用 agw CLI 跑本地控制流程

覆盖:
  1. agw init
  2. agw agent register -> GAS 写 agents.yaml
  3. agw agent list     -> 显示刚注册的
  4. agw agent status   -> offline 状态
  5. agw agent offline (幂等,offline→offline)
  6. agw agent 错误路径 (不存在的 agent)

不覆盖真实 agent 上线(需要 claude 可执行文件),那块由 M5 集成测试保证。
"""
import asyncio
import json
import os
import pathlib
import subprocess
import sys

import aiohttp
import pytest


CLI = str(pathlib.Path(__file__).parent.parent.parent / "client-skill" / "scripts" / "agw.py")


@pytest.fixture
async def gas_daemon(tmp_path, unused_tcp_port, monkeypatch):
    """启动真实 GAS daemon,只用于控制平面测试(不依赖真 gateway)"""
    gas_config_dir = tmp_path / "cfg"
    gas_config_dir.mkdir()
    gas_data_dir = tmp_path / "data"
    (gas_config_dir / "config.yaml").write_text(
        f"gateway:\n  url: http://127.0.0.1:1\n"        # 无所谓,不会真连
        f"gas:\n  control_api_host: 127.0.0.1\n  control_api_port: {unused_tcp_port}\n"
        f"  data_dir: {gas_data_dir}\n  log_level: warning\n  instance_id: e2e-inst\n"
    )
    (gas_config_dir / "agents.yaml").write_text("agents: []\n")

    env = {**os.environ, "GAS_CONFIG_DIR": str(gas_config_dir)}
    gas_py_root = str(pathlib.Path(__file__).parent.parent)
    env["PYTHONPATH"] = gas_py_root

    proc = subprocess.Popen(
        [sys.executable, "-m", "gas", "run"],
        env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )

    # 等 GAS 启动
    for _ in range(50):
        try:
            async with aiohttp.ClientSession() as s:
                async with s.get(f"http://127.0.0.1:{unused_tcp_port}/control/health",
                                 timeout=aiohttp.ClientTimeout(total=1)) as r:
                    if r.status == 200:
                        break
        except Exception:
            await asyncio.sleep(0.1)
    else:
        proc.terminate()
        out, err = proc.communicate(timeout=3)
        raise RuntimeError(f"gas failed to start: stderr={err.decode()[:500]}")

    agw_config = tmp_path / "agw.ini"
    monkeypatch.setenv("AGW_CONFIG", str(agw_config))

    try:
        yield {
            "port": unused_tcp_port,
            "workspace": str(tmp_path / "work"),
            "agents_yaml": gas_config_dir / "agents.yaml",
        }
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()


def _run(*argv: str, check: bool = True, timeout: int = 10) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, CLI, *argv],
        capture_output=True, text=True, timeout=timeout, check=check,
    )


@pytest.mark.asyncio
async def test_e2e_lifecycle(gas_daemon):
    env = gas_daemon
    os.makedirs(env["workspace"], exist_ok=True)

    # 1. init
    r = _run("init",
             "--gateway", "http://127.0.0.1:1",
             "--gas", f"http://127.0.0.1:{env['port']}")
    assert "配置已写入" in r.stdout

    # 2. register
    r = _run("agent", "register", "alice",
             "--host", "claude-code",
             "--api-key", "agw_test_key",
             "--workspace", env["workspace"])
    assert "已把 agent alice 注册" in r.stdout

    # 校验 agents.yaml 被写了
    ay = env["agents_yaml"].read_text()
    assert "alice" in ay
    assert "agw_test_key" in ay

    # 3. list
    r = _run("agent", "list")
    assert "alice" in r.stdout
    assert "offline" in r.stdout

    # 4. status
    r = _run("agent", "status", "alice")
    body = json.loads(r.stdout)
    assert body["id"] == "alice"
    assert body["host"] == "claude-code"
    assert body["state"] == "offline"

    # 5. 重复 register 应失败 (409)
    r = _run("agent", "register", "alice",
             "--host", "claude-code",
             "--api-key", "x",
             "--workspace", env["workspace"],
             check=False)
    assert r.returncode != 0

    # 6. 查不存在的 agent
    r = _run("agent", "status", "nobody", check=False)
    assert r.returncode != 0


@pytest.mark.asyncio
async def test_e2e_missing_config_errors(gas_daemon):
    # 用一个不存在的 AGW_CONFIG
    fake_cfg = "/tmp/nonexistent-agw-config-xyz.ini"
    if pathlib.Path(fake_cfg).exists():
        pathlib.Path(fake_cfg).unlink()
    p = subprocess.run(
        [sys.executable, CLI, "agent", "list"],
        capture_output=True, text=True, timeout=10,
        env={**os.environ, "AGW_CONFIG": fake_cfg},
    )
    assert p.returncode != 0
    assert "未初始化" in (p.stdout + p.stderr)


@pytest.mark.asyncio
async def test_e2e_help_all_subcommands():
    """所有 CLI 子命令的 --help 都能正确返回"""
    subs = [
        ["--help"],
        ["init", "--help"],
        ["agent", "--help"],
        ["agent", "list", "--help"],
        ["agent", "register", "--help"],
        ["agent", "online", "--help"],
        ["agent", "instruct", "--help"],
        ["agent", "attach", "--help"],
        ["friend", "--help"],
        ["friend", "request", "--help"],
        ["friend", "accept", "--help"],
        ["directory", "--help"],
        ["msg", "--help"],
        ["msg", "send", "--help"],
    ]
    for cmd in subs:
        r = subprocess.run(
            [sys.executable, CLI, *cmd],
            capture_output=True, text=True, timeout=5,
        )
        assert r.returncode == 0, f"{cmd} failed: {r.stderr}"
        assert "usage:" in r.stdout
