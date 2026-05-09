import pytest

from gas.config import AgentConfig, AgentsFile, Config, GASSection
from gas.feed_storage import FeedStorage
from gas.manager import AgentManager, AgentState
from gas.events import InputEvent


def _cfg(id_="x", auto_start=False):
    return AgentConfig(id=id_, host="claude-code", api_key="k",
                       workspace_dir="/tmp", auto_start=auto_start)


def _mgr(tmp_path, *cfgs, instance_id="inst-test"):
    cfg = Config()
    cfg.gas = GASSection(
        control_api_host="127.0.0.1",
        control_api_port=0,
        data_dir=str(tmp_path),
        log_level="warning",
        instance_id=instance_id,
    )
    feed = FeedStorage(data_dir=tmp_path)
    return AgentManager(cfg, AgentsFile(agents=list(cfgs)), feed), feed


@pytest.mark.asyncio
async def test_manager_list_and_get(tmp_path):
    m, _ = _mgr(tmp_path, _cfg("a"), _cfg("b"))
    ids = [e.config.id for e in m.list()]
    assert ids == ["a", "b"]
    assert m.get("a").state == AgentState.OFFLINE
    assert m.get("missing") is None


@pytest.mark.asyncio
async def test_manager_add_remove(tmp_path):
    m, _ = _mgr(tmp_path)
    m.add(_cfg("x"))
    assert m.get("x") is not None
    assert m.remove("x") is True
    assert m.get("x") is None
    assert m.remove("x") is False


@pytest.mark.asyncio
async def test_manager_start_unknown_agent(tmp_path):
    m, _ = _mgr(tmp_path)
    with pytest.raises(ValueError):
        await m.start_agent("nope")


@pytest.mark.asyncio
async def test_manager_send_requires_online(tmp_path):
    m, _ = _mgr(tmp_path, _cfg("x"))
    with pytest.raises(RuntimeError):
        await m.send_to_agent("x", InputEvent(kind="user_input", data={"text": "hi"}))
