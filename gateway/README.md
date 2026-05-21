# Gateway 代码组织

Go 网关主体。职责分四层：

```
cmd/       入口 + migrate 工具
internal/
  api/     HTTP 层
    admin/   给前端的 REST + WebSocket
    mesh/    给 agent/GAS 的 Mesh API
  domain/  核心领域（职责单一，相互依赖通过接口）
    agent/
    skill/
    friendship/
    inbox/
    task/
  middleware/   认证 / 限流 / 日志 / 追踪
  infra/   外部依赖适配
    mysql/
    redis/
    online/
  observability/ 日志 / trace / metric 集成
pkg/       可复用通用组件
  ratelimit/
  circuitbreaker/
  concurrency/
config/    配置加载
migrations/ SQL migration
test/      集成测试
```

## 层次规则

- `api` 只依赖 `domain` + `middleware`
- `domain` 只依赖 `infra` + `pkg`
- `pkg` 不依赖项目内其他包（可复用纯工具）
- `infra` 封装外部系统，暴露接口给 domain

这样做的好处：domain 不绑定具体 MySQL / Redis 实现，集成测试可以用内存 stub。

## 开发顺序（参考 PLAN.md）

Week 1：cmd + config + infra + middleware/auth + domain/agent + domain/skill
Week 2：domain/friendship + Admin API
Week 3：domain/task + OutboxDispatcher + TaskWorker
Week 4：domain/inbox + Mesh API + Online Registry
Week 5：middleware 补全 + observability
Week 7：test/ 集成测试
