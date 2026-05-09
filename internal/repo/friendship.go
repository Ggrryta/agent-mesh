package repo

import (
	"context"
	"errors"
	"time"

	"agent-gateway/internal/model"

	"gorm.io/gorm"
)

var (
	ErrFriendshipNotFound  = errors.New("friendship not found")
	ErrFriendshipDuplicate = errors.New("friendship already exists")
	ErrFriendshipSelf      = errors.New("cannot friend yourself")
	ErrFriendshipBadState  = errors.New("friendship in incompatible state")
)

// FriendshipRepo Agent 好友关系管理
// 所有查询/写入都经过 model.NormalizePair 规范化 agent_a_id < agent_b_id
type FriendshipRepo struct {
	db *gorm.DB
}

func NewFriendshipRepo(db *gorm.DB) *FriendshipRepo {
	return &FriendshipRepo{db: db}
}

// Request 发起加好友请求。
// 幂等:若已存在 accepted 关系直接返回已有记录; 若有 pending 则返回该记录; 若有 rejected/revoked 则覆盖为新的 pending。
func (r *FriendshipRepo) Request(ctx context.Context, initiatorID, targetID, reason string) (*model.Friendship, error) {
	if initiatorID == targetID {
		return nil, ErrFriendshipSelf
	}
	a, b := model.NormalizePair(initiatorID, targetID)

	var f model.Friendship
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("agent_a_id = ? AND agent_b_id = ?", a, b).First(&f)
		if res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
			return res.Error
		}
		if errors.Is(res.Error, gorm.ErrRecordNotFound) {
			f = model.Friendship{
				AgentAID:    a,
				AgentBID:    b,
				Status:      model.FriendshipStatusPending,
				InitiatorID: initiatorID,
				Reason:      reason,
			}
			return tx.Create(&f).Error
		}
		// 已存在,按状态处理
		switch f.Status {
		case model.FriendshipStatusAccepted:
			return nil // 已是好友,幂等返回
		case model.FriendshipStatusPending:
			return nil // 已有 pending 请求
		case model.FriendshipStatusRejected, model.FriendshipStatusRevoked:
			// 允许重新发起
			return tx.Model(&f).Updates(map[string]any{
				"status":       model.FriendshipStatusPending,
				"initiator_id": initiatorID,
				"reason":       reason,
				"accepted_at":  nil,
			}).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// Accept 接受加好友请求。只有被请求方才能接受(即 initiator_id != self)
func (r *FriendshipRepo) Accept(ctx context.Context, id int64, self string) error {
	now := time.Now()
	res := r.db.WithContext(ctx).Model(&model.Friendship{}).
		Where("id = ? AND status = ? AND initiator_id != ?",
			id, model.FriendshipStatusPending, self).
		Where("(agent_a_id = ? OR agent_b_id = ?)", self, self).
		Updates(map[string]any{
			"status":      model.FriendshipStatusAccepted,
			"accepted_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrFriendshipNotFound
	}
	return nil
}

// Reject 拒绝加好友请求。同 Accept,只有被请求方能拒绝
func (r *FriendshipRepo) Reject(ctx context.Context, id int64, self string) error {
	res := r.db.WithContext(ctx).Model(&model.Friendship{}).
		Where("id = ? AND status = ? AND initiator_id != ?",
			id, model.FriendshipStatusPending, self).
		Where("(agent_a_id = ? OR agent_b_id = ?)", self, self).
		Update("status", model.FriendshipStatusRejected)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrFriendshipNotFound
	}
	return nil
}

// Revoke 解除好友关系。双方都能撤销已建立的好友
func (r *FriendshipRepo) Revoke(ctx context.Context, id int64, self string) error {
	res := r.db.WithContext(ctx).Model(&model.Friendship{}).
		Where("id = ? AND status = ?", id, model.FriendshipStatusAccepted).
		Where("(agent_a_id = ? OR agent_b_id = ?)", self, self).
		Update("status", model.FriendshipStatusRevoked)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrFriendshipNotFound
	}
	return nil
}

// IsFriend 检查两个 agent 是否已是好友(status=accepted)
// 这个方法会被 dispatcher 中间件频繁调用,需要快速返回
func (r *FriendshipRepo) IsFriend(ctx context.Context, a, b string) (bool, error) {
	if a == b {
		return false, nil
	}
	x, y := model.NormalizePair(a, b)
	var count int64
	err := r.db.WithContext(ctx).Model(&model.Friendship{}).
		Where("agent_a_id = ? AND agent_b_id = ? AND status = ?",
			x, y, model.FriendshipStatusAccepted).
		Count(&count).Error
	return count > 0, err
}

// ListFriends 查询某 agent 的好友列表(status=accepted)
func (r *FriendshipRepo) ListFriends(ctx context.Context, agentID string) ([]model.Friendship, error) {
	var list []model.Friendship
	err := r.db.WithContext(ctx).
		Where("(agent_a_id = ? OR agent_b_id = ?) AND status = ?",
			agentID, agentID, model.FriendshipStatusAccepted).
		Order("accepted_at DESC").
		Find(&list).Error
	return list, err
}

// ListPending 查询某 agent 收到的待处理加好友请求(initiator != self 且 status=pending)
func (r *FriendshipRepo) ListPending(ctx context.Context, agentID string) ([]model.Friendship, error) {
	var list []model.Friendship
	err := r.db.WithContext(ctx).
		Where("(agent_a_id = ? OR agent_b_id = ?) AND status = ? AND initiator_id != ?",
			agentID, agentID, model.FriendshipStatusPending, agentID).
		Order("created_at DESC").
		Find(&list).Error
	return list, err
}

// GetByID 按主键查询
func (r *FriendshipRepo) GetByID(ctx context.Context, id int64) (*model.Friendship, error) {
	var f model.Friendship
	if err := r.db.WithContext(ctx).First(&f, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrFriendshipNotFound
		}
		return nil, err
	}
	return &f, nil
}
