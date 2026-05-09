package repo

import (
	"context"
	"errors"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/pkg/cache"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var (
	ErrAgentPermissionNotFound = errors.New("agent permission not found")
	ErrAgentApplyNotFound      = errors.New("agent apply not found")
	ErrAgentApplyDuplicate     = errors.New("agent apply already pending")
)

type permissionBlacklistStore interface {
	Exists(ctx context.Context, key string) (bool, error)
	Set(ctx context.Context, key string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

type redisPermissionBlacklist struct {
	rdb *redis.Client
}

func (s *redisPermissionBlacklist) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.rdb.Exists(ctx, key).Result()
	return n > 0, err
}

func (s *redisPermissionBlacklist) Set(ctx context.Context, key string, ttl time.Duration) error {
	return s.rdb.Set(ctx, key, "1", ttl).Err()
}

func (s *redisPermissionBlacklist) Delete(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, key).Err()
}

// AgentPermissionRepo Agent 调用权限管理
type AgentPermissionRepo struct {
	db        *gorm.DB
	blacklist permissionBlacklistStore
	cache     *cache.TTLCache[string, bool]
}

const revokeBlacklistTTL = 6 * time.Minute

func NewAgentPermissionRepo(db *gorm.DB, rdb *redis.Client) *AgentPermissionRepo {
	var blacklist permissionBlacklistStore
	if rdb != nil {
		blacklist = &redisPermissionBlacklist{rdb: rdb}
	}
	return &AgentPermissionRepo{
		db:        db,
		blacklist: blacklist,
		cache:     cache.New[string, bool](5 * time.Minute),
	}
}

func permCacheKey(agentID, consumerAppID string) string {
	return agentID + ":" + consumerAppID
}

func revokeBlacklistKey(agentID, consumerAppID string) string {
	return "perm:revoked:" + agentID + ":" + consumerAppID
}

func (r *AgentPermissionRepo) HasPermission(ctx context.Context, agentID, consumerAppID string) (bool, error) {
	key := permCacheKey(agentID, consumerAppID)

	if r.blacklist != nil {
		if blocked, err := r.blacklist.Exists(ctx, revokeBlacklistKey(agentID, consumerAppID)); err == nil && blocked {
			return false, nil
		}
	}

	if v, ok := r.cache.Get(key); ok {
		return v, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&model.AgentPermission{}).
		Where("agent_id = ? AND consumer_app_id = ?", agentID, consumerAppID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	has := count > 0
	r.cache.Set(key, has)
	return has, nil
}

func (r *AgentPermissionRepo) Grant(ctx context.Context, agentID, ownerAppID, consumerAppID string) error {
	perm := &model.AgentPermission{
		AgentID:       agentID,
		OwnerAppID:    ownerAppID,
		ConsumerAppID: consumerAppID,
		GrantedAt:     time.Now(),
	}
	err := r.db.WithContext(ctx).
		Where(model.AgentPermission{AgentID: agentID, ConsumerAppID: consumerAppID}).
		Assign(perm).
		FirstOrCreate(perm).Error
	if err == nil {
		r.cache.Delete(permCacheKey(agentID, consumerAppID))
		r.clearRevokeBlacklist(ctx, agentID, consumerAppID)
	}
	return err
}

func (r *AgentPermissionRepo) Revoke(ctx context.Context, agentID, consumerAppID string) error {
	result := r.db.WithContext(ctx).
		Where("agent_id = ? AND consumer_app_id = ?", agentID, consumerAppID).
		Delete(&model.AgentPermission{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrAgentPermissionNotFound
	}
	r.cache.Delete(permCacheKey(agentID, consumerAppID))
	if r.blacklist != nil {
		_ = r.blacklist.Set(ctx, revokeBlacklistKey(agentID, consumerAppID), revokeBlacklistTTL)
	}
	return nil
}

// AgentApplyRepo Agent 权限申请管理
type AgentApplyRepo struct {
	db        *gorm.DB
	blacklist permissionBlacklistStore
}

func NewAgentApplyRepo(db *gorm.DB, rdb *redis.Client) *AgentApplyRepo {
	var blacklist permissionBlacklistStore
	if rdb != nil {
		blacklist = &redisPermissionBlacklist{rdb: rdb}
	}
	return &AgentApplyRepo{db: db, blacklist: blacklist}
}

func (r *AgentApplyRepo) Create(ctx context.Context, a *model.AgentApply) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.AgentApply{}).
			Where("agent_id = ? AND applicant_app_id = ? AND status = ?",
				a.AgentID, a.ApplicantAppID, model.ApplyStatusPending).
			Set("gorm:query_option", "FOR UPDATE").
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrAgentApplyDuplicate
		}
		return tx.Create(a).Error
	})
}

func (r *AgentApplyRepo) GetByID(ctx context.Context, id int64) (*model.AgentApply, error) {
	var a model.AgentApply
	if err := r.db.WithContext(ctx).First(&a, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentApplyNotFound
		}
		return nil, err
	}
	return &a, nil
}

// Approve 事务内完成：更新申请状态 → 写入权限表
func (r *AgentApplyRepo) Approve(ctx context.Context, id int64, agentID, ownerAppID, consumerAppID string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.AgentApply{}).
			Where("id = ? AND status = ?", id, model.ApplyStatusPending).
			Updates(map[string]any{
				"status":      model.ApplyStatusApproved,
				"reviewed_at": time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrAgentApplyNotFound
		}
		perm := &model.AgentPermission{
			AgentID:       agentID,
			OwnerAppID:    ownerAppID,
			ConsumerAppID: consumerAppID,
			GrantedAt:     time.Now(),
		}
		return tx.Where(model.AgentPermission{AgentID: agentID, ConsumerAppID: consumerAppID}).
			Assign(perm).
			FirstOrCreate(perm).Error
	})
	if err == nil {
		r.clearRevokeBlacklist(ctx, agentID, consumerAppID)
	}
	return err
}

func (r *AgentApplyRepo) Reject(ctx context.Context, id int64) error {
	result := r.db.WithContext(ctx).Model(&model.AgentApply{}).
		Where("id = ? AND status = ?", id, model.ApplyStatusPending).
		Updates(map[string]any{
			"status":      model.ApplyStatusRejected,
			"reviewed_at": time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrAgentApplyNotFound
	}
	return nil
}

func (r *AgentApplyRepo) ListInbox(ctx context.Context, ownerAppID string, status *model.ApplyStatus) ([]model.AgentApply, error) {
	q := r.db.WithContext(ctx).Where("owner_app_id = ?", ownerAppID)
	if status != nil {
		q = q.Where("status = ?", *status)
	}
	var applies []model.AgentApply
	return applies, q.Order("created_at DESC").Find(&applies).Error
}

func (r *AgentApplyRepo) ListOutbox(ctx context.Context, applicantAppID string, status *model.ApplyStatus) ([]model.AgentApply, error) {
	q := r.db.WithContext(ctx).Where("applicant_app_id = ?", applicantAppID)
	if status != nil {
		q = q.Where("status = ?", *status)
	}
	var applies []model.AgentApply
	return applies, q.Order("created_at DESC").Find(&applies).Error
}

func (r *AgentPermissionRepo) clearRevokeBlacklist(ctx context.Context, agentID, consumerAppID string) {
	if r.blacklist == nil {
		return
	}
	_ = r.blacklist.Delete(ctx, revokeBlacklistKey(agentID, consumerAppID))
}

func (r *AgentApplyRepo) clearRevokeBlacklist(ctx context.Context, agentID, consumerAppID string) {
	if r.blacklist == nil {
		return
	}
	_ = r.blacklist.Delete(ctx, revokeBlacklistKey(agentID, consumerAppID))
}
