"""
M5 集成测试

场景:用 fake claude 替代真 claude,用 mock aiohttp 扮 Gateway,
启动 AgentRunner 跑完整循环,验证:
  1. runner 成功 start (spawn + gateway online)
  2. instruct 一条 user_input → fake claude 调 send_to → IPC → gateway /v2/messages 收到
  3. 注入 SSE task_message → fake claude 回 reply → 新 /v2/messages 收到
  4. runner stop 干净退出 (gateway offline + process 退出)
"""
import asyncio
import json
import pathlib
import sys

import pytest
from aiohttp import web

from gas.config import AgentConfig, Config, GASSection, GatewayConfig
from gas.events import InputEvent
from gas.feed_storage import FeedStorage
from gas.ipc import IPCServer
from gas.runner import AgentRunner


FAKE_CLAUDE = str(pathlib.Path(__file__).parent / "_fake_claude.py")


@pytest.fixture
async def mock_gw(unused_tcp_port):
    state = {
        "online_calls": 0,
        "offline_calls": 0,
        "hb_calls": 0,
        "messages": [],
    }
    sse_q: asyncio.Queue = asyncio.Queue()
    close_flag = {"close": False}

    async def h_online(req):
        state["online_calls"] += 1
        await req.read()
        return web.json_response({"code": 0})

    async def h_heartbeat(req):
        state["hb_calls"] += 1
        return web.json_response({"code": 0})

    async def h_offline(req):
        state["offline_calls"] += 1
        return web.json_response({"code": 0})

    async def h_send(req):
        body = await req.json()
        state["messages"].append(body)
        return web.json_response({"code": 0, "data": {
            "task_id": body.get("task_id") or "t_new",
            "seq": 0 if not body.get("task_id") else 1,
            "message_id": body["message_id"],
            "is_new_task": not body.get("task_id"),
        }})

    async def h_close(req):
        return web.json_response({"code": 0})

    async def h_sse(req):
        resp = web.StreamResponse(status=200, headers={
            "Content-Type": "text/event-stream", "Cache-Control": "no-cache",
        })
        await resp.prepare(req)
        while not close_flag["close"]:
            try:
                evt = await asyncio.wait_for(sse_q.get(), timeout=0.1)
            except asyncio.TimeoutError:
                continue
            frame = f"event: {evt['kind']}\ndata: {json.dumps(evt)}\n\n"
            try:
                await resp.write(frame.encode())
            except ConnectionResetError:
                return resp
        return resp

    async def h_friends(req):
        return web.json_response({"code": 0, "data": []})

    app = web.Application()
    app.router.add_post("/agents/online", h_online)
    app.router.add_post("/agents/heartbeat", h_heartbeat)
    app.router.add_post("/agents/offline", h_offline)
    app.router.add_post("/v2/messages", h_send)
    app.router.add_post("/v2/tasks/{id}/close", h_close)
    app.router.add_get("/a2a/inbox/stream", h_sse)
    app.router.add_get("/friendships", h_friends)

    runner = web.AppRunner(app, handle_signals=False, access_log=None)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", unused_tcp_port)
    await site.start()
    try:
        yield f"http://127.0.0.1:{unused_tcp_port}", state, sse_q
    finally:
        close_flag["close"] = True
        await site.stop()
        await runner.cleanup()


@pytest.fixture
async def runner_ctx(mock_gw, tmp_path, monkeypatch):
    # 测试环境:stdout 路径直接 dispatch 到 gateway(fake claude 不跑 MCP 子进程)
    monkeypatch.setenv("GAS_STDOUT_DISPATCH", "1")

    url, state, sse_q = mock_gw
    data_dir = tmp_path / "data"
    data_dir.mkdir()

    cfg = Config(
        gateway=GatewayConfig(url=url, timeout_ms=10000),
        gas=GASSection(data_dir=str(data_dir), log_level="warning", instance_id="int-inst"),
    )
    agent_cfg = AgentConfig(
        id="alice",
        host="claude-code",
        api_key="agw_test",
        workspace_dir=str(tmp_path),
        extra_env={"GAS_CLAUDE_BIN": sys.executable},
        extra_args=[FAKE_CLAUDE],
    )
    feed = FeedStorage(data_dir=data_dir)

    # IPC: 每个 agent 一个 socket,放 /tmp
    import tempfile
    sock_dir = pathlib.Path(tempfile.mkdtemp(prefix="gas-int-", dir="/tmp"))
    sock = sock_dir / "s.sock"

    runner = AgentRunner(
        config=agent_cfg, gas_config=cfg, data_dir=data_dir,
        feed=feed, ipc_socket_path=sock,
    )

    # 起 IPC server
    async def ipc_handler(aid, method, params):
        return await runner.handle_ipc(method, params)
    ipc = IPCServer(sock, ipc_handler)
    await ipc.start()

    try:
        yield runner, state, sse_q, feed
    finally:
        try:
            await runner.stop()
        except Exception:
            pass
        await ipc.stop()
        feed.close()


@pytest.mark.asyncio
async def test_runner_start_stop(runner_ctx):
    runner, state, _, _ = runner_ctx
    await runner.start()
    await asyncio.sleep(0.3)
    assert state["online_calls"] == 1
    await runner.stop()
    assert state["offline_calls"] >= 1


@pytest.mark.asyncio
async def test_runner_outgoing_message(runner_ctx):
    """user 指令 → fake claude 调 send_to → mock gateway 收到 /v2/messages POST"""
    runner, state, _, feed = runner_ctx
    await runner.start()
    await asyncio.sleep(0.2)
    await runner.send_input(InputEvent(kind="user_input", data={"text": "send to bob"}))

    # 等待 fake claude 处理 + IPC 调用 + gateway 收到
    for _ in range(40):
        if state["messages"]:
            break
        await asyncio.sleep(0.05)
    assert len(state["messages"]) == 1
    msg = state["messages"][0]
    assert msg["target_agent_id"] == "bob"
    assert msg["parts"][0]["text"] == "hello bob"
    assert msg["message_id"].startswith("msg_")

    # feed 里也能看到 outgoing 记录
    entries = feed.read_recent("alice")
    kinds = [e.kind for e in entries]
    assert "user_instruct" in kinds
    assert "outgoing" in kinds
    assert "tool_call" in kinds  # adapter parse 产出的审计记录


@pytest.mark.asyncio
async def test_runner_incoming_triggers_reply(runner_ctx):
    """网关 SSE 推 task_message → runner 转 a2a_incoming → fake claude 调 reply"""
    runner, state, sse_q, feed = runner_ctx
    await runner.start()
    await asyncio.sleep(0.3)

    # 推一条入站消息
    await sse_q.put({
        "kind": "task_message",
        "seq": 42,
        "data": {
            "task_id": "t_abc",
            "sender": "bob",
            "seq": 0,
            "parts": [{"kind": "text", "text": "hello alice"}],
        },
    })

    # 等 runner 处理完并触发 fake claude 的 reply 调用
    for _ in range(60):
        if state["messages"]:
            break
        await asyncio.sleep(0.05)

    assert len(state["messages"]) == 1
    reply = state["messages"][0]
    assert reply["task_id"] == "t_abc"
    assert reply["parts"][0]["text"] == "ack from fake agent"

    entries = feed.read_recent("alice")
    kinds = [e.kind for e in entries]
    assert "incoming" in kinds
    assert "outgoing" in kinds
