package repo

import (
	"context"
	"errors"

	"agent-gateway/internal/model"

	"gorm.io/gorm"
)

var ErrDuplicateAppID = errors.New("app_id already exists")

type ConsumerRepo struct {
	db *gorm.DB
}

func NewConsumerRepo(db *gorm.DB) *ConsumerRepo {
	return &ConsumerRepo{db: db}
}

func (r *ConsumerRepo) Create(ctx context.Context, c *model.Consumer) error {
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		if isDuplicateKeyError(err) {
			return ErrDuplicateAppID
		}
		return err
	}
	return nil
}

func (r *ConsumerRepo) GetByAppID(ctx context.Context, appID string) (*model.Consumer, error) {
	var c model.Consumer
	if err := r.db.WithContext(ctx).Where("app_id = ?", appID).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}
