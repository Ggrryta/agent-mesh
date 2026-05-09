"""
M7 真 Claude 端到端:
  - 启动 minigw Gateway
  - 启动真的 Alice GAS(`claude -p`)
  - 启动真的 Bob GAS(`claude -p`)
  - 互加好友
  - Alice instruct: "Hi, please send the single word 'ping' to bob-reviewer using the send_to tool."
  - 真 Claude 推理 → a2a-bus.send_to → Gateway → SSE → Bob
  - Bob Claude 收到 "[A2A incoming]" → reply
  - 双方 feed 有完整消息流

前置:
  - `claude` 在 PATH 且已登录
  - 运行约消耗 0.5-1 USD
  - 测试时长约 1-3 分钟

跑法:
  cd agent-gateway/gas
  . .venv/bin/activate
  pytest tests/test_m7_real_claude.py -v -s
"""
import asyncio
import json
import os
import pathlib
import shutil
import subprocess
import sys
import time

import aiohttp
import pytest


REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
MINIGW_BIN = pathlib.Path("/tmp/minigw")
CLI = REPO_ROOT / "client-skill" / "scripts" / "agw.py"


def _check_prereqs():
    if not MINIGW_BIN.exists():
        pytest.skip("minigw not built")
    if shutil.which("claude") is None:
        pytest.skip("claude CLI not on PATH")


# 真 Claude 版 extra_args:注册 host 为 claude-code,让 ClaudeCodeAdapter 自己用 claude 可执行
def _build_agent_yaml(agent_id: str, api_key: str, workspace: str) -> str:
    return (
        f"agents:\n"
        f"  - id: {agent_id}\n"
        f"    host: claude-code\n"
        f"    api_key: {api_key}\n"
        f"    workspace_dir: {workspace}\n"
        f"    auto_start: false\n"
        f"    system_prompt_addition: |\n"
        f"      When asked to send a message to another agent, call the a2a-bus.send_to tool directly.\n"
        f"      When you receive an [A2A incoming] message, reply briefly using the a2a-bus.reply tool.\n"
        f"      After replying once, do NOT continue replying to avoid infinite loops.\n"
    )


@pytest.fixture(scope="module")
async def mega_env():
    _check_prereqs()

    import tempfile
    root = pathlib.Path(tempfile.mkdtemp(prefix="m7-real-"))

    # ── minigw ──
    seed = {
        "agents": [
            {"id": "alice-real", "name": "Alice Real", "owner_app_id": "app_alice", "delivery_mode": 1},
            {"id": "bob-real", "name": "Bob Real", "owner_app_id": "app_bob", "delivery_mode": 1},
        ],
        "api_keys": [
            {"app_id": "app_alice", "key": "agw_alice_real_xxxxxx"},
            {"app_id": "app_bob", "key": "agw_bob_real_xxxxxxxx"},
        ],
    }
    seed_path = root / "seed.json"
    seed_path.write_text(json.dumps(seed))
    gw_stderr_path = root / "gw.stderr"

    gw_proc = subprocess.Popen(
        [str(MINIGW_BIN), "-seed-file", str(seed_path), "-log-level", "info"],
        stdout=subprocess.PIPE,
        stderr=open(gw_stderr_path, "wb"),
    )
    ready = gw_proc.stdout.readline().decode()
    if not ready:
        gw_proc.terminate()
        raise RuntimeError("minigw failed to start")
    gw_url = "http://" + json.loads(ready)["addr"]
    print(f"\n[env] minigw ready: {gw_url}", flush=True)

    # ── 两个 GAS ──
    def _spawn_gas(name, agent_id, api_key):
        # 每 GAS 一个独立 ControlAPI 端口
        cfg_dir = root / name / "cfg"; cfg_dir.mkdir(parents=True)
        data_dir = root / name / "data"
        workspace = root / name / "work"; workspace.mkdir(parents=True)

        # 动态选端口
        import socket
        s = socket.socket(); s.bind(("127.0.0.1", 0)); port = s.getsockname()[1]; s.close()

        (cfg_dir / "config.yaml").write_text(
            f"gateway:\n  url: {gw_url}\n"
            f"gas:\n  control_api_host: 127.0.0.1\n  control_api_port: {port}\n"
            f"  data_dir: {data_dir}\n  log_level: info\n  instance_id: real-{name}\n"
        )
        (cfg_dir / "agents.yaml").write_text(_build_agent_yaml(agent_id, api_key, str(workspace)))

        env = {
            **os.environ,
            "GAS_CONFIG_DIR": str(cfg_dir),
            "PYTHONPATH": str(REPO_ROOT / "gas"),
            # 真 Claude 场景:stdout 路径不 dispatch,走 a2a-bus MCP 子进程的 IPC
            # 不设 GAS_STDOUT_DISPATCH
        }
        stderr_path = root / f"gas_{name}.stderr"
        proc = subprocess.Popen(
            [sys.executable, "-m", "gas", "run"],
            env=env, stdout=subprocess.DEVNULL, stderr=open(stderr_path, "wb"),
        )
        return {
            "proc": proc, "port": port, "agent_id": agent_id, "api_key": api_key,
            "workspace": str(workspace), "stderr": stderr_path, "cfg_dir": cfg_dir,
        }

    alice = _spawn_gas("alice", "alice-real", "agw_alice_real_xxxxxx")
    bob = _spawn_gas("bob", "bob-real", "agw_bob_real_xxxxxxxx")

    async def _wait(port):
        for _ in range(50):
            try:
                async with aiohttp.ClientSession() as s:
                    async with s.get(f"http://127.0.0.1:{port}/control/health",
                                     timeout=aiohttp.ClientTimeout(total=1)) as r:
                        if r.status == 200:
                            return True
            except Exception:
                await asyncio.sleep(0.1)
        return False

    for name, info in [("alice", alice), ("bob", bob)]:
        ok = await _wait(info["port"])
        if not ok:
            raise RuntimeError(f"GAS {name} not ready")
        print(f"[env] GAS {name} ready on port {info['port']}", flush=True)

    try:
        yield {"gw_url": gw_url, "alice": alice, "bob": bob, "root": root,
               "gw_stderr": gw_stderr_path}
    finally:
        print("[env] teardown...", flush=True)
        for info in (alice, bob):
            info["proc"].terminate()
            try:
                info["proc"].wait(timeout=15)
            except subprocess.TimeoutExpired:
                info["proc"].kill()
        gw_proc.terminate()
        try:
            gw_proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            gw_proc.kill()


def _agw(*argv, config_path: str, check: bool = True, timeout: int = 30) -> subprocess.CompletedProcess:
    env = {**os.environ, "AGW_CONFIG": config_path}
    r = subprocess.run(
        [sys.executable, str(CLI), *argv],
        env=env, capture_output=True, text=True, timeout=timeout,
    )
    if check and r.returncode != 0:
        raise RuntimeError(f"agw {argv} failed ({r.returncode}):\nstdout={r.stdout}\nstderr={r.stderr}")
    return r


async def _http_json(method: str, url: str, headers=None, body=None):
    async with aiohttp.ClientSession() as s:
        async with s.request(method, url, json=body, headers=headers or {},
                             timeout=aiohttp.ClientTimeout(total=30)) as r:
            t = await r.text()
            if r.status >= 400:
                raise RuntimeError(f"HTTP {r.status} {method} {url}: {t[:300]}")
            return json.loads(t) if t else {}


@pytest.mark.asyncio
async def test_real_claude_two_agents(mega_env):
    env = mega_env
    alice, bob, gw_url = env["alice"], env["bob"], env["gw_url"]
    root = env["root"]

    alice_cfg = str(root / "alice.agw.ini")
    bob_cfg = str(root / "bob.agw.ini")

    _agw("init", "--gateway", gw_url, "--gas", f"http://127.0.0.1:{alice['port']}",
         "--api-key", alice["api_key"], config_path=alice_cfg)
    _agw("init", "--gateway", gw_url, "--gas", f"http://127.0.0.1:{bob['port']}",
         "--api-key", bob["api_key"], config_path=bob_cfg)

    print("\n[test] alice online...", flush=True)
    _agw("agent", "online", "alice-real", config_path=alice_cfg)
    print("[test] bob online...", flush=True)
    _agw("agent", "online", "bob-real", config_path=bob_cfg)

    # claude -p 冷启比 fake 慢很多
    print("[test] waiting 8s for claude spawn + MCP handshake...", flush=True)
    await asyncio.sleep(8)

    alice_status = json.loads(_agw("agent", "status", "alice-real", config_path=alice_cfg).stdout)
    bob_status = json.loads(_agw("agent", "status", "bob-real", config_path=bob_cfg).stdout)
    assert alice_status["state"] == "online", alice_status
    assert bob_status["state"] == "online", bob_status
    print("[test] both online", flush=True)

    # ── 加好友 ──
    print("[test] alice -> friend request bob", flush=True)
    _agw("friend", "request", "--as", "alice-real", "--to", "bob-real", "--reason", "real e2e",
         config_path=alice_cfg)
    pend = await _http_json("GET", f"{gw_url}/friendships/pending",
                            headers={"Authorization": f"Bearer {bob['api_key']}",
                                     "X-Agent-ID": "bob-real"})
    fid = pend["data"][0]["id"]
    _agw("friend", "accept", str(fid), "--as", "bob-real", config_path=bob_cfg)
    print(f"[test] friendship {fid} accepted", flush=True)

    # ── 核心指令 ──
    instruction = (
        "Please send the message 'ping' to agent bob-real using the a2a-bus.send_to tool. "
        "Do it now."
    )
    print(f"[test] instructing alice: {instruction}", flush=True)
    _agw("agent", "instruct", "alice-real", *instruction.split(), config_path=alice_cfg, timeout=20)

    # 真 Claude 推理 + bob 推理一回合,预计 30-60s
    print("[test] waiting for real claude conversation (up to 90s)...", flush=True)
    deadline = time.time() + 90
    alice_got_outgoing = False
    bob_got_incoming = False
    alice_got_incoming = False
    while time.time() < deadline:
        async with aiohttp.ClientSession() as s:
            async with s.get(f"http://127.0.0.1:{alice['port']}/control/agents/alice-real/feed?limit=200") as r:
                af = (await r.json())["items"]
            async with s.get(f"http://127.0.0.1:{bob['port']}/control/agents/bob-real/feed?limit=200") as r:
                bf = (await r.json())["items"]
        alice_kinds = [e["kind"] for e in af]
        bob_kinds = [e["kind"] for e in bf]
        alice_got_outgoing = "outgoing" in alice_kinds
        bob_got_incoming = "incoming" in bob_kinds
        alice_got_incoming = "incoming" in alice_kinds
        if alice_got_outgoing and bob_got_incoming and alice_got_incoming:
            print("[test] full round completed!", flush=True)
            break
        await asyncio.sleep(2)

    # 打印完整 feed 辅助诊断
    print("\n=== Alice feed ===", flush=True)
    for e in af:
        text = json.dumps(e["data"], ensure_ascii=False)
        if len(text) > 150: text = text[:150] + "…"
        print(f"  {e['seq']:<3} {e['kind']:<14} {text}", flush=True)
    print("\n=== Bob feed ===", flush=True)
    for e in bf:
        text = json.dumps(e["data"], ensure_ascii=False)
        if len(text) > 150: text = text[:150] + "…"
        print(f"  {e['seq']:<3} {e['kind']:<14} {text}", flush=True)

    # ── 核心断言:alice 成功发出消息 ──
    assert alice_got_outgoing, f"alice should have sent a message; kinds={[e['kind'] for e in af]}"
    # ── bob 真的收到了 ──
    assert bob_got_incoming, f"bob should have received the message; kinds={[e['kind'] for e in bf]}"

    # 检查消息内容包含 "ping"
    alice_outgoing_entries = [e for e in af if e["kind"] == "outgoing"]
    assert any("ping" in json.dumps(e["data"], ensure_ascii=False).lower()
               for e in alice_outgoing_entries), \
        f"alice's outgoing should mention 'ping': {alice_outgoing_entries}"

    # Bob 如果完成一轮回复,alice 也会有 incoming
    if alice_got_incoming:
        print("\n[test] ✅ bidirectional chat completed (bob replied to alice)")
    else:
        print("\n[test] ⚠️ alice sent ok, bob received ok, but no reply observed yet (may need more time)")

    # 下线
    _agw("agent", "offline", "alice-real", config_path=alice_cfg)
    _agw("agent", "offline", "bob-real", config_path=bob_cfg)
