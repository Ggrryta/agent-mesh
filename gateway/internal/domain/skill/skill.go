// Package skill 承载 agent 对外声明的能力集合。每个 agent 的 skills 在
// Register 时原子替换。Skills 只是发现用的元信息，mesh 路由不校验 skill
// 契约。
package skill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Skill 对应 skills 表一行。
type Skill struct {
	ID          int64
	AgentID     string
	SkillID     string
	Name        string
	Description string
	Tags        []string
	InputModes  []string
	OutputModes []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Input 是 service 层接收的 payload，字段形状对齐 A2A AgentCard。
type Input struct {
	SkillID     string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// 域错误。
var (
	ErrInvalidSkillID = errors.New("skill: skill_id must be 2-128 chars [a-z0-9._-]")
	ErrInvalidName    = errors.New("skill: name is required")
	ErrDuplicateID    = errors.New("skill: duplicate skill_id within payload")
)

var skillIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,126}[a-z0-9]$`)

// ValidateInput 在真正落库前确保 payload 自洽。
func ValidateInput(in []Input) error {
	seen := make(map[string]struct{}, len(in))
	for _, s := range in {
		if !skillIDRE.MatchString(s.SkillID) {
			return fmt.Errorf("%w: %q", ErrInvalidSkillID, s.SkillID)
		}
		if s.Name == "" {
			return ErrInvalidName
		}
		if _, dup := seen[s.SkillID]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateID, s.SkillID)
		}
		seen[s.SkillID] = struct{}{}
	}
	return nil
}

// Repo 是 skill 的数据访问接口。
type Repo interface {
	// ReplaceByAgentID 在一个事务内原子替换某个 agent 的 skill 集合
	// （DELETE + INSERT）。
	ReplaceByAgentID(ctx context.Context, agentID string, skills []Input) error
	ListByAgentID(ctx context.Context, agentID string) ([]*Skill, error)
	// ListByAgentIDs 批量查询多个 agent 的 skills，避免 N+1。
	// 返回一个 agent_id → skills 的 map；空 map 表示没有任何匹配。
	ListByAgentIDs(ctx context.Context, agentIDs []string) (map[string][]*Skill, error)
}

// SQLRepo 是 MySQL 实现。
type SQLRepo struct {
	db *sql.DB
}

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

// ReplaceByAgentID 是写入路径。用事务的原因：
//   - 调用方给了 N 条 skill，如果只写成功了部分就失败，agent 对外就会
//     同时广告新旧 skill，出现一致性破绽。
//   - agent 的 skill 数量不多，DELETE + INSERT 比 diff-and-update 更便宜。
func (r *SQLRepo) ReplaceByAgentID(ctx context.Context, agentID string, skills []Input) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("skill: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, "DELETE FROM skills WHERE agent_id = ?", agentID); err != nil {
		return fmt.Errorf("skill: delete old: %w", err)
	}
	for _, s := range skills {
		tags, _ := json.Marshal(s.Tags)
		ins, _ := json.Marshal(s.InputModes)
		outs, _ := json.Marshal(s.OutputModes)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO skills (agent_id, skill_id, name, description, tags_json, input_modes_json, output_modes_json)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			agentID, s.SkillID, s.Name, s.Description,
			string(tags), string(ins), string(outs),
		); err != nil {
			return fmt.Errorf("skill: insert %q: %w", s.SkillID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("skill: commit: %w", err)
	}
	committed = true
	return nil
}

func (r *SQLRepo) ListByAgentID(ctx context.Context, agentID string) ([]*Skill, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, agent_id, skill_id, name, description,
		       tags_json, input_modes_json, output_modes_json,
		       created_at, updated_at
		FROM skills WHERE agent_id = ? ORDER BY skill_id ASC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("skill: list: %w", err)
	}
	defer rows.Close()
	out := make([]*Skill, 0, 8)
	for rows.Next() {
		s := &Skill{}
		var tags, ins, outs []byte
		if err := rows.Scan(&s.ID, &s.AgentID, &s.SkillID, &s.Name, &s.Description,
			&tags, &ins, &outs, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if len(tags) > 0 {
			_ = json.Unmarshal(tags, &s.Tags)
		}
		if len(ins) > 0 {
			_ = json.Unmarshal(ins, &s.InputModes)
		}
		if len(outs) > 0 {
			_ = json.Unmarshal(outs, &s.OutputModes)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListByAgentIDs 批量读取多个 agent 的 skills。一次 SQL 查询替代 N 次
// ListByAgentID，Market 列表路径关键优化。返回 map[agent_id][]Skill，
// 没有 skills 的 agent 不会出现在 map 里（由调用方默认空 slice）。
func (r *SQLRepo) ListByAgentIDs(ctx context.Context, agentIDs []string) (map[string][]*Skill, error) {
	if len(agentIDs) == 0 {
		return map[string][]*Skill{}, nil
	}
	// 动态构造 IN(?,?,...) 占位符。agentIDs 长度在几十到几百级别，单条 SQL 可承受。
	placeholders := make([]string, len(agentIDs))
	args := make([]any, len(agentIDs))
	for i, id := range agentIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`
		SELECT id, agent_id, skill_id, name, description,
		       tags_json, input_modes_json, output_modes_json,
		       created_at, updated_at
		FROM skills WHERE agent_id IN (%s)
		ORDER BY agent_id, skill_id ASC`, strings.Join(placeholders, ","))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("skill: list by ids: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]*Skill, len(agentIDs))
	for rows.Next() {
		s := &Skill{}
		var tags, ins, outs []byte
		if err := rows.Scan(&s.ID, &s.AgentID, &s.SkillID, &s.Name, &s.Description,
			&tags, &ins, &outs, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if len(tags) > 0 {
			_ = json.Unmarshal(tags, &s.Tags)
		}
		if len(ins) > 0 {
			_ = json.Unmarshal(ins, &s.InputModes)
		}
		if len(outs) > 0 {
			_ = json.Unmarshal(outs, &s.OutputModes)
		}
		out[s.AgentID] = append(out[s.AgentID], s)
	}
	return out, rows.Err()
}
