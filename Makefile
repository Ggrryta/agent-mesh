# Agent-Mesh 开发用 Makefile。本地 build / run / test 的单一入口。
#
# 用法速查：
#   make help           列出所有 target
#   make build          把 gateway 二进制编到 ./bin/
#   make test           用 race detector 跑 Go 测试
#   make lint           go vet + gofmt 检查
#   make docker         构建镜像并推到本地 k3d registry
#   make dev-up         起 k3d 集群并 apply 清单
#   make dev-down       删除 k3d 集群
#   make dev-deploy     重建镜像并滚动发布
#   make dev-logs       跟踪所有 gateway pod 的日志
#   make smoke          通过 port-forward 跑端到端 smoke 测试

SHELL := /usr/bin/env bash -eo pipefail

REGISTRY     := localhost:5555
IMAGE        := agent-mesh-gateway
TAG          ?= dev
FULL_IMAGE   := $(REGISTRY)/$(IMAGE):$(TAG)

CLUSTER      := agent-mesh-dev
NAMESPACE    := agent-mesh
REGISTRY_CFG := deploy/k3d/registries.yaml

GO           := go
GATEWAY_DIR  := gateway
K8S_BASE     := deploy/k8s/base

# 本地 dev MySQL / Redis（docker-compose.dev.yml）。改端口时用 env 覆盖。
DEV_MYSQL_DSN   ?= mesh:dev_mesh_pw@tcp(localhost:3308)/agent_mesh?parseTime=true
DEV_REDIS_ADDR  ?= localhost:6381

.PHONY: help
help:
	@awk 'BEGIN {FS=":.*##"; printf "\nTargets:\n"} /^[a-zA-Z_-]+:.*##/ { printf "  %-20s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ── Go ──

.PHONY: build
build: ## 编译所有 gateway 服务
	cd $(GATEWAY_DIR) && $(GO) build -o ../bin/identity-svc ./cmd/identity-svc
	cd $(GATEWAY_DIR) && $(GO) build -o ../bin/messaging-svc ./cmd/messaging-svc
	cd $(GATEWAY_DIR) && $(GO) build -o ../bin/push-gateway ./cmd/push-gateway
	cd $(GATEWAY_DIR) && $(GO) build -o ../bin/api-gateway ./cmd/gateway

.PHONY: build-migrate
build-migrate: ## 编译 migrate CLI
	cd $(GATEWAY_DIR) && $(GO) build -o ../bin/migrate ./cmd/migrate

.PHONY: test
test: ## 跑测试（race detector 开；跳过 live infra 测试）
	cd $(GATEWAY_DIR) && $(GO) test -race ./...

.PHONY: test-live
test-live: ## 跑全部测试，含真实 MySQL / Redis 集成
	cd $(GATEWAY_DIR) && \
		AGENT_MESH_TEST_MYSQL_DSN='$(DEV_MYSQL_DSN)' \
		AGENT_MESH_TEST_REDIS_ADDR='$(DEV_REDIS_ADDR)' \
		$(GO) test -race -cover ./...

.PHONY: lint
lint: ## go vet + gofmt 检查
	cd $(GATEWAY_DIR) && $(GO) vet ./... && test -z "$$(gofmt -l .)"

.PHONY: tidy
tidy: ## go mod tidy（走国内 proxy）
	cd $(GATEWAY_DIR) && GOPROXY=https://goproxy.cn,direct $(GO) mod tidy

# ── 开发依赖（docker-compose）──

.PHONY: compose-up
compose-up: ## 起本地 MySQL / Redis
	docker compose -f docker-compose.dev.yml up -d

.PHONY: compose-down
compose-down: ## 停 dev 依赖（保留数据）
	docker compose -f docker-compose.dev.yml down

.PHONY: compose-wipe
compose-wipe: ## 停 dev 依赖并删 volume
	docker compose -f docker-compose.dev.yml down -v

.PHONY: migrate-up
migrate-up: build-migrate ## 把所有未应用的 migration 跑到 dev MySQL
	MYSQL_DSN='$(DEV_MYSQL_DSN)&multiStatements=true' ./bin/migrate up

.PHONY: migrate-status
migrate-status: build-migrate ## 查看 migration 状态
	MYSQL_DSN='$(DEV_MYSQL_DSN)&multiStatements=true' ./bin/migrate status

.PHONY: migrate-reset
migrate-reset: build-migrate ## 回滚全部 migration（仅限 DEV）
	MYSQL_DSN='$(DEV_MYSQL_DSN)&multiStatements=true' ./bin/migrate reset

# ── Docker ──

.PHONY: docker
docker: ## 构建镜像并推到本地 k3d registry
	cd $(GATEWAY_DIR) && docker build -t $(FULL_IMAGE) .
	docker push $(FULL_IMAGE)
	# 同时 import 到每个 k3d 节点，新层立刻可见。
	k3d image import $(FULL_IMAGE) -c $(CLUSTER) || true

# ── k3d / K8s ──

.PHONY: dev-up
dev-up: ## 起 k3d 集群（1 server + 2 agents）
	@if k3d cluster list | grep -q $(CLUSTER); then \
		echo "cluster $(CLUSTER) already exists"; \
	else \
		k3d registry create mesh-registry --port 5555 || true; \
		k3d cluster create $(CLUSTER) \
			--servers 1 --agents 2 \
			--registry-use k3d-mesh-registry:5555 \
			--registry-config $(REGISTRY_CFG) \
			--port "80:80@loadbalancer" \
			--port "443:443@loadbalancer" \
			--wait; \
	fi

.PHONY: dev-down
dev-down: ## 删除 k3d 集群
	k3d cluster delete $(CLUSTER) || true
	k3d registry delete k3d-mesh-registry || true

.PHONY: dev-deploy
dev-deploy: docker ## 构建 + 推 + 滚动发布
	kubectl apply -k $(K8S_BASE)
	kubectl -n $(NAMESPACE) rollout restart deployment/gateway
	kubectl -n $(NAMESPACE) rollout status deployment/gateway --timeout=180s

.PHONY: dev-delete
dev-delete: ## 删除应用清单（保留集群）
	kubectl delete -k $(K8S_BASE) || true

.PHONY: dev-logs
dev-logs: ## 跟踪所有 gateway pod 的日志
	kubectl -n $(NAMESPACE) logs -l app.kubernetes.io/name=gateway --tail=100 -f

.PHONY: dev-pods
dev-pods: ## 查看 pod 状态
	kubectl -n $(NAMESPACE) get pods -o wide

# ── Smoke 测试 ──

.PHONY: smoke
smoke: ## 通过 port-forward 跑端到端 smoke
	@echo "=== starting port-forward ==="
	@kubectl -n $(NAMESPACE) port-forward svc/gateway-business 38080:80 > /tmp/agent-mesh-pf.log 2>&1 & \
	 PF_PID=$$!; \
	 trap "kill $$PF_PID 2>/dev/null" EXIT; \
	 sleep 3; \
	 echo "--- GET / ---"; curl -sS http://localhost:38080/; echo; \
	 echo "--- GET /healthz ---"; curl -sS http://localhost:38080/healthz; echo; \
	 echo "--- GET /readyz ---"; curl -sS http://localhost:38080/readyz; echo; \
	 echo "--- GET /startupz ---"; curl -sS http://localhost:38080/startupz; echo

# ── Load test ──

LOAD_BASE_URL ?= http://localhost:38080

.PHONY: load-register
load-register: ## k6 跑 register+login 基线（需先 port-forward :38080）
	BASE_URL=$(LOAD_BASE_URL) k6 run loadtest/register_login.js

.PHONY: load-heartbeat
load-heartbeat: ## k6 跑 heartbeat 基线（需先 port-forward :38080）
	BASE_URL=$(LOAD_BASE_URL) k6 run loadtest/heartbeat.js
	@echo "=== starting port-forward ==="
	@kubectl -n $(NAMESPACE) port-forward svc/gateway-business 38080:80 > /tmp/agent-mesh-pf.log 2>&1 & \
	 PF_PID=$$!; \
	 trap "kill $$PF_PID 2>/dev/null" EXIT; \
	 sleep 3; \
	 echo "--- GET / ---"; curl -sS http://localhost:38080/; echo; \
	 echo "--- GET /healthz ---"; curl -sS http://localhost:38080/healthz; echo; \
	 echo "--- GET /readyz ---"; curl -sS http://localhost:38080/readyz; echo; \
	 echo "--- GET /startupz ---"; curl -sS http://localhost:38080/startupz; echo
