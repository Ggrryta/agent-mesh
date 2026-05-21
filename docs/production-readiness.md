# Production Readiness Backlog

> Agent-Mesh 距离生产级别的差距清单。Week 0-7 完成 MVP + 部署基础设施；
> 本文档列出上线前必须 / 应该 / 建议补齐的工作项。

**当前状态**：MVP 功能完整，部署清单就绪，文档齐全；缺 CI/CD、镜像、真实压测数据、生产级 secret 管理。

**上线最低工作量**：约 1-1.5 周全职。

---

## 优先级分层

| Tier | 含义 | 上线影响 |
|------|------|----------|
| P0 | 必须做 | 不做无法上线 |
| P1 | 应该做 | 不做能上线但稳定性堪忧 |
| P2 | 强烈建议 | 影响安全 / 合规 / 业务正确性 |
| P3 | 锦上添花 | 业务功能扩展 |

---

## P0 — 上线阻塞项

### P0-1: CI/CD Pipeline

**现状**：本地手工 `go test` + `helm install`，无自动化。

**目标**：
- PR 触发自动测试（go test + helm lint + 前端 tsc）
- 合并 main 自动构建镜像 + push 到 registry
- 自动 helm upgrade（dev → staging → prod 三级环境）
- 失败自动 rollback

**交付物**：
- `.github/workflows/test.yml` 或 `.gitlab-ci.yml`
- `.github/workflows/release.yml`（tag 触发发布）
- 部署脚本：`scripts/deploy.sh`

**工作量**：2-3 天

**Owner**：DevOps / 开发

---

### P0-2: 容器镜像构建

**现状**：
- Frontend Dockerfile 已就绪（nginx + 多阶段）
- **Gateway 没有 Dockerfile**

**目标**：
- Gateway 多阶段 Dockerfile（构建 + distroless 运行时）
- 镜像安全扫描（Trivy / Grype）
- 镜像签名（cosign）
- Push 到生产 registry

**交付物**：
- `gateway/Dockerfile`
- `.dockerignore`
- 镜像构建文档：`docs/build.md`

**工作量**：1 天

---

### P0-3: 真实压测 + 容量基线

**现状**：k6 脚本就绪（`test/load/`），但**没在生产规格机器上跑过**。

**目标**：
- 在生产同等规格的机器上跑 3 个 k6 场景
- 拿到 P50 / P95 / P99 / 错误率数据
- 内存 / goroutine 数长跑曲线（24h）
- 单 Pod / 多 Pod / 不同副本数下吞吐量曲线
- 基于实测数据设定 HPA 阈值

**交付物**：
- 填实 `docs/benchmarks.md`
- 调整 `deploy/helm/agent-mesh/values.production.yaml` 的 resources / HPA 阈值

**工作量**：1-2 天（含问题定位）

**风险**：可能压出未知 bug，需要预留修复时间

---

### P0-4: Secret 管理接入

**现状**：JWT secret / DB 密码通过 `helm --set` 传入，明文出现在命令历史里。

**目标**：
- 接入 ExternalSecrets / Vault / SealedSecrets / 公司密钥管理系统
- Secret 不进 git（`values.production.yaml` 里只能是占位）
- JWT secret 支持轮换（双 key 共存方案）

**交付物**：
- ExternalSecret CRD 模板（如用 ExternalSecrets Operator）
- `docs/secrets.md`：secret 来源 + 轮换流程

**工作量**：1 天（取决于公司基建）

---

### P0-5: 数据库迁移流程

**现状**：本地手工 `go run ./cmd/migrate`。

**目标**：
- 上线流程包含 migration 步骤（CI/CD 里跑）
- 大表加列用 Online DDL（pt-online-schema-change / gh-ost）
- 回滚预案（每个 migration 都有 down）
- migration 失败时业务降级策略

**交付物**：
- CI/CD 里的 migration job
- `docs/migration-playbook.md`：每次发版的 schema 变更检查清单

**工作量**：1 天

---

## P1 — 稳定性增强

### P1-1: 可观测性后端接入

**现状**：metrics 埋点 + dashboard JSON + 告警规则已就绪，但**未对接实际服务**。

**目标**：
- Prometheus 实例对接 Gateway scrape
- Grafana 实例导入 dashboard
- Alertmanager 告警通道（PagerDuty / 企微 / Slack）
- 日志聚合（Loki / ELK / 公司日志系统）
- Trace 后端（Jaeger / Tempo / OTLP collector）

**交付物**：
- `deploy/helm/agent-mesh/values.production.yaml` 加 ServiceMonitor / PrometheusRule 配置
- 告警通道配置文件
- `docs/observability.md`：怎么查日志 / 怎么看 trace / 怎么 debug

**工作量**：1-3 天（取决于公司现有基建）

---

### P1-2: 前端 CDN + 版本管理

**现状**：前端 nginx 直接 serve 静态文件，每次发版重启容器。

**目标**：
- 构建产物上传 CDN（带版本 hash 的文件名）
- index.html 不缓存，hashed assets 长缓存
- 前端 / 后端版本独立发布

**交付物**：
- 前端 CI/CD 单独流程
- CDN 配置文档

**工作量**：1 天

---

### P1-3: 备份 + 恢复演练

**现状**：runbook 提到 MySQL 备份恢复，**未实际演练**。

**目标**：
- 自动备份策略（云厂商 RDS 自动备份 + 跨区域复制）
- 手工演练一次完整恢复流程，记录耗时
- Redis 重建策略（数据丢失影响评估）

**交付物**：
- `docs/disaster-recovery.md`：RPO / RTO 目标 + 演练记录

**工作量**：1 天

---

### P1-4: SLO 定义

**现状**：告警规则按经验设定阈值，没有明确 SLO。

**目标**：
- 定义业务 SLO（如"99.9% 的 task 提交在 500ms 内完成"）
- 错误预算（error budget）消耗策略
- SLO 看板（Grafana SLO panels）

**交付物**：
- `docs/slo.md`
- Grafana SLO dashboard

**工作量**：0.5 天（决策为主）

---

### P1-5: testcontainers-go 集成测试

**现状**：现有 E2E 用 `httptest` + 内存 repo，没连真实 MySQL/Redis。

**目标**：
- testcontainers-go 起真实 MySQL/Redis 容器
- E2E 测试连真实 SQL 执行（验证 SQL repo + index 行为）
- CI 里跑（每次 PR）

**交付物**：
- `gateway/internal/e2e/integration_test.go`（带 build tag `integration`）
- CI 配置启用 docker-in-docker

**工作量**：1-2 天

---

## P2 — 安全 / 合规 / 业务正确性

### P2-1: 安全审计

**现状**：JWT + API Key + 限流 + 熔断 + 参数化查询，但未做系统审计。

**目标**：
- `govulncheck` 跑过零 high severity
- 依赖 CVE 扫描（Snyk / Dependabot）
- SQL 注入审计（重点审 `internal/domain/*/sql_repo.go`）
- 渗透测试（外包或安全团队）
- JWT secret 轮换机制
- API Key 前缀脱敏（日志不能输出全 key）

**交付物**：
- `docs/security.md`：威胁模型 + 审计结果 + 补救项
- `.github/workflows/security.yml`：定期 CVE 扫描

**工作量**：3-5 天

---

### P2-2: Audit Log

**现状**：业务日志走 zap，**没有审计日志**（谁在什么时候做了什么敏感操作）。

**目标**：
- 关键操作落审计表：注册、登录、API Key 创建/吊销、agent 删除、群组创建/移除成员、好友申请通过/拒绝
- 审计日志保留 ≥ 1 年（合规要求）
- 审计日志独立存储（不走业务 DB）

**交付物**：
- 新表 `audit_logs`（migration）
- `internal/domain/audit/` 域
- 关键 handler 调用 `audit.Log()`

**工作量**：2-3 天

---

### P2-3: 数据保留策略

**现状**：所有数据永久保留，没有 TL。

**目标**：
- 定义保留策略：
  - 已完成 task 保留 90 天
  - 已 delivered inbox 事件保留 30 天
  - 用户主动删除 → 软删除 7 天后硬删除
- 后台 job 定期清理过期数据
- GDPR 合规：用户请求删除账号时的数据处理流程

**交付物**：
- `docs/data-retention.md`
- 后台 cleanup job（cron 形式）
- 用户删除账号 API + 流程

**工作量**：1-2 天

---

### P2-4: 多租户隔离审视

**现状**：通过 `OwnerUID` 隔离，但未系统审计。

**目标**：
- 审计每个 SQL query 是否带 `owner_uid` 过滤
- 限流加"租户"维度（防止单用户拖垮集群）
- 资源配额（每用户最多 N 个 agent / M 个 group）
- 跨租户访问 deny by default 测试

**交付物**：
- `docs/multi-tenancy.md`
- 配额相关 migration + domain 实现
- 跨租户 deny 测试用例

**工作量**：1-2 天

---

## P3 — 业务扩展（不阻塞上线）

### P3-1: 前端打磨

- 错误提示统一（toast 组件）
- 加载状态 / 空状态 / 错误状态完善
- 移动端适配（responsive）
- 国际化（i18n，目前中英混合）
- 主题切换（暗色模式）
- 键盘快捷键

**工作量**：3-5 天

---

### P3-2: GAS daemon 进阶

- **AgentManager**：自动拉起 Claude Code 子进程
- **ControlAPI**：localhost HTTP 管理接口（启动/停止/状态查询）
- **FedStorage**：SQLite 本地缓存离线消息
- **多 agent 模式**：单 GAS 进程管理多个 agent

**工作量**：1-2 周

---

### P3-3: A2A 协议完整对齐

- `/.well-known/agent-card.json`（A2A 协议要求的自描述端点）
- AgentCard 规范完整覆盖（capabilities / authentication / extensions）
- 兼容外部 A2A agent（非 Claude Code）

**工作量**：3-5 天

---

### P3-4: 群组进阶能力

- 消息 broadcast（群公告）
- 动态成员（运行中加/移成员的 race 处理）
- Cross-user 群组（已留扩展点）
- 群组角色 + 权限矩阵（admin / member / viewer）

**工作量**：1 周

---

### P3-5: Agent 市场扩展

- 用户从市场"购买"别人的 agent（按调用计费）
- Agent 评分 / 评论
- Agent 分类标签

**工作量**：2 周+

---

## 上线最小集合（推荐路径）

如果时间紧，按以下顺序做：

| Step | 项 | 工作量 | 累计 |
|------|---|------|------|
| 1 | P0-2 镜像构建 | 1d | 1d |
| 2 | P0-1 CI/CD | 2d | 3d |
| 3 | P0-4 Secret 管理 | 1d | 4d |
| 4 | P0-5 Migration 流程 | 1d | 5d |
| 5 | P0-3 真实压测 | 2d | 7d |
| 6 | P2-1 安全审计（最小集） | 1d | 8d |
| 7 | P2-3 数据保留策略 | 1d | 9d |

**约 1.5-2 周**全职工作可以达到生产最低标准。

P1 项可以上线后并行补，P2 / P3 按业务优先级排。

---

## 维护清单

每周 review：

- [ ] CVE 扫描有无新 high severity
- [ ] SLO 错误预算消耗
- [ ] 告警有无误报 / 漏报
- [ ] 容量是否需要扩容（HPA / 资源限额）
- [ ] 慢查询有没有新增

每月 review：

- [ ] 备份恢复演练
- [ ] 安全配置审视
- [ ] 文档更新
- [ ] 依赖升级

每季度 review：

- [ ] 全链路压测
- [ ] Chaos 演练（包括生产级场景）
- [ ] 灾难恢复演练
