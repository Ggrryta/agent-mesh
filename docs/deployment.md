# Deployment Guide

> Agent-Mesh 生产部署指南。

## 前置条件

- Kubernetes 1.27+ 集群（生产推荐 EKS / GKE / 自建）
- MySQL 8.0+（建议 RDS / Cloud SQL）
- Redis 7+（建议 ElastiCache / Cloud Memorystore）
- 可选：Prometheus + Grafana（监控）

## 数据库初始化

执行 migration（在 CI/CD 或本地）：

```bash
MYSQL_DSN="user:pwd@tcp(host:3306)/agent_mesh?parseTime=true" \
  go run ./gateway/cmd/migrate/
```

应用所有 5 个迁移：
- `0001_init.sql` - 基础表
- `0002_api_keys.sql` - API Key
- `0003_inbox.sql` - inbox 事件
- `0004_groups_outbox.sql` - 群组 + outbox
- `0005_message_preview.sql` - message preview 字段

## Helm 部署

```bash
# 1. 创建 namespace + 部署
helm upgrade --install agent-mesh ./deploy/helm/agent-mesh \
  -f deploy/helm/agent-mesh/values.production.yaml \
  --set secret.jwtSecret="$(openssl rand -base64 32)" \
  --set secret.mysqlDSN="user:pwd@tcp(rds.example.com:3306)/agent_mesh?parseTime=true" \
  --set secret.redisAddr="redis.example.com:6379" \
  --set image.repository=registry.example.com/agent-mesh-gateway \
  --set image.tag=0.5.0 \
  --set ingress.host=mesh.example.com

# 2. 验证
kubectl -n agent-mesh get pods
kubectl -n agent-mesh get svc
```

生产环境推荐用 ExternalSecrets 注入 secret 而不是 `--set`。

## 健康检查

```bash
# 业务端口（外部可访问）
curl https://mesh.example.com/healthz    # → {"status":"alive"}
curl https://mesh.example.com/readyz     # → {"status":"ready",...}

# Admin 端口（仅集群内）
kubectl -n agent-mesh port-forward svc/agent-mesh-gateway 9090:9090
curl http://localhost:9090/metrics       # Prometheus 指标
```

## Prometheus 接入

Gateway 通过 Pod annotation 暴露 scrape 配置，无需额外修改。

如果用 prometheus-operator + PodMonitor：

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: agent-mesh-gateway
  namespace: agent-mesh
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: gateway
  podMetricsEndpoints:
    - port: admin
      path: /metrics
```

## Grafana Dashboard

```bash
# 通过 API 导入
curl -X POST -H "Content-Type: application/json" \
  -d @deploy/grafana/agent-mesh-dashboard.json \
  http://grafana.example.com/api/dashboards/db
```

## Alertmanager 规则

```bash
kubectl apply -f deploy/alertmanager/rules.yaml
```

## 升级 / 回滚

```bash
# 升级（滚动更新，maxUnavailable=0）
helm upgrade agent-mesh ./deploy/helm/agent-mesh \
  -f deploy/helm/agent-mesh/values.production.yaml \
  --set image.tag=0.5.1

# 查看历史
helm history agent-mesh

# 回滚到上一版本
helm rollback agent-mesh
```

## 容量规划

基于 Week 7 压测基线（详见 `docs/benchmarks.md`）：

| 副本数 | CPU req | Mem req | 推荐 QPS 上限 |
|-------|---------|---------|-------------|
| 2 | 200m | 256Mi | ~200 req/s |
| 5 | 500m | 512Mi | ~1000 req/s |
| 10 | 500m | 512Mi | ~2500 req/s（开 HPA）|

HPA 默认 CPU 70%，根据实际负载调整。

## 故障恢复

参考 `docs/runbook.md`。

## 旧 kustomize 部署

`deploy/k8s/` 下的 kustomize 清单已废弃，仅作历史参考。生产部署只用 Helm。
