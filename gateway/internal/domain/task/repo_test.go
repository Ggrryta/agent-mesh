package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func liveDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("AGENT_MESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENT_MESH_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}

// uniqueTaskID 让每个 live 测试用独立 task_id，避免撞历史。
func uniqueTaskID(t *testing.T, tag string) string {
	t.Helper()
	return fmt.Sprintf("tt-%s-%d", tag, time.Now().UnixNano())
}

func cleanup(t *testing.T, db *sql.DB, taskID string) {
	t.Helper()
	_, _ = db.Exec("DELETE FROM task_artifacts WHERE task_id = ?", taskID)
	_, _ = db.Exec("DELETE FROM task_messages  WHERE task_id = ?", taskID)
	_, _ = db.Exec("DELETE FROM reliable_async_tasks WHERE task_id = ?", taskID)
}

// ─── CreateTask + 幂等 ─────────────────────────────────────────────

func TestSQLRepo_CreateTask_HappyPath(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	tid := uniqueTaskID(t, "create")
	t.Cleanup(func() { cleanup(t, db, tid) })

	task := &Task{
		TaskID: tid, ContextID: tid, FromAgentID: "alice-live", ToAgentID: "bob-live",
		Status: StateSubmitted,
	}
	msg := &Message{
		MessageID: tid + "-m1", TaskID: tid, ContextID: tid,
		Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "hi"}},
	}

	got, err := repo.CreateTask(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.TaskID != tid || got.Status != StateSubmitted {
		t.Fatalf("unexpected: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("timestamps missing")
	}
}

func TestSQLRepo_CreateTask_Idempotent(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	tid := uniqueTaskID(t, "idem")
	t.Cleanup(func() { cleanup(t, db, tid) })

	task := &Task{
		TaskID: tid, ContextID: tid, FromAgentID: "alice-live", ToAgentID: "bob-live",
		Status: StateSubmitted,
	}
	msg := &Message{
		MessageID: tid + "-m1", TaskID: tid, ContextID: tid,
		Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "first"}},
	}

	_, err := repo.CreateTask(context.Background(), task, msg)
	if err != nil {
		t.Fatal(err)
	}
	// 重复 create 同 task / 同 message：不报错，应返回已存在任务
	_, err = repo.CreateTask(context.Background(), task, msg)
	if err != nil {
		t.Fatalf("idempotent create: %v", err)
	}

	// 主表和消息表都只一行
	var cnt int
	_ = db.QueryRow("SELECT COUNT(*) FROM reliable_async_tasks WHERE task_id=?", tid).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("task rows: %d", cnt)
	}
	_ = db.QueryRow("SELECT COUNT(*) FROM task_messages WHERE task_id=?", tid).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("message rows: %d", cnt)
	}
}

// ─── AppendMessage 幂等 + 冲突 ─────────────────────────────────────

func TestSQLRepo_AppendMessage_Idempotent(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	tid := uniqueTaskID(t, "msgidem")
	t.Cleanup(func() { cleanup(t, db, tid) })
	seedTask(t, repo, tid)

	msg := &Message{
		MessageID: tid + "-msg", TaskID: tid, ContextID: tid,
		Role: RoleAgent, Parts: []Part{{Kind: PartText, Text: "reply"}},
	}
	first, err := repo.AppendMessage(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	// 同 message_id 重复 append：不报错，返回已有
	second, err := repo.AppendMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected same row: %d vs %d", first.ID, second.ID)
	}
}

func TestSQLRepo_AppendMessage_ConflictDifferentTask(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	tid1 := uniqueTaskID(t, "conf1")
	tid2 := uniqueTaskID(t, "conf2")
	t.Cleanup(func() { cleanup(t, db, tid1); cleanup(t, db, tid2) })
	seedTask(t, repo, tid1)
	seedTask(t, repo, tid2)

	shared := tid1 + "-shared"
	first := &Message{
		MessageID: shared, TaskID: tid1, ContextID: tid1,
		Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "for t1"}},
	}
	_, err := repo.AppendMessage(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	// 相同 message_id 但 task_id 不同：冲突
	second := &Message{
		MessageID: shared, TaskID: tid2, ContextID: tid2,
		Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "for t2"}},
	}
	_, err = repo.AppendMessage(context.Background(), second)
	if !errors.Is(err, ErrMessageIDDuplicate) {
		t.Fatalf("want ErrMessageIDDuplicate, got %v", err)
	}
}

// ─── AppendArtifact 冲突 ──────────────────────────────────────────

func TestSQLRepo_AppendArtifact_Duplicate(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	tid := uniqueTaskID(t, "artifact")
	t.Cleanup(func() { cleanup(t, db, tid) })
	seedTask(t, repo, tid)

	a := &Artifact{
		ArtifactID: "a-1", TaskID: tid, ContextID: tid,
		Parts: []Part{{Kind: PartText, Text: "v1"}},
	}
	_, err := repo.AppendArtifact(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	a2 := &Artifact{
		ArtifactID: "a-1", TaskID: tid, ContextID: tid,
		Parts: []Part{{Kind: PartText, Text: "v2"}},
	}
	_, err = repo.AppendArtifact(context.Background(), a2)
	if !errors.Is(err, ErrArtifactIDDuplicate) {
		t.Fatalf("want dup, got %v", err)
	}
}

// ─── TransitionStatus CAS 并发 ────────────────────────────────────

func TestSQLRepo_TransitionStatus_CASConcurrent(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	tid := uniqueTaskID(t, "cas")
	t.Cleanup(func() { cleanup(t, db, tid) })
	seedTask(t, repo, tid)

	// 8 个 goroutine 同时把 submitted → working，只能一个赢
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, _, err := repo.TransitionStatus(context.Background(), tid,
				[]State{StateSubmitted}, StateWorking, "", "")
			if err != nil {
				t.Errorf("transition: %v", err)
				return
			}
			if changed {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("want exactly 1 winner, got %d", got)
	}
	// 最终状态应为 working
	task, _, _, err := repo.GetTask(context.Background(), tid, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StateWorking {
		t.Fatalf("status: %v", task.Status)
	}
}

func TestSQLRepo_TransitionStatus_WrongFromRejects(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	tid := uniqueTaskID(t, "wrongfrom")
	t.Cleanup(func() { cleanup(t, db, tid) })
	seedTask(t, repo, tid)

	// submitted → working
	changed, _, _ := repo.TransitionStatus(context.Background(), tid,
		[]State{StateSubmitted}, StateWorking, "", "")
	if !changed {
		t.Fatal("first transition should win")
	}
	// 再次尝试 submitted → working（但当前已是 working）
	changed2, _, _ := repo.TransitionStatus(context.Background(), tid,
		[]State{StateSubmitted}, StateWorking, "", "")
	if changed2 {
		t.Fatal("transition from wrong state should fail")
	}
}

// ─── GetTask / ListByContext ──────────────────────────────────────

func TestSQLRepo_GetTask_WithHistoryAndArtifacts(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	tid := uniqueTaskID(t, "full")
	t.Cleanup(func() { cleanup(t, db, tid) })
	seedTask(t, repo, tid)

	// 追加消息 + artifact
	_, _ = repo.AppendMessage(context.Background(), &Message{
		MessageID: tid + "-m2", TaskID: tid, ContextID: tid,
		Role: RoleAgent, Parts: []Part{{Kind: PartText, Text: "bob reply"}},
	})
	_, _ = repo.AppendArtifact(context.Background(), &Artifact{
		ArtifactID: "art-1", TaskID: tid, ContextID: tid,
		Name: "result", Parts: []Part{{Kind: PartText, Text: "done"}},
	})

	task, history, arts, err := repo.GetTask(context.Background(), tid, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if task.TaskID != tid {
		t.Fatal("task mismatch")
	}
	if len(history) != 2 { // 种子 1 条 + 追加 1 条
		t.Fatalf("history len: %d", len(history))
	}
	if len(arts) != 1 {
		t.Fatalf("artifacts len: %d", len(arts))
	}
}

func TestSQLRepo_ListByContext(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)

	ctxID := fmt.Sprintf("ctx-%d", time.Now().UnixNano())
	tid1 := ctxID + "-t1"
	tid2 := ctxID + "-t2"
	t.Cleanup(func() { cleanup(t, db, tid1); cleanup(t, db, tid2) })

	for _, tid := range []string{tid1, tid2} {
		_, err := repo.CreateTask(context.Background(),
			&Task{TaskID: tid, ContextID: ctxID, FromAgentID: "a", ToAgentID: "b", Status: StateSubmitted},
			&Message{MessageID: tid + "-m1", TaskID: tid, ContextID: ctxID, Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "x"}}},
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	list, err := repo.ListByContext(context.Background(), ctxID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2, got %d", len(list))
	}
}

func TestSQLRepo_GetTaskNotFound(t *testing.T) {
	db := liveDB(t)
	defer db.Close()
	repo := NewSQLRepo(db)
	_, _, _, err := repo.GetTask(context.Background(), "nonexistent-"+uniqueTaskID(t, "404"), false, false)
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("want ErrTaskNotFound, got %v", err)
	}
}

// ─── helpers ─────────────────────────────────────────────────────

// seedTask 建一个基础 task + 首条 message，返回 task。
func seedTask(t *testing.T, repo *SQLRepo, tid string) *Task {
	t.Helper()
	task := &Task{
		TaskID: tid, ContextID: tid, FromAgentID: "alice-live", ToAgentID: "bob-live",
		Status: StateSubmitted,
	}
	msg := &Message{
		MessageID: tid + "-m1", TaskID: tid, ContextID: tid,
		Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "seed"}},
	}
	got, err := repo.CreateTask(context.Background(), task, msg)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// 防止 strings import 被标未用
var _ = strings.HasPrefix
