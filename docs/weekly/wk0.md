# Week 0 — K8s 基建 + 项目脚手架

> 2026-05-11 / 2026-05-12，一人开发，每日全职。

## 目标

本地 k3d 集群跑 2 副本 Agent-Mesh Gateway，CI 流水线就绪，"一个命令起全栈"。

## 完成项

### Day 0.1 工具链
- ✅ `brew install helm k3d`
- ✅ 工具版本：go 1.26 / docker 29.3 / kubectl v1.34 / helm v4.1.4 / k3d v5.8.3 (k3s v1.33.6)
- ✅ k3d 集群 `agent-mesh-dev`（1 server + 2 agents）+ 内嵌 registry（`k3d-mesh-registry:5555`）
- ✅ 配置 CN 镜像 mirror（`deploy/k3d/registries.yaml`，aliyun + dockerproxy）

### Day 0.2 Go 脚手架 + 三种探针
- ✅ `gateway/` 模块初始化，依赖：`go.uber.org/zap`、`github.com/prometheus/client_golang`
- ✅ `config/` env-driven 配置（支持默认值 + 环境变量覆盖），附单元测试
- ✅ `internal/observability/logger/` zap JSON + stdout，带 `pod` 字段
- ✅ `internal/observability/health/` 三种探针：liveness / readiness / startup
  - 业务端口（`:8080`）对外暴露探针
  - 管理端口（`:9090`）Prometheus `/metrics` + 冗余 `/healthz`
- ✅ `cmd/server/main.go` 串联入口 + SIGTERM 三阶段优雅停机
- ✅ 单元测试全绿：`go test -race ./... ` 通过

### Day 0.3 Dockerfile + 本地构建
- ✅ 多阶段 `Dockerfile`：`golang:1.23-alpine` builder → `alpine:3.20` runtime
  - 原计划用 distroless，改成 alpine：gcr.io 在 CN 不可达
  - 镜像走 `goproxy.cn` + `sum.golang.google.cn`
- ✅ 非 root 运行（uid 65532），readOnlyRootFilesystem，drop all caps
- ✅ 镜像大小约 20 MB（alpine 基础镜像）
- ✅ 镜像推到本地 k3d registry（`localhost:5555/agent-mesh-gateway:dev`）

### Day 0.4 K8s 清单
- ✅ `deploy/k8s/base/`：
  - `namespace.yaml` → `agent-mesh`
  - `configmap.yaml` → 非敏感配置 (12-factor env)
  - `secret.yaml` → 敏感配置占位（生产用 ExternalSecrets）
  - `deployment.yaml` → 2 副本 + 三种探针 + preStop sleep + topology spread + securityContext
  - `service.yaml` → 业务 / admin 两个 ClusterIP 分离
  - `ingress.yaml` → `mesh.localhost` 前端路由
  - `kustomization.yaml` → labels 语法，images 替换
- ✅ 滚动升级策略：`maxUnavailable=0` + `maxSurge=1`
- ✅ `terminationGracePeriodSeconds: 90` 配合 `STARTUP_READY_DELAY=5s`
- ✅ Pod 资源：requests 100m/128Mi、limits 500m/256Mi（Week 7 再按压测调）
- ✅ 预拉 pause:3.6 / coredns:1.13 / metrics-server:0.8 至所有节点（CN mirror 回落）

### Day 0.5 Makefile + CI
- ✅ 根目录 `Makefile`，覆盖 build / test / lint / tidy / docker / dev-up / dev-down / dev-deploy / dev-logs / dev-pods / smoke
- ✅ `.github/workflows/ci.yaml`：go test + lint + docker build + kubeconform 校验 kustomize
- ✅ `.github/workflows/release.yaml`：tag 触发多架构 (amd64 + arm64) 镜像发布

## 关键验收

| 场景 | 验证方式 | 结果 |
|---|
| 2 副本同时运行 | `kubectl -n agent-mesh get pods` | ✅ 2/2 Running |
| 探针全绿 | `make smoke` | ✅ healthz / readyz / startupz 均 200 |
| Pod 自愈 | `kubectl delete pod $POD` | ✅ 20s 内自动重建 |
| JSON 结构化日志 | `kubectl logs` | ✅ 字段 ts/level/msg/pod/caller 齐全 |
| Prometheus 指标 | `curl :9090/metrics` | ✅ go runtime metrics 暴露 |
| SIGTERM 优雅退出 | `docker run` + Ctrl-C | ✅ 观察到 drain → shutdown 日志 |
| Makefile | `make smoke` | ✅ 打印 / /healthz /readyz /startupz |
| Kustomize 无 deprecation | `kubectl kustomize deploy/k8s/base` | ✅ 无 warning |

**未完成**：
- Ingress 公网触达（`mesh.localhost`）当前因 traefik 镜像慢未完全 ready。服务通过 port-forward 验证无问题；等 traefik 镜像全部拉下来后可用 `curl -H 'Host: mesh.localhost' http://localhost/` 直接触达（已在文档中备注）。
- CI 还没实际跑过（仓库未推到 GitHub），Week 1 首次提交时触发验证。

## 遗留与风险

1. **CN 镜像 mirror 慢/抖**：本机拉镜像经常卡，已经通过 aliyun 直拉 + `ctr tag` 预热关键镜像。新加入 kube-system 组件可能还需手动预拉。建议后续维护一个"节点预热脚本"。
2. **traefik 启动慢**：首次集群创建时 `helm-install-traefik` 拉 klipper-helm 镜像走 mirror 要 3-5 分钟。不影响 App 本身，port-forward 可用。
3. **本地镜像 push 主机名**：`docker push` 用 `localhost:5555`，k8s 内部拉用 `k3d-mesh-registry:5555`。kustomization 已对齐，但外部文档/README 需要说明。

## 下一步（Week 1）

- Day 1：DB 层 + migration 工具 + `/readyz` 接入 DB/Redis ping
- Day 2：User 域 + JWT + Admin auth API
- Day 3：Agent 域核心 + cache + virtual user-agent
- Day 4：Agent 注册/心跳 API
- Day 5：Skill 域
- Day 6：Agent Prober（DB 时间戳并发安全版）
- Day 7：集成测试 + 缓冲

目标里程碑：用 curl 走完"注册 → 登录 → 建 agent → 查 agent → prober 探活"全流程，2 副本表现一致。

## 产出清单

```
agent-mesh/
├── Makefile                      # 开发命令总入口
├── .github/workflows/
│   ├── ci.yaml                   # 每次提交跑
│   └── release.yaml              # tag 发版
├── gateway/
│   ├── go.mod / go.sum
│   ├── Dockerfile                # 多阶段 alpine
│   ├── .dockerignore
│   ├── cmd/server/main.go        # 入口 + 三阶段 shutdown
│   ├── config/
│   │   ├── config.go             # env-driven 配置
│   │   └── config_test.go
│   ├── internal/observability/
│   │   ├── logger/logger.go      # zap JSON
│   │   └── health/
│   │       ├── probe.go          # 三种探针
│   │       └── probe_test.go
│   └── README.md                 # 代码分层说明
└── deploy/
    ├── k3d/registries.yaml       # CN mirror
    └── k8s/base/                 # kustomize base
        ├── kustomization.yaml
        ├── namespace.yaml
        ├── configmap.yaml
        ├── secret.yaml
        ├── deployment.yaml
        ├── service.yaml
        └── ingress.yaml
```

测试覆盖：
- `gateway/config`：默认值、env 覆盖、非法 int/duration 四个用例
- `internal/observability/health`：liveness、startup 翻转、readiness 无检查、readiness 带失败检查、draining 五个用例

## 给下一次接手者

1. `make dev-up` 起集群（如集群已存在会跳过）
2. `make docker && make dev-deploy` 构建镜像并部署
3. `make smoke` 跑端到端验证
4. `make dev-down` 删除集群

如果 pull 镜像卡住，按 `docs/weekly/wk0.md §遗留与风险` 手动预热节点镜像。
