import asyncio
import json
import pathlib
import tempfile

import pytest

from gas.ipc import IPCMethodError, IPCServer


def _short_sock_path() -> pathlib.Path:
    """macOS Unix socket 限 104 字节,pytest tmp_path 路径常常超限。"""
    d = pathlib.Path(tempfile.mkdtemp(prefix="gas-ipc-", dir="/tmp"))
    return d / "s.sock"


@pytest.mark.asyncio
async def test_ipc_roundtrip():
    calls: list[tuple[str, str, dict]] = []

    async def handler(agent_id, method, params):
        calls.append((agent_id, method, params))
        if method == "fail":
            raise IPCMethodError(400, "bad")
        return {"echo": method, "params": params}

    sock = _short_sock_path()
    server = IPCServer(sock, handler)
    await server.start()
    try:
        reader, writer = await asyncio.open_unix_connection(str(sock))
        writer.write((json.dumps({"id": 1, "agent_id": "alice", "method": "ping", "params": {"x": 1}}) + "\n").encode())
        await writer.drain()
        line = await reader.readline()
        resp = json.loads(line)
        assert resp["id"] == 1
        assert resp["result"] == {"echo": "ping", "params": {"x": 1}}

        # 错误路径
        writer.write((json.dumps({"id": 2, "agent_id": "alice", "method": "fail"}) + "\n").encode())
        await writer.drain()
        line = await reader.readline()
        resp = json.loads(line)
        assert resp["id"] == 2 and resp["error"]["code"] == 400

        writer.close()
        await writer.wait_closed()
    finally:
        await server.stop()

    assert calls[0] == ("alice", "ping", {"x": 1})


@pytest.mark.asyncio
async def test_ipc_invalid_json():
    async def handler(*a, **kw):
        return {}

    sock = _short_sock_path()
    server = IPCServer(sock, handler)
    await server.start()
    try:
        reader, writer = await asyncio.open_unix_connection(str(sock))
        writer.write(b"not json\n")
        await writer.drain()
        line = await reader.readline()
        resp = json.loads(line)
        assert "error" in resp and resp["error"]["code"] == -32700
        writer.close()
        await writer.wait_closed()
    finally:
        await server.stop()
