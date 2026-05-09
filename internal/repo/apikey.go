package repo

import (
	"context"
	"errors"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/pkg/cache"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type APIKeyRepo struct {
	db    *gorm.DB
	cache *cache.TTLCache[string, *model.APIKey] // key: prefix → APIKey（含 hash）
}

func NewAPIKeyRepo(db *gorm.DB) *APIKeyRepo {
	return &APIKeyRepo{
		db:    db,
		cache: cache.New[string, *model.APIKey](time.Minute),
	}
}

// Upsert 创建或覆盖 API Key（一个 app_id 只保留一条）
func (r *APIKeyRepo) Upsert(ctx context.Context, appID, keyHash, keyPrefix string) error {
	key := &model.APIKey{
		AppID:     appID,
		KeyHash:   keyHash,
		KeyPrefix: keyPrefix,
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "app_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"key_hash", "key_prefix", "created_at", "last_used_at"}),
		}).
		Create(key).Error
}

// GetByAppID 查询账号的 Key 信息（不含 hash）
func (r *APIKeyRepo) GetByAppID(ctx context.Context, appID string) (*model.APIKey, error) {
	var key model.APIKey
	err := r.db.WithContext(ctx).
		Select("id, app_id, key_prefix, created_at, last_used_at").
		Where("app_id = ?", appID).
		First(&key).Error
	if err != nil {
		return nil, err
	}
	return &key, nil
}

// GetByPrefix 按前缀查询（含 hash，用于鉴权），结果缓存 1 分钟
func (r *APIKeyRepo) GetByPrefix(ctx context.Context, prefix string) (*model.APIKey, error) {
	if v, ok := r.cache.Get(prefix); ok {
		return v, nil
	}
	var key model.APIKey
	err := r.db.WithContext(ctx).
		Where("key_prefix = ?", prefix).
		First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.cache.Set(prefix, &key)
	return &key, nil
}

// DeleteByAppID 吊销 API Key，同时清除缓存
func (r *APIKeyRepo) DeleteByAppID(ctx context.Context, appID string) error {
	// 先查出 prefix 以便清缓存
	var key model.APIKey
	if err := r.db.WithContext(ctx).Select("key_prefix").Where("app_id = ?", appID).First(&key).Error; err == nil {
		r.cache.Delete(key.KeyPrefix)
	}
	result := r.db.WithContext(ctx).
		Where("app_id = ?", appID).
		Delete(&model.APIKey{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateLastUsed 异步更新最后使用时间
func (r *APIKeyRepo) UpdateLastUsed(ctx context.Context, appID string) {
	now := time.Now()
	r.db.WithContext(ctx).
		Model(&model.APIKey{}).
		Where("app_id = ?", appID).
		Update("last_used_at", now)
}
