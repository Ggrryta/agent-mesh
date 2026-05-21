# Runbook

> Agent-Mesh 生产运维手册。每个告警 / 故障场景对应一个明确的处置流程。

## 启动 / 停机

### 启动顺序

1. 确认 MySQL + Redis 可达
2. 跑 migration（如有 schema 变更）
3. `helm upgrade --install` 或 `kubectl rollout restart`
4. 等所有 Pod `/readyz` 返回 200

### 优雅停机

K8s 滚动升级时自动执行：

1. Pod 收到 SIGTERM
2. preStop hook sleep 5s（让流量从 Service 摘除）
3. Gateway 进程 shutdown：拒绝新请求 + 等 in-flight 完成（最多 60s）
4. terminationGracePeriodSeconds=90s 兜底

### 强制停机

仅在卡住时使用：

```bash
kubectl -n agent-mesh delete pod <pod> --grace-period=0 --force
```

风险：in-flight 请求被中断，agent 会从 inbox 拉到这些事件。

## 常见故障

### `/readyz` 返回 503

**排查顺序**：

1. 看 `/readyz` 响应 body，明确哪个依赖失败
2. MySQL：`kubectl exec` 进 Pod 测试 `mysql -h ... -p`
3. Redis：`redis-cli -h ... ping`

**处置**：依赖恢复后 Gateway 自动恢复，无需重启。详见 `test/chaos/scenario_mysql_down.sh`。

### Gateway P99 延迟飙升

**排查**：

1. Grafana 看 `mesh_http_request_duration_seconds` 按 path 分组，定位是哪个端点慢
2. 看 MySQL 慢查询日志
3. 看下游 agent push 失败率（`mesh_inbox_push_fail_total`）

**处置**：

- MySQL 慢：加索引、扩 RDS 实例
- 下游 agent 慢：熔断器会自动打开保护
- 整体 CPU 满：HPA 应该自动扩容；没扩说明阈值要调

### Task 失败率高

**告警**：`TaskFailureRateHigh`

**排查**：

1. Grafana `mesh_task_transitions_total{to_state="failed"}` 看哪些 agent 在失败
2. `kubectl logs` Gateway 找 task error 日志
3. 排查具体 agent 是不是有问题

**处置**：通常不是 Gateway 问题，需要联系 agent 维护方。

### Push 大量失败

**告警**：`PushDeliveryFailing`

**排查**：

1. 是否所有 agent 都失败 → 网络问题
2. 单个 agent 失败 → 该 agent URL 不可达 / 已下线
3. 看 `mesh_breaker_state` 看熔断器是否打开

**处置**：Push 失败不丢消息，agent 下次拉 inbox 会补齐。但可能需要清理"幽灵 agent"（注册了 URL 但实际不在线的）。

### 熔断器打开

**告警**：`CircuitBreakerOpen`

**排查**：

1. `mesh_breaker_state{agent_id="xxx"} == 2` 哪个 agent
2. 该 agent 是否 drain / 下线 / 网络隔离
3. 该 agent 错误模式（看错误日志）

**处置**：30s 后熔断器自动半开探测。期间该 agent 收不到 push，但能从 inbox 拉到。

### 限流拒绝飙升

**告警**：`RateLimitSpike`

**排查**：

1. `mesh_ratelimit_reject_total` 按 agent_id 看是单个还是全局
2. 单个 agent 异常 → 联系运维介入
3. 全局飙升 → 可能是攻击或 GAS daemon bug

**处置**：紧急情况下可调整 limiter 配置（需重新部署）。

### 在线 agent 数骤降

**告警**：`OnlineAgentsDropped`

**排查顺序**：

1. Gateway 是否健康（自身故障会让所有 agent 看起来都掉了）
2. Redis Pub/Sub 是否正常（agent cache 跨 Pod 同步依赖它）
3. 网络分区？检查 Pod 状态 + Node 状态

**处置**：Gateway 恢复后 agent 心跳会重新填充 cache，~1min 内恢复。

### MySQL 不可用

**预期降级行为**（已通过 `test/chaos/scenario_mysql_down.sh` 验证）：

- `/healthz` 仍 200（liveness 通过）
- `/readyz` 503（K8s 摘除流量）
- `/metrics` 仍 200
- 业务端点快速失败（~15ms 返回 5xx），不 hang

**处置**：恢复 MySQL，Gateway 自动恢复（~2s）。

### Pod OOMKilled

**告警**：`PodOOMKilled`

**排查**：

1. `kubectl describe pod` 看 last terminated reason
2. Grafana 看 `container_memory_usage_bytes` 趋势
3. 是否有内存泄漏（看 goroutine 数量趋势 `go_goroutines`）

**处置**：

- 临时：`helm upgrade --set resources.limits.memory=1Gi`
- 长期：定位泄漏点（pprof）

## 数据恢复

### MySQL 备份恢复

依赖云厂商 RDS 的自动备份。手工恢复：

```bash
mysqldump > backup.sql
mysql < backup.sql
```

恢复后 Gateway 无需特殊操作，自动重连即可。

### Redis 数据丢失

Redis 只用于 Pub/Sub 和缓存（FeedHub），数据丢失不影响业务正确性：

- FeedHub 跨 Pod 广播失效 → 单 Pod 内仍正常
- agent cache 重建（agent 心跳触发）

**处置**：清空重启 Redis，无副作用。

### Outbox 重放

Outbox 当前未挂载（保留给未来广播功能）。如启用后出现堆积：

```bash
# 查看 pending 事件数
kubectl exec mysql-pod -- mysql -e "SELECT COUNT(*) FROM outbox_events WHERE status='pending'"

# 强制重置失败状态以重试
kubectl exec mysql-pod -- mysql -e "UPDATE outbox_events SET status='pending', retries=0 WHERE status='failed'"
```

## 升级 / 回滚

### 滚动升级（默认）

```bash
helm upgrade agent-mesh ./deploy/helm/agent-mesh \
  -f deploy/helm/agent-mesh/values.production.yaml \
  --set image.tag=NEW_VERSION
```

`maxUnavailable=0 maxSurge=1`，零不可用。

### 回滚

```bash
# 看历史
helm history agent-mesh

# 回滚到上一版
helm rollback agent-mesh

# 或指定 revision
helm rollback agent-mesh 3
```

### Migration 顺序

加字段：先发新版（兼容旧字段为 NULL）→ migration → 全量切流。

去字段：先停止读 → 全量切流 → migration 删字段。

## 应急联系

- On-call: TBD
- Slack: #agent-mesh-ops
- PagerDuty: TBD
