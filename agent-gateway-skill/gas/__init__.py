"""
Gateway Agent Server (GAS)

在用户机器上本地运行的守护进程,负责:
  1. 启动/维护 Agent Core 子进程(claude -p / codex / gemini 等 headless 模式)
  2. 与 Gateway 通过 A2A 协议维持长连接,订阅 inbox 事件
  3. 对外提供 ControlAPI 给交互会话 layer 使用 (attach/detach/instruct/feed)

设计原则:
  - GAS 零智能,不调 LLM,不做基于消息内容的决策
  - 所有推理由 Agent Core 自身承担(复用用户已装 agent 框架)
  - 多 agent 并存,各自独立进程
"""

__version__ = "0.1.0"
