package user

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost 控制密码哈希的工作因子。10 在普通硬件上大约 60ms，是
// "用户体感 vs 爆破抗性"的折中点。Go 的 bcrypt 实现比 C 慢，所以偏低一点。
const bcryptCost = bcrypt.DefaultCost // 10

// Service 是 user 相关操作的事务边界。
type Service struct {
	repo Repo
}

func NewService(repo Repo) *Service { return &Service{repo: repo} }

// Register 新建用户 + 其配对的 virtual-user agent。
//
// 返回值包含刚创建的记录，方便 handler 立刻签发 JWT；handler 一定不要
// 把 password hash 透回给客户端。
func (s *Service) Register(ctx context.Context, username, password string) (*User, error) {
	username = normalizeUsername(username)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if len(password) < 6 {
		return nil, errors.New("user: password must be at least 6 chars")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("user: hash password: %w", err)
	}

	return s.repo.CreateWithVirtualAgent(ctx, username, string(hash))
}

// Login 校验凭证，成功返回 user。bcrypt 比对相对 hash cost 是常数时间的。
func (s *Service) Login(ctx context.Context, username, password string) (*User, error) {
	username = normalizeUsername(username)
	u, err := s.repo.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// 刻意返回和"密码错"相同的错误，避免攻击者枚举用户名。
			return nil, ErrInvalidPassword
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidPassword
	}
	return u, nil
}

// GetByID 供 /me 端点使用。
func (s *Service) GetByID(ctx context.Context, id int64) (*User, error) {
	return s.repo.GetByID(ctx, id)
}

// normalizeUsername 归一化：全小写 + 去前后空格，
// 避免 "Alice" 和 " alice" 在后续 DB 查找时撞上。
func normalizeUsername(s string) string {
	return toLowerTrim(s)
}
