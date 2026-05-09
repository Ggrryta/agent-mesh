package repo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"agent-gateway/internal/model"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type ReliableAsyncTaskRepo struct {
	db *gorm.DB
}

func NewReliableAsyncTaskRepo(db *gorm.DB) *ReliableAsyncTaskRepo {
	return &ReliableAsyncTaskRepo{db: db}
}

func (r *ReliableAsyncTaskRepo) Create(ctx context.Context, t *model.AsyncTask) error {
	now := time.Now()
	t.Status = model.AsyncTaskStatusPending
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now

	payload, err := json.Marshal(map[string]any{
		"task_id":    t.TaskID,
		"agent_id":   t.AgentID,
		"skill_id":   t.SkillID,
		"app_id":     t.AppID,
		"event_type": model.OutboxEventAsyncTaskCreated,
		"created_at": now,
	})
	if err != nil {
		return err
	}

	rec := &model.ReliableAsyncTask{
		TaskID:    t.TaskID,
		AgentID:   t.AgentID,
		SkillID:   t.SkillID,
		AppID:     t.AppID,
		Input:     datatypes.JSON(t.Input),
		Status:    string(t.Status),
		Retries:   t.Retries,
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
	}
	event := &model.OutboxEvent{
		EventID:       uuid.NewString(),
		AggregateType: model.OutboxAggregateAsyncTask,
		AggregateID:   t.TaskID,
		EventType:     model.OutboxEventAsyncTaskCreated,
		Payload:       datatypes.JSON(payload),
		Status:        model.OutboxStatusPending,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(rec).Error; err != nil {
			return err
		}
		return tx.Create(event).Error
	})
}

func (r *ReliableAsyncTaskRepo) GetByTaskID(ctx context.Context, taskID string) (*model.AsyncTask, error) {
	var rec model.ReliableAsyncTask
	err := r.db.WithContext(ctx).Where("task_id = ?", taskID).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return reliableRecordToTask(&rec), nil
}

func (r *ReliableAsyncTaskRepo) Claim(ctx context.Context, taskID string) (bool, error) {
	now := time.Now()
	result := r.db.WithContext(ctx).Model(&model.ReliableAsyncTask{}).
		Where("task_id = ? AND status IN ? AND (next_run_at IS NULL OR next_run_at <= ?)", taskID, []string{
			string(model.AsyncTaskStatusPending),
			string(model.AsyncTaskStatusRetrying),
		}, now).
		Updates(map[string]any{
			"status":     string(model.AsyncTaskStatusRunning),
			"version":    gorm.Expr("version + 1"),
			"updated_at": now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (r *ReliableAsyncTaskRepo) ListRunnable(ctx context.Context, limit int) ([]*model.AsyncTask, error) {
	if limit <= 0 {
		limit = 100
	}
	now := time.Now()
	var records []model.ReliableAsyncTask
	if err := r.db.WithContext(ctx).
		Where("status = ? OR (status = ? AND (next_run_at IS NULL OR next_run_at <= ?))",
			string(model.AsyncTaskStatusPending),
			string(model.AsyncTaskStatusRetrying),
			now,
		).
		Order("updated_at ASC").
		Limit(limit).
		Find(&records).Error; err != nil {
		return nil, err
	}
	tasks := make([]*model.AsyncTask, 0, len(records))
	for i := range records {
		tasks = append(tasks, reliableRecordToTask(&records[i]))
	}
	return tasks, nil
}

func (r *ReliableAsyncTaskRepo) Complete(ctx context.Context, taskID string, output []byte) error {
	return r.db.WithContext(ctx).Model(&model.ReliableAsyncTask{}).
		Where("task_id = ?", taskID).
		Updates(map[string]any{
			"status":      string(model.AsyncTaskStatusCompleted),
			"output":      datatypes.JSON(output),
			"error_msg":   "",
			"next_run_at": nil,
			"updated_at":  time.Now(),
		}).Error
}

func (r *ReliableAsyncTaskRepo) Retry(ctx context.Context, taskID string, retries int, errMsg string, delay time.Duration) error {
	nextRunAt := time.Now().Add(delay)
	payload, err := json.Marshal(map[string]any{
		"task_id":     taskID,
		"event_type":  model.OutboxEventAsyncTaskRetry,
		"retry_count": retries,
		"next_run_at": nextRunAt,
		"created_at":  time.Now(),
	})
	if err != nil {
		return err
	}

	event := &model.OutboxEvent{
		EventID:       uuid.NewString(),
		AggregateType: model.OutboxAggregateAsyncTask,
		AggregateID:   taskID,
		EventType:     model.OutboxEventAsyncTaskRetry,
		Payload:       datatypes.JSON(payload),
		Status:        model.OutboxStatusPending,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ReliableAsyncTask{}).
			Where("task_id = ?", taskID).
			Updates(map[string]any{
				"status":      string(model.AsyncTaskStatusRetrying),
				"error_msg":   errMsg,
				"retries":     retries,
				"next_run_at": nextRunAt,
				"updated_at":  time.Now(),
			}).Error; err != nil {
			return err
		}
		return tx.Create(event).Error
	})
}

func (r *ReliableAsyncTaskRepo) Fail(ctx context.Context, taskID string, errMsg string) error {
	return r.db.WithContext(ctx).Model(&model.ReliableAsyncTask{}).
		Where("task_id = ?", taskID).
		Updates(map[string]any{
			"status":      string(model.AsyncTaskStatusFailed),
			"error_msg":   errMsg,
			"next_run_at": nil,
			"updated_at":  time.Now(),
		}).Error
}

func reliableRecordToTask(rec *model.ReliableAsyncTask) *model.AsyncTask {
	return &model.AsyncTask{
		TaskID:    rec.TaskID,
		AgentID:   rec.AgentID,
		SkillID:   rec.SkillID,
		AppID:     rec.AppID,
		Input:     json.RawMessage(rec.Input),
		Output:    json.RawMessage(rec.Output),
		Status:    model.AsyncTaskStatus(rec.Status),
		ErrorMsg:  rec.ErrorMsg,
		Retries:   rec.Retries,
		CreatedAt: rec.CreatedAt,
		UpdatedAt: rec.UpdatedAt,
	}
}
