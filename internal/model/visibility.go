package model

// VisibilityMode agent 权限模式
type VisibilityMode int8

const (
	VisibilityPublic  VisibilityMode = 0 // 公开，无需 token
	VisibilityPrivate VisibilityMode = 1 // 私有，需要 token + 权限名单
)

// ApplyStatus 申请状态
type ApplyStatus int8

const (
	ApplyStatusPending  ApplyStatus = 1
	ApplyStatusApproved ApplyStatus = 2
	ApplyStatusRejected ApplyStatus = 3
)
