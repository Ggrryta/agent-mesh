// Package repo 数据访问层
package repo

import (
	"context"

	"agent-gateway/internal/model"

	"gorm.io/gorm"
)

// ConfigRepo 配置版本仓储
type ConfigRepo struct {
	db *gorm.DB
}

// NewConfigRepo 创建配置仓储实例
func NewConfigRepo(db *gorm.DB) *ConfigRepo {
	return &ConfigRepo{db: db}
}

// GetLatest 获取指定类型的最新配置版本
func (r *ConfigRepo) GetLatest(ctx context.Context, configType model.ConfigType) (*model.ConfigVersion, error) {
	var cfg model.ConfigVersion
	err := r.db.WithContext(ctx).
		Where("config_type = ?", configType).
		Order("created_at DESC").
		First(&cfg).Error
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// GetByVersion 根据版本号获取配置
func (r *ConfigRepo) GetByVersion(ctx context.Context, version string) (*model.ConfigVersion, error) {
	var cfg model.ConfigVersion
	err := r.db.WithContext(ctx).
		Where("version = ?", version).
		First(&cfg).Error
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Create 创建新配置版本
func (r *ConfigRepo) Create(ctx context.Context, cfg *model.ConfigVersion) error {
	return r.db.WithContext(ctx).Create(cfg).Error
}

// ListByType 分页查询指定类型的配置历史
func (r *ConfigRepo) ListByType(ctx context.Context, configType model.ConfigType, limit, offset int) ([]model.ConfigVersion, int64, error) {
	var list []model.ConfigVersion
	var total int64

	// 查询总数
	if err := r.db.WithContext(ctx).Model(&model.ConfigVersion{}).
		Where("config_type = ?", configType).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	if err := r.db.WithContext(ctx).
		Where("config_type = ?", configType).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&list).Error; err != nil {
		return nil, 0, err
	}

	return list, total, nil
}
