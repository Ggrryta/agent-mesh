package service

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
)

const processingHeartbeatInterval = time.Minute

type TaskWorker struct {
	taskRepo   *repo.AsyncTaskRepo
	agentCache *AgentCache
	invoker    *A2AInvoker
	callGuard  *AgentCallGuard
	workers    int
	done       chan struct{}
}

func NewTaskWorker(taskRepo *repo.AsyncTaskRepo, agentCache *AgentCache, invoker *A2AInvoker, guards ...*AgentCallGuard) *TaskWorker {
	workers := runtime.NumCPU() * 2
	if workers < 4 {
		workers = 4
	}
	if workers > 16 {
		workers = 16
	}
	var guard *AgentCallGuard
	if len(guards) > 0 {
		guard = guards[0]
	}
	return &TaskWorker{
		taskRepo:   taskRepo,
		agentCache: agentCache,
		invoker:    invoker,
		callGuard:  guard,
		workers:    workers,
		done:       make(chan struct{}),
	}
}

func (w *TaskWorker) Start() {
	sem := make(chan struct{}, w.workers)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("task worker goroutine panic", zap.Any("recover", r))
			}
		}()

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-w.done:
				return
			case <-ticker.C:
				w.pollAndExecute(sem)
			}
		}
	}()
}

func (w *TaskWorker) Stop() {
	close(w.done)
}

func (w *TaskWorker) pollAndExecute(sem chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	available := cap(sem) - len(sem)
	if available <= 0 {
		return
	}

	// 优先领取到期重试任务，但不超过当前空闲 worker 数。
	delayed, err := w.taskRepo.PopDelayedTasks(ctx, available)
	if err != nil {
		logger.Error("task worker: pop delayed tasks failed", zap.Error(err))
		return
	}
	for _, t := range delayed {
		sem <- struct{}{}
		go func(t *model.AsyncTask) {
			defer func() { <-sem }()
			w.executeTask(t)
		}(t)
	}
	available -= len(delayed)
	if available <= 0 {
		return
	}

	// 再按剩余容量尽量多领取 pending 任务，避免吞吐被 1 task/s 限死。
	for i := 0; i < available; i++ {
		task, err := w.taskRepo.PopPendingTask(ctx)
		if err != nil {
			logger.Error("task worker: pop pending task failed", zap.Error(err))
			return
		}
		if task == nil {
			return
		}

		sem <- struct{}{}
		go func(t *model.AsyncTask) {
			defer func() { <-sem }()
			w.executeTask(t)
		}(task)
	}
}

func (w *TaskWorker) executeTask(task *model.AsyncTask) {
	ctx := context.Background()

	ok, err := w.taskRepo.UpdateStatusCAS(ctx, task.TaskID,
		model.AsyncTaskStatusPending, model.AsyncTaskStatusRunning, nil, "")
	if err != nil || !ok {
		return
	}
	stopHeartbeat := w.startProcessingHeartbeat(task.TaskID)
	defer stopHeartbeat()

	startTime := time.Now()
	output, execErr := w.doInvoke(ctx, task)
	duration := time.Since(startTime)

	if execErr != nil {
		logger.Error("task worker: execute failed",
			zap.String("task_id", task.TaskID),
			zap.Duration("duration", duration),
			zap.Error(execErr))

		task.Retries++
		if task.Retries < w.taskRepo.MaxRetries() {
			delay := time.Duration(task.Retries) * 10 * time.Second
			if err := w.taskRepo.EnqueueDelayed(ctx, task, delay); err != nil {
				logger.Error("task worker: enqueue delayed failed", zap.String("task_id", task.TaskID), zap.Error(err))
			}
			logger.Info("task worker: scheduled retry",
				zap.String("task_id", task.TaskID),
				zap.Int("retry", task.Retries),
				zap.Duration("delay", delay))
		} else {
			if err := w.taskRepo.UpdateStatus(ctx, task.TaskID, model.AsyncTaskStatusFailed, nil, execErr.Error()); err != nil {
				logger.Error("task worker: update status failed", zap.String("task_id", task.TaskID), zap.Error(err))
			}
		}
		return
	}

	logger.Info("task worker: execute completed",
		zap.String("task_id", task.TaskID),
		zap.Int("output_len", len(output)),
		zap.Duration("duration", duration))

	if err := w.taskRepo.UpdateStatus(ctx, task.TaskID, model.AsyncTaskStatusCompleted, output, ""); err != nil {
		logger.Error("task worker: update status failed", zap.String("task_id", task.TaskID), zap.Error(err))
	}
}

func (w *TaskWorker) startProcessingHeartbeat(taskID string) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(processingHeartbeatInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				hbCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := w.taskRepo.RefreshClaimedTask(hbCtx, taskID)
				cancel()
				if err != nil {
					logger.Warn("task worker: refresh claimed task failed",
						zap.String("task_id", taskID),
						zap.Error(err))
				}
			}
		}
	}()
	return func() {
		close(done)
	}
}

func (w *TaskWorker) doInvoke(ctx context.Context, task *model.AsyncTask) ([]byte, error) {
	agent, ok := w.agentCache.Get(task.AgentID)
	if !ok {
		return nil, fmt.Errorf("agent not found: %s", task.AgentID)
	}
	if agent.Status != model.AgentStatusActive {
		return nil, fmt.Errorf("agent not active: %s", task.AgentID)
	}

	if w.callGuard != nil {
		result, err := w.callGuard.Execute(task.AgentID, func() (json.RawMessage, error) {
			return w.invoker.Send(ctx, agent.URL, task.Input, task.SkillID)
		})
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	result, err := w.invoker.Send(ctx, agent.URL, task.Input, task.SkillID)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ExecuteAsyncTask 供 TaskCleaner 调用，恢复僵尸任务时直接执行
func (w *TaskWorker) ExecuteAsyncTask(ctx context.Context, task *model.AsyncTask) {
	output, err := w.doInvoke(ctx, task)
	if err != nil {
		if updateErr := w.taskRepo.UpdateStatus(ctx, task.TaskID, model.AsyncTaskStatusFailed, nil, err.Error()); updateErr != nil {
			logger.Error("ExecuteAsyncTask: update status failed", zap.String("task_id", task.TaskID), zap.Error(updateErr))
		}
		return
	}
	if updateErr := w.taskRepo.UpdateStatus(ctx, task.TaskID, model.AsyncTaskStatusCompleted, output, ""); updateErr != nil {
		logger.Error("ExecuteAsyncTask: update status failed", zap.String("task_id", task.TaskID), zap.Error(updateErr))
	}
}
