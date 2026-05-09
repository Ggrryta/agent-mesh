import json

from gas.adapters.claude_code import ClaudeCodeAdapter
from gas.events import InputEvent


def test_parse_text_output():
    a = ClaudeCodeAdapter()
    line = json.dumps({
        "type": "assistant",
        "message": {"content": [{"type": "text", "text": "hello"}]},
    }).encode()
    ev = a.parse_output(line)
    assert ev is not None and ev.kind == "log"
    assert ev.data["text"] == "hello"


def test_parse_a2a_send_to():
    a = ClaudeCodeAdapter()
    line = json.dumps({
        "type": "assistant",
        "message": {"content": [{
            "type": "tool_use", "name": "mcp__a2a-bus__send_to",
            "input": {"agent_id": "bob", "content": "hi"},
        }]},
    }).encode()
    ev = a.parse_output(line)
    assert ev is not None
    assert ev.kind == "send_message"
    assert ev.data["tool"] == "send_to"
    assert ev.data["input"]["agent_id"] == "bob"


def test_parse_close_task():
    a = ClaudeCodeAdapter()
    line = json.dumps({
        "type": "assistant",
        "message": {"content": [{
            "type": "tool_use", "name": "mcp__a2a-bus__close_task",
            "input": {"task_id": "t_1"},
        }]},
    }).encode()
    ev = a.parse_output(line)
    assert ev is not None and ev.kind == "close_task"


def test_parse_non_a2a_tool():
    a = ClaudeCodeAdapter()
    line = json.dumps({
        "type": "assistant",
        "message": {"content": [{
            "type": "tool_use", "name": "Bash",
            "input": {"command": "ls"},
        }]},
    }).encode()
    ev = a.parse_output(line)
    assert ev is not None and ev.kind == "tool_call"


def test_parse_result_turn_end():
    a = ClaudeCodeAdapter()
    line = json.dumps({
        "type": "result", "subtype": "success",
        "num_turns": 1, "total_cost_usd": 0.01,
    }).encode()
    ev = a.parse_output(line)
    assert ev is not None and ev.kind == "turn_end"
    assert ev.data["num_turns"] == 1


def test_parse_system_init_becomes_ready_event():
    """system.init 应被识别为 ready 信号(Fix 2: ready 探针)"""
    a = ClaudeCodeAdapter()
    line = json.dumps({"type": "system", "subtype": "init",
                       "session_id": "s1", "mcp_servers": []}).encode()
    ev = a.parse_output(line)
    assert ev is not None and ev.kind == "system_init"
    assert ev.data["session_id"] == "s1"


def test_parse_ignores_other_system_events():
    a = ClaudeCodeAdapter()
    # 非 init 的 system 事件仍然忽略
    line = json.dumps({"type": "system", "subtype": "other"}).encode()
    assert a.parse_output(line) is None


def test_parse_invalid_json():
    a = ClaudeCodeAdapter()
    assert a.parse_output(b"not json\n") is None
    assert a.parse_output(b"") is None


def test_render_user_input():
    a = ClaudeCodeAdapter()
    s = a._render(InputEvent(kind="user_input", data={"text": "hi"}))
    assert s == "hi"


def test_render_a2a_incoming():
    a = ClaudeCodeAdapter()
    s = a._render(InputEvent(kind="a2a_incoming", data={
        "sender": "bob", "task_id": "t_1", "seq": 3,
        "parts": [{"kind": "text", "text": "ok"}],
    }))
    assert "bob" in s and "t_1" in s and "ok" in s
    assert "reply" in s  # 指引提示存在
