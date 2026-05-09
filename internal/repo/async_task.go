package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"agent-gateway/internal/model"

	"github.com/redis/go-redis/v9"
)

const (
	asyncTaskKeyPrefix     = "async:task:"
	asyncTaskTTL           = 48 * time.Hour
	asyncTaskListKey       = "async:task:pending"
	asyncTaskProcessingKey = "async:task:processing"
	asyncTaskRetryKey      = "async:task:retry"
	maxRetries             = 3
)

var (
	// ErrTaskStatusMismatch 任务状态不符合预期（被其他实例抢占）
	ErrTaskStatusMismatch = errors.New("task status mismatch")
	// ErrTaskNotFound 任务不存在
	ErrTaskNotFound = errors.New("task not found")
)

var claimPendingTaskScript = redis.NewScript(`
local taskID = redis.call('SPOP', KEYS[1])
if not taskID then
	return false
end
redis.call('ZADD', KEYS[2], ARGV[1], taskID)
return taskID
`)

var claimDelayedTasksScript = redis.NewScript(`
local ids = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
if #ids == 0 then
	return {}
end
redis.call('ZREM', KEYS[1], unpack(ids))
for i = 1, #ids do
	redis.call('ZADD', KEYS[2], ARGV[3], ids[i])
end
return ids
`)

// AsyncTaskRepo 异步任务 Redis 仓储
type AsyncTaskRepo struct {
	rdb *redis.Client
}

func NewAsyncTaskRepo(rdb *redis.Client) *AsyncTaskRepo {
	return &AsyncTaskRepo{rdb: rdb}
}

// Create 创建异步任务（写入 Redis）
func (r *AsyncTaskRepo) Create(ctx context.Context, t *model.AsyncTask) error {
	key := asyncTaskKeyPrefix + t.TaskID
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}

	// 写入任务数据 + 添加到 pending 集合
	pipe := r.rdb.Pipeline()
	pipe.Set(ctx, key, data, asyncTaskTTL)
	pipe.SAdd(ctx, asyncTaskListKey, t.TaskID)
	_, err = pipe.Exec(ctx)
	return err
}

// SetTaskCache only writes the task detail key. Reliable async mode uses it as
// a read-through cache while keeping MySQL as the source of truth.
func (r *AsyncTaskRepo) SetTaskCache(ctx context.Context, t *model.AsyncTask) error {
	key := asyncTaskKeyPrefix + t.TaskID
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return r.rdb.Set(ctx, key, data, asyncTaskTTL).Err()
}

// GetByTaskID 根据 task_id 查询任务
func (r *AsyncTaskRepo) GetByTaskID(ctx context.Context, taskID string) (*model.AsyncTask, error) {
	key := asyncTaskKeyPrefix + taskID
	data, err := r.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}

	var t model.AsyncTask
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	// 兼容旧格式：capability_id → agent_id（48h TTL 后可删除此 shim）
	if t.AgentID == "" {
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) == nil {
			if v, ok := raw["capability_id"]; ok {
				_ = json.Unmarshal(v, &t.AgentID)
			}
		}
	}
	return &t, nil
}

// UpdateStatus 更新任务状态
func (r *AsyncTaskRepo) UpdateStatus(ctx context.Context, taskID string, status model.AsyncTaskStatus, output []byte, errMsg string) error {
	t, err := r.GetByTaskID(ctx, taskID)
	if err != nil {
		return err
	}

	t.Status = status
	t.ErrorMsg = errMsg
	t.UpdatedAt = time.Now()
	if output != nil {
		t.Output = output
	}

	key := asyncTaskKeyPrefix + taskID
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}

	// 如果任务完成/失败，从 pending 集合移除
	pipe := r.rdb.Pipeline()
	pipe.Set(ctx, key, data, asyncTaskTTL)
	if status == model.AsyncTaskStatusCompleted || status == model.AsyncTaskStatusFailed {
		pipe.SRem(ctx, asyncTaskListKey, taskID)
		pipe.ZRem(ctx, asyncTaskProcessingKey, taskID)
		pipe.ZRem(ctx, asyncTaskRetryKey, taskID)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// UpdateStatusCAS CAS 更新任务状态（使用 Redis WATCH + MULTI）
func (r *AsyncTaskRepo) UpdateStatusCAS(ctx context.Context, taskID string,
	fromStatus, toStatus model.AsyncTaskStatus, output []byte, errMsg string) (bool, error) {

	key := asyncTaskKeyPrefix + taskID

	for i := 0; i < 3; i++ { // 最多重试 3 次
		err := r.rdb.Watch(ctx, func(tx *redis.Tx) error {
			// 读取当前值
			data, err := tx.Get(ctx, key).Bytes()
			if err != nil {
				return err
			}

			var t model.AsyncTask
			if err := json.Unmarshal(data, &t); err != nil {
				return err
			}

			// 检查状态是否符合预期
			if t.Status != fromStatus {
				return fmt.Errorf("%w: expected %s, got %s", ErrTaskStatusMismatch, fromStatus, t.Status)
			}

			// 更新状态
			t.Status = toStatus
			t.ErrorMsg = errMsg
			t.UpdatedAt = time.Now()
			if output != nil {
				t.Output = output
			}

			newData, err := json.Marshal(t)
			if err != nil {
				return err
			}

			// 事务执行
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, newData, asyncTaskTTL)
				if toStatus == model.AsyncTaskStatusCompleted || toStatus == model.AsyncTaskStatusFailed {
					pipe.SRem(ctx, asyncTaskListKey, taskID)
					pipe.ZRem(ctx, asyncTaskProcessingKey, taskID)
					pipe.ZRem(ctx, asyncTaskRetryKey, taskID)
				}
				return nil
			})
			return err
		}, key)

		if err == nil {
			return true, nil
		}

		// 如果是状态不匹配，返回 false（被其他实例抢占）
		if errors.Is(err, ErrTaskStatusMismatch) {
			return false, nil
		}

		// 其他错误重试
		if err != redis.Nil {
			continue
		}

		return false, err
	}

	return false, nil
}

// PopPendingTask 从 pending 集合领取一个任务，并记录到 processing 集合。
func (r *AsyncTaskRepo) PopPendingTask(ctx context.Context) (*model.AsyncTask, error) {
	result, err := claimPendingTaskScript.Run(ctx, r.rdb, []string{asyncTaskListKey, asyncTaskProcessingKey}, time.Now().UnixMilli()).Result()
	if err == redis.Nil || result == nil || result == false {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	taskID := fmt.Sprint(result)
	task, err := r.GetByTaskID(ctx, taskID)
	if errors.Is(err, ErrTaskNotFound) {
		return nil, r.clearTaskMarkers(ctx, taskID)
	}
	return task, err
}

// Delete 删除任务（用于清理）
func (r *AsyncTaskRepo) Delete(ctx context.Context, taskID string) error {
	key := asyncTaskKeyPrefix + taskID
	pipe := r.rdb.Pipeline()
	pipe.Del(ctx, key)
	pipe.SRem(ctx, asyncTaskListKey, taskID)
	pipe.ZRem(ctx, asyncTaskProcessingKey, taskID)
	pipe.ZRem(ctx, asyncTaskRetryKey, taskID)
	_, err := pipe.Exec(ctx)
	return err
}

// ScanTasks 扫描所有任务 key 并执行回调
func (r *AsyncTaskRepo) ScanTasks(ctx context.Context, pattern string, fn func(*model.AsyncTask)) {
	cursor := uint64(0)
	for {
		keys, nextCursor, err := r.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return
		}

		for _, key := range keys {
			data, err := r.rdb.Get(ctx, key).Bytes()
			if err != nil {
				continue
			}

			var task model.AsyncTask
			if err := json.Unmarshal(data, &task); err != nil {
				continue
			}
			fn(&task)
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
}

// AddToPendingSet 添加 task_id 到 pending 集合
func (r *AsyncTaskRepo) AddToPendingSet(ctx context.Context, taskID string) {
	r.rdb.SAdd(ctx, asyncTaskListKey, taskID)
}

// MaxRetries 返回最大重试次数
func (r *AsyncTaskRepo) MaxRetries() int { return maxRetries }

// EnqueueDelayed 将任务加入延迟重试队列（Sorted Set，score = 执行时间戳）
func (r *AsyncTaskRepo) EnqueueDelayed(ctx context.Context, task *model.AsyncTask, delay time.Duration) error {
	task.Status = model.AsyncTaskStatusPending
	task.ErrorMsg = ""
	task.UpdatedAt = time.Now()
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	pipe := r.rdb.Pipeline()
	pipe.Set(ctx, asyncTaskKeyPrefix+task.TaskID, data, asyncTaskTTL)
	pipe.SRem(ctx, asyncTaskListKey, task.TaskID)
	pipe.ZRem(ctx, asyncTaskProcessingKey, task.TaskID)
	pipe.ZAdd(ctx, asyncTaskRetryKey, redis.Z{
		Score:  float64(time.Now().Add(delay).UnixMilli()),
		Member: task.TaskID,
	})
	_, err = pipe.Exec(ctx)
	return err
}

// PopDelayedTasks 领取到期的延迟重试任务，并记录到 processing 集合。
func (r *AsyncTaskRepo) PopDelayedTasks(ctx context.Context, limit int) ([]*model.AsyncTask, error) {
	if limit <= 0 {
		return nil, nil
	}
	now := time.Now()
	result, err := claimDelayedTasksScript.Run(
		ctx,
		r.rdb,
		[]string{asyncTaskRetryKey, asyncTaskProcessingKey},
		now.UnixMilli(),
		limit,
		now.UnixMilli(),
	).Result()
	if err != nil {
		return nil, err
	}
	ids, ok := result.([]interface{})
	if !ok || len(ids) == 0 {
		return nil, nil
	}

	tasks := make([]*model.AsyncTask, 0, len(ids))
	for _, rawID := range ids {
		taskID := fmt.Sprint(rawID)
		t, err := r.GetByTaskID(ctx, taskID)
		if errors.Is(err, ErrTaskNotFound) {
			_ = r.clearTaskMarkers(ctx, taskID)
			continue
		}
		if err != nil {
			return nil, err
		}
		t.Status = model.AsyncTaskStatusPending
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (r *AsyncTaskRepo) ListAllClaimedTaskIDs(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	return r.rdb.ZRange(ctx, asyncTaskProcessingKey, 0, int64(limit-1)).Result()
}

func (r *AsyncTaskRepo) ListStaleClaimedTaskIDs(ctx context.Context, before time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	return r.rdb.ZRangeByScore(ctx, asyncTaskProcessingKey, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   strconv.FormatInt(before.UnixMilli(), 10),
		Count: int64(limit),
	}).Result()
}

func (r *AsyncTaskRepo) RefreshClaimedTask(ctx context.Context, taskID string) error {
	return r.rdb.ZAdd(ctx, asyncTaskProcessingKey, redis.Z{
		Score:  float64(time.Now().UnixMilli()),
		Member: taskID,
	}).Err()
}

func (r *AsyncTaskRepo) RecoverClaimedTask(ctx context.Context, taskID string) error {
	task, err := r.GetByTaskID(ctx, taskID)
	if errors.Is(err, ErrTaskNotFound) {
		return r.clearTaskMarkers(ctx, taskID)
	}
	if err != nil {
		return err
	}
	if task.Status == model.AsyncTaskStatusCompleted || task.Status == model.AsyncTaskStatusFailed {
		return r.clearProcessingTask(ctx, taskID)
	}

	task.Status = model.AsyncTaskStatusPending
	task.ErrorMsg = ""
	task.UpdatedAt = time.Now()

	data, err := json.Marshal(task)
	if err != nil {
		return err
	}

	pipe := r.rdb.Pipeline()
	pipe.Set(ctx, asyncTaskKeyPrefix+taskID, data, asyncTaskTTL)
	pipe.SAdd(ctx, asyncTaskListKey, taskID)
	pipe.ZRem(ctx, asyncTaskProcessingKey, taskID)
	pipe.ZRem(ctx, asyncTaskRetryKey, taskID)
	_, err = pipe.Exec(ctx)
	return err
}

func (r *AsyncTaskRepo) clearProcessingTask(ctx context.Context, taskID string) error {
	pipe := r.rdb.Pipeline()
	pipe.ZRem(ctx, asyncTaskProcessingKey, taskID)
	pipe.ZRem(ctx, asyncTaskRetryKey, taskID)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *AsyncTaskRepo) clearTaskMarkers(ctx context.Context, taskID string) error {
	pipe := r.rdb.Pipeline()
	pipe.SRem(ctx, asyncTaskListKey, taskID)
	pipe.ZRem(ctx, asyncTaskProcessingKey, taskID)
	pipe.ZRem(ctx, asyncTaskRetryKey, taskID)
	_, err := pipe.Exec(ctx)
	return err
}
