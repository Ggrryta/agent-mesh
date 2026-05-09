package repo

import (
	"context"
	"errors"
	"time"

	"agent-gateway/internal/model"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	ErrTaskV2NotFound     = errors.New("task not found")
	ErrTaskV2BadState     = errors.New("task in incompatible state")
	ErrTaskMessageConflict = errors.New("task message seq conflict")
)

// TaskV2Repo 管理 tasks_v2 / task_members / task_messages 三表
// 一次 task 代表一次多轮对话,参与方通过 messages 流往来
type TaskV2Repo struct {
	db *gorm.DB
}

func NewTaskV2Repo(db *gorm.DB) *TaskV2Repo {
	return &TaskV2Repo{db: db}
}

// CreateParams 创建 task 的参数
type CreateTaskParams struct {
	TaskID         string
	Title          string
	CreatorAgentID string
	Members        []string // 包含 creator,创建 task 时指定所有参与方
	TTL            time.Duration
	InitialMessage *InitialMessage // 可选,创建时立即写入一条消息
}

// InitialMessage 创建 task 时可选附带的首条消息
type InitialMessage struct {
	MessageID string
	Content   datatypes.JSON // A2A parts 数组
}

// Create 事务内创建 task + members + (可选)首条消息
func (r *TaskV2Repo) Create(ctx context.Context, p CreateTaskParams) (*model.TaskV2, error) {
	now := time.Now()
	ttl := p.TTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	expire := now.Add(ttl)

	task := &model.TaskV2{
		TaskID:         p.TaskID,
		Title:          p.Title,
		CreatorAgentID: p.CreatorAgentID,
		Status:         model.TaskV2StatusActive,
		ExpireAt:       &expire,
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		// creator 作为 role=creator,其他作为 member
		seen := map[string]bool{}
		members := make([]model.TaskMember, 0, len(p.Members))
		for _, aid := range p.Members {
			if aid == "" || seen[aid] {
				continue
			}
			seen[aid] = true
			role := model.TaskMemberRoleMember
			if aid == p.CreatorAgentID {
				role = model.TaskMemberRoleCreator
			}
			members = append(members, model.TaskMember{
				TaskID:   p.TaskID,
				AgentID:  aid,
				Role:     role,
			})
		}
		if len(members) > 0 {
			if err := tx.Create(&members).Error; err != nil {
				return err
			}
		}
		if p.InitialMessage != nil {
			msg := model.TaskMessage{
				TaskID:        p.TaskID,
				Seq:           0,
				SenderAgentID: p.CreatorAgentID,
				MessageID:     p.InitialMessage.MessageID,
				Content:       p.InitialMessage.Content,
			}
			if err := tx.Create(&msg).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return task, nil
}

// Get 按 task_id 查任务(不含消息和成员)
func (r *TaskV2Repo) Get(ctx context.Context, taskID string) (*model.TaskV2, error) {
	var t model.TaskV2
	err := r.db.WithContext(ctx).Where("task_id = ?", taskID).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTaskV2NotFound
		}
		return nil, err
	}
	return &t, nil
}

// ListMembers 查询 task 的所有成员
func (r *TaskV2Repo) ListMembers(ctx context.Context, taskID string) ([]model.TaskMember, error) {
	var list []model.TaskMember
	err := r.db.WithContext(ctx).Where("task_id = ?", taskID).Find(&list).Error
	return list, err
}

// IsMember 检查 agent 是否是 task 成员(未离开)
func (r *TaskV2Repo) IsMember(ctx context.Context, taskID, agentID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.TaskMember{}).
		Where("task_id = ? AND agent_id = ? AND left_at IS NULL", taskID, agentID).
		Count(&count).Error
	return count > 0, err
}

// AppendMessage 向 task 追加一条消息,seq 自动累加
// 使用乐观锁处理并发:先读 max(seq),再写 seq+1,写入失败则重试
func (r *TaskV2Repo) AppendMessage(ctx context.Context, taskID, senderAgentID, messageID string, content datatypes.JSON) (*model.TaskMessage, error) {
	var result *model.TaskMessage
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 检查 task 状态 active
		var t model.TaskV2
		if err := tx.Where("task_id = ?", taskID).First(&t).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTaskV2NotFound
			}
			return err
		}
		if t.Status != model.TaskV2StatusActive {
			return ErrTaskV2BadState
		}
		// 求下一个 seq
		var maxSeq struct {
			Val *int
		}
		if err := tx.Model(&model.TaskMessage{}).
			Select("MAX(seq) as val").
			Where("task_id = ?", taskID).
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		nextSeq := 0
		if maxSeq.Val != nil {
			nextSeq = *maxSeq.Val + 1
		}
		msg := &model.TaskMessage{
			TaskID:        taskID,
			Seq:           nextSeq,
			SenderAgentID: senderAgentID,
			MessageID:     messageID,
			Content:       content,
		}
		if err := tx.Create(msg).Error; err != nil {
			if isDuplicateKeyError(err) {
				return ErrTaskMessageConflict
			}
			return err
		}
		// touch task.updated_at
		if err := tx.Model(&model.TaskV2{}).Where("task_id = ?", taskID).
			Update("updated_at", time.Now()).Error; err != nil {
			return err
		}
		result = msg
		return nil
	})
	return result, err
}

// ListMessages 按 seq 范围查询消息(since_seq 为已读最大 seq,返回 seq > since_seq 的消息)
func (r *TaskV2Repo) ListMessages(ctx context.Context, taskID string, sinceSeq, limit int) ([]model.TaskMessage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var list []model.TaskMessage
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND seq > ?", taskID, sinceSeq).
		Order("seq ASC").
		Limit(limit).
		Find(&list).Error
	return list, err
}

// Close 关闭 task,status 转为 closed / timeout / failed
func (r *TaskV2Repo) Close(ctx context.Context, taskID string, status model.TaskV2Status) error {
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&model.TaskV2{}).
		Where("task_id = ? AND status = ?", taskID, model.TaskV2StatusActive).
		Updates(map[string]any{
			"status":    status,
			"closed_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTaskV2NotFound
	}
	return nil
}

// ListByMember 查询某 agent 参与的 task 列表,按更新时间倒序
func (r *TaskV2Repo) ListByMember(ctx context.Context, agentID string, status *model.TaskV2Status, limit int) ([]model.TaskV2, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := r.db.WithContext(ctx).Table("tasks_v2 AS t").
		Joins("INNER JOIN task_members m ON m.task_id = t.task_id").
		Where("m.agent_id = ? AND m.left_at IS NULL", agentID)
	if status != nil {
		q = q.Where("t.status = ?", *status)
	}
	var list []model.TaskV2
	err := q.Order("t.updated_at DESC").Limit(limit).Find(&list).Error
	return list, err
}

// UpdateLastReadSeq 更新某成员的 last_read_seq
func (r *TaskV2Repo) UpdateLastReadSeq(ctx context.Context, taskID, agentID string, seq int) error {
	res := r.db.WithContext(ctx).Model(&model.TaskMember{}).
		Where("task_id = ? AND agent_id = ? AND last_read_seq < ?", taskID, agentID, seq).
		Update("last_read_seq", seq)
	return res.Error
}
