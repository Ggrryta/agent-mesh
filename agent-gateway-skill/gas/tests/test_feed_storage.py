import asyncio
import json
import pathlib

import pytest

from gas.feed_storage import FeedStorage


@pytest.mark.asyncio
async def test_append_and_read(tmp_path):
    fs = FeedStorage(tmp_path)
    e1 = await fs.append("alice", "log", {"text": "hi"})
    e2 = await fs.append("alice", "log", {"text": "bye"})
    assert e1.seq == 1 and e2.seq == 2

    items = fs.read_recent("alice", since_seq=0, limit=10)
    assert [i.data["text"] for i in items] == ["hi", "bye"]

    items = fs.read_recent("alice", since_seq=1)
    assert len(items) == 1 and items[0].seq == 2


@pytest.mark.asyncio
async def test_subscribe_fanout(tmp_path):
    fs = FeedStorage(tmp_path)
    q = fs.subscribe("alice")
    await fs.append("alice", "log", {"text": "x"})
    e = await asyncio.wait_for(q.get(), timeout=1)
    assert e.data["text"] == "x"
    fs.unsubscribe("alice", q)


@pytest.mark.asyncio
async def test_separate_dbs_per_agent(tmp_path):
    fs = FeedStorage(tmp_path)
    await fs.append("a", "log", {})
    await fs.append("b", "log", {})
    assert len(fs.read_recent("a")) == 1
    assert len(fs.read_recent("b")) == 1
    # alice 的 feed.db 文件应该存在
    assert (tmp_path / "agents" / "a" / "feed.db").exists()
    assert (tmp_path / "agents" / "b" / "feed.db").exists()
