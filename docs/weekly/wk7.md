# Week 7 — 测试 + 稳定性 + Helm Chart

> 2026-05-14。Week 7 完成。

## 范围调整

PLAN 原版包含 7 天计划（集成测试、压测、Chaos、Helm、Grafana、Alertmanager、文档）。
按"建议方案"精简为 6 天工作量：

- Day 1：扩展 E2E 测试（覆盖群组协作完整路径）
- Day 2：Helm Chart 替代 kustomize
- Day 3：k6 压测脚本（点对点 + 群组 fan-out + WebSocket）
- Day 4：Grafana dashboard JSON + Alertmanager 规则
- Day 5：Chaos 演练脚本 + 实际跑 MySQL down 场景
- Day 6-7：Deployment + Runbook + Benchmarks 文档

## 已交付

### Day 1：集成测试

`gateway/internal/e2e/week7_test.go` —— 群组协作完整路径 E2E：

1. 用户建群 + 加成员（包含好友校验测试，stranger agent 被 403 拒绝）
2. Roster 接口拿到 AgentCard
3. 用户通过 virtual-user-agent 下令 alice
4. alice 在群内主动给 bob 发子任务（**同群鉴权生效**）
5. bob 收到子任务并完成
6. Timeline 包含完整协作过程（user→alice + alice→bob + bob→alice）
7. Preview 字段端到端贯通验证

`group_repo_test.go` —— 内存群组 repo 实现（重用于 e2e 测试）

23 个包测试全绿，0 失败。

### Day 2：Helm Chart

`deploy/helm/agent-mesh/`：

- `Chart.yaml` + `values.yaml`（默认开发值）
- `values.production.yaml`（生产覆盖：3 副本起、HPA 启用、TLS、敏感字段占位）
- 8 个 template：`namespace`、`gateway-deployment`、`gateway-service`、
  `gateway-configmap`、`gateway-secret`、`frontend`（含 service）、
  `ingress`、`hpa`、`_helpers.tpl`

`helm lint` + `helm template` 双重验证通过。生产 values 渲染出 9 个 K8s 资源。

### Day 3：k6 压测脚本

`test/load/`：

- `p2p_submit.js`：点对点 task 提交，stages 50→200→500 req/s
- `group_fanout.js`：群组 timeline fan-out，N agent 并发写共享 context
- `ws_feed.js`：200 并发 WebSocket 连接 + 30s 长连接
- `setup.sh`：一键准备测试数据 + 输出 USER_TOKEN

全部带 thresholds（P95/P99 + 错误率），k6 跑完会自动判定 pass/fail。

### Day 4：Grafana + Alertmanager

`deploy/grafana/agent-mesh-dashboard.json` —— 6 个分组、14 个 panel：

- HTTP Traffic（请求率 + P50/P95/P99 延迟）
- Task Activity（创建/消息/产物/状态变更）
- Inbox & Push Delivery（拉取率 + 推送成功/失败）
- Feed（活跃订阅者 + 发布率）
- Governance（限流拒绝 + 熔断器状态）
- Resource（在线 agent 数 + 心跳率）

`deploy/alertmanager/rules.yaml` —— PrometheusRule CRD 格式：

- GatewayHighErrorRate（5xx > 5%）
- GatewayHighLatency（P99 > 2s）
- TaskFailureRateHigh（task 失败 > 10%）
- PushDeliveryFailing（push 失败率 > 50%）
- CircuitBreakerOpen
- RateLimitSpike
- OnlineAgentsDropped（在线数骤降 > 30%）
- PodOOMKilled

### Day 5：Chaos 演练

`test/chaos/`：

- `scenario_mysql_down.sh`：停 MySQL → 验证降级 → 恢复
- `scenario_redis_down.sh`：停 Redis → 验证降级

**实际跑 MySQL 场景验证通过**：

| 状态 | 期望 | 实测 |
|-----|------|------|
| `/healthz` | 200 | ✅ 200 |
| `/readyz` | 503 | ✅ 503 |
| `/metrics` | 200 | ✅ 200 |
| 业务接口延迟 | <100ms 快速失败 | ✅ 15ms |
| 恢复时间 | <30s | ✅ 2s |

设计契约（"Gateway 依赖缺失时探针正确响应、业务快速失败、自动恢复"）完全符合。

### Day 6-7：文档

- `docs/deployment.md` —— 完整生产部署指南：依赖、migration、Helm 命令、
  健康检查、Prometheus 接入、升级回滚、容量规划
- `docs/runbook.md` —— 运维手册：每个告警 / 故障场景对应明确处置流程
- `docs/benchmarks.md` —— 压测基线模板（实测数据待跑）
- `docs/weekly/wk7.md` —— 本周报

## 测试汇总

| 类别 | 数量 | 状态 |
|------|------|------|
| 单元测试 + 集成测试包 | 23 | ✅ 全部通过 |
| Week 7 新增 E2E | 1 | ✅ 群组协作完整路径 |
| Helm chart lint | 1 | ✅ 通过 |
| Chaos 实测场景 | 1 | ✅ MySQL down → recover 全流程符合预期 |

## 代码 / 资产量

- E2E 测试：~350 行（week7_test.go + group_repo_test.go）
- Helm chart：~250 行 YAML（8 templates + values）
- k6 脚本：~200 行
- Grafana JSON：~150 行
- Alertmanager 规则：~100 行
- Chaos 脚本：~80 行
- 文档：~600 行 markdown

合计约 **1700 行新增**。

## 关键决策

1. **Helm 替代 kustomize**：旧 kustomize 标注废弃，生产部署单一真相
2. **压测调整**：原 PLAN 的"群消息 fan-out 100 成员"改为 timeline_update 元数据 fan-out（符合新协作模型）
3. **Chaos 范围**：只做最小集（MySQL/Redis down），生产级 chaos（网络分区、节点失效）留后续
4. **Benchmarks 留模板**：实测数据需要真实集群跑出来，本周先准备脚本和模板

## 遗留 / 后续

- [ ] 真实跑 k6 脚本，填实 benchmarks.md 数据
- [ ] testcontainers-go 集成测试（连真实 MySQL/Redis）
- [ ] CI/CD pipeline（GitHub Actions / Jenkins）
- [ ] 生产级 chaos 工具（chaos-mesh）
- [ ] 蓝绿部署 / 金丝雀策略
- [ ] DBA review schema + index 优化建议

## 下一步

按 PLAN，Week 7 是最后一周。后续根据实际需求：

- **直接上线**：补 CI/CD + 真实环境压测，进入运维阶段
- **继续迭代**：群组进阶功能（消息 broadcast、动态成员、跨用户群组）、
  GAS daemon AgentManager（自动拉起 Claude Code）、
  Agent Card `.well-known` 端点
