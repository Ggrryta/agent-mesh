package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxMessageHistoryRows 是 listMessages / context list 的硬上限。
// 长 task 的 history 用这个上限来防止单查询超慢；调用方应通过返回值长度
// 判断是否被截断（len == MaxMessageHistoryRows 视为可能被截，提示分页）。
//
// 5000 选定理由：单 task 真实最大观察值 ~1000 条（agent 之间长 brainstorm）。
// 5000 留 5x 余量，单行 ~2KB 算下来 10MB 一次查询，在可接受范围。
// 后续如果需要无限历史，加 since_id / limit 分页（见 TODO PAG-001）。
const MaxMessageHistoryRows = 5000

// Repo 是数据访问接口。Service 依赖它，不直接依赖 *sql.DB。
//
// 设计要点：
//   - 所有写操作都带幂等保证（UNIQUE 索引 + ON DUPLICATE 或 INSERT IGNORE）
//   - TransitionStatus 用 CAS（`UPDATE ... WHERE status IN (...) AND RowsAffected=1`），
//     同 Prober / Friendship 的成熟模式
//   - Get 接口分两层：GetTaskOnly 只查主表，GetTask(withHistory, withArtifacts)
//     按需附带关联表（避免小查询也拖大 JOIN）
type Repo interface {
	// CreateTask 在一个事务内 INSERT tasks + INSERT 首条 message。
	// firstMessage.TaskID / ContextID / MessageID 必须已由 service 层赋好值。
	//
	// 两种幂等语义：
	//   - task_id 已存在：返回已有 Task（不报错），message 也不重复插入
	//   - 单看 message_id 已存在：视为重复提交，同上
	// 并发情况下第二次 Create 可能同时撞上 task 和 message 的 UNIQUE；
	// 都走"取已有"的路径，调用方幂等重试安全。
	CreateTask(ctx context.Context, t *Task, firstMessage *Message) (*Task, error)

	// AppendMessage 追加一条 message 到 task_messages 表。
	// message_id 已存在（且内容相同）→ 返回已有，不报错（幂等）
	// message_id 已存在但内容不同  → 返回 ErrMessageIDDuplicate
	AppendMessage(ctx context.Context, m *Message) (*Message, error)

	// AppendArtifact 追加 artifact。
	// (task_id, artifact_id) 已存在 → 返回 ErrArtifactIDDuplicate
	// （artifact 不做"内容相同就幂等"的判定，因为 parts 比较成本高）
	AppendArtifact(ctx context.Context, a *Artifact) (*Artifact, error)

	// TransitionStatus 原子切换状态。
	//   - fromStates 必须非空；SQL 里用 `status IN (...)`
	//   - statusMessage / errorMsg 可空
	// 返回值：
	//   - 第一个 bool：是否真的发生了转换（RowsAffected==1）
	//   - 第二个 *Task：转换后的 task 最新状态（即使没转换也查一下回）
	TransitionStatus(ctx context.Context, taskID string, fromStates []State, to State, statusMessage, errorMsg string) (bool, *Task, error)

	// GetTask 查 task 主表。withHistory / withArtifacts 控制是否附带关联。
	GetTask(ctx context.Context, taskID string, withHistory, withArtifacts bool) (*Task, []*Message, []*Artifact, error)

	// ListByContext 返回同 contextID 下的全部 task（按 created_at 升序）。
	// 不附带 history/artifacts，列表页一般只需要主表。
	ListByContext(ctx context.Context, contextID string) ([]*Task, error)

	// ListRecentByAgents 返回 agentIDs 任一方参与（from/to）的近期 task。
	// 按 updated_at 倒序，limit 截断。Dashboard "Continue Working" 用。
	ListRecentByAgents(ctx context.Context, agentIDs []string, limit int) ([]*Task, error)

	// GetMessageByID 按 message_id 查单条消息，幂等校验用。
	GetMessageByID(ctx context.Context, messageID string) (*Message, error)

	// ListTimeline 返回 context 下所有元数据事件（message/artifact），
	// 按时间升序，sinceID 游标分页。
	ListTimeline(ctx context.Context, contextID string, sinceID int64, limit int) ([]*TimelineEntry, error)

	// UpdateChatStreak 更新闲聊连击计数。increment=true → streak++；false → reset + 更新 last_substantive_at。
	UpdateChatStreak(ctx context.Context, taskID string, increment bool) error

	// ListChatterTasks 返回 streak >= minStreak 且 last_substantive_at < before 的非终态 task。
	// auto-close 定时任务用。
	ListChatterTasks(ctx context.Context, minStreak int, before time.Time) ([]*Task, error)

	// DeleteTerminalTasksBefore 删除 ownerUID 名下、before 之前的终态 task + 关联数据。
	// 返回删除的 task 数量。
	DeleteTerminalTasksBefore(ctx context.Context, ownerUID int64, before time.Time) (int, error)

	// DeleteTaskByID 删除单个 task + 关联 messages/artifacts/inbox。
	DeleteTaskByID(ctx context.Context, taskID string) error

	// TouchActivity 刷新 task 的 updated_at（活跃心跳，防止 TTL 超时）。
	TouchActivity(ctx context.Context, taskID string) error

	// ListInactiveNonTerminal 返回 updated_at < cutoff 的非终态 task。
	// 活跃超时扫描用。
	ListInactiveNonTerminal(ctx context.Context, cutoff time.Time) ([]*Task, error)
}

// SQLRepo 是 MySQL 实现。
type SQLRepo struct{ db *sql.DB }

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

// ───────────────────────────────────────────────────────────────────
// CreateTask
// ───────────────────────────────────────────────────────────────────

func (r *SQLRepo) CreateTask(ctx context.Context, t *Task, firstMessage *Message) (*Task, error) {
	if t == nil || firstMessage == nil {
		return nil, fmt.Errorf("task: nil task or first message")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("task: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// INSERT tasks：UNIQUE (task_id) 冲突时认为"已存在"，取现有返回。
	metadataJSON, err := marshalOptional(t.Metadata)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO reliable_async_tasks
			(task_id, context_id, from_agent_id, to_agent_id, status,
			 status_message, error_msg, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TaskID, t.ContextID, t.FromAgentID, t.ToAgentID,
		string(t.Status), t.StatusMessage, t.ErrorMsg, metadataJSON,
	)
	if err != nil && !isDup(err) {
		return nil, fmt.Errorf("task: insert task: %w", err)
	}
	// 无论是否 dup，下面都直接 INSERT message；也用 dup 容错。

	partsJSON, err := json.Marshal(firstMessage.Parts)
	if err != nil {
		return nil, fmt.Errorf("task: marshal parts: %w", err)
	}
	msgMetaJSON, err := marshalOptional(firstMessage.Metadata)
	if err != nil {
		return nil, err
	}
	refsJSON, err := marshalOptional(firstMessage.RefTaskIDs)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO task_messages
			(message_id, task_id, context_id, role, parts_json, preview,
			 metadata_json, reference_task_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		firstMessage.MessageID, firstMessage.TaskID, firstMessage.ContextID,
		string(firstMessage.Role), partsJSON, nullableString(firstMessage.Preview),
		msgMetaJSON, refsJSON,
	)
	if err != nil && !isDup(err) {
		return nil, fmt.Errorf("task: insert first message: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("task: commit: %w", err)
	}
	committed = true

	// 读回一次，拿到数据库赋的 created_at / updated_at。
	return r.getTaskOnlyContext(ctx, t.TaskID)
}

// ───────────────────────────────────────────────────────────────────
// AppendMessage
// ───────────────────────────────────────────────────────────────────

func (r *SQLRepo) AppendMessage(ctx context.Context, m *Message) (*Message, error) {
	partsJSON, err := json.Marshal(m.Parts)
	if err != nil {
		return nil, fmt.Errorf("task: marshal parts: %w", err)
	}
	metaJSON, err := marshalOptional(m.Metadata)
	if err != nil {
		return nil, err
	}
	refsJSON, err := marshalOptional(m.RefTaskIDs)
	if err != nil {
		return nil, err
	}

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO task_messages
			(message_id, task_id, context_id, role, parts_json, preview,
			 metadata_json, reference_task_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.MessageID, m.TaskID, m.ContextID, string(m.Role),
		partsJSON, nullableString(m.Preview), metaJSON, refsJSON,
	)
	if err != nil {
		if isDup(err) {
			// 同 message_id 已存在：查出来比对关键字段。
			existing, getErr := r.GetMessageByID(ctx, m.MessageID)
			if getErr != nil {
				return nil, fmt.Errorf("task: lookup existing message: %w", getErr)
			}
			if existing.TaskID != m.TaskID || existing.Role != m.Role {
				// 同 id 但上下文不同 —— 视为冲突
				return nil, ErrMessageIDDuplicate
			}
			// 视为幂等重试
			return existing, nil
		}
		return nil, fmt.Errorf("task: insert message: %w", err)
	}

	return r.GetMessageByID(ctx, m.MessageID)
}

// ───────────────────────────────────────────────────────────────────
// AppendArtifact
// ───────────────────────────────────────────────────────────────────

func (r *SQLRepo) AppendArtifact(ctx context.Context, a *Artifact) (*Artifact, error) {
	partsJSON, err := json.Marshal(a.Parts)
	if err != nil {
		return nil, fmt.Errorf("task: marshal parts: %w", err)
	}
	metaJSON, err := marshalOptional(a.Metadata)
	if err != nil {
		return nil, err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO task_artifacts
			(artifact_id, task_id, context_id, name, description,
			 parts_json, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ArtifactID, a.TaskID, a.ContextID, a.Name, a.Description,
		partsJSON, metaJSON,
	)
	if err != nil {
		if isDup(err) {
			return nil, ErrArtifactIDDuplicate
		}
		return nil, fmt.Errorf("task: insert artifact: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("task: artifact last insert id: %w", err)
	}
	// 读回一次拿 created_at
	row := r.db.QueryRowContext(ctx, `
		SELECT id, artifact_id, task_id, context_id, name, description,
		       parts_json, metadata_json, created_at
		FROM task_artifacts WHERE id = ? LIMIT 1`, id)
	return scanArtifact(row)
}

// ───────────────────────────────────────────────────────────────────
// TransitionStatus
// ───────────────────────────────────────────────────────────────────

func (r *SQLRepo) TransitionStatus(ctx context.Context, taskID string, fromStates []State, to State, statusMessage, errorMsg string) (bool, *Task, error) {
	if len(fromStates) == 0 {
		return false, nil, fmt.Errorf("task: TransitionStatus: fromStates empty")
	}
	// 构造 IN 占位符：?, ?, ?, ...
	placeholders := make([]string, len(fromStates))
	args := make([]any, 0, len(fromStates)+4)
	args = append(args, string(to), statusMessage, errorMsg)
	for i, s := range fromStates {
		placeholders[i] = "?"
		args = append(args, string(s))
	}
	args = append(args, taskID)

	q := fmt.Sprintf(`
		UPDATE reliable_async_tasks
		SET status = ?, status_message = ?, error_msg = ?
		WHERE status IN (%s) AND task_id = ?`,
		strings.Join(placeholders, ","))
	res, err := r.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, nil, fmt.Errorf("task: transition: %w", err)
	}
	n, _ := res.RowsAffected()
	// 不论是否真的转换成功，都读回一次最新状态，便于调用方返回当前真相。
	t, tErr := r.getTaskOnlyContext(ctx, taskID)
	if tErr != nil {
		return n == 1, nil, tErr
	}
	return n == 1, t, nil
}

// ───────────────────────────────────────────────────────────────────
// 查询
// ───────────────────────────────────────────────────────────────────

// getTaskOnlyContext 只读主表一行。
func (r *SQLRepo) getTaskOnlyContext(ctx context.Context, taskID string) (*Task, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, task_id, context_id, from_agent_id, to_agent_id,
		       status, status_message, error_msg, metadata_json,
		       created_at, updated_at
		FROM reliable_async_tasks WHERE task_id = ? LIMIT 1`, taskID)
	return scanTask(row)
}

func (r *SQLRepo) GetTask(ctx context.Context, taskID string, withHistory, withArtifacts bool) (*Task, []*Message, []*Artifact, error) {
	t, err := r.getTaskOnlyContext(ctx, taskID)
	if err != nil {
		return nil, nil, nil, err
	}
	var history []*Message
	var artifacts []*Artifact
	if withHistory {
		history, err = r.listMessages(ctx, taskID)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if withArtifacts {
		artifacts, err = r.listArtifacts(ctx, taskID)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return t, history, artifacts, nil
}

func (r *SQLRepo) ListByContext(ctx context.Context, contextID string) ([]*Task, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, task_id, context_id, from_agent_id, to_agent_id,
		       status, status_message, error_msg, metadata_json,
		       created_at, updated_at
		FROM reliable_async_tasks
		WHERE context_id = ?
		ORDER BY created_at ASC
		LIMIT 500`, contextID)
	if err != nil {
		return nil, fmt.Errorf("task: list by context: %w", err)
	}
	defer rows.Close()
	out := make([]*Task, 0, 8)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListRecentByAgents 返回 agentIDs 任一方参与（from 或 to）的近期 task，
// 按 updated_at 倒序，limit 截断。Dashboard 用。
func (r *SQLRepo) ListRecentByAgents(ctx context.Context, agentIDs []string, limit int) ([]*Task, error) {
	if len(agentIDs) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	placeholders := make([]string, len(agentIDs))
	args := make([]any, 0, len(agentIDs)*2+1)
	for i, id := range agentIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	for _, id := range agentIDs {
		args = append(args, id)
	}
	args = append(args, limit)
	q := fmt.Sprintf(`
		SELECT id, task_id, context_id, from_agent_id, to_agent_id,
		       status, status_message, error_msg, metadata_json,
		       created_at, updated_at
		FROM reliable_async_tasks
		WHERE from_agent_id IN (%s) OR to_agent_id IN (%s)
		ORDER BY updated_at DESC
		LIMIT ?`,
		strings.Join(placeholders, ","), strings.Join(placeholders, ","))
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("task: list recent by agents: %w", err)
	}
	defer rows.Close()
	out := make([]*Task, 0, limit)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *SQLRepo) GetMessageByID(ctx context.Context, messageID string) (*Message, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, message_id, task_id, context_id, role,
		       parts_json, preview, metadata_json, reference_task_ids, created_at
		FROM task_messages WHERE message_id = ? LIMIT 1`, messageID)
	return scanMessage(row)
}

func (r *SQLRepo) listMessages(ctx context.Context, taskID string) ([]*Message, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, message_id, task_id, context_id, role,
		       parts_json, preview, metadata_json, reference_task_ids, created_at
		FROM task_messages WHERE task_id = ?
		ORDER BY created_at ASC, id ASC
		LIMIT ?`, taskID, MaxMessageHistoryRows)
	if err != nil {
		return nil, fmt.Errorf("task: list messages: %w", err)
	}
	defer rows.Close()
	out := make([]*Message, 0, 8)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *SQLRepo) listArtifacts(ctx context.Context, taskID string) ([]*Artifact, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, artifact_id, task_id, context_id, name, description,
		       parts_json, metadata_json, created_at
		FROM task_artifacts WHERE task_id = ?
		ORDER BY created_at ASC, id ASC
		LIMIT 500`, taskID)
	if err != nil {
		return nil, fmt.Errorf("task: list artifacts: %w", err)
	}
	defer rows.Close()
	out := make([]*Artifact, 0, 4)
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ───────────────────────────────────────────────────────────────────
// scan helpers
// ───────────────────────────────────────────────────────────────────

type scanner interface{ Scan(dest ...any) error }

func scanTask(s scanner) (*Task, error) {
	var (
		t       Task
		metaRaw sql.NullString
		status  string
	)
	err := s.Scan(&t.ID, &t.TaskID, &t.ContextID, &t.FromAgentID, &t.ToAgentID,
		&status, &t.StatusMessage, &t.ErrorMsg, &metaRaw,
		&t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	t.Status = State(status)
	if metaRaw.Valid && metaRaw.String != "" {
		m := map[string]any{}
		if e := json.Unmarshal([]byte(metaRaw.String), &m); e == nil {
			t.Metadata = m
		}
	}
	return &t, nil
}

func scanMessage(s scanner) (*Message, error) {
	var (
		m          Message
		role       string
		partsRaw   []byte
		previewRaw sql.NullString
		metaRaw    sql.NullString
		refsRaw    sql.NullString
	)
	err := s.Scan(&m.ID, &m.MessageID, &m.TaskID, &m.ContextID, &role,
		&partsRaw, &previewRaw, &metaRaw, &refsRaw, &m.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMessageNotFound
		}
		return nil, err
	}
	m.Role = Role(role)
	if previewRaw.Valid {
		m.Preview = previewRaw.String
	}
	if len(partsRaw) > 0 {
		_ = json.Unmarshal(partsRaw, &m.Parts)
	}
	if metaRaw.Valid && metaRaw.String != "" {
		mm := map[string]any{}
		if e := json.Unmarshal([]byte(metaRaw.String), &mm); e == nil {
			m.Metadata = mm
		}
	}
	if refsRaw.Valid && refsRaw.String != "" {
		var refs []string
		if e := json.Unmarshal([]byte(refsRaw.String), &refs); e == nil {
			m.RefTaskIDs = refs
		}
	}
	return &m, nil
}

func scanArtifact(s scanner) (*Artifact, error) {
	var (
		a        Artifact
		partsRaw []byte
		metaRaw  sql.NullString
	)
	err := s.Scan(&a.ID, &a.ArtifactID, &a.TaskID, &a.ContextID,
		&a.Name, &a.Description, &partsRaw, &metaRaw, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	if len(partsRaw) > 0 {
		_ = json.Unmarshal(partsRaw, &a.Parts)
	}
	if metaRaw.Valid && metaRaw.String != "" {
		m := map[string]any{}
		if e := json.Unmarshal([]byte(metaRaw.String), &m); e == nil {
			a.Metadata = m
		}
	}
	return &a, nil
}

// ───────────────────────────────────────────────────────────────────
// utils
// ───────────────────────────────────────────────────────────────────

// marshalOptional 把 map/slice 序列化为 JSON；nil / 空视为 NULL（sql.NullString 方式）。
func marshalOptional(v any) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	// reflect 判断"空值"成本大，用类型断言 + 直接 marshal 更直接。
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("task: marshal optional: %w", err)
	}
	// 空对象 / 空数组视为 NULL（避免 DB 里 "{}", "[]" 噪声）
	s := string(b)
	if s == "null" || s == "{}" || s == "[]" {
		return sql.NullString{}, nil
	}
	return sql.NullString{String: s, Valid: true}, nil
}

// nullableString 空字符串存 NULL。
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// isDup 识别 MySQL 1062 唯一键冲突，和其他域一致。
func isDup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Error 1062")
}

// now 封装 time.Now，给测试替换。MVP 未使用，保留接口。
var now = func() time.Time { return time.Now() }

// ListTimeline 返回 context_id 下所有类型的元数据事件（message/artifact/transition），
// 按发生时间升序，支持 sinceID 游标分页。
//
// 只返回元数据（含 preview），不返回 parts 正文。调用方需要正文时再单独拉。
// 三张表 UNION ALL，合并后按 created_at 排序。
func (r *SQLRepo) ListTimeline(ctx context.Context, contextID string, sinceID int64, limit int) ([]*TimelineEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		(SELECT 'message' AS kind, id, task_id, context_id, message_id AS ref_id,
		        role AS from_field, '' AS to_field, COALESCE(preview, '') AS preview,
		        '' AS name, '' AS description, created_at
		 FROM task_messages WHERE context_id = ? AND id > ?)
		UNION ALL
		(SELECT 'artifact' AS kind, id, task_id, context_id, artifact_id AS ref_id,
		        '' AS from_field, '' AS to_field, '' AS preview,
		        COALESCE(name, '') AS name, COALESCE(description, '') AS description, created_at
		 FROM task_artifacts WHERE context_id = ? AND id > ?)
		ORDER BY created_at ASC, id ASC
		LIMIT ?`,
		contextID, sinceID, contextID, sinceID, limit)
	if err != nil {
		return nil, fmt.Errorf("task: list timeline: %w", err)
	}
	defer rows.Close()

	out := make([]*TimelineEntry, 0, limit)
	for rows.Next() {
		var e TimelineEntry
		var fromField, toField string
		if err := rows.Scan(&e.Kind, &e.EntryID, &e.TaskID, &e.ContextID, &e.RefID,
			&fromField, &toField, &e.Preview, &e.Name, &e.Descrption, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.From = fromField
		e.To = toField
		out = append(out, &e)
	}
	return out, rows.Err()
}

// UpdateChatStreak 更新 task 的闲聊连击计数。
// increment=true → streak++；increment=false → streak=0 + last_substantive_at=NOW()
func (r *SQLRepo) UpdateChatStreak(ctx context.Context, taskID string, increment bool) error {
	if increment {
		_, err := r.db.ExecContext(ctx,
			`UPDATE reliable_async_tasks SET chat_streak = chat_streak + 1 WHERE task_id = ?`,
			taskID)
		return err
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE reliable_async_tasks SET chat_streak = 0, last_substantive_at = NOW(3) WHERE task_id = ?`,
		taskID)
	return err
}

// ListChatterTasks 返回闲聊连击达标且超过冷却期的非终态 task。
func (r *SQLRepo) ListChatterTasks(ctx context.Context, minStreak int, before time.Time) ([]*Task, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT task_id, context_id, from_agent_id, to_agent_id, status, status_message,
		       created_at, updated_at
		FROM reliable_async_tasks
		WHERE status IN ('submitted', 'working', 'input-required')
		  AND chat_streak >= ?
		  AND (last_substantive_at IS NULL OR last_substantive_at < ?)
		ORDER BY updated_at ASC
		LIMIT 100`, minStreak, before)
	if err != nil {
		return nil, fmt.Errorf("task: list chatter tasks: %w", err)
	}
	defer rows.Close()
	out := make([]*Task, 0)
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.TaskID, &t.ContextID, &t.FromAgentID, &t.ToAgentID,
			&t.Status, &t.StatusMessage, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// DeleteTerminalTasksBefore 删除 ownerUID 名下、before 之前的终态 task + 关联 messages/artifacts/inbox。
func (r *SQLRepo) DeleteTerminalTasksBefore(ctx context.Context, ownerUID int64, before time.Time) (int, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT agent_id FROM agents WHERE owner_uid = ?`, ownerUID)
	if err != nil {
		return 0, fmt.Errorf("task cleanup: list agents: %w", err)
	}
	defer rows.Close()
	var agentIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		agentIDs = append(agentIDs, id)
	}
	if len(agentIDs) == 0 {
		return 0, nil
	}

	ph := strings.Repeat("?,", len(agentIDs))
	ph = ph[:len(ph)-1]
	args := make([]any, 0, len(agentIDs)*2+1)
	for _, id := range agentIDs {
		args = append(args, id)
	}
	for _, id := range agentIDs {
		args = append(args, id)
	}
	args = append(args, before)

	q := fmt.Sprintf(`SELECT task_id FROM reliable_async_tasks
		WHERE (from_agent_id IN (%s) OR to_agent_id IN (%s))
		  AND status IN ('completed','failed','timeout')
		  AND updated_at < ? LIMIT 1000`, ph, ph)
	taskRows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("task cleanup: list tasks: %w", err)
	}
	defer taskRows.Close()
	var taskIDs []string
	for taskRows.Next() {
		var tid string
		if err := taskRows.Scan(&tid); err != nil {
			return 0, err
		}
		taskIDs = append(taskIDs, tid)
	}
	if len(taskIDs) == 0 {
		return 0, nil
	}

	// 事务内删除：保证要么全删，要么全不删，不留孤儿数据
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("task cleanup: begin tx: %w", err)
	}
	defer tx.Rollback()

	tp := strings.Repeat("?,", len(taskIDs))
	tp = tp[:len(tp)-1]
	tArgs := make([]any, len(taskIDs))
	for i, id := range taskIDs {
		tArgs[i] = id
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM task_messages WHERE task_id IN (%s)`, tp), tArgs...); err != nil {
		return 0, fmt.Errorf("task cleanup: delete messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM task_artifacts WHERE task_id IN (%s)`, tp), tArgs...); err != nil {
		return 0, fmt.Errorf("task cleanup: delete artifacts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM inbox_events WHERE task_id IN (%s)`, tp), tArgs...); err != nil {
		return 0, fmt.Errorf("task cleanup: delete inbox: %w", err)
	}
	res, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM reliable_async_tasks WHERE task_id IN (%s)`, tp), tArgs...)
	if err != nil {
		return 0, fmt.Errorf("task cleanup: delete tasks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("task cleanup: commit: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteTaskByID 删除单个 task + 关联 messages/artifacts/inbox。
func (r *SQLRepo) DeleteTaskByID(ctx context.Context, taskID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("task delete: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM task_messages WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("task delete: messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_artifacts WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("task delete: artifacts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM inbox_events WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("task delete: inbox: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reliable_async_tasks WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("task delete: task: %w", err)
	}
	return tx.Commit()
}

// TouchActivity 刷新 task 的 updated_at。
// 利用 MySQL 的 ON UPDATE CURRENT_TIMESTAMP：只要 UPDATE 了行，updated_at 自动刷新。
func (r *SQLRepo) TouchActivity(ctx context.Context, taskID string) error {
	// 用一个无实际变化的 UPDATE 触发 ON UPDATE CURRENT_TIMESTAMP
	_, err := r.db.ExecContext(ctx,
		`UPDATE reliable_async_tasks SET task_id = task_id WHERE task_id = ?`, taskID)
	return err
}

// ListInactiveNonTerminal 返回 updated_at < cutoff 的非终态 task（活跃超时候选）。
func (r *SQLRepo) ListInactiveNonTerminal(ctx context.Context, cutoff time.Time) ([]*Task, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT task_id, context_id, from_agent_id, to_agent_id, status, status_message,
		       created_at, updated_at
		FROM reliable_async_tasks
		WHERE status IN ('submitted', 'working', 'input-required')
		  AND updated_at < ?
		ORDER BY updated_at ASC
		LIMIT 100`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("task: list inactive: %w", err)
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.TaskID, &t.ContextID, &t.FromAgentID, &t.ToAgentID,
			&t.Status, &t.StatusMessage, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}
