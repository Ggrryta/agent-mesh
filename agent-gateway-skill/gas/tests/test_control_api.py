
import aiohttp
import pytest

from gas.config import AgentConfig, AgentsFile, Config, GASSection, GatewayConfig
from gas.control_api import ControlAPI
from gas.feed_storage import FeedStorage
from gas.manager import AgentManager


@pytest.fixture
async def api(tmp_path):
    cfg = Config(
        gateway=GatewayConfig(url="http://gw"),
        gas=GASSection(
            control_api_host="127.0.0.1",
            control_api_port=0,                      # 让系统随机端口
            data_dir=str(tmp_path),
            log_level="warning",
            instance_id="test-inst",
        ),
    )
    agents = AgentsFile(agents=[
        AgentConfig(id="alice", host="claude-code", api_key="k",
                    workspace_dir=str(tmp_path)),
    ])
    feed = FeedStorage(data_dir=tmp_path)
    manager = AgentManager(cfg, agents, feed)
    api = ControlAPI(cfg, manager, feed)

    # 手动启动以便拿到实际端口
    from aiohttp import web
    runner = web.AppRunner(api.app, handle_signals=False, access_log=None)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    # aiohttp 0 端口后从 server 拿地址
    sock = site._server.sockets[0]
    port = sock.getsockname()[1]
    try:
        yield port
    finally:
        await site.stop()
        await runner.cleanup()


@pytest.mark.asyncio
async def test_health(api):
    port = api
    async with aiohttp.ClientSession() as s:
        async with s.get(f"http://127.0.0.1:{port}/control/health") as r:
            assert r.status == 200
            body = await r.json()
            assert body["status"] == "ok"
            assert body["instance_id"] == "test-inst"
            assert body["agent_count"] == 1


@pytest.mark.asyncio
async def test_list_and_status(api):
    port = api
    async with aiohttp.ClientSession() as s:
        async with s.get(f"http://127.0.0.1:{port}/control/agents") as r:
            body = await r.json()
            assert len(body["agents"]) == 1
            assert body["agents"][0]["id"] == "alice"
        async with s.get(f"http://127.0.0.1:{port}/control/agents/alice/status") as r:
            assert r.status == 200
        async with s.get(f"http://127.0.0.1:{port}/control/agents/nope/status") as r:
            assert r.status == 404


@pytest.mark.asyncio
async def test_instruct_not_online(api):
    port = api
    async with aiohttp.ClientSession() as s:
        # 未 start_agent,agent 状态是 OFFLINE,instruct 应返回 409
        async with s.post(f"http://127.0.0.1:{port}/control/agents/alice/instruct",
                          json={"text": "hi"}) as r:
            assert r.status == 409


@pytest.mark.asyncio
async def test_instruct_invalid_body(api):
    port = api
    async with aiohttp.ClientSession() as s:
        async with s.post(f"http://127.0.0.1:{port}/control/agents/alice/instruct",
                          data="notjson",
                          headers={"Content-Type": "application/json"}) as r:
            assert r.status == 400
