package user

import "strings"

// toLowerTrim 集中处理归一化逻辑，方便测试直接调，不必 import net/http 等。
func toLowerTrim(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
