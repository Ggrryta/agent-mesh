# ADR 009：客户端 Token 刷新策略（定时续签 + 被动兜底）

- **状态**：Accepted
- **日期**：2026-05-13
- **关联**：ADR 007（API Key + JWT 混合认证）

## 背景

ADR 007 定义了 agent JWT 的签发路径（`POST /v1/mesh/auth/token`），但没有
规定客户端**什么时候**来刷。留空带来几个风险：

- **第一次业务请求必然延迟**：agent 长时间空闲后第一个请求撞到 40110 →
  触发刷新 → 重试，总共 3 次 HTTP 往返
- **刷新风暴**：N 个 agent 同时启动，TTL 到期时**同时**触发刷新，Gateway 瞬时压力
- **并发请求撞 40110 各自刷**：10 个业务请求同时过期，如果各自独立刷，会打 10 次 DB 查询
- **启动时密钥错误无感知**：agent 启动正常，第一个业务请求才发现 API Key 无效

本 ADR 约束**所有 agent 客户端 SDK**（Week 4 的 Go SDK 及以后可能加的
Python / TS SDK）按统一策略刷 token。

## 决策

**定时续签作主路径 + 40110 被动刷作兜底**，二者并存。

### 1. 启动路径

Agent 初始化时**必须先换第一把 JWT** 才进入可用状态：

```
NewClient(ctx, cfg):
    POST /v1/mesh/auth/token
      X-Api-Key: cfg.APIKey
      body: {"agent_id": cfg.AgentID}
    -> 40111 / 40112 / 40113 → 启动失败，直接抛错
    -> 200 → 保存 token，启动 refresh goroutine
```

启动失败的 3 种错误码都是**配置问题**（key 格式错 / 被吊销 / agent 不归你），
应当让 agent 进程**直接退出**或至少报告给运维，而不是"先起来再说"。

### 2. 主路径：定时在 TTL 的 2/3 处刷新

```
token TTL = 3600s (服务端 JWT_AGENT_TTL，从 expires_in 读)
refresh 周期 = TTL * 2/3 + jitter
jitter      = ± TTL * 5%
            = (2/3 ± 5%) * TTL   ≈ 36min ~ 45min 之间
```

**为什么 2/3**：
- 不硬编码 40min 或 45min — TTL 改了 SDK 自动跟
- 1/3 TTL 的缓冲 = **网络抖动 + 退避重试 + 降级恢复** 的窗口

**为什么 ±5% jitter（必须）**：
- N 个 agent 同时启动 → 40min 后**同时**刷 → Gateway 瞬时 N 倍 QPS
- ±5% = ±3min 把刷新时刻错开，Gateway 平稳

**实现示意**：

```go
func (c *Client) refreshLoop(ctx context.Context) {
    for {
        delay := c.ttl * 2 / 3
        jitter := time.Duration(rand.Int63n(int64(c.ttl / 10))) - c.ttl/20
        select {
        case <-ctx.Done():
            return
        case <-time.After(delay + jitter):
            c.refreshWithBackoff(ctx)
        }
    }
}
```

### 3. 主路径：刷新失败的分级处理

```
refresh() 返回的情况      │  SDK 行为
──────────────────────────│──────────────────────────────────
网络错误 / 5xx            │  退避重试：5s / 15s / 30s，最多 3 次
401 40111 token_invalid   │  同上（JWT 格式错，罕见；继续用 API Key 换）
401 40112 key_revoked     │  停止 refresh goroutine，抛 KeyRevokedError
403 40113 agent_not_owned │  同上（配置错误，停止 loop）
200 OK                    │  原子替换 token，重置下一个刷新窗口
```

**40112 / 40113 为什么停止 loop**：这两种错误是**持久性的**（除非运维重新签
key），继续刷只是白做；客户端应上报给监控 / 日志。

### 4. 主路径：降级模式 + 自愈

连续 3 次刷新失败（不含 40112/40113）→ 进入**降级模式**：
- refresh loop **不退出**，但切换成 `TTL/3` 的探测节奏
- 业务请求仍用旧 JWT 照打；JWT 一旦自然过期，由"被动刷兜底"接管
- 每 `TTL/3` 尝试一次刷新；成功则恢复正常 2/3 节奏

```
normal → 3x fail → degraded ─── every TTL/3 ─── try refresh ─ ok ─▶ normal
                                                             │
                                                             └─ fail ─▶ 维持 degraded
```

**为什么不直接退出 loop**：网络抖动 3 次不代表永久挂了。真正持久性的
失败（40112/40113）已经单独处理。

### 5. 兜底：被动刷（收到 40110 时）

所有业务请求都包一层 SDK `Do()`：

```go
func (c *Client) Do(req *http.Request) (*http.Response, error) {
    resp, _ := c.doOnce(req)
    if errCode(resp) != 40110 {
        return resp, nil
    }
    // 被动刷：single-flight 合并
    _, _, _ = c.refreshGroup.Do("refresh", func() (any, error) {
        return nil, c.refreshOnce()
    })
    return c.doOnce(req) // 仅重试一次，不循环
}
```

**single-flight 是必需的**。没它，N 个并发请求同时撞 40110 会打 N 次
DB 查询。用 `singleflight.Group` 让第一个触发，其它挂起等同一次结果。

**为什么只重试 1 次**：刷新后仍 40110 在理论上可能（极端时钟漂移），
但继续循环没意义 —— 把错误抛给业务层，由上层处理。

### 6. 并发与线程安全

- **Token 存储**：用 `atomic.Value` 或 `sync.RWMutex`。读频繁（每请求）、
  写稀少（每 40min），`atomic.Value` 更合适
- **Close** / ctx cancel：refresh goroutine 在收到 ctx.Done 时立即退出，
  不等当前的 refresh 完成（如果正在刷就让它跑完，反正刷成功也没人读了）

## 考虑过的替代

### A. 纯被动（仅 40110 触发刷新）
- 优点：实现最简
- 缺点：
  - 长空闲后第一个请求必然 3 次 HTTP
  - 10 个并发请求同时撞过期要 single-flight 合并（还是得做）
- **放弃**：主动已是必做，纯被动没好处

### B. 阈值刷（每请求前检查 `exp - now < 10min` 就刷）
- 优点：永不撞 40110
- 缺点：
  - 每请求都要算一次 `time.Now() - exp`
  - 业务请求分布不均时，刷新时刻也会不均匀（10000 QPS 时每秒都有刷请求）
- **放弃**：节奏不可预测，定时路径无法推理

### C. 定时刷 + 阈值刷（双保险）
- 优点：最严密
- 缺点：复杂度无收益 —— 定时刷已经在 2/3 TTL 处刷，不会撞过期
- **放弃**

### D. 被动续签（服务端响应头 X-New-Token）
- 详见 ADR 007：服务端刻意不做
- 本 ADR 不改变这个决策

## 参数总结

| 参数 | 取值 | 注释 |
|---|---|---|
| 启动 | 必须签第一把 JWT | 失败直接报错 |
| 主刷频率 | TTL × 2/3 | 默认 TTL=1h → 40min |
| jitter | ± TTL × 5% | 默认 ±3min |
| 刷新失败退避 | 5s / 15s / 30s | 3 次 |
| 降级节奏 | TTL × 1/3 | 默认 20min 探测一次 |
| 被动刷重试次数 | 1 次 | 不循环 |
| 并发刷合并 | single-flight | 必须做 |
| 40112 / 40113 | 停止 loop，上报 | 配置错误 |
| 网络错误 / 40111 | 退避重试 | |

## 对服务端的影响

**零。** `/v1/mesh/auth/token` 的行为不变，不 care 客户端用什么节奏来刷。
这个 ADR 全部是客户端约束。

## 对 SDK 实现者的要求

凡是要接 Agent-Mesh 的 SDK（不论语言），都**应该**按本 ADR 实现 refresh
策略。不严格强制，但不遵守会带来：
- Gateway 侧 `/auth/token` QPS 尖峰（没有 jitter）
- 多 agent 场景下 DB 压力放大（没有 single-flight）
- 长空闲后首请求延迟（没有主动刷）

Week 4 Go SDK 里会提供一个**可复用的 refresh loop 实现**（`sdk/auth`
子包），其它语言 SDK 实现时照抄流程即可。

## 后果

### 得
- 业务请求的 JWT 永远"温热"，不会撞过期（正常情况）
- 失败分级清晰：暂态退避 / 持久态上报 / 降级自愈
- 并发场景零冗余刷新（single-flight）
- N agent 场景刷新分布平滑（jitter）

### 失
- 客户端实现复杂度 +200 行（refresh loop + single-flight + 分级错误处理）
- 但这段代码写一次在 SDK 里，业务方零感知

### 可测试性
- SDK `sdk/auth` 子包要有单测覆盖：
  - 2/3 TTL + jitter 窗口正确
  - 刷新失败退避次数
  - 40112 停止 loop
  - 降级 + 自愈
  - single-flight 合并并发请求

## 参考

- ADR 007：API Key + JWT 混合认证
- docs/api.md §Auth §客户端刷新策略
- Week 4 Day 4：实现 Go SDK 的 `sdk/auth`
