#!/usr/bin/env python3
"""
fake-claude —— 仿 claude -p --input-format stream-json --output-format stream-json

协议:
  stdin:  {"type":"user","message":{"role":"user","content":"..."}}
  stdout:
    {"type":"system","subtype":"init",...}
    {"type":"assistant","message":{"content":[{"type":"tool_use","name":"...","input":{...}}]}}
    {"type":"result","subtype":"success","num_turns":1,"total_cost_usd":0.0}

行为: 每收到一条用户输入,根据内容模式输出预定 tool_use:
  - 如果内容包含 "[A2A incoming]" => 调 reply
  - 否则 => 调 send_to,target 从 "send to <name>" 解析
"""
import json
import sys
import re


def send(o):
    sys.stdout.write(json.dumps(o) + "\n")
    sys.stdout.flush()


SESSION = "fake-session-123"


def emit_init():
    send({"type": "system", "subtype": "init",
          "session_id": SESSION, "mcp_servers": [{"name": "a2a-bus", "status": "connected"}]})


def emit_tool_use(tool: str, tool_input: dict):
    send({"type": "assistant",
          "message": {"content": [{
              "type": "tool_use", "name": "mcp__a2a-bus__" + tool,
              "input": tool_input,
          }]}})


def emit_result():
    send({"type": "result", "subtype": "success", "num_turns": 1,
          "total_cost_usd": 0.0, "session_id": SESSION})


def handle(text: str):
    emit_init()
    if "[A2A incoming]" in text:
        # 提取 task_id,调 reply
        m = re.search(r"task=(\S+)", text)
        task_id = m.group(1) if m else "t_unknown"
        # 如果是 "ack from fake agent" 的回显(即我们之前发过的回复,说明对方也在回我们),
        # 就不再 reply,避免无限循环。
        if "ack from fake agent" in text:
            # 不 reply,直接 end turn
            pass
        else:
            emit_tool_use("reply", {"task_id": task_id, "content": "ack from fake agent"})
    else:
        m = re.search(r"send to (\w+)", text)
        target = m.group(1) if m else "bob"
        emit_tool_use("send_to", {"agent_id": target, "content": "hello " + target})
    emit_result()


def main():
    # 启动时立刻 emit system.init,让 Runner 的 ready 探针能就绪
    emit_init()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except Exception:
            continue
        if req.get("type") == "user":
            content = req.get("message", {}).get("content", "")
            if isinstance(content, list):
                content = " ".join(
                    (p.get("text") or "") for p in content if isinstance(p, dict)
                )
            handle(str(content))


if __name__ == "__main__":
    main()
