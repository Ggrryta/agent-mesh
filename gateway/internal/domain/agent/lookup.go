package agent

import "context"

// LookupAdapter 让其他 domain（如 friendship / task）通过小接口访问 agent
// 元数据，避免对 agent 包的结构体直接依赖。签名刻意保守 —— 返回原语类型。
type LookupAdapter struct{ svc *Service }

// NewLookupAdapter 包一层 Service，暴露两个 Lookup 方法。
func NewLookupAdapter(svc *Service) *LookupAdapter { return &LookupAdapter{svc: svc} }

// Lookup 返回 (ownerUID, kind, found)。
//
// 优先走内存 cache（Service.cache 由 Reloader 周期同步 + 写路径主动 Set）。
// cache miss 时回源到 repo 保证结果可信 —— 只在 agent 新建后 + 首次 reload
// 之前的短暂窗口发生。
func (a *LookupAdapter) Lookup(ctx context.Context, agentID string) (int64, string, bool) {
	id := NormalizeAgentID(agentID)
	if ag, ok := a.svc.cache.Get(id); ok {
		return ag.OwnerUID, string(ag.Kind), true
	}
	ag, err := a.svc.repo.GetByAgentID(ctx, id)
	if err != nil {
		return 0, "", false
	}
	return ag.OwnerUID, string(ag.Kind), true
}

// LookupURL 返回 agent 在 agents 表登记的 URL（"" 表示未登记）。
//
// push worker 用它决定能否尝试 HTTP push。URL 为空时返回 ok=false。
// 优先走 cache；miss 时回源 DB。
func (a *LookupAdapter) LookupURL(ctx context.Context, agentID string) (string, bool) {
	id := NormalizeAgentID(agentID)
	if ag, ok := a.svc.cache.Get(id); ok {
		if ag.URL == "" {
			return "", false
		}
		return ag.URL, true
	}
	ag, err := a.svc.repo.GetByAgentID(ctx, id)
	if err != nil {
		return "", false
	}
	if ag.URL == "" {
		return "", false
	}
	return ag.URL, true
}
