import pytest

from gas import config as gascfg


@pytest.fixture
def tmp_config_dir(tmp_path, monkeypatch):
    monkeypatch.setenv("GAS_CONFIG_DIR", str(tmp_path))
    return tmp_path


def test_load_config_missing_returns_defaults(tmp_config_dir):
    cfg = gascfg.load_config()
    assert cfg.gateway.url == "http://localhost:11556"
    assert cfg.gas.control_api_port == 7789
    assert cfg.gas.log_level == "info"


def test_load_config_overrides(tmp_config_dir):
    (tmp_config_dir / "config.yaml").write_text(
        "gateway:\n  url: http://gw:8080\n"
        "gas:\n  control_api_port: 9999\n  log_level: debug\n"
    )
    cfg = gascfg.load_config()
    assert cfg.gateway.url == "http://gw:8080"
    assert cfg.gas.control_api_port == 9999
    assert cfg.gas.log_level == "debug"


def test_load_agents_missing(tmp_config_dir):
    af = gascfg.load_agents()
    assert af.agents == []


def test_load_agents_full(tmp_config_dir):
    (tmp_config_dir / "agents.yaml").write_text(
        "agents:\n"
        "  - id: alice-dev\n"
        "    host: claude-code\n"
        "    api_key: agw_xxx\n"
        "    workspace_dir: /tmp/work\n"
        "    auto_start: true\n"
        "    extra_args: ['--fast']\n"
    )
    af = gascfg.load_agents()
    assert len(af.agents) == 1
    a = af.agents[0]
    assert a.id == "alice-dev"
    assert a.host == "claude-code"
    assert a.auto_start is True
    assert a.extra_args == ["--fast"]


def test_save_and_reload_agents(tmp_config_dir):
    a = gascfg.AgentConfig(
        id="bob",
        host="claude-code",
        api_key="k",
        workspace_dir="/tmp/bob",
    )
    gascfg.save_agents(gascfg.AgentsFile(agents=[a]))
    reloaded = gascfg.load_agents()
    assert len(reloaded.agents) == 1
    assert reloaded.agents[0].id == "bob"


def test_invalid_agent_raises(tmp_config_dir):
    (tmp_config_dir / "agents.yaml").write_text(
        "agents:\n  - host: claude-code\n    api_key: k\n    workspace_dir: /tmp\n"
    )
    with pytest.raises(gascfg.ConfigError):
        gascfg.load_agents()
