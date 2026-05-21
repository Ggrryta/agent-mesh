// Package user 负责"人类账号 + 其配对的 virtual-user-agent"这对实体的生命周期。
// users 表的每一行始终对应一行 kind='virtual-user'、同 owner_uid 的 agents 行；
// 这对实体在事务内原子创建，任何时刻观察都不会看到半态。
package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// User 对应 users 表一行。password hash 不会从 repo 层主动暴露，除非调用者
// 明确索取。
type User struct {
	ID                 int64
	Username           string
	PasswordHash       string
	VirtualUserAgentID string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// 域错误。handler 层按这些 sentinel 映射 HTTP code。
var (
	ErrUsernameTaken   = errors.New("user: username already taken")
	ErrUserNotFound    = errors.New("user: not found")
	ErrInvalidPassword = errors.New("user: invalid credentials")
	ErrInvalidUsername = errors.New("user: username must be 3-64 chars [a-z0-9_-]")
)

var usernameRE = regexp.MustCompile(`^[a-z0-9_-]{3,64}$`)

// ValidateUsername 导出以便 API 层能和 DB 层的约束保持一致。
func ValidateUsername(s string) error {
	if !usernameRE.MatchString(s) {
		return ErrInvalidUsername
	}
	return nil
}

// VirtualAgentIDFor 由 uid 推导出对应的虚拟 agent_id。
// 固定前缀让这对关系在日志和 mesh 查询里一眼可辨，省一次 JOIN。
func VirtualAgentIDFor(uid int64) string {
	return fmt.Sprintf("virtual-user-%d", uid)
}

// Repo 是数据访问接口。保持最小，service 层可以用内存 stub 写单测。
type Repo interface {
	CreateWithVirtualAgent(ctx context.Context, username, passwordHash string) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	GetByID(ctx context.Context, id int64) (*User, error)
}

// SQLRepo 是 MySQL 实现。
type SQLRepo struct {
	db *sql.DB
}

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

// CreateWithVirtualAgent 在单个事务里原子创建 users 行 + 配对的
// virtual-user agents 行。
func (r *SQLRepo) CreateWithVirtualAgent(ctx context.Context, username, passwordHash string) (*User, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("user: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx,
		"INSERT INTO users (username, password_hash) VALUES (?, ?)",
		username, passwordHash)
	if err != nil {
		if isDuplicate(err) {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("user: insert user: %w", err)
	}
	uid, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("user: last insert id: %w", err)
	}

	virtualID := VirtualAgentIDFor(uid)

	// virtual agent 行：URL 留空、kind='virtual-user'。description 让运维
	// 翻 agents 表时一眼看出这行是干嘛的。
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agents (agent_id, owner_uid, name, description, kind, status)
		VALUES (?, ?, ?, ?, 'virtual-user', 'active')`,
		virtualID, uid, username, "virtual agent representing user "+username,
	); err != nil {
		return nil, fmt.Errorf("user: insert virtual agent: %w", err)
	}

	// 把 users.virtual_user_agent_id 指针回填，之后单次查询就能拿到对子。
	if _, err := tx.ExecContext(ctx,
		"UPDATE users SET virtual_user_agent_id = ? WHERE id = ?",
		virtualID, uid,
	); err != nil {
		return nil, fmt.Errorf("user: backfill virtual id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("user: commit: %w", err)
	}
	committed = true

	return &User{
		ID:                 uid,
		Username:           username,
		PasswordHash:       passwordHash,
		VirtualUserAgentID: virtualID,
	}, nil
}

func (r *SQLRepo) GetByUsername(ctx context.Context, username string) (*User, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, COALESCE(virtual_user_agent_id, ''),
		       created_at, updated_at
		FROM users WHERE username = ? LIMIT 1`, username)
	u := &User{}
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.VirtualUserAgentID,
		&u.CreatedAt, &u.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return u, nil
}

func (r *SQLRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, COALESCE(virtual_user_agent_id, ''),
		       created_at, updated_at
		FROM users WHERE id = ? LIMIT 1`, id)
	u := &User{}
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.VirtualUserAgentID,
		&u.CreatedAt, &u.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return u, nil
}

// isDuplicate 识别 MySQL 1062 唯一键冲突，而不引入 driver 的错误类型。
func isDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Error 1062")
}
