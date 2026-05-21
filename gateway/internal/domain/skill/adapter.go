package skill

import (
	"context"
	"fmt"
)

// Adapter 把 Repo 包成满足 agent.SkillRepo 接口的形式
// （agent.SkillRepo 用 any 规避 import cycle）。调用方在 Register 时传
// `[]Input`，这里做一次 type-assert 再转发。
type Adapter struct {
	repo Repo
}

// NewAdapter 返回一个满足 agent.SkillRepo 的 skill 替换器。
func NewAdapter(repo Repo) *Adapter { return &Adapter{repo: repo} }

// ReplaceByAgentID 实现 agent.SkillRepo。
func (a *Adapter) ReplaceByAgentID(ctx context.Context, agentID string, payload any) error {
	if payload == nil {
		return a.repo.ReplaceByAgentID(ctx, agentID, nil)
	}
	skills, ok := payload.([]Input)
	if !ok {
		return fmt.Errorf("skill: expected []Input, got %T", payload)
	}
	if err := ValidateInput(skills); err != nil {
		return err
	}
	return a.repo.ReplaceByAgentID(ctx, agentID, skills)
}
