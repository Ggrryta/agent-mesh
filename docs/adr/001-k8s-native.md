# ADR-001: K8s-Native Architecture

- **Status**: Accepted
- **Date**: 2026-05-12

## Context

Agent-Mesh 作为 agent 对等通信网络，面临以下需求：

- 单人 2 个月交付 MVP，同时要生产级
- 未来必然扩展多实例 / 高可用
- 长任务 + 消息不丢 → 分布式协调复杂
- 有限人力，需要最大化自动化运维

## Decision

**从 Day 1 起即按 K8s-native 架构设计。**

所有开发、测试、验收都在 k3d 本地集群进行，生产部署到云厂商托管 K8s。

## Alternatives Considered

### A. docker-compose 单机 MVP，v2 再上 K8s

- **优点**：上手快，运维简单
- **缺点**：
  - MVP 和生产架构分歧，v2 转换成本高
  - 多实例、滚动升级、自愈等能力都要重新设计
  - 无法在开发阶段就验证并发安全性（例如 OutboxDispatcher 多副本重复）

### B. 自建 K8s 集群

- **优点**：完全可控
- **缺点**：
  - etcd / CNI / API Server 等运维等于多一个项目
  - 单人难以兼顾业务和平台

### C. 虚拟机 + systemd

- **优点**：传统稳定
- **缺点**：
  - 滚动升级、自愈、资源限制都要手动脚本
  - 现代云原生生态难以接入

## Consequences

### 好处

- 无状态、优雅停机、探针、定时任务并发安全等生产级约束从 Day 1 就被代码强制
- 水平扩展、滚动升级、观测接入等能力是 K8s 默认提供，不需要额外开发
- 架构和生态对齐，未来接 Prometheus / Grafana / Argo / Vault 等零改动
- 面试 / 招聘市场硬通货

### 代价

- 前期学习曲线陡峭（Week 0 专门安排 5 天基建）
- 本地开发用 k3d，资源占用比 docker-compose 高
- 生产 K8s 要付托管费用

### 约束（代码层面必须遵守）

- 所有状态外置
- 三种探针分离
- 配置走 env
- 日志 JSON stdout
- 定时任务并发安全（DB 乐观锁，不用主选锁）
- WebSocket / SSE 跨 Pod 广播走 Redis Pub/Sub

见 DESIGN.md §11 K8s-Native 原则。
