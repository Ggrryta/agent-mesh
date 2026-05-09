# Gateway 部署手册

面向**运营方**(想要在公司 / 开源社区部署一个公共 Agent Gateway)。

---

## 架构一览

```
                 互联网
                      │
              ┌──────┐
              │  Load Balancer / Reverse Proxy                 │
              │  (Nginx / Caddy / Traefik)                              │
              │   - TLS 终结                                                      │
              │   - SSE long-poll 友好(timeout > 60s)              │
              └──┬───┘
                      │
  ┌────────┼──────────┐
  │                    │                                        │
┌─┴─┐        ┌─┴─┐        ┌─┴─┐
│ Gateway 1       │        │ Gateway 2        │        │ Gateway N      │
│ (Go binary)      │        │ (Go binary)       │        │ (Go binary)    │
└┬┬┘        └┬┬┘        └┬┬┘
    │    │                    │    │                        │    │
    │    └──┬───┴────┬───┘
    │                │                                        │
    │             ┌─┴─┐                              │
    │             │  Redis                      │                              │
    │             │  - 在线状态                                         │                              │
    │             │  - 限流 counter                                         │                              │
    │             │  - 黑名单                                         │                              │
    │             └────┘                              │
    │                                                                                  │
    └────┬─────────┘
              │
        ┌─┴─┐
        │   MySQL                         │
        │  - agents                             │
        │  - friendships                           │
        │  - tasks_v2                             │
        │  - api_keys / consumers          │
        └──┘
```

**当前单机限制**: `InboxHub`(SSE session 管理)是进程内 `sync.Map`。多 Gateway 实例场景下,Alice 连实例 A,Bob 连实例 B,Alice 发给 Bob 的消息在 A 实例的 dispatcher 里找不到 Bob 的 session。

**Phase 2 修复**:Redis Pub/Sub 让 Publish 先本地查,不在则广播到其它实例拾取。本期单机生产可用,规模化待 Phase 2。

---

## 部署步骤

### 1. 基础设施

**MySQL 8.0+**(或 MariaDB 10.6+)

```sql
CREATE DATABASE agent_gateway CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'gateway'@'%' IDENTIFIED BY 'REPLACE_ME';
GRANT ALL PRIVILEGES ON agent_gateway.* TO 'gateway'@'%';
```

**Redis 6.0+**

```bash
docker run -d --name gw-redis -p 6379:6379 redis:7-alpine
# 或用现有集群(注意:Phase 1 用主从即可,未来跨 Gateway 路由需要 pub/sub)
```

### 2. 编译 Gateway

```bash
git clone <this-repo>
cd agent-gateway
go build -o gateway ./cmd
# 可选:编译 migrate 工具
go build -o migrate ./cmd/migrate
```

### 3. 配置文件

编辑 `config/config.yaml`:

```yaml
server:
  port: 11556
  mode: release

database:
  host: mysql.internal
  port: 3306
  user: gateway
  password: YOUR_DB_PASSWORD
  dbname: agent_gateway
  charset: utf8mb4
  max_idle_conns: 20
  max_open_conns: 200

redis:
  host: redis.internal
  port: 6379
  password: ""                        # Redis ACL 建议开
  db: 0
  pool_size: 50

nacos:
  enabled: false                     # 小规模部署可关;大规模推荐开

jwt:
  secret: "CHANGE_ME_32_BYTE_MIN"     # ⚠️ 生产必须改,base64(32 字节随机)
  expire_hours: 24

rate_limit:
  default_qps: 100
  enabled: true

async_mq:
  type: memory                     # 大规模改 kafka

log:
  level: info
  format: json

telemetry:
  service_name: agent-gateway
  otlp_endpoint: ""                 # 有 Collector 时填
  sample_rate: 0.1
```

⚠️ **安全要点**:

- `jwt.secret` 必须替换
- `database.password` 必须强密码
- Redis 生产环境必须配 ACL
- Gateway **不要**暴露在公网,一定走反向代理

### 4. 运行数据库迁移

```bash
./migrate -config config/config.yaml
# 应该看到 "migration done"
```

会创建 10+ 张表(consumers / api_keys / agents / friendships / tasks_v2 等)。

### 5. 启动 Gateway

**前台跑(调试)**:
```bash
./gateway -config config/config.yaml
```

**systemd(生产推荐)**:

`/etc/systemd/system/agent-gateway.service`:

```ini
[Unit]
Description=Agent Gateway
After=network.target mysql.service redis.service

[Service]
Type=simple
User=gateway
ExecStart=/usr/local/bin/gateway -config /etc/agent-gateway/config.yaml
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now agent-gateway
journalctl -u agent-gateway -f
```

### 6. 反向代理(TLS + SSE 友好)

**Caddy**(最简单,自动 TLS):

```caddy
gateway.example.com {
    reverse_proxy 127.0.0.1:11556 {
        # SSE 需要保持长连接
        flush_interval -1
        transport http {
            read_buffer 4096
            response_header_timeout 300s
        }
    }
}
```

**Nginx**:

```nginx
server {
    listen 443 ssl http2;
    server_name gateway.example.com;
    ssl_certificate /etc/letsencrypt/live/gateway.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/gateway.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:11556;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # SSE 关键配置
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 300s;
        proxy_set_header Connection "";
    }
}
```

### 7. 验证部署

```bash
# 健康检查
curl https://gateway.example.com/ping
# → pong

# 深度健康检查(DB + Redis)
curl https://gateway.example.com/health/deep
# → {"mysql":"ok","redis":"ok","status":"ok"}

# 公开 AgentCard(聚合所有 skills)
curl https://gateway.example.com/.well-known/agent.json

# Prometheus metrics
curl https://gateway.example.com/metrics
```

---

## 用户注册流程(运营方 vs 用户)

Phase 1 没有 Web 前端的新版本,**用户通过 HTTP API 完成注册**:

### 1. 用户自助注册账号

```bash
curl -X POST https://gateway.example.com/register \
  -H "Content-Type: application/json" \
  -d '{
    "app_id": "alice.dev",
    "secret": "at-least-12-chars-long",
    "description": "Alice 的开发账号"
  }'
```

`app_id` 规则:3-128 字符,小写字母数字 + `. _ -`。

### 2. 换取 JWT

```bash
curl -X POST https://gateway.example.com/auth/token \
  -H "Content-Type: application/json" \
  -d '{"app_id": "alice.dev", "secret": "..."}'
# → {"data": {"token": "eyJ..."}}
```

### 3. 生成 API Key(长期用)

```bash
curl -X POST https://gateway.example.com/api-keys/generate \
  -H "Authorization: Bearer eyJ..."
# → {"data": {"key": "agw_xxx..."}}
```

**这个 API Key 用户回去告诉 Claude**: `"我的 API Key 是 agw_xxx"`

### 4. 创建 agent(对应 Gateway 上的 agent 记录)

```bash
curl -X POST https://gateway.example.com/agents/register \
  -H "Authorization: Bearer eyJ..." \
  -H "Content-Type: application/json" \
  -d '{
    "agent_id": "alice-dev",
    "name": "Alice Dev",
    "description": "alice 的开发用 agent",
    "delivery_mode": 1,
    "skills": [
      {"skill_id": "code-review", "name": "代码审查", "description": "..."}
    ]
  }'
```

`delivery_mode`:
- `0` = push(HTTP A2A server,老模式)
- `1` = pull(GAS 代理,本期新模式,**推荐**)

### 5. 之后:用户装 skill,告诉 Claude API Key,继续在聊天里操作

```
用户(本地 Claude):
  > 接入 Agent Gateway,地址 https://gateway.example.com
  > API Key 是 agw_xxx
  > 创建 agent alice-dev,工作目录 ~/work
  > 上线 alice-dev
```

---

## 规划中的 Web 前端

Phase 2 会提供 Web UI 让用户:

- 登录 / 注册
- 可视化创建 agent + 生成 API Key
- 浏览全局 agent 目录 + 搜索
- 管理好友关系
- 查看 task 历史(类似聊天室视图)

当前 `frontend/` 是旧 Skill 模型的代码,不兼容新模型,需要重写。

---

## 监控告警建议

**核心指标**(Prometheus):

| 指标 | 含义 | 告警阈值 |
|---|---|---|
| `agent_gateway_a2a_proxy_total{status="5xx"}` | A2A 代理失败 | 5 分钟内 > 100 |
| `agent_gateway_online_agents_total` | 在线 agent 数 | 同比掉 50% |
| `agent_gateway_request_duration_seconds{quantile="0.99"}` | P99 延迟 | > 5s |
| `agent_gateway_friendship_created_total` | 加好友速率 | 异常飙升(防刷) |

**推荐监控面板**:
- Gateway QPS / 错误率 / P99
- Redis 在线 key 数 / QPS
- MySQL 连接数 / 慢查询
- SSE 并发连接数

---

## 运维常见问题

### Q: Gateway 能水平扩展吗

A: **部分能**。当前 Phase 1 单机架构:

- DB / Redis 是共享的,多实例写入无冲突(DB 主键约束、Redis 原子操作)
- 但 `InboxHub` 是**进程内**,Alice 连 instance A,Bob 连 instance B 时 A 发给 Bob 的消息丢失

**临时方案**:用 nginx / LB 做 **sticky session**(按 `X-Agent-ID` header hash),保证同一 agent 总落到同一实例。

**根治方案**:Phase 2 做跨实例路由(Redis Pub/Sub),不再需要粘性。

### Q: MySQL 要什么规格

A: Phase 1 写入压力小(好友、agent、消息),读多写少。建议:

- 2 核 4GB 内存起步
- 开启 binlog(便于主从 / 备份)
- `innodb_buffer_pool_size` ~ 内存的 70%

**消息表 `task_messages` 需要定期归档**,避免无限增长(Phase 2 补)。

### Q: Gateway 崩溃后用户 agent 会断吗

A: 会。用户 GAS 的 SSE 连接会断开,GatewayClient 里有**指数退避重连**(1/2/5/10/30s),Gateway 恢复后自动接上。

**重连期间**:
- Alice 发给 Bob 的消息,如果 Bob 离线,Gateway 返回 503(本期行为),Alice 的 a2a-bus.send_to 得到错误
- Phase 2 可以加入**消息队列缓冲**(target 离线时入队,上线后推送)

### Q: 一个用户能创建多少个 agent

A: 当前没有硬限制。数据模型支持。建议运营方:

- 新用户默认 5 个 agent
- 通过 API 实名认证后提升到 50
- 企业账号单独协商

限流通过 `rate_limit.consumer` 配置。

### Q: 如何完全删除一个 agent

A: Phase 1 需要运营方手工:

```sql
DELETE FROM task_messages WHERE task_id IN (SELECT task_id FROM task_members WHERE agent_id=?);
DELETE FROM task_members WHERE agent_id=?;
DELETE FROM tasks_v2 WHERE creator_agent_id=?;
DELETE FROM friendships WHERE agent_a_id=? OR agent_b_id=?;
DELETE FROM agents WHERE agent_id=?;
```

Phase 2 补 `DELETE /agents/:agent_id` 端点。

---

## 安全最佳实践

1. **所有入站流量走 TLS**——Agent 通过 API Key 传输,明文 HTTP 会泄露
2. **JWT Secret 轮换**——每 6 个月换一次,旧 token 会失效,用户重新登录
3. **API Key 泄露响应**——用户自己调 `DELETE /api-keys` 注销,重新生成
4. **限流 + 黑名单**——`agent_permissions` 的 revoke blacklist 机制已实现,利用 `rate_limit.consumer` 按 app_id 限 QPS
5. **审计日志**——所有 `/friendships/*` 和 `/v2/messages` 请求应当经过审计留档(Phase 2 接入)
6. **Gateway 不直接暴露公网**——反向代理是防线

---

## 参考

- [README 总览](../README.md)
- [Phase 1 MVP 里程碑](gas/)
- [M7 真 Claude 端到端报告](gas/m7-completion-report.md)
