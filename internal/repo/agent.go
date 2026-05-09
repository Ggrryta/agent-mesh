package repo

import (
	"context"
	"errors"
	"time"

	"agent-gateway/internal/model"

	"gorm.io/gorm"
)

var (
	ErrDuplicateAgentID   = errors.New("agent_id already exists")
	ErrAgentNotFound      = errors.New("agent not found")
	ErrAgentSkillNotFound = errors.New("agent skill not found")
)

// AgentFilter 列表查询过滤条件
type AgentFilter struct {
	Keyword    string // 模糊匹配 name/description
	Tag        string // 按 skill tag 过滤（关联 agent_skills 表）
	OwnerAppID string // 只看自己注册的
	Page       int    // 从 1 开始，0 表示不分页
	PageSize   int
}

type AgentRepo struct {
	db *gorm.DB
}

func NewAgentRepo(db *gorm.DB) *AgentRepo {
	return &AgentRepo{db: db}
}

func (r *AgentRepo) Create(ctx context.Context, a *model.Agent) error {
	if err := r.db.WithContext(ctx).Create(a).Error; err != nil {
		if isDuplicateKeyError(err) {
			return ErrDuplicateAgentID
		}
		return err
	}
	return nil
}

func (r *AgentRepo) GetByAgentID(ctx context.Context, agentID string) (*model.Agent, error) {
	var a model.Agent
	err := r.db.WithContext(ctx).
		Where("agent_id = ? AND status != ?", agentID, model.AgentStatusDraining).
		First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAgentNotFound
	}
	return &a, err
}

// GetActiveByAgentID 只返回 Active 状态的 Agent（路由专用）
func (r *AgentRepo) GetActiveByAgentID(ctx context.Context, agentID string) (*model.Agent, error) {
	var a model.Agent
	err := r.db.WithContext(ctx).
		Where("agent_id = ? AND status = ?", agentID, model.AgentStatusActive).
		First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAgentNotFound
	}
	return &a, err
}

func (r *AgentRepo) List(ctx context.Context, f AgentFilter) ([]*model.Agent, int64, error) {
	q := r.db.WithContext(ctx).Where("status = ?", model.AgentStatusActive)

	if f.OwnerAppID != "" {
		q = q.Where("owner_app_id = ?", f.OwnerAppID)
	}
	if f.Keyword != "" {
		like := "%" + f.Keyword + "%"
		q = q.Where("name LIKE ? OR description LIKE ?", like, like)
	}
	if f.Tag != "" {
		// 关联 agent_skills 过滤 tag
		q = q.Where("agent_id IN (SELECT agent_id FROM agent_skills WHERE JSON_CONTAINS(tags, ?))",
			`"`+f.Tag+`"`)
	}

	var total int64
	if err := q.Model(&model.Agent{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if f.Page > 0 && f.PageSize > 0 {
		q = q.Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize)
	}

	var list []*model.Agent
	return list, total, q.Order("created_at DESC").Find(&list).Error
}

func (r *AgentRepo) UpdateStatus(ctx context.Context, agentID string, status model.AgentStatus) error {
	result := r.db.WithContext(ctx).
		Model(&model.Agent{}).
		Where("agent_id = ?", agentID).
		Update("status", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrAgentNotFound
	}
	return nil
}

func (r *AgentRepo) UpdateHeartbeat(ctx context.Context, agentID string) error {
	now := time.Now()
	// Active 或 Inactive 都接受心跳；Inactive 时同步恢复为 Active
	result := r.db.WithContext(ctx).
		Model(&model.Agent{}).
		Where("agent_id = ? AND status IN ?", agentID,
			[]model.AgentStatus{model.AgentStatusActive, model.AgentStatusInactive}).
		Updates(map[string]any{
			"last_heartbeat_at": now,
			"status":            model.AgentStatusActive,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// UpdateAgentCard 更新 AgentCard JSON 及能力标志位（重新注册时使用）
func (r *AgentRepo) UpdateAgentCard(ctx context.Context, agentID string, updates map[string]any) error {
	result := r.db.WithContext(ctx).
		Model(&model.Agent{}).
		Where("agent_id = ?", agentID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// Delete 软删除（注销）
func (r *AgentRepo) Delete(ctx context.Context, agentID string) error {
	result := r.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Delete(&model.Agent{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// ListStaleAgents 返回心跳超时的 Active Agent（健康检查专用）
func (r *AgentRepo) ListStaleAgents(ctx context.Context, before time.Time) ([]*model.Agent, error) {
	var list []*model.Agent
	err := r.db.WithContext(ctx).
		Where("status = ? AND (last_heartbeat_at IS NULL OR last_heartbeat_at < ?)",
			model.AgentStatusActive, before).
		Find(&list).Error
	return list, err
}

// --- AgentSkill ---

type AgentSkillRepo struct {
	db *gorm.DB
}

func NewAgentSkillRepo(db *gorm.DB) *AgentSkillRepo {
	return &AgentSkillRepo{db: db}
}

func (r *AgentSkillRepo) ReplaceByAgentID(ctx context.Context, agentID string, skills []*model.AgentSkill) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("agent_id = ?", agentID).Delete(&model.AgentSkill{}).Error; err != nil {
			return err
		}
		if len(skills) == 0 {
			return nil
		}
		return tx.Create(&skills).Error
	})
}

func (r *AgentSkillRepo) ListByAgentID(ctx context.Context, agentID string) ([]*model.AgentSkill, error) {
	var list []*model.AgentSkill
	return list, r.db.WithContext(ctx).Where("agent_id = ?", agentID).Find(&list).Error
}

func (r *AgentSkillRepo) GetByAgentIDAndSkillID(ctx context.Context, agentID, skillID string) (*model.AgentSkill, error) {
	var skill model.AgentSkill
	err := r.db.WithContext(ctx).
		Where("agent_id = ? AND skill_id = ?", agentID, skillID).
		First(&skill).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAgentSkillNotFound
	}
	return &skill, err
}
