"""
ControlAPI — 本地 HTTP server,供交互会话层(Claude Code / Codex 等里的 client-skill)调用

端点(全部监听 127.0.0.1):
  GET  /control/health
  GET  /control/agents                        列出本机所有 agent 及状态
  POST /control/agents/:id/online             启动 Agent Core
  POST /control/agents/:id/offline            停止
  GET  /control/agents/:id/status             状态详情
  POST /control/agents/:id/instruct           {text} → 作为 user_input 注入
  GET  /control/agents/:id/feed/stream        SSE 实时推送 activity feed
  GET  /control/agents/:id/feed?since=<seq>   一次性拉取 feed 最近 N 条

M4 先实现前三个 + health,其余在 M5/M6 补。
"""
from __future__ import annotations

import asyncio
import json
from typing import Optional

from aiohttp import web

from gas import __version__
from gas.config import Config
from gas.feed_storage import FeedStorage, entry_to_dict
from gas.log import get_logger
from gas.manager import AgentManager

log = get_logger("gas.control_api")


class ControlAPI:
    def __init__(self, cfg: Config, manager: AgentManager, feed: FeedStorage,
                 shutdown_callback=None):
        self.cfg = cfg
        self.manager = manager
        self.feed = feed
        self._shutdown_cb = shutdown_callback
        self.app = web.Application()
        self._runner: Optional[web.AppRunner] = None
        self._site: Optional[web.BaseSite] = None
        self._bind_routes()

    def _bind_routes(self):
        app = self.app
        app.router.add_get("/control/health", self._health)
        app.router.add_get("/control/agents", self._list_agents)
        app.router.add_post("/control/agents", self._register_agent)
        app.router.add_get("/control/agents/{agent_id}/status", self._agent_status)
        app.router.add_delete("/control/agents/{agent_id}", self._delete_agent)
        app.router.add_post("/control/agents/{agent_id}/online", self._agent_online)
        app.router.add_post("/control/agents/{agent_id}/offline", self._agent_offline)
        app.router.add_post("/control/agents/{agent_id}/instruct", self._agent_instruct)
        app.router.add_get("/control/agents/{agent_id}/feed", self._feed_stub)
        app.router.add_get("/control/agents/{agent_id}/feed/stream", self._feed_stream_stub)
        app.router.add_post("/control/shutdown", self._shutdown)

    async def _health(self, _req: web.Request) -> web.Response:
        return _json({
            "status": "ok",
            "version": __version__,
            "instance_id": self.cfg.gas.instance_id,
            "agent_count": len(self.manager.list()),
        })

    async def _list_agents(self, _req: web.Request) -> web.Response:
        items = []
        for e in self.manager.list():
            started = e.runner.started_at if e.runner else None
            items.append({
                "id": e.config.id,
                "host": e.config.host,
                "state": e.state.value,
                "auto_start": e.config.auto_start,
                "workspace_dir": e.config.workspace_dir,
                "started_at": started,
                "last_error": e.last_error,
            })
        return _json({"agents": items})

    async def _agent_status(self, req: web.Request) -> web.Response:
        aid = req.match_info["agent_id"]
        e = self.manager.get(aid)
        if e is None:
            return _json({"error": "not found"}, status=404)
        started = e.runner.started_at if e.runner else None
        return _json({
            "id": e.config.id,
            "host": e.config.host,
            "state": e.state.value,
            "started_at": started,
            "last_error": e.last_error,
        })

    async def _register_agent(self, req: web.Request) -> web.Response:
        """POST /control/agents — 将一个新 agent 加入 agents.yaml 并加载到 manager

        body: {id, host, api_key, workspace_dir, auto_start?, system_prompt_addition?}
        """
        try:
            body = await req.json()
        except Exception:
            return _json({"error": "invalid json"}, status=400)
        for k in ("id", "host", "api_key", "workspace_dir"):
            if not body.get(k):
                return _json({"error": f"{k} required"}, status=400)
        if self.manager.get(body["id"]) is not None:
            return _json({"error": "agent already registered"}, status=409)
        try:
            from gas.daemon import agent_add
            cfg = agent_add(
                id_=body["id"], host=body["host"],
                api_key=body["api_key"], workspace_dir=body["workspace_dir"],
                auto_start=bool(body.get("auto_start", False)),
                system_prompt_addition=body.get("system_prompt_addition", ""),
            )
            self.manager.add(cfg)
        except ValueError as e:
            return _json({"error": str(e)}, status=409)
        except Exception as e:
            log.exception("register agent failed")
            return _json({"error": str(e)}, status=500)
        return _json({"ok": True, "id": cfg.id})

    async def _delete_agent(self, req: web.Request) -> web.Response:
        aid = req.match_info["agent_id"]
        e = self.manager.get(aid)
        if e is None:
            return _json({"error": "not found"}, status=404)
        if e.state.value != "offline":
            return _json({"error": "agent not offline, stop first"}, status=409)
        try:
            from gas.daemon import agent_remove
            agent_remove(aid)
            self.manager.remove(aid)
        except Exception as e:
            return _json({"error": str(e)}, status=500)
        return _json({"ok": True})

    async def _agent_online(self, req: web.Request) -> web.Response:
        aid = req.match_info["agent_id"]
        try:
            await self.manager.start_agent(aid)
        except ValueError as e:
            return _json({"error": str(e)}, status=404)
        except Exception as e:
            log.exception("start_agent failed", extra={"agent_id": aid})
            return _json({"error": str(e)}, status=500)
        return _json({"ok": True})

    async def _agent_offline(self, req: web.Request) -> web.Response:
        aid = req.match_info["agent_id"]
        try:
            await self.manager.stop_agent(aid)
        except ValueError as e:
            return _json({"error": str(e)}, status=404)
        except Exception as e:
            log.exception("stop_agent failed", extra={"agent_id": aid})
            return _json({"error": str(e)}, status=500)
        return _json({"ok": True})

    async def _agent_instruct(self, req: web.Request) -> web.Response:
        aid = req.match_info["agent_id"]
        try:
            body = await req.json()
        except Exception:
            return _json({"error": "invalid json"}, status=400)
        text = (body or {}).get("text")
        if not text:
            return _json({"error": "text required"}, status=400)
        from gas.events import InputEvent
        try:
            await self.manager.send_to_agent(aid, InputEvent(kind="user_input", data={"text": text}))
        except ValueError:
            return _json({"error": "agent not found"}, status=404)
        except RuntimeError as e:
            return _json({"error": str(e)}, status=409)
        return _json({"ok": True})

    async def _feed_stub(self, req: web.Request) -> web.Response:
        aid = req.match_info["agent_id"]
        if self.manager.get(aid) is None:
            return _json({"error": "agent not found"}, status=404)
        since = int(req.query.get("since", "0") or 0)
        limit = int(req.query.get("limit", "100") or 100)
        try:
            items = self.feed.read_recent(aid, since_seq=since, limit=min(limit, 500))
        except Exception as e:
            return _json({"error": str(e)}, status=500)
        return _json({"items": [entry_to_dict(e) for e in items]})

    async def _feed_stream_stub(self, req: web.Request) -> web.StreamResponse:
        aid = req.match_info["agent_id"]
        if self.manager.get(aid) is None:
            return _json({"error": "agent not found"}, status=404)

        resp = web.StreamResponse(
            status=200,
            headers={
                "Content-Type": "text/event-stream",
                "Cache-Control": "no-cache",
                "X-Accel-Buffering": "no",
            },
        )
        await resp.prepare(req)

        # 先把缓冲中的历史(可选 since)一次性喂出去
        since = int(req.query.get("since", "0") or 0)
        for e in self.feed.read_recent(aid, since_seq=since, limit=200):
            await self._send_feed_event(resp, e)

        q = self.feed.subscribe(aid)
        try:
            while True:
                try:
                    e = await asyncio.wait_for(q.get(), timeout=30.0)
                except asyncio.TimeoutError:
                    await resp.write(b": ping\n\n")
                    continue
                await self._send_feed_event(resp, e)
        except asyncio.CancelledError:
            pass
        except ConnectionResetError:
            pass
        finally:
            self.feed.unsubscribe(aid, q)
        return resp

    async def _send_feed_event(self, resp: web.StreamResponse, entry):
        payload = {
            "seq": entry.seq, "ts_ms": entry.ts_ms, "kind": entry.kind, "data": entry.data,
        }
        frame = f"event: feed\ndata: {json.dumps(payload, ensure_ascii=False)}\n\n".encode()
        try:
            await resp.write(frame)
        except ConnectionResetError:
            raise

    async def _shutdown(self, _req: web.Request) -> web.Response:
        """POST /control/shutdown — 通知 daemon 主循环退出

        先返回 200,然后异步调 shutdown_callback 以便客户端收到响应后再真停
        """
        if self._shutdown_cb is None:
            return _json({"error": "shutdown not supported"}, status=501)

        async def _later():
            await asyncio.sleep(0.2)
            try:
                self._shutdown_cb()
            except Exception as e:
                log.warning("shutdown callback failed", extra={"err": str(e)})

        asyncio.create_task(_later())
        return _json({"ok": True, "message": "daemon shutting down"})

    async def start(self):
        self._runner = web.AppRunner(self.app, handle_signals=False, access_log=None)
        await self._runner.setup()
        self._site = web.TCPSite(self._runner, self.cfg.gas.control_api_host, self.cfg.gas.control_api_port)
        await self._site.start()
        log.info("control_api listening", extra={
            "host": self.cfg.gas.control_api_host,
            "port": self.cfg.gas.control_api_port,
        })

    async def stop(self):
        if self._site is not None:
            await self._site.stop()
        if self._runner is not None:
            await self._runner.cleanup()
        log.info("control_api stopped")


def _json(data, status: int = 200) -> web.Response:
    return web.Response(
        status=status,
        content_type="application/json",
        text=json.dumps(data, ensure_ascii=False),
    )
