"""
GAS 内部事件定义

两层事件:
  - InputEvent:外界 → Agent Core(用户指令 / 网关推送 / 系统通知)
  - OutputEvent:Agent Core → GAS(tool call / 文本 / 状态)

Adapter 负责在"宿主 stdout 原生事件格式"和"这些统一事件"之间翻译。
EventRouter 负责把 OutputEvent 分发到合适的去处:Gateway / feed / UI。
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Literal

InputKind = Literal["user_input", "a2a_incoming", "friend_status", "system"]

OutputKind = Literal[
    "send_message",   # agent 调 a2a-bus.send_to/reply → 要发消息
    "create_task",    # agent 调 a2a-bus.create_task
    "close_task",     # agent 调 a2a-bus.close_task
    "thinking",       # agent 内部思考文本(仅记录 feed)
    "log",            # agent 文本输出(仅记录 feed)
    "tool_call",      # agent 调用非 a2a-bus 的工具,仅 feed
    "turn_end",       # 一轮推理结束
    "error",
]


@dataclass
class InputEvent:
    kind: InputKind
    data: dict[str, Any] = field(default_factory=dict)


@dataclass
class OutputEvent:
    kind: OutputKind
    data: dict[str, Any] = field(default_factory=dict)
    raw: Any = None  # 保留原始 stdout JSON 便于调试
