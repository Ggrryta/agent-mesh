# Benchmarks

> 性能基线 + 容量规划数据。

## 测试环境

- **Gateway**: 2 副本，每副本 200m CPU / 256Mi memory
- **MySQL**: 8.0，单实例，2 core 4G
- **Redis**: 7，单实例
- **Pressure tool**: k6
- **测试网络**: docker-compose 本地，无网络抖动

## 测试场景

### 场景 1：点对点 task 提交

脚本：`test/load/p2p_submit.js`

- 50 → 200 → 500 req/s 阶梯压测
- 测试用户通过 admin API 给 alice 发 task
- 关键端点：`POST /v1/admin/tasks`

**预期基线**（待实测填充）：

| 阶段 | QPS | P50 | P95 | P99 | 错误率 |
|-----|-----|-----|-----|-----|--------|
| 预热 | 50 | TBD | TBD | TBD | <0.1% |
| 常规 | 200 | TBD | TBD | TBD | <0.5% |
| 冲击 | 500 | TBD | TBD | TBD | <2% |

**通过标准**：
- P95 < 500ms
- P99 < 2s
- 错误率 < 1%

### 场景 2：群组协作 fan-out

脚本：`test/load/group_fanout.js`

- N 个 agent 同时给共享 context 发消息
- 触发 timeline_update 写入 N-1 次 inbox
- 测试 SQL 写入瓶颈

**预期基线**（10 人群，10 req/s）：

| 维度 | 数值 |
|-----|-----|
| 每条消息 inbox 写入次数 | 9 |
| 总 inbox INSERT QPS | 90 |
| 单条消息端到端延迟 P95 | <300ms |

**结论模板**：在 50 人群、10 msg/s 场景下，每条消息触发 49 次 inbox INSERT，
总 IOPS 约 490。MySQL 单实例可承受到 50-100 倍负载。

### 场景 3：WebSocket FeedHub

脚本：`test/load/ws_feed.js`

- 200 并发 WebSocket 连接同时订阅
- 业务侧每秒触发 ~50 个 feed 事件
- 测试 Gateway 内存 + 推送延迟

**预期基线**：

| 维度 | 数值 |
|-----|-----|
| 单 Pod 最大连接数 | TBD |
| 推送延迟 P95 | <100ms |
| 内存占用增量 | ~1MB / 100 连接 |

## 跑测步骤

```bash
# 1. 起依赖
docker compose -f docker-compose.dev.yml up -d

# 2. 起 Gateway
cd gateway
MYSQL_DSN="..." REDIS_ADDR="..." JWT_SECRET="..." \
  go run ./cmd/server/

# 3. 准备测试数据
USER_TOKEN=$(bash test/load/setup.sh)

# 4. 跑压测
k6 run -e BASE_URL=http://localhost:8080 \
       -e USER_TOKEN=$USER_TOKEN \
       test/load/p2p_submit.js
```

## 容量规划建议

基于上述基线（待实测验证）：

| 业务规模 | 副本数 | CPU req | Mem req | 备注 |
|---------|-------|---------|---------|------|
| 0-100 agent，<10 task/s | 2 | 200m | 256Mi | MVP |
| 100-500 agent，<100 task/s | 5 | 500m | 512Mi | 中等规模 |
| 500-2000 agent，<500 task/s | 10+HPA | 500m | 512Mi | 大规模 |

HPA CPU 阈值默认 70%，达到后自动扩容。Mem 一般不是瓶颈（Gateway 是 stateless）。

## 已知瓶颈

1. **Inbox INSERT**：群组消息 fan-out 是 O(N)，超大群（>200 人）需要批量 INSERT 优化
2. **WebSocket 单 Pod 上限**：每连接有内存开销，建议单 Pod 不超过 1000 连接
3. **MySQL 写入**：高频 task 创建场景下，主从延迟需关注

## 待补充

- [ ] 实际跑出来的 P50/P95/P99 数据
- [ ] 不同副本数下的吞吐量曲线
- [ ] 内存增长曲线（长跑测试）
- [ ] 与同类系统的对比（如有）
