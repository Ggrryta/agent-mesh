"""
M7 真·端到端集成测试

拓扑:
    ┌─────────┐  ┌─────────┐
    │  Alice 的 GAS     │   │   Bob 的 GAS     │
    │  + fake claude  │   │  + fake claude │
    └────┬────┘   └────┬────┘
         │                  │
         └───────┬────────┘
                          │ A2A/SE
                           ▼
                  ┌──────┐
                  │    minigw (in-proc Gateway)            │
                  │   - sqlite in-memory                                    │
                  │   - miniredis                                                │
                  │   - 种子: alice/bob agent + API keys        │
                  └──────┘

测试场景:
  1. 启动 minigw (带 seed: alice/bob agents + API keys)
  2. 启动 Alice GAS (subprocess)
  3. 启动 Bob GAS (subprocess)
  4. Alice/Bob 通过 agw CLI 上线
  5. Alice 通过 friend request 加 Bob 为好友
  6. Bob accept
  7. Alice instruct "send to bob ..." → fake claude → gateway → SSE → Bob GAS
  8. Bob 的 fake claude 自动 reply → gateway → SSE → Alice GAS
  9. 双方 feed 里都能看到完整交互
"""
import asyncio
import json
import os
import pathlib
import subprocess
import sys

import aiohttp
import pytest


REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
MINIGW_BIN = pathlib.Path("/tmp/minigw")
CLI = REPO_ROOT / "client-skill" / "scripts" / "agw.py"
FAKE_CLAUDE = REPO_ROOT / "gas" / "tests" / "_fake_claude.py"


def _check_prereqs():
    if not MINIGW_BIN.exists():
        pytest.skip(f"minigw binary not built: {MINIGW_BIN} (run: go build -o /tmp/minigw ./test/e2e/cmd/minigw)")
    if not CLI.exists():
        pytest.skip(f"agw CLI missing: {CLI}")
    if not FAKE_CLAUDE.exists():
        pytest.skip(f"fake claude missing: {FAKE_CLAUDE}")


@pytest.fixture
async def minigw(tmp_path):
    """启动 minigw + 塞测试数据"""
    _check_prereqs()
    seed = {
        "agents": [
            {"id": "alice", "name": "Alice", "owner_app_id": "app_alice", "delivery_mode": 1},
            {"id": "bob", "name": "Bob", "owner_app_id": "app_bob", "delivery_mode": 1},
        ],
        "api_keys": [
            {"app_id": "app_alice", "key": "agw_alice_test_key"},
            {"app_id": "app_bob", "key": "agw_bob_test_key"},
        ],
    }
    seed_path = tmp_path / "seed.json"
    seed_path.write_text(json.dumps(seed))

    gw_stderr = tmp_path / "gw.stderr"
    proc = subprocess.Popen(
        [str(MINIGW_BIN), "-seed-file", str(seed_path), "-log-level", "info"],
        stdout=subprocess.PIPE, stderr=open(gw_stderr, "wb"), text=True,
    )
    ready_line = proc.stdout.readline()
    if not ready_line:
        proc.terminate()
        out, err = proc.communicate(timeout=3)
        raise RuntimeError(f"minigw failed: {err}")
    info = json.loads(ready_line)
    base_url = f"http://{info['addr']}"
    try:
        yield base_url
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
        # 打印 gateway 关键日志
        try:
            content = gw_stderr.read_text()
            print("\n=== GW stderr FULL (zap json lines only) ===")
            for line in content.splitlines():
                # zap 默认输出 {"level": ...} 的 JSON
                if line.startswith("{"):
                    print(line[:500])
        except Exception:
            pass


@pytest.fixture
async def gas_pair(minigw, tmp_path, unused_tcp_port_factory):
    """启动两个 GAS daemon,alice 和 bob 各一个"""
    gas_py_root = str(REPO_ROOT / "gas")
    procs = {}

    def _spawn_gas(name: str, agent_id: str, api_key: str, port: int) -> dict:
        cfg_dir = tmp_path / name / "cfg"
        cfg_dir.mkdir(parents=True, exist_ok=True)
        data_dir = tmp_path / name / "data"
        workspace = tmp_path / name / "work"
        workspace.mkdir(parents=True, exist_ok=True)

        (cfg_dir / "config.yaml").write_text(
            f"gateway:\n  url: {minigw}\n"
            f"gas:\n  control_api_host: 127.0.0.1\n  control_api_port: {port}\n"
            f"  data_dir: {data_dir}\n  log_level: warning\n"
            f"  instance_id: e2e-{name}\n"
        )
        (cfg_dir / "agents.yaml").write_text(
            f"agents:\n"
            f"  - id: {agent_id}\n"
            f"    host: claude-code\n"
            f"    api_key: {api_key}\n"
            f"    workspace_dir: {workspace}\n"
            f"    auto_start: false\n"
            f"    extra_env:\n"
            f"      GAS_CLAUDE_BIN: {sys.executable}\n"
            f"      GAS_STDOUT_DISPATCH: '1'\n"
            f"    extra_args:\n"
            f"      - {FAKE_CLAUDE}\n"
        )
        env = {
            **os.environ,
            "GAS_CONFIG_DIR": str(cfg_dir),
            "PYTHONPATH": gas_py_root,
            "GAS_STDOUT_DISPATCH": "1",       # 让 Runner 走 stdout dispatch(fake claude 不调 MCP)
        }
        proc = subprocess.Popen(
            [sys.executable, "-m", "gas", "run"],
            env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        return {
            "proc": proc, "port": port, "agent_id": agent_id, "api_key": api_key,
            "cfg_dir": cfg_dir, "workspace": str(workspace),
        }

    alice = _spawn_gas("alice", "alice", "agw_alice_test_key", unused_tcp_port_factory())
    bob = _spawn_gas("bob", "bob", "agw_bob_test_key", unused_tcp_port_factory())
    procs["alice"] = alice
    procs["bob"] = bob

    # 等 GAS 起来
    async def _wait_gas(port: int):
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

    for name, info in procs.items():
        ok = await _wait_gas(info["port"])
        if not ok:
            _cleanup_procs(procs)
            info["proc"].terminate()
            out, err = info["proc"].communicate(timeout=3)
            raise RuntimeError(f"GAS {name} not ready: {err.decode()[:500]}")

    try:
        yield procs
    finally:
        _cleanup_procs(procs)


def _cleanup_procs(procs: dict):
    for info in procs.values():
        p = info["proc"]
        try:
            p.terminate()
            p.wait(timeout=5)
        except subprocess.TimeoutExpired:
            p.kill()
        except Exception:
            pass


# ── agw CLI helpers ─────────────────────────────────────────────

def _agw(*argv, config_path: str, check: bool = True) -> subprocess.CompletedProcess:
    env = {**os.environ, "AGW_CONFIG": config_path}
    r = subprocess.run(
        [sys.executable, str(CLI), *argv],
        env=env, capture_output=True, text=True, timeout=15,
    )
    if check and r.returncode != 0:
        raise RuntimeError(f"agw {argv} failed ({r.returncode}):\nstdout: {r.stdout}\nstderr: {r.stderr}")
    return r


# ── HTTP helpers (直接调 Gateway / GAS control API) ──────────────

async def _http_json(method: str, url: str, headers: dict | None = None, body: dict | None = None) -> dict:
    async with aiohttp.ClientSession() as s:
        async with s.request(method, url, json=body, headers=headers or {},
                             timeout=aiohttp.ClientTimeout(total=10)) as r:
            text = await r.text()
            if r.status >= 400:
                raise RuntimeError(f"HTTP {r.status} {method} {url}: {text[:500]}")
            return json.loads(text) if text else {}


# ── 主测试 ───────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_e2e_two_agents_chat(minigw, gas_pair, tmp_path):
    """完整跑通:两个 GAS + 两个 fake claude,互加好友 → 互发消息"""
    alice = gas_pair["alice"]
    bob = gas_pair["bob"]

    alice_agw_cfg = str(tmp_path / "alice.agw.ini")
    bob_agw_cfg = str(tmp_path / "bob.agw.ini")

    # ── 1. 初始化 agw 配置 ──────────
    _agw("init", "--gateway", minigw, "--gas", f"http://127.0.0.1:{alice['port']}",
         "--api-key", alice["api_key"], config_path=alice_agw_cfg)
    _agw("init", "--gateway", minigw, "--gas", f"http://127.0.0.1:{bob['port']}",
         "--api-key", bob["api_key"], config_path=bob_agw_cfg)

    # ── 2. 通过 agw 让两 agent 上线 ──────────
    r = _agw("agent", "online", "alice", config_path=alice_agw_cfg)
    assert "已上线" in r.stdout

    # debug:确认 Gateway 还活着
    health = await _http_json("GET", f"{minigw}/friendships/pending",
                              headers={"Authorization": f"Bearer {bob['api_key']}", "X-Agent-ID": "bob"})
    assert isinstance(health, dict)

    r = _agw("agent", "online", "bob", config_path=bob_agw_cfg)
    assert "已上线" in r.stdout

    # 给 fake claude 一点时间完成 spawn + MCP 握手
    await asyncio.sleep(1.0)

    # ── 3. 验证两人都 online ──────────
    alice_status = json.loads(_agw("agent", "status", "alice", config_path=alice_agw_cfg).stdout)
    bob_status = json.loads(_agw("agent", "status", "bob", config_path=bob_agw_cfg).stdout)
    assert alice_status["state"] == "online", f"alice not online: {alice_status}"
    assert bob_status["state"] == "online", f"bob not online: {bob_status}"

    # ── 4. 加好友 ──────────────────
    r = _agw("friend", "request", "--as", "alice", "--to", "bob", "--reason", "e2e test",
             config_path=alice_agw_cfg)
    assert "已发起好友请求" in r.stdout

    json.loads(_agw("friend", "pending", "--as", "bob", config_path=bob_agw_cfg).stdout
                         if False else "{}")  # stdout 是表格,解析太麻烦
    # 直接调 gateway 查 pending id
    pend_data = await _http_json(
        "GET", f"{minigw}/friendships/pending",
        headers={"Authorization": f"Bearer {bob['api_key']}", "X-Agent-ID": "bob"},
    )
    items = pend_data.get("data") or []
    assert len(items) == 1, f"bob should have 1 pending req, got: {items}"
    fid = items[0]["id"]

    r = _agw("friend", "accept", str(fid), "--as", "bob", config_path=bob_agw_cfg)
    assert "已接受" in r.stdout

    # ── 5. 校验好友关系建立 ──────────
    alice_friends = await _http_json(
        "GET", f"{minigw}/friendships",
        headers={"Authorization": f"Bearer {alice['api_key']}", "X-Agent-ID": "alice"},
    )
    assert len(alice_friends.get("data") or []) == 1

    # ── 6. alice 给 bob 发消息 ──────────
    _agw("agent", "instruct", "alice", "send", "to", "bob",
         config_path=alice_agw_cfg)

    # 等双向消息走完
    await asyncio.sleep(2.5)

    # ── 7. 验证结果 ─────────────────
    # 通过 GAS feed 查 alice 和 bob 的记录
    async with aiohttp.ClientSession() as s:
        async with s.get(f"http://127.0.0.1:{alice['port']}/control/agents/alice/feed?limit=100") as r:
            alice_feed = (await r.json())["items"]
        async with s.get(f"http://127.0.0.1:{bob['port']}/control/agents/bob/feed?limit=100") as r:
            bob_feed = (await r.json())["items"]

    alice_kinds = [e["kind"] for e in alice_feed]
    bob_kinds = [e["kind"] for e in bob_feed]
    print("\n=== Alice feed ===")
    for e in alice_feed:
        print(f"  {e['seq']:<3} {e['kind']:<14} {json.dumps(e['data'], ensure_ascii=False)[:100]}")
    print("\n=== Bob feed ===")
    for e in bob_feed:
        print(f"  {e['seq']:<3} {e['kind']:<14} {json.dumps(e['data'], ensure_ascii=False)[:100]}")

    # alice:发过指令 + 发过消息 + 收过回复
    assert "user_instruct" in alice_kinds
    # 如果 outgoing 不出现,打 debug 信息
    if "outgoing" not in alice_kinds:
        async with aiohttp.ClientSession() as s:
            async with s.get(f"{minigw}/v2/tasks",
                             headers={"Authorization": f"Bearer {alice['api_key']}", "X-Agent-ID": "alice"}) as r:
                at = await r.json()
        print(f"\n=== alice tasks seen by gateway: {at}")
    assert "outgoing" in alice_kinds
    assert "incoming" in alice_kinds, f"alice should receive bob's reply: {alice_kinds}"

    # bob:收过消息 + 发过回复
    assert "incoming" in bob_kinds
    assert "outgoing" in bob_kinds, f"bob should send reply: {bob_kinds}"

    # 校验消息内容链:alice 的 outgoing 包含 "hello bob"
    alice_outgoing = [e for e in alice_feed if e["kind"] == "outgoing"]
    assert any("hello bob" in json.dumps(e["data"], ensure_ascii=False) for e in alice_outgoing), \
        f"alice outgoing should say 'hello bob': {alice_outgoing}"

    # bob 的 outgoing 是 fake claude 的固定回复
    bob_outgoing = [e for e in bob_feed if e["kind"] == "outgoing"]
    assert any("ack from fake agent" in json.dumps(e["data"], ensure_ascii=False) for e in bob_outgoing), \
        f"bob outgoing should be ack: {bob_outgoing}"

    # ── 8. 优雅下线 ────────────────
    _agw("agent", "offline", "alice", config_path=alice_agw_cfg)
    _agw("agent", "offline", "bob", config_path=bob_agw_cfg)
    await asyncio.sleep(0.3)
    alice_status = json.loads(_agw("agent", "status", "alice", config_path=alice_agw_cfg).stdout)
    assert alice_status["state"] == "offline"
