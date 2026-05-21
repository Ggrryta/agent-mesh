# Week 4 — Long-poll + FeedHub + GAS Daemon

> 2026-05-13。Week 4 主体完成。

## 核心决策变更

Week 4 开工前对齐了三个重大设计调整：

1. **SSE 砍掉**（ADR 011）：agent 间通信是异步长任务语义，long-poll 给出
   等价延迟但保持 Gateway 无状态。前端实时性由独立的 FeedHub WebSocket 保证。

2. **群组推迟到 Week 5**：当前核心场景是点对点协作（Alice↔Bob），群组
   fan-out + Outbox 留到下周。

3. **GAS 是本地 daemon，不是 SDK 库**：agent 通过 MCP skill 调用 GAS，
   GAS 内部维护 JWT 续签 + inbox long-poll + MCP server。任何支持 MCP 的
   agent（如 Claude Code）装载 mesh skill 即可接入，零二次开发。

## 已交付

### 决策与文档
- **ADR 011**：Long-poll 替代 SSE
- **ADR 012**：FeedHub 设计（Redis Pub/Sub + WebSocket）
- **skills/mesh/SKILL.md**：Claude 使用 mesh 通信的说明

### Gateway 侧代码

**Day 1：Inbox Long-poll**
- `domain/inbox/service.go`：`PollWithWait(ctx, agentID, sinceID, limit, timeout)`
- handler 支持 `wait` query 参数（0-30s），向后兼容
- 5 个单测（立即返回/等待到达/超时/零等待/ctx取消）

**Day 2：FeedHub**
- `internal/feed/hub.go`：Hub + Subscriber + FeedEvent
- Redis Pub/Sub 跨 Pod 广播（`PSUBSCRIBE feed:user:*`）
- 本地 subscriber map + 非阻塞投递
- 7 个单测

**Day 3：Admin WebSocket**
- `internal/api/admin/ws_feed.go`：gorilla/websocket 升级 + 读写 goroutine
- mesh handler 加 `publishFeed` 钩子（submit/message/artifact/transition）
- Handler 改为 Option 模式（`WithFeed` / `WithLogger` / `WithAgentLookup`）
- 3 个 WebSocket 测试（收到事件/无auth拒绝/用户隔离）

### GAS Daemon 代码

**Day 4：核心**
- `gas/daemon/internal/gateway/auth.go`：AuthManager（ADR 009 续签）
- `gas/daemon/internal/gateway/client.go`：GatewayClient（HTTP + long-poll）
- `gas/daemon/cmd/gasd/main.go`：入口 + inbox poller + heartbeat
- 4 个单测（bootstrap 成功/失败/refresh loop/poll inbox）

**Day 5：MCP Server**
- `gas/daemon/internal/bus/server.go`：JSON-RPC over stdin/stdout
- 4 个 MCP tools：`mesh_send_message` / `mesh_reply` / `mesh_get_inbox` / `mesh_transition`
- `DispatchEvent` → MCP notification 推给 Claude
- 5 个单测（initialize/tools list/send message/reply/dispatch event）

### E2E 验证

**Day 6：端到端联调**（全内存，不依赖 MySQL/Redis/k3d）

完整场景（Alice → Bob，同 owner）：
1. ✅ Alice POST /tasks → task 创建
2. ✅ 前端 WebSocket 收到 `task_created` 事件（×2，alice+bob 同 owner）
3. ✅ Bob GET /inbox → 收到 message 事件
4. ✅ Bob POST /messages → 回复 Alice
5. ✅ 前端收到 `task_message` 事件
6. ✅ Bob POST /transition(working) → 前端收到 transition
7. ✅ Bob POST /transition(completed) → 前端收到 transition
8. ✅ Alice GET /inbox → 收到 3 个事件（reply + working + completed）
9. ✅ Long-poll wait=1s 超时正确返回

## 测试汇总

| 模块 | 测试数 | 状态 |
|------|--------|------|
| Gateway（19 个包） | 全部通过 | ✅ |
| GAS daemon（2 个包） | 9 个测试 | ✅ |
| E2E | 1 个完整场景（9 个断言） | ✅ |

## 代码量

- Gateway 新增约 **600 行**（feed hub + ws handler + long-poll + publishFeed）
- GAS daemon 新增约 **800 行**（auth + client + bus + main + tests）
- Skill 文档 **80 行**

## 关键工程决策

1. **Long-poll 用 DB 轮询而非 channel 通知**：500ms 间隔对异步任务足够，
   避免引入 notifier 复杂度。未来可加 channel 唤醒优化。

2. **FeedHub 触发点在 handler 层**：只推已确认持久化的事件，不在 domain 层
   触发（避免事务未提交就推送的问题）。

3. **publishFeed 推给双方 owner**：submit 时推 from + to 两个 agent 的 owner，
   确保双方前端都能看到。

4. **GAS 用 Go 而非 Python**：与 Gateway 同语言，复用 auth 逻辑模式，
   编译为单二进制部署简单。MCP server 用 stdin/stdout JSON-RPC。

5. **Admin Handler 改 Option 模式**：`New()` 保持向后兼容（可选依赖通过
   `WithFeed` / `WithLogger` 注入），不破坏已有测试。

## 遗留 / 后续

### 没做的（刻意留给未来）

- **群组消息 + Outbox + fan-out**：Week 5
- **GAS AgentManager**（拉起 Claude Code）：Week 5+
- **GAS FeedStorage SQLite**（本地缓存）：Week 5+
- **GAS ControlAPI**（localhost 管理接口）：Week 5+
- **k3d 滚动升级验证**：需要实际集群，本周用内存 E2E 替代
- **Prometheus 指标**：Week 5 observability 集中补

### 略微不完美的点

- E2E 是全内存 stub，没有真正跑 k3d 双副本
- FeedHub 的 Redis Pub/Sub 路径没有集成测试（需要真 Redis）
- GAS daemon 的 MCP server 没有测试 stdin/stdout 真实管道场景

## 下一步（Week 5）

原 PLAN Week 5 是治理层 + 观测 + 群组 API + FeedHub。按现在进度：
- FeedHub 已在 Week 4 做完
- Week 5 主要补：群组 + Outbox + Prometheus + OTel + 限流/熔断

建议顺序：
1. Day 1-2：群组模型（groups / group_members 表 + domain）
2. Day 3-4：Outbox + MQ 抽象 + fan-out
3. Day 5-6：Prometheus + OTel 接入
4. Day 7：限流/熔断 + 文档
