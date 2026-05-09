package service

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
)

const reliableTaskMaxRetries = 3

type reliableTaskCache interface {
	SetTaskCache(ctx context.Context, t *model.AsyncTask) error
}

type ReliableTaskWorker struct {
	reliableRepo *repo.ReliableAsyncTaskRepo
	cacheRepo    reliableTaskCache
	agentCache   *AgentCache
	invoker      *A2AInvoker
	callGuard    *AgentCallGuard
	events       <-chan TaskEvent
	workers      int
	done         chan struct{}
	stopOnce     sync.Once
}

func NewReliableTaskWorker(
	reliableRepo *repo.ReliableAsyncTaskRepo,
	cacheRepo reliableTaskCache,
	agentCache *AgentCache,
	invoker *A2AInvoker,
	callGuard *AgentCallGuard,
	events <-chan TaskEvent,
) *ReliableTaskWorker {
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > 8 {
		workers = 8
	}
	return &ReliableTaskWorker{
		reliableRepo: reliableRepo,
		cacheRepo:    cacheRepo,
		agentCache:   agentCache,
		invoker:      invoker,
		callGuard:    callGuard,
		events:       events,
		workers:      workers,
		done:         make(chan struct{}),
	}
}

func (w *ReliableTaskWorker) Start() {
	sem := make(chan struct{}, w.workers)
	go w.consumeEvents(sem)
	go w.scanRunnableTasks(sem)
	logger.Info("reliable task worker started", zap.Int("workers", w.workers))
}

func (w *ReliableTaskWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.done)
	})
}

func (w *ReliableTaskWorker) consumeEvents(sem chan struct{}) {
	for {
		select {
		case <-w.done:
			return
		case event := <-w.events:
			if event.TaskID == "" {
				continue
			}
			w.submit(sem, event.TaskID)
		}
	}
}

func (w *ReliableTaskWorker) scanRunnableTasks(sem chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			available := cap(sem) - len(sem)
			if available <= 0 {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			tasks, err := w.reliableRepo.ListRunnable(ctx, available)
			cancel()
			if err != nil {
				logger.Error("reliable task worker: list runnable tasks failed", zap.Error(err))
				continue
			}
			for _, task := range tasks {
				w.submit(sem, task.TaskID)
			}
		}
	}
}

func (w *ReliableTaskWorker) submit(sem chan struct{}, taskID string) {
	select {
	case sem <- struct{}{}:
		go func() {
			defer func() { <-sem }()
			w.executeTask(taskID)
		}()
	default:
	}
}

func (w *ReliableTaskWorker) executeTask(taskID string) {
	ctx := context.Background()
	claimed, err := w.reliableRepo.Claim(ctx, taskID)
	if err != nil {
		logger.Error("reliable task worker: claim failed", zap.String("task_id", taskID), zap.Error(err))
		return
	}
	if !claimed {
		return
	}

	task, err := w.reliableRepo.GetByTaskID(ctx, taskID)
	if err != nil {
		logger.Error("reliable task worker: get task failed", zap.String("task_id", taskID), zap.Error(err))
		return
	}
	task.Status = model.AsyncTaskStatusRunning

	start := time.Now()
	output, execErr := w.doInvoke(ctx, task)
	duration := time.Since(start)
	if execErr != nil {
		w.handleFailure(ctx, task, execErr)
		logger.Error("reliable task worker: execute failed",
			zap.String("task_id", task.TaskID),
			zap.Int("retry", task.Retries),
			zap.Duration("duration", duration),
			zap.Error(execErr))
		return
	}

	if err := w.reliableRepo.Complete(ctx, task.TaskID, output); err != nil {
		logger.Error("reliable task worker: complete task failed", zap.String("task_id", task.TaskID), zap.Error(err))
		return
	}
	task.Status = model.AsyncTaskStatusCompleted
	task.Output = output
	task.ErrorMsg = ""
	task.UpdatedAt = time.Now()
	w.refreshCache(ctx, task)
	logger.Info("reliable task worker: execute completed",
		zap.String("task_id", task.TaskID),
		zap.Duration("duration", duration))
}

func (w *ReliableTaskWorker) handleFailure(ctx context.Context, task *model.AsyncTask, execErr error) {
	nextRetries := task.Retries + 1
	if nextRetries < reliableTaskMaxRetries {
		delay := time.Duration(nextRetries) * 10 * time.Second
		if err := w.reliableRepo.Retry(ctx, task.TaskID, nextRetries, execErr.Error(), delay); err != nil {
			logger.Error("reliable task worker: schedule retry failed", zap.String("task_id", task.TaskID), zap.Error(err))
			return
		}
		task.Status = model.AsyncTaskStatusRetrying
		task.Retries = nextRetries
		task.ErrorMsg = execErr.Error()
		task.UpdatedAt = time.Now()
		return
	}

	if err := w.reliableRepo.Fail(ctx, task.TaskID, execErr.Error()); err != nil {
		logger.Error("reliable task worker: fail task failed", zap.String("task_id", task.TaskID), zap.Error(err))
		return
	}
	task.Status = model.AsyncTaskStatusFailed
	task.Retries = nextRetries
	task.ErrorMsg = execErr.Error()
	task.UpdatedAt = time.Now()
	w.refreshCache(ctx, task)
}

func (w *ReliableTaskWorker) doInvoke(ctx context.Context, task *model.AsyncTask) ([]byte, error) {
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

func (w *ReliableTaskWorker) refreshCache(ctx context.Context, task *model.AsyncTask) {
	if w.cacheRepo == nil {
		return
	}
	if task.Status != model.AsyncTaskStatusCompleted && task.Status != model.AsyncTaskStatusFailed {
		return
	}
	if err := w.cacheRepo.SetTaskCache(ctx, task); err != nil {
		logger.Warn("reliable task worker: refresh redis task cache failed",
			zap.String("task_id", task.TaskID),
			zap.Error(err))
	}
}
