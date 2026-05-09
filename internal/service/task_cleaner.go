package service

import (
	"context"
	"time"

	"agent-gateway/internal/repo"
	"agent-gateway/pkg/logger"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

// TaskCleaner 定期恢复僵尸任务（Redis 版）
// Redis 任务有 TTL 自动过期，不需要清理逻辑
type TaskCleaner struct {
	c             *cron.Cron
	taskRepo      taskRecoveryRepo
	zombieTimeout time.Duration
	batchSize     int
}

type taskRecoveryRepo interface {
	ListAllClaimedTaskIDs(ctx context.Context, limit int) ([]string, error)
	ListStaleClaimedTaskIDs(ctx context.Context, before time.Time, limit int) ([]string, error)
	RecoverClaimedTask(ctx context.Context, taskID string) error
}

func NewTaskCleaner(taskRepo *repo.AsyncTaskRepo, _ *TaskWorker) *TaskCleaner {
	return &TaskCleaner{
		c:             cron.New(),
		taskRepo:      taskRepo,
		zombieTimeout: 30 * time.Minute,
		batchSize:     100,
	}
}

func (tc *TaskCleaner) WithZombieTimeout(timeout time.Duration) *TaskCleaner {
	tc.zombieTimeout = timeout
	return tc
}

// Start 启动定时任务
func (tc *TaskCleaner) Start() {
	// 启动时立即恢复所有已领取未确认的任务
	tc.recoverAllClaimedTasks()

	// 每分钟扫描 claim 超时的任务
	tc.c.AddFunc("* * * * *", tc.recoverZombieTasks)

	tc.c.Start()
	logger.Info("task cleaner started",
		zap.Duration("zombie_timeout", tc.zombieTimeout))
}

// recoverZombieTasks 恢复超时未确认的任务。
func (tc *TaskCleaner) recoverZombieTasks() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	staleBefore := time.Now().Add(-tc.zombieTimeout)
	totalRecovered := 0
	for {
		taskIDs, err := tc.taskRepo.ListStaleClaimedTaskIDs(ctx, staleBefore, tc.batchSize)
		if err != nil {
			logger.Error("task cleaner: list stale claimed tasks failed", zap.Error(err))
			return
		}
		if len(taskIDs) == 0 {
			break
		}

		logger.Warn("task cleaner: found stale claimed tasks", zap.Int("count", len(taskIDs)))
		recovered := tc.recoverTasks(ctx, taskIDs)
		totalRecovered += recovered
		if len(taskIDs) < tc.batchSize {
			break
		}
	}

	if totalRecovered > 0 {
		logger.Info("task cleaner: stale claimed tasks recovered", zap.Int("count", totalRecovered))
	}
}

// recoverAllClaimedTasks 恢复所有 processing 集合中的任务（网关启动时调用）。
func (tc *TaskCleaner) recoverAllClaimedTasks() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	totalRecovered := 0
	for {
		taskIDs, err := tc.taskRepo.ListAllClaimedTaskIDs(ctx, tc.batchSize)
		if err != nil {
			logger.Error("task cleaner: list claimed tasks on startup failed", zap.Error(err))
			return
		}
		if len(taskIDs) == 0 {
			if totalRecovered == 0 {
				logger.Info("task cleaner: no claimed tasks to recover")
			}
			break
		}

		logger.Warn("task cleaner: found claimed tasks on startup, recovering",
			zap.Int("count", len(taskIDs)))
		totalRecovered += tc.recoverTasks(ctx, taskIDs)
		if len(taskIDs) < tc.batchSize {
			break
		}
	}

	if totalRecovered > 0 {
		logger.Info("task cleaner: claimed tasks recovered on startup",
			zap.Int("count", totalRecovered))
	}
}

func (tc *TaskCleaner) recoverTasks(ctx context.Context, taskIDs []string) int {
	recovered := 0
	for _, taskID := range taskIDs {
		if err := tc.taskRepo.RecoverClaimedTask(ctx, taskID); err != nil {
			logger.Error("task cleaner: recover claimed task failed",
				zap.String("task_id", taskID),
				zap.Error(err))
			continue
		}
		recovered++
	}
	return recovered
}

func (tc *TaskCleaner) Stop() {
	tc.c.Stop()
}
