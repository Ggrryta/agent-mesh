# Nacos 服务发现使用指南

## 功能概述

Agent-Gateway 现已支持通过 Nacos 动态发现和管理下游服务实例，实现：
- ✅ 自动服务注册/注销
- ✅ 健康检查（自动剔除故障实例）
- ✅ 负载均衡（轮询/随机）
- ✅ 服务变更实时通知
- ✅ 兼容旧版静态 endpoint 配置

---

## 架构设计

### 调用流程

```
请求到达 Gateway
    ↓
查询 Capability 配置（DB/缓存）
    ↓
检查 service_name 字段
    ↓
┌─────────────┬──────────────┐
│ service_name│   为空       │
│   不为空    │              │
↓             ↓              ↓
Nacos 服务发现           使用静态 endpoint
    ↓
获取健康实例列表
    ↓
负载均衡选择实例
    ↓
调用目标服务
```

### 降级策略

```
Nacos 服务发现失败
    ↓
1. 回退到本地缓存（最近一次成功的实例列表）
    ↓
2. 缓存也失效 → 回退到静态 endpoint
    ↓
3. endpoint 也为空 → 返回错误
```

---

## 配置方式

### 方式 1: 动态服务发现（推荐）

```sql
INSERT INTO capabilities (
    capability_id, 
    service_name,      -- Nacos 服务名称
    group_name,        -- Nacos 分组（可选，默认 DEFAULT_GROUP）
    protocol, 
    grpc_method,       -- gRPC 协议时需要
    ...
) VALUES (
    'my-skill',
    'my-skill-service',  -- Nacos 中注册的服务名
    'DEFAULT_GROUP',
    2,                   -- HTTP 协议
    NULL,
    ...
);
```

**优势**:
- 实例动态扩缩容，无需修改配置
- 自动健康检查，故障实例自动剔除
- 负载均衡自动分配流量

---

### 方式 2: 静态地址（兼容旧版）

```sql
INSERT INTO capabilities (
    capability_id, 
    endpoint,          -- 静态地址
    protocol, 
    ...
) VALUES (
    'legacy-skill',
    'http://10.0.1.100:8080/invoke',
    2,                 -- HTTP 协议
    ...
);
```

**说明**: 保留兼容旧版配置，当 `service_name` 为空时使用 `endpoint`

---

## 下游服务注册到 Nacos

### HTTP 服务示例（Go）

```go
import (
    "github.com/nacos-group/nacos-sdk-go/v2/clients"
    "github.com/nacos-group/nacos-sdk-go/v2/common/constant"
    "github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// 创建 Naming 客户端
client, _ := clients.NewNamingClient(vo.NacosClientParam{
    ClientConfig: &constant.ClientConfig{
        NamespaceId: "dev",
        ServerAddr:  "localhost:8848",
    },
})

// 注册服务实例
client.RegisterInstance(vo.RegisterInstanceParam{
    ServiceName: "my-skill-service",
    Ip:          "10.0.1.100",
    Port:        8080,
    Weight:      1.0,
    Enable:      true,
    Healthy:     true,
    Metadata: map[string]string{
        "version": "1.0.0",
    },
})
```

### gRPC 服务示例

```go
// 注册 gRPC 服务
client.RegisterInstance(vo.RegisterInstanceParam{
    ServiceName: "my-grpc-service",
    Ip:          "10.0.1.101",
    Port:        50051,
    Weight:      1.0,
    Enable:      true,
    Healthy:     true,
})
```

---

## 负载均衡策略

### 当前支持的策略

| 策略 | 说明 | 适用场景 |
|------|------|---------|
| `round_robin` | 轮询（默认） | 实例性能相近 |
| `random` | 随机 | 简单场景 |
| `least_conn` | 最少连接（待实现） | 实例性能差异大 |

### 配置策略

在 `cmd/main.go` 中修改：

```go
serviceResolver := service.NewServiceResolver(
    nacosClient, 
    service.StrategyRoundRobin, // 可改为 StrategyRandom
)
```

---

## 服务订阅（实时更新）

Gateway 启动时会自动订阅所有配置的 Service，当实例上下线时：

1. Nacos 推送变更事件
2. ServiceResolver 更新本地缓存
3. 重置负载均衡索引
4. 后续请求使用新实例列表

**日志示例**:
```json
{
  "level": "info",
  "msg": "service instances cache updated via subscription",
  "service_name": "my-skill-service",
  "instance_count": 3
}
```

---

## 健康检查机制

### Nacos 侧健康检查

- **TCP 健康检查**: 默认启用
- **HTTP 健康检查**: 可配置健康检查路径
- **心跳检测**: 实例需定期发送心跳

### Gateway 侧降级

当 Nacos 返回的实例标记为不健康时：
- ❌ 不会选择该实例
- ✅ 自动从候选列表中排除
- ✅ 日志记录剔除原因

---

## 监控和调试

### 查看缓存状态

通过 API 查看服务实例缓存（待实现）：

```bash
curl http://localhost:11556/admin/service-resolver/stats
```

**响应示例**:
```json
{
  "my-skill-service": 3,
  "another-service": 5
}
```

### 日志追踪

启用服务发现后，日志会自动包含：

```json
{
  "level": "info",
  "msg": "skill invoked",
  "trace_id": "abc123...",
  "service_name": "my-skill-service",
  "resolved_endpoint": "10.0.1.100:8080"
}
```

---

## 故障排查

### 问题 1: 服务发现失败

**现象**: 日志显示 `service resolve failed`

**排查步骤**:
1. 检查 Nacos 是否正常运行
2. 检查服务是否已注册到 Nacos
3. 检查 service_name 和 group_name 是否正确

**解决**:
```bash
# 查看 Nacos 服务列表
curl http://localhost:8848/nacos/v1/ns/service/list

# 查看实例列表
curl "http://localhost:8848/nacos/v1/ns/instance/list?serviceName=my-skill-service"
```

---

### 问题 2: 实例不健康被剔除

**现象**: 日志显示实例数量为 0

**排查步骤**:
1. 检查实例健康检查配置
2. 检查实例心跳是否正常
3. 检查网络连通性

---

### 问题 3: 负载均衡不均

**现象**: 某些实例流量过大

**排查步骤**:
1. 检查实例权重配置
2. 切换负载均衡策略（round_robin → random）
3. 检查是否有实例健康状态异常

---

## 性能优化

### 缓存策略

- **本地缓存**: 避免每次调用都查询 Nacos
- **订阅更新**: 实例变更时自动刷新缓存
- **降级回退**: Nacos 不可用时使用缓存

### 连接复用

- gRPC 连接按 endpoint 缓存复用
- HTTP 连接使用连接池
- 空闲连接自动清理

---

## 迁移指南

### 从静态地址迁移到服务发现

**步骤**:

1. **下游服务注册到 Nacos**
   ```bash
   # 在下游服务启动时注册
   curl -X POST "http://localhost:8848/nacos/v1/ns/instance" \
     -d "serviceName=my-skill-service&ip=10.0.1.100&port=8080"
   ```

2. **更新 Capability 配置**
   ```sql
   UPDATE capabilities 
   SET service_name = 'my-skill-service',
       group_name = 'DEFAULT_GROUP'
   WHERE capability_id = 'my-skill';
   ```

3. **验证服务发现**
   ```bash
   # 调用 Skill 并观察日志
   curl http://localhost:11556/gateway/invoke/skill/my-skill \
     -d '{"input": {"key": "value"}}'
   ```

4. **清理静态 endpoint（可选）**
   ```sql
   UPDATE capabilities 
   SET endpoint = '' 
   WHERE capability_id = 'my-skill';
   ```

---

## 最佳实践

### ✅ 推荐做法

1. **生产环境使用服务发现**
   - 支持动态扩缩容
   - 自动故障转移

2. **配置健康检查**
   - 确保实例健康状态准确
   - 及时剔除故障实例

3. **监控实例数量**
   - 设置告警（实例数 < 2）
   - 定期查看缓存统计

4. **保留 endpoint 作为降级**
   - 服务发现失败时可回退
   - 提高系统可用性

### ❌ 避免做法

1. **不要混用 service_name 和 endpoint**
   - 优先使用 service_name
   - endpoint 仅作降级备用

2. **不要忘记注册服务**
   - 下游服务启动时必须注册
   - 停止时必须注销

3. **不要忽略健康检查日志**
   - 定期检查实例健康状态
   - 及时处理异常实例

---

## 总结

通过 Nacos 服务发现，Agent-Gateway 实现了：
- ✅ 动态实例管理（自动增删）
- ✅ 高可用（健康检查 + 自动剔除）
- ✅ 负载均衡（轮询/随机）
- ✅ 优雅降级（多层回退策略）

**生产就绪**，可以放心使用！🚀
