package service

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type stubTaskRecoveryRepo struct {
	allBatches   [][]string
	staleBatches [][]string
	recovered    []string
	staleBefore  []time.Time
}

func (r *stubTaskRecoveryRepo) ListAllClaimedTaskIDs(_ context.Context, _ int) ([]string, error) {
	if len(r.allBatches) == 0 {
		return nil, nil
	}
	batch := r.allBatches[0]
	r.allBatches = r.allBatches[1:]
	return batch, nil
}

func (r *stubTaskRecoveryRepo) ListStaleClaimedTaskIDs(_ context.Context, before time.Time, _ int) ([]string, error) {
	r.staleBefore = append(r.staleBefore, before)
	if len(r.staleBatches) == 0 {
		return nil, nil
	}
	batch := r.staleBatches[0]
	r.staleBatches = r.staleBatches[1:]
	return batch, nil
}

func (r *stubTaskRecoveryRepo) RecoverClaimedTask(_ context.Context, taskID string) error {
	r.recovered = append(r.recovered, taskID)
	return nil
}

func TestTaskCleaner_RecoverAllClaimedTasks(t *testing.T) {
	repo := &stubTaskRecoveryRepo{
		allBatches: [][]string{
			{"task-1", "task-2"},
			{"task-3"},
		},
	}
	cleaner := &TaskCleaner{
		taskRepo:      repo,
		zombieTimeout: 30 * time.Minute,
		batchSize:     2,
	}

	cleaner.recoverAllClaimedTasks()

	want := []string{"task-1", "task-2", "task-3"}
	if !reflect.DeepEqual(repo.recovered, want) {
		t.Fatalf("unexpected recovered tasks: got %v want %v", repo.recovered, want)
	}
}

func TestTaskCleaner_RecoverZombieTasks(t *testing.T) {
	repo := &stubTaskRecoveryRepo{
		staleBatches: [][]string{
			{"task-1", "task-2"},
			{"task-3"},
		},
	}
	cleaner := &TaskCleaner{
		taskRepo:      repo,
		zombieTimeout: 10 * time.Minute,
		batchSize:     2,
	}

	beforeCall := time.Now()
	cleaner.recoverZombieTasks()
	afterCall := time.Now()

	want := []string{"task-1", "task-2", "task-3"}
	if !reflect.DeepEqual(repo.recovered, want) {
		t.Fatalf("unexpected recovered tasks: got %v want %v", repo.recovered, want)
	}
	if len(repo.staleBefore) == 0 {
		t.Fatalf("expected stale list to be queried")
	}
	expectedMin := beforeCall.Add(-cleaner.zombieTimeout).Add(-time.Second)
	expectedMax := afterCall.Add(-cleaner.zombieTimeout).Add(time.Second)
	if repo.staleBefore[0].Before(expectedMin) || repo.staleBefore[0].After(expectedMax) {
		t.Fatalf("unexpected staleBefore: got %v want between %v and %v", repo.staleBefore[0], expectedMin, expectedMax)
	}
}
