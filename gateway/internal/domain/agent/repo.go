package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Repo 是数据访问接口。service 依赖这个接口，不依赖 *sql.DB。
type Repo interface {
	Create(ctx context.Context, a *Agent) error
	Upsert(ctx context.Context, a *Agent) error
	GetByAgentID(ctx context.Context, agentID string) (*Agent, error)
	UpdateStatus(ctx context.Context, agentID string, s Status) error
	UpdateHeartbeat(ctx context.Context, agentID string, ts time.Time) error
	Delete(ctx context.Context, agentID string) error
	List(ctx context.Context, filter Filter) ([]*Agent, error)
}

// Filter 收窄 List 调用。零值字段被视为"全部"。
type Filter struct {
	OwnerUID int64
	Status   Status
	Kind     Kind
	// Search 做 name + description 的 LIKE 模糊匹配。空字符串表示不过滤。
	// 仅 UI 目录浏览用，高 QPS 路径禁用。
	Search string
	// Limit / Offset 走分页；Limit<=0 时走默认值（500，给缓存预热用）。
	// 外部 API 的 Market 会显式传 Limit<=100。
	Limit  int
	Offset int
}

// SQLRepo 是 MySQL 实现。
type SQLRepo struct {
	db *sql.DB
}

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

// Create 插入全新的一行。唯一键冲突时返回 ErrAgentIDExists。
func (r *SQLRepo) Create(ctx context.Context, a *Agent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agents (agent_id, owner_uid, name, description, headline, url, version, kind, status, agent_card_json, system_prompt, workspace_path, last_heartbeat_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.AgentID, a.OwnerUID, a.Name, a.Description, nullableStr(a.Headline), a.URL, a.Version,
		string(a.Kind), string(a.Status), toNullJSON(a.AgentCardJSON),
		nullableStr(a.SystemPrompt), nullableStr(a.WorkspacePath), nullTime(a.LastHeartbeatAt),
	)
	if err != nil {
		if isDup(err) {
			return ErrAgentIDExists
		}
		return fmt.Errorf("agent: insert: %w", err)
	}
	return nil
}

// Upsert 复用唯一键合并到已有行。owner_uid 绝不更新——ownership 不可被
// 静默移交。
func (r *SQLRepo) Upsert(ctx context.Context, a *Agent) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agents (agent_id, owner_uid, name, description, headline, url, version, kind, status, agent_card_json, system_prompt, workspace_path, last_heartbeat_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			name = VALUES(name),
			description = VALUES(description),
			headline = VALUES(headline),
			url = VALUES(url),
			version = VALUES(version),
			status = VALUES(status),
			agent_card_json = VALUES(agent_card_json),
			system_prompt = VALUES(system_prompt),
			workspace_path = VALUES(workspace_path),
			last_heartbeat_at = VALUES(last_heartbeat_at)`,
		a.AgentID, a.OwnerUID, a.Name, a.Description, nullableStr(a.Headline), a.URL, a.Version,
		string(a.Kind), string(a.Status), toNullJSON(a.AgentCardJSON),
		nullableStr(a.SystemPrompt), nullableStr(a.WorkspacePath), nullTime(a.LastHeartbeatAt),
	)
	if err != nil {
		return fmt.Errorf("agent: upsert: %w", err)
	}
	return nil
}

func (r *SQLRepo) GetByAgentID(ctx context.Context, agentID string) (*Agent, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, agent_id, owner_uid, name, description, headline, url, version,
		       kind, status, agent_card_json, system_prompt, workspace_path,
		       last_heartbeat_at, last_probed_at, created_at, updated_at
		FROM agents WHERE agent_id = ? LIMIT 1`, agentID)
	a, err := scan(row)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// UpdateStatus 是切换 active / draining / inactive 的唯一入口。
// 走 DATETIME 更新以便审计状态转换。
func (r *SQLRepo) UpdateStatus(ctx context.Context, agentID string, s Status) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE agents SET status = ? WHERE agent_id = ?",
		string(s), agentID)
	if err != nil {
		return fmt.Errorf("agent: update status: %w", err)
	}
	return nil
}

// UpdateHeartbeat 刷新 last_heartbeat_at，并把 inactive 的 agent 拉回 active。
// draining 刻意不复位：draining 是运维意图，心跳不该替它改回来。
func (r *SQLRepo) UpdateHeartbeat(ctx context.Context, agentID string, ts time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE agents
		SET last_heartbeat_at = ?,
		    status = CASE WHEN status = 'inactive' THEN 'active' ELSE status END
		WHERE agent_id = ?`, ts, agentID)
	if err != nil {
		return fmt.Errorf("agent: heartbeat: %w", err)
	}
	return nil
}

func (r *SQLRepo) Delete(ctx context.Context, agentID string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM agents WHERE agent_id = ?", agentID)
	if err != nil {
		return fmt.Errorf("agent: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// List 返回匹配 filter 的 agents。零值字段忽略。
func (r *SQLRepo) List(ctx context.Context, filter Filter) ([]*Agent, error) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if filter.OwnerUID != 0 {
		clauses = append(clauses, "owner_uid = ?")
		args = append(args, filter.OwnerUID)
	}
	if filter.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, string(filter.Kind))
	}
	if filter.Search != "" {
		// 双字段 LIKE。ESCAPE 默认是 `\\`，我们把 % _ \ 全部转义防止注入式通配。
		// %...% 两头加；只用 prefix 匹配太严，MVP 不做性能优化。
		esc := escapeLike(filter.Search)
		clauses = append(clauses, "(name LIKE ? OR description LIKE ?)")
		args = append(args, "%"+esc+"%", "%"+esc+"%")
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 500
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	q := fmt.Sprintf(`
		SELECT id, agent_id, owner_uid, name, description, headline, url, version,
		       kind, status, agent_card_json, system_prompt, workspace_path,
		       last_heartbeat_at, last_probed_at, created_at, updated_at
		FROM agents %s ORDER BY id DESC LIMIT %d OFFSET %d`, where, limit, offset)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("agent: list: %w", err)
	}
	defer rows.Close()
	out := make([]*Agent, 0, 16)
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// scanner 用同一个 Scan 签名把 *sql.Row 和 *sql.Rows 抽象到一起。
type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Agent, error) {
	a := &Agent{}
	var (
		card      []byte // NULLable JSON
		prompt    sql.NullString
		headline  sql.NullString
		workspace sql.NullString
		hb        sql.NullTime
		probe     sql.NullTime
		kind      string
		stat      string
	)
	err := s.Scan(&a.ID, &a.AgentID, &a.OwnerUID, &a.Name, &a.Description, &headline,
		&a.URL, &a.Version, &kind, &stat, &card, &prompt, &workspace,
		&hb, &probe, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}
	a.Kind = Kind(kind)
	a.Status = Status(stat)
	if len(card) > 0 {
		a.AgentCardJSON = card
	}
	if prompt.Valid {
		a.SystemPrompt = prompt.String
	}
	if headline.Valid {
		a.Headline = headline.String
	}
	if workspace.Valid {
		a.WorkspacePath = workspace.String
	}
	if hb.Valid {
		t := hb.Time
		a.LastHeartbeatAt = &t
	}
	if probe.Valid {
		t := probe.Time
		a.LastProbedAt = &t
	}
	return a, nil
}

// toNullJSON 把空 slice 转成 NULL 保持列整洁；
// 非空 payload 要先校验是合法 JSON，防止写入脏数据。
func toNullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if !json.Valid(b) {
		// 刻意把非法 JSON 降级为空对象；service 层本应先校验。
		return "{}"
	}
	return string(b)
}

// nullableStr 空字符串落 NULL，避免列里出现"" 这种语义模糊的值。
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func isDup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Error 1062")
}

// escapeLike 把 LIKE 通配符 (%、_) 和反斜杠本身转义，防止调用方无意或
// 恶意把 `%` 塞进搜索词后把全表拉回来。
// 依赖默认的 ESCAPE '\\'。
func escapeLike(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	)
	return r.Replace(s)
}
