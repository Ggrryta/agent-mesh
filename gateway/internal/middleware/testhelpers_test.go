package middleware

import "github.com/Ggrryta/agent-mesh/gateway/pkg/auth"

// newClaimsForTest 给本包的测试造一个最小 auth.Claims。
// 生产代码不会用 —— 仅在 _test.go 里被引用。
func newClaimsForTest(uid int64, agentID string) *auth.Claims {
	return &auth.Claims{UID: uid, AgentID: agentID}
}
