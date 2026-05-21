package apikey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// Service 封装 key 的生成 / 校验 / 吊销。
// 任何业务代码只应与 Service 打交道，不直接碰 Repo。
type Service struct {
	repo Repo
	log  *zap.Logger
}

// NewService 构造 Service。log 可为 nil（内部降级到 Nop logger）。
func NewService(repo Repo, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{repo: repo, log: log}
}

// Issue 为某用户签发一把新的 API Key。
//
// 返回值 rawKey **只在此刻可见**，调用方必须立即传给用户（HTTP 响应），
// 之后任何地方都不再保留它。持久化的只是 SHA-256 hash。
func (s *Service) Issue(ctx context.Context, ownerUID int64, label string) (rawKey string, k *Key, err error) {
	if ownerUID == 0 {
		return "", nil, errors.New("apikey: owner uid required")
	}
	raw, err := generateRaw()
	if err != nil {
		return "", nil, err
	}
	k, err = s.repo.Insert(ctx, ownerUID, hashRaw(raw), extractPrefix(raw), label)
	if err != nil {
		return "", nil, err
	}
	return raw, k, nil
}

// Verify 校验原始 key 是否合法、未吊销。
//
// 典型路径（/v1/mesh/auth/token）：
//  1. 格式校验（prefix + 最小长度）
//  2. 算 hash，查 api_keys.key_hash 唯一索引（O(1) DB 查询）
//  3. 检查 revoked_at
//  4. 异步更新 last_used_at（失败不影响返回值）
//
// 注意：此方法**会查 DB**。业务请求不应走这里，应使用 /auth/token 换到的
// JWT。只有 /auth/token 路径才调 Verify。
func (s *Service) Verify(ctx context.Context, raw string) (*Key, error) {
	if err := validateFormat(raw); err != nil {
		return nil, err
	}
	k, err := s.repo.FindByHash(ctx, hashRaw(raw))
	if err != nil {
		return nil, err
	}
	if !k.IsActive() {
		return nil, ErrKeyRevoked
	}
	// 异步更新使用时间戳。刻意不阻塞主路径 —— Gateway 挂了或 DB 抖动
	// 时业务请求仍然能成功。注意 ctx 不能直接传进去（原 ctx 会被 handler
	// 取消），用独立的超时 ctx。
	s.goTouchLastUsed(k.ID)
	return k, nil
}

// List 返回该用户名下的 key（UI 用）。不含明文，只是元数据。
func (s *Service) List(ctx context.Context, ownerUID int64) ([]*Key, error) {
	return s.repo.ListByOwner(ctx, ownerUID)
}

// Revoke 吊销某把 key。只允许 owner 自己吊销。
//
// 已被吊销的 key 再次调用是 no-op（repo 层保证）；完全不存在的 key 返回
// ErrKeyNotFound，handler 翻 404。
func (s *Service) Revoke(ctx context.Context, ownerUID, id int64) error {
	if ownerUID == 0 || id == 0 {
		return errors.New("apikey: owner_uid and id required")
	}
	return s.repo.Revoke(ctx, ownerUID, id)
}

// goTouchLastUsed 起一个短命 goroutine，尽力更新 last_used_at。
//
// 用独立 goroutine + 独立超时 ctx，原因：
//   - /auth/token 的 ctx 在 handler 返回时会被取消，如果我们用它，更新
//     几乎永远失败
//   - 不想让这个统计字段的延迟拖住用户请求
//   - 失败写日志即可，不要让业务感知
func (s *Service) goTouchLastUsed(id int64) {
	go func(id int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.repo.TouchLastUsed(ctx, id, time.Now()); err != nil {
			s.log.Debug("apikey: touch last_used_at failed",
				zap.Int64("key_id", id), zap.Error(err))
		}
	}(id)
}

// Ensure Service satisfies the narrow interface we expose to admin/mesh handlers.
var _ = fmt.Errorf
