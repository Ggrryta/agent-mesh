// Package agent 负责 agent 的注册、基于缓存的发现，以及 active / draining /
// inactive 三种状态的优雅生命周期。本包刻意不负责：
//   - skills（独立的域，和 agent 并列）
//   - tasks、friendships、probing（各自独立包）
//
// 这样 agent 包保持小而可测。
package agent

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Kind 区分"普通 mesh agent"和"代表真实用户的虚拟 agent"。
type Kind string

const (
	KindNormal      Kind = "normal"
	KindVirtualUser Kind = "virtual-user"
)

// Status 是 agent 的可见状态机。
type Status string

const (
	// StatusActive 能接受被路由的流量。
	StatusActive Status = "active"
	// StatusDraining 处于下线过渡态：仍然可触达，继续处理在飞请求，
	// 但不再被路由器对外广播。
	StatusDraining Status = "draining"
	// StatusInactive 已从 mesh 中摘除，直到心跳成功或 prober 重新放行。
	StatusInactive Status = "inactive"
)

// Agent 对应 agents 表一行。大部分字段平铺；AgentCard 原始负载保持序列化
// 在 AgentCardJSON，这样本结构体在 cache 里拷贝成本低。
type Agent struct {
	ID              int64
	AgentID         string
	OwnerUID        int64
	Name            string
	Description     string
	Headline        string // 一句话摘要（注入到其他 agent 的 system prompt）
	URL             string
	Version         string
	Kind            Kind
	Status          Status
	AgentCardJSON   []byte
	SystemPrompt    string // 用户配置的角色身份提示词；agent 大脑启动时拉取作为 LLM system message
	WorkspacePath   string // agent 的工作目录路径；meshd 启动 worker 时用作 cwd
	LastHeartbeatAt *time.Time
	LastProbedAt    *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// 域错误；API handler 据此翻译 HTTP code。
var (
	ErrAgentNotFound       = errors.New("agent: not found")
	ErrInvalidAgentID      = errors.New("agent: agent_id must be 3-64 chars [a-z0-9._-]")
	ErrAgentIDExists       = errors.New("agent: agent_id already exists")
	ErrNotOwner            = errors.New("agent: caller is not owner")
	ErrReservedVirtualName = errors.New("agent: virtual-user- prefix is reserved")
	ErrSystemPromptTooLong = errors.New("agent: system_prompt exceeds 8KB")
)

// MaxSystemPromptBytes 上限 8KB：身份配置不该太长，避免每次 LLM 调用浪费 context。
const MaxSystemPromptBytes = 8192

// agentIDRE 限定 agent_id 在 URL / DNS 友好字符集。
// 允许 . _ - @：@ 是命名空间分隔符，把 owner_username 嵌进 agent_id（"bot@alice"）
// 让不同用户的 slug 不冲突。slug 仍只能用 a-z0-9._-。
var agentIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._@-]{1,62}[a-z0-9]$`)

// slugRE 校验用户在 UI 输入的"短 ID"（不含命名空间分隔符）。
// 区分于 agentIDRE：slug 不允许 @，强制由 Gateway 拼接。
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}[a-z0-9]$|^[a-z0-9]$`)

// ValidateAgentID 对齐 DB 约束，导出给 API 层提前失败用，免得打到 repo。
func ValidateAgentID(s string) error {
	if !agentIDRE.MatchString(s) {
		return ErrInvalidAgentID
	}
	if strings.HasPrefix(s, "virtual-user-") {
		return ErrReservedVirtualName
	}
	return nil
}

// ValidateSlug 校验用户输入的 slug 部分（命名空间前的短 ID）。
// 比 agent_id 更严：不允许 @，免得用户绕过命名空间机制自己拼 ID。
func ValidateSlug(s string) error {
	if s == "" || !slugRE.MatchString(s) {
		return ErrInvalidAgentID
	}
	if strings.HasPrefix(s, "virtual-user-") {
		return ErrReservedVirtualName
	}
	return nil
}

// ComposeAgentID 把 owner username + slug 拼成最终 agent_id：`slug@username`。
//
// 选 @ 不选 / 是为了 URL path safe（不需 escape，mux 路径解析也不会被切）。
// slug 在前 username 在后是用户视角更自然的"agent 名 + 命名空间"，跟 email 模式一致。
func ComposeAgentID(slug, ownerUsername string) string {
	return strings.ToLower(strings.TrimSpace(slug)) + "@" + strings.ToLower(strings.TrimSpace(ownerUsername))
}

// ParseAgentID 拆出 agent_id 中的 slug + owner 部分。
// 不带 @ 的（老数据 / virtual-user-{uid}）：slug = 整个 ID，owner = ""。
func ParseAgentID(id string) (slug string, ownerUsername string) {
	id = strings.ToLower(strings.TrimSpace(id))
	if idx := strings.LastIndex(id, "@"); idx > 0 && idx < len(id)-1 {
		return id[:idx], id[idx+1:]
	}
	return id, ""
}

// NormalizeAgentID 去空格 + 转小写。整条链路保持大小写不敏感，
// 这样 AgentCache 的 key 和 DB 查询永远对齐。
func NormalizeAgentID(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
