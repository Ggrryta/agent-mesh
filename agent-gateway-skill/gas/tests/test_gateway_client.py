"""GatewayClient 集成测试:用 aiohttp mock 服务器扮演 Gateway"""
import asyncio
import json
import pytest
from aiohttp import web

from gas.gateway_client import GatewayClient, GatewayInboxEvent


@pytest.fixture
async def mock_gw(unused_tcp_port):
    state = {"online": 0, "hb": 0, "offline": 0, "sent": [], "sse_clients": 0}
    sse_queue: asyncio.Queue = asyncio.Queue()

    async def handle_online(req: web.Request):
        state["online"] += 1
        body = await req.json()
        assert req.headers.get("X-Agent-ID")
        assert body.get("gas_instance_id")
        return web.json_response({"code": 0})

    async def handle_heartbeat(req: web.Request):
        state["hb"] += 1
        return web.json_response({"code": 0})

    async def handle_offline(req: web.Request):
        state["offline"] += 1
        return web.json_response({"code": 0})

    async def handle_send(req: web.Request):
        body = await req.json()
        state["sent"].append(body)
        return web.json_response({"code": 0, "data": {
            "task_id": "t_xyz", "seq": 0, "message_id": body["message_id"],
            "is_new_task": True,
        }})

    async def handle_sse(req: web.Request):
        state["sse_clients"] += 1
        resp = web.StreamResponse(
            status=200,
            headers={"Content-Type": "text/event-stream", "Cache-Control": "no-cache"},
        )
        await resp.prepare(req)
        while True:
            try:
                evt = await asyncio.wait_for(sse_queue.get(), timeout=0.2)
            except asyncio.TimeoutError:
                # 检查是否该关闭
                if state.get("sse_close"):
                    return resp
                continue
            frame = f"event: {evt['kind']}\ndata: {json.dumps(evt)}\n\n"
            try:
                await resp.write(frame.encode())
            except ConnectionResetError:
                return resp

    app = web.Application()
    app.router.add_post("/agents/online", handle_online)
    app.router.add_post("/agents/heartbeat", handle_heartbeat)
    app.router.add_post("/agents/offline", handle_offline)
    app.router.add_post("/v2/messages", handle_send)
    app.router.add_get("/a2a/inbox/stream", handle_sse)

    runner = web.AppRunner(app, handle_signals=False, access_log=None)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", unused_tcp_port)
    await site.start()
    try:
        yield f"http://127.0.0.1:{unused_tcp_port}", state, sse_queue
    finally:
        state["sse_close"] = True
        await site.stop()
        await runner.cleanup()


@pytest.mark.asyncio
async def test_online_heartbeat_offline(mock_gw):
    url, state, _ = mock_gw
    events: list[GatewayInboxEvent] = []
    async def on_evt(e):
        events.append(e)

    client = GatewayClient(url, "alice", "agw_k", "inst-1", on_evt)
    await client.start()
    assert state["online"] == 1
    # 停止客户端
    await client.stop()
    assert state["offline"] == 1


@pytest.mark.asyncio
async def test_send_message(mock_gw):
    url, state, _ = mock_gw
    async def on_evt(e): pass
    c = GatewayClient(url, "alice", "k", "inst-1", on_evt)
    await c.start()
    try:
        r = await c.send_message(target_agent_id="bob", title="hi",
                                 parts=[{"kind": "text", "text": "hello"}])
        assert r["task_id"] == "t_xyz"
        assert r["is_new_task"] is True
        # 校验请求体结构
        body = state["sent"][0]
        assert body["target_agent_id"] == "bob"
        assert body["parts"][0]["text"] == "hello"
        assert body.get("message_id", "").startswith("msg_")
    finally:
        await c.stop()


@pytest.mark.asyncio
async def test_receive_sse_event(mock_gw):
    url, _, sse_queue = mock_gw
    received: list[GatewayInboxEvent] = []
    async def on_evt(e):
        received.append(e)

    c = GatewayClient(url, "alice", "k", "inst-1", on_evt)
    await c.start()
    try:
        # 给 sse 一点时间连接
        await asyncio.sleep(0.2)
        await sse_queue.put({"kind": "task_message",
                             "data": {"task_id": "t_1", "sender": "bob", "seq": 0, "parts": []},
                             "seq": 1})
        # 等事件到达
        for _ in range(20):
            if received:
                break
            await asyncio.sleep(0.05)
        assert len(received) >= 1
        assert received[0].kind == "task_message"
        assert received[0].data["sender"] == "bob"
    finally:
        await c.stop()
