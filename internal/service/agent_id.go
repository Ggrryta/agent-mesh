package service

import (
	"regexp"
	"strings"
)

// agentIDPattern agent_id 格式约束:小写字母+数字+连字符+点+下划线,3-64 字符
// 规范化后应满足此约束
var agentIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}[a-z0-9]$|^[a-z0-9]{1,3}$`)

// NormalizeAgentID 规范化 agent_id:去首尾空格 + 转小写
//
// Gateway 统一在所有入口(register / AgentAuth / dispatcher / friendship)
// 做规范化,保证 MySQL 与 Redis 存取的 agent_id 一致,避免大小写不匹配导致 offline 误判。
func NormalizeAgentID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// ValidateAgentID 规范化之后再校验格式
func ValidateAgentID(id string) bool {
	return agentIDPattern.MatchString(id)
}
