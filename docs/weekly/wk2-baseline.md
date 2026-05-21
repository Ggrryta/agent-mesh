# 压测基线报告模版

> Week 2 建基线；Week 5（治理层）+ Week 7（Chaos）回归。
>
> 基线不求极致性能，只记录"当前无治理时 Gateway 裸跑的表现"，方便之后
> 对比 "加了限流 / 熔断 / 并发控制" 后的差异。

## 环境

| 项 | 值 |
|---|---|
| 集群 | k3d 本地（1 server + 2 agents）|
| Gateway 副本数 | 2 |
| Pod 资源 | requests 100m/128Mi，limits 500m/256Mi（参考 deployment.yaml）|
| MySQL | 8.0 via docker-compose.dev.yml（宿主机 3308） |
| Redis | 7-alpine via docker-compose.dev.yml（宿主机 6381） |
| k6 版本 | 0.49.0 |
| 客户端位置 | 宿主机（macOS） |
| 网络 | port-forward `svc/gateway-business :38080` |

## 压测前准备

```bash
make compose-up
make migrate-up
make dev-up && make dev-deploy
kubectl -n agent-mesh port-forward svc/gateway-business 38080:80 &
# 清一遍数据库避免旧数据影响 Prober：
docker exec agent-mesh-dev-mysql mysql -umesh -pdev_mesh_pw agent_mesh \
  -e "DELETE FROM friendships; DELETE FROM skills; DELETE FROM api_keys; \
      DELETE FROM agents; DELETE FROM users;"
```

## 场景 1：Register + Login（bcrypt 重路径）

脚本：`loadtest/register_login.js`
参数：`VUS=20 DURATION=30s`

### 结果

| 指标 | 值 | 备注 |
|---|---|---|
| iterations | _ TODO _ | 成功 iteration 数 |
| checks | _ TODO _ % | register+login 各自 200 OK 的比例 |
| http_req_failed | _ TODO _ % | 阈值 <5% |
| http_req_duration p50 | _ TODO _ ms | |
| http_req_duration p95 | _ TODO _ ms | 阈值 <500ms |
| http_req_duration p99 | _ TODO _ ms | 阈值 <1500ms |
| Gateway Pod CPU（双副本）| _ TODO _ % | 运行期间 `kubectl top pod` 峰值 |
| Gateway Pod Mem（双副本）| _ TODO _ Mi | 运行期间 `kubectl top pod` 峰值 |
| MySQL CPU | _ TODO _ % | 宿主机 `docker stats` |

### 观察

- bcrypt 是主 CPU 开销（default cost=10）。若 p95 > 500ms，先查 Pod CPU limit 是否成为瓶颈
- 如果 `http_req_failed > 5%`，看是否有 `context deadline exceeded` —— 连接池不够
- 重复注册会走 uk_username → 409，不会撞 bcrypt，算健康

## 场景 2：Heartbeat（高频 JWT 校验 + DB UPDATE）

脚本：`loadtest/heartbeat.js`
参数：`VUS=10 DURATION=30s`（基线档，可以放宽到 VUS=50 跑压力档）

### 基线档结果

| 指标 | 值 | 备注 |
|---|---|---|
| iterations | _ TODO _ | |
| checks | _ TODO _ % | heartbeat 200 + status=active |
| http_req_failed | _ TODO _ % | 阈值 <1% |
| http_req_duration p50 | _ TODO _ ms | |
| http_req_duration p95 | _ TODO _ ms | 阈值 <100ms |
| http_req_duration p99 | _ TODO _ ms | 阈值 <300ms |
| Gateway CPU | _ TODO _ % | |
| MySQL QPS（update agents）| _ TODO _ | `SHOW GLOBAL STATUS LIKE 'Com_update'` |

### 压力档结果（VUS=50）

_ TODO _ 表格同上

### 观察

- heartbeat 不查 cache，每请求一次 UPDATE agents。MySQL 写 QPS 是主要瓶颈
- JWT 校验是 HS256 + 一次 map 查 claims，单次 < 1ms
- 如果 p99 > 300ms 且 MySQL CPU 低，大概率是 `MYSQL_MAX_OPEN_CONNS=50` 不够；调高后回归
- 观察 Prober 是否也在打这些 agent（URL 是 http://xxx:0 不可达，Prober 会尝试 + 失败 + 翻 inactive），可能干扰结果。压测期间考虑先把 Prober 关掉：
  - 临时方案：把 `prober.Config{Interval: 1h}` 改长后重启 Gateway

## Week 2 基线 TODO

- [ ] 跑一次 `register_login.js`，填写表格
- [ ] 跑一次 `heartbeat.js` VUS=10
- [ ] 跑一次 `heartbeat.js` VUS=50
- [ ] 补到 `docs/weekly/wk2-baseline.md`（本报告）

## 回归点

| Week | 触发 | 对比项 |
|---|---|---|
| Week 5 Day 6 | 限流 / 熔断 / 并发控制上线 | 同场景 p95/p99 + error rate |
| Week 7 Day 5 | Chaos + 压测综合 | 同上 + 1h 稳定性曲线 |
| Week 8 Day 1 | 生产 K8s 部署 | 同上 + 真实数据量 |
