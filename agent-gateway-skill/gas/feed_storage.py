"""
FeedStorage —— 每个 agent 的 activity feed 持久化

MVP 实现:基于 SQLite,每个 agent 一个库文件。
schema 简单:append-only,按 seq 递增。

上层:ControlAPI 的 feed/stream 端点从这里读。
"""
from __future__ import annotations

import asyncio
import json
import pathlib
import sqlite3
import time
from dataclasses import dataclass, asdict
from typing import Any, Optional

from gas.log import get_logger

log = get_logger("gas.feed_storage")


@dataclass
class FeedEntry:
    seq: int
    ts_ms: int
    kind: str            # user_instruct / incoming / outgoing / thinking / log / tool_call / status / error
    data: dict           # 任意 JSON


class FeedStorage:
    """
    文件布局:{data_dir}/agents/{agent_id}/feed.db
    单张表 feed(seq, ts_ms, kind, data_json)
    """

    def __init__(self, data_dir: pathlib.Path):
        self.data_dir = data_dir
        self._conns: dict[str, sqlite3.Connection] = {}
        self._locks: dict[str, asyncio.Lock] = {}
        # 每个 agent 订阅通道(ControlAPI 的 feed/stream 用)
        self._subs: dict[str, set[asyncio.Queue]] = {}

    def _conn(self, agent_id: str) -> sqlite3.Connection:
        if agent_id in self._conns:
            return self._conns[agent_id]
        agent_dir = self.data_dir / "agents" / agent_id
        agent_dir.mkdir(parents=True, exist_ok=True)
        db_path = agent_dir / "feed.db"
        conn = sqlite3.connect(str(db_path), isolation_level=None, check_same_thread=False)
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute("PRAGMA synchronous=NORMAL")
        conn.execute(
            """CREATE TABLE IF NOT EXISTS feed(
                 seq INTEGER PRIMARY KEY AUTOINCREMENT,
                 ts_ms INTEGER NOT NULL,
                 kind TEXT NOT NULL,
                 data_json TEXT NOT NULL
               )"""
        )
        self._conns[agent_id] = conn
        self._locks[agent_id] = asyncio.Lock()
        self._subs.setdefault(agent_id, set())
        return conn

    async def append(self, agent_id: str, kind: str, data: dict) -> FeedEntry:
        conn = self._conn(agent_id)
        ts_ms = int(time.time() * 1000)
        payload = json.dumps(data, ensure_ascii=False)
        async with self._locks[agent_id]:
            # sqlite3 operations run sync; for MVP 这样够用,量大了再换 aiosqlite
            cur = conn.execute(
                "INSERT INTO feed(ts_ms, kind, data_json) VALUES(?,?,?)",
                (ts_ms, kind, payload),
            )
            seq = cur.lastrowid
        entry = FeedEntry(seq=seq, ts_ms=ts_ms, kind=kind, data=data)
        await self._fanout(agent_id, entry)
        return entry

    async def _fanout(self, agent_id: str, entry: FeedEntry):
        for q in list(self._subs.get(agent_id) or ()):
            try:
                q.put_nowait(entry)
            except asyncio.QueueFull:
                # 订阅者消费慢 → 丢弃一条,不能卡住 append
                log.warning("feed subscriber queue full, drop",
                            extra={"agent_id": agent_id, "seq": entry.seq})

    def subscribe(self, agent_id: str) -> asyncio.Queue:
        q: asyncio.Queue = asyncio.Queue(maxsize=256)
        self._subs.setdefault(agent_id, set()).add(q)
        return q

    def unsubscribe(self, agent_id: str, q: asyncio.Queue):
        self._subs.get(agent_id, set()).discard(q)

    def read_recent(self, agent_id: str, since_seq: int = 0, limit: int = 100) -> list[FeedEntry]:
        conn = self._conn(agent_id)
        cur = conn.execute(
            "SELECT seq, ts_ms, kind, data_json FROM feed WHERE seq > ? ORDER BY seq ASC LIMIT ?",
            (since_seq, limit),
        )
        out: list[FeedEntry] = []
        for row in cur.fetchall():
            out.append(FeedEntry(
                seq=row[0],
                ts_ms=row[1],
                kind=row[2],
                data=json.loads(row[3]) if row[3] else {},
            ))
        return out

    def close(self):
        for conn in self._conns.values():
            try:
                conn.close()
            except Exception:
                pass
        self._conns.clear()


def entry_to_dict(e: FeedEntry) -> dict[str, Any]:
    return asdict(e)
