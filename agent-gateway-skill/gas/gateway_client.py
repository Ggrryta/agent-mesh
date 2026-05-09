"""
GatewayClient —— 单个 agent 与 Agent Gateway 的通信客户端

职责:
  1. /agents/online 上线声明
  2. /agents/heartbeat 30s 续约
  3. /a2a/inbox/stream SSE 长连接订阅入站事件
  4. /v2/messages POST 发送消息到网关
  5. /friendships/... 好友关系查询(list/pending 只读即可,加好友走前端)

对外事件:通过 on_event 回调推送 inbox 事件到上层。
"""
from __future__ import annotations

import asyncio
import json
import uuid
from dataclasses import dataclass
from typing import Any, Awaitable, Callable, Optional

import aiohttp

from gas.log import get_logger

log = get_logger("gas.gateway_client")

HEARTBEAT_INTERVAL = 30.0     # 秒
SSE_RECONNECT_BACKOFF = [1, 2, 5, 10, 30]  # 秒


@dataclass
class GatewayInboxEvent:
    kind: str                # task_message / task_created / task_closed / friend_* / ping
    data: dict
    seq: int = 0


EventCallback = Callable[[GatewayInboxEvent], Awaitable[None]]


class GatewayClient:
    def __init__(
        self,
        base_url: str,
        agent_id: str,
        api_key: str,
        gas_instance_id: str,
        on_event: EventCallback,
    ):
        self.base_url = base_url.rstrip("/")
        self.agent_id = agent_id
        self.api_key = api_key
        self.gas_instance_id = gas_instance_id
        self.on_event = on_event

        self._session: Optional[aiohttp.ClientSession] = None
        self._hb_task: Optional[asyncio.Task] = None
        self._sse_task: Optional[asyncio.Task] = None
        self._stopped = asyncio.Event()
        self._online = False

    # ── 生命周期 ──────────────────────────

    async def start(self):
        """连 gateway:online + 启动心跳 + 订阅 SE 流"""
        self._session = aiohttp.ClientSession(
            headers={
                "Authorization": f"Bearer {self.api_key}",
                "X-Agent-ID": self.agent_id,
            },
            timeout=aiohttp.ClientTimeout(total=None, sock_connect=10, sock_read=None),
        )
        await self._online_req()
        self._online = True
        self._hb_task = asyncio.create_task(self._heartbeat_loop(), name=f"gw-hb-{self.agent_id}")
        self._sse_task = asyncio.create_task(self._sse_loop(), name=f"gw-sse-{self.agent_id}")
        log.info("gateway client started", extra={"agent_id": self.agent_id})

    async def stop(self):
        self._stopped.set()
        # 发 offline(best-effort)
        if self._online and self._session and not self._session.closed:
            try:
                await self._offline_req()
            except Exception as e:
                log.warning("offline req failed", extra={"agent_id": self.agent_id, "err": str(e)})
        for t in (self._hb_task, self._sse_task):
            if t:
                t.cancel()
                try:
                    await t
                except (asyncio.CancelledError, Exception):
                    pass
        if self._session:
            await self._session.close()
        self._online = False
        log.info("gateway client stopped", extra={"agent_id": self.agent_id})

    # ── 对外 API ──────────────────────────

    async def send_message(
        self,
        *,
        target_agent_id: Optional[str] = None,
        task_id: Optional[str] = None,
        title: str = "",
        parts: list[dict],
        message_id: Optional[str] = None,
    ) -> dict:
        """POST /v2/messages。返回 {task_id, seq, message_id, is_new_task}。"""
        if not self._session:
            raise RuntimeError("client not started")
        body: dict[str, Any] = {
            "message_id": message_id or ("msg_" + uuid.uuid4().hex),
            "parts": parts,
            "title": title,
        }
        if task_id:
            body["task_id"] = task_id
        elif target_agent_id:
            body["target_agent_id"] = target_agent_id
        else:
            raise ValueError("need target_agent_id or task_id")
        async with self._session.post(self.base_url + "/v2/messages", json=body) as resp:
            text = await resp.text()
            if resp.status >= 400:
                raise GatewayError(resp.status, text)
            payload = json.loads(text)
            return payload.get("data", payload)

    async def close_task(self, task_id: str):
        async with self._session.post(self.base_url + f"/v2/tasks/{task_id}/close") as resp:
            if resp.status >= 400:
                raise GatewayError(resp.status, await resp.text())

    async def list_friends(self) -> list[dict]:
        async with self._session.get(self.base_url + "/friendships") as resp:
            if resp.status >= 400:
                raise GatewayError(resp.status, await resp.text())
            data = await resp.json()
            return data.get("data") or []

    # ── 内部 ─────────────────────────────

    async def _online_req(self):
        body = {"gas_instance_id": self.gas_instance_id}
        async with self._session.post(self.base_url + "/agents/online", json=body) as resp:
            if resp.status >= 400:
                raise GatewayError(resp.status, await resp.text())

    async def _offline_req(self):
        body = {"gas_instance_id": self.gas_instance_id}
        try:
            async with self._session.post(self.base_url + "/agents/offline", json=body) as resp:
                _ = await resp.text()
        except Exception:
            pass

    async def _heartbeat_once(self):
        body = {"gas_instance_id": self.gas_instance_id}
        async with self._session.post(self.base_url + "/agents/heartbeat", json=body) as resp:
            if resp.status >= 400:
                raise GatewayError(resp.status, await resp.text())

    async def _heartbeat_loop(self):
        while not self._stopped.is_set():
            try:
                await asyncio.wait_for(self._stopped.wait(), timeout=HEARTBEAT_INTERVAL)
                return
            except asyncio.TimeoutError:
                pass
            try:
                await self._heartbeat_once()
            except Exception as e:
                log.warning("heartbeat failed", extra={"agent_id": self.agent_id, "err": str(e)})

    async def _sse_loop(self):
        """SSE 长连接,断线指数退避重连。"""
        backoff_idx = 0
        while not self._stopped.is_set():
            try:
                log.info("sse connecting", extra={"agent_id": self.agent_id})
                async with self._session.get(self.base_url + "/a2a/inbox/stream",
                                             headers={"Accept": "text/event-stream"}) as resp:
                    if resp.status >= 400:
                        raise GatewayError(resp.status, await resp.text())
                    backoff_idx = 0
                    log.info("sse connected", extra={"agent_id": self.agent_id})
                    await self._consume_sse(resp)
            except asyncio.CancelledError:
                raise
            except Exception as e:
                log.warning("sse loop err",
                            extra={"agent_id": self.agent_id, "err": str(e)})
            # 退避重连
            if self._stopped.is_set():
                return
            delay = SSE_RECONNECT_BACKOFF[min(backoff_idx, len(SSE_RECONNECT_BACKOFF) - 1)]
            backoff_idx += 1
            try:
                await asyncio.wait_for(self._stopped.wait(), timeout=delay)
                return
            except asyncio.TimeoutError:
                pass

    async def _consume_sse(self, resp: aiohttp.ClientResponse):
        """解析 SSE 流 text/event-stream 格式"""
        event_kind = ""
        buf_data = []
        async for raw_line in resp.content:
            if self._stopped.is_set():
                return
            line = raw_line.decode("utf-8", errors="replace").rstrip("\r\n")
            if line == "":
                # 事件结束
                if event_kind or buf_data:
                    try:
                        data_str = "\n".join(buf_data) if buf_data else "{}"
                        payload = json.loads(data_str) if data_str else {}
                    except Exception as e:
                        log.warning("sse parse err",
                                    extra={"agent_id": self.agent_id, "err": str(e)})
                        event_kind = ""
                        buf_data = []
                        continue
                    evt = GatewayInboxEvent(
                        kind=event_kind or payload.get("kind", ""),
                        data=payload.get("data") if isinstance(payload, dict) else payload,
                        seq=int(payload.get("seq", 0)) if isinstance(payload, dict) else 0,
                    )
                    if evt.kind == "ping":
                        event_kind = ""
                        buf_data = []
                        continue
                    try:
                        await self.on_event(evt)
                    except Exception as e:
                        log.exception("on_event raised",
                                      extra={"agent_id": self.agent_id, "err": str(e)})
                event_kind = ""
                buf_data = []
                continue
            if line.startswith(":"):
                continue  # SE 注释
            if line.startswith("event:"):
                event_kind = line[len("event:"):].strip()
            elif line.startswith("data:"):
                buf_data.append(line[len("data:"):].lstrip())


class GatewayError(Exception):
    def __init__(self, status: int, body: str):
        self.status = status
        self.body = body
        super().__init__(f"gateway {status}: {body[:200]}")
