// Package apikey 管理用户持有的长期凭证。
//
// 本包负责生成、校验、吊销 API Key。agent 启动时用 API Key 跟 Gateway 换
// 短期 JWT（见 /v1/mesh/auth/token），之后的业务请求只走 JWT，不再接触
// api_keys 表。详见 ADR 007。
//
// 边界：
//   - 一把 key 属于 **用户**，不绑 agent。用户自行决定把这把 key 发给哪些 agent。
//   - `Issue` 返回的原始 key 只在那一次出现；DB 只存 SHA-256 hash。
//   - 吊销走软删除（revoked_at），不物理删除，便于审计。
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// keyPrefixVisible 代表原始 key 字符串中"展示给 UI"的前缀长度。
// 让 UI 能区分同一个用户下的多把 key，不会泄露完整内容。
const keyPrefixVisible = 16

// rawKeyEntropyBytes 是随机部分的字节数（编码前）。
// 32 字节 = 256 bit，足够抗爆破。
const rawKeyEntropyBytes = 32

// rawKeyPrefix 是所有 key 的统一前缀，方便从日志 / 代码里一眼辨识。
// 前缀 + "_" + 随机部分。
const rawKeyPrefix = "sk-am"

// Key 对应 api_keys 表一行，不含明文 key。
type Key struct {
	ID         int64
	OwnerUID   int64
	KeyPrefix  string     // 原始 key 的可见前缀，仅供 UI 展示
	Label      string     // 用户自定义的标签
	LastUsedAt *time.Time // 最近一次 Verify 成功的时间
	RevokedAt  *time.Time // 非 nil 即已吊销
	CreatedAt  time.Time
}

// IsActive 报告这把 key 是否仍可用（未吊销）。
func (k *Key) IsActive() bool { return k.RevokedAt == nil }

// 错误值。handler 层据此翻译 HTTP code。
var (
	ErrKeyNotFound = errors.New("apikey: not found")
	ErrKeyRevoked  = errors.New("apikey: revoked")
	ErrKeyInvalid  = errors.New("apikey: invalid format")
)

// generateRaw 产出一把新的原始 API Key。
// 形如 "sk-am_<base64url(32 random bytes)>"，总长约 50 字符。
// 只在 Issue 返回值里出现一次，之后 DB 只留 hash。
func generateRaw() (string, error) {
	buf := make([]byte, rawKeyEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("apikey: random: %w", err)
	}
	enc := base64.RawURLEncoding.EncodeToString(buf)
	return rawKeyPrefix + "_" + enc, nil
}

// hashRaw 算 SHA-256(raw) 并以 hex 返回，供 DB 查询和存储。
// 不使用 bcrypt 是刻意选择：key 本身熵足够高，只要不泄露 hash，暴力攻击
// 不可行；换取 <0.01ms 的校验开销，/auth/token 之外的路径完全不受影响。
func hashRaw(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// extractPrefix 截取原始 key 前 keyPrefixVisible 个字符。
// 不足就原样返回（理论上不会发生，写防御式）。
func extractPrefix(raw string) string {
	if len(raw) <= keyPrefixVisible {
		return raw
	}
	return raw[:keyPrefixVisible]
}

// validateFormat 做最基础的形状校验，防止明显非法的字符串去查 DB。
// 严格校验留给 hash lookup：hash 算出来在 DB 里找不到就是非法。
func validateFormat(raw string) error {
	if !strings.HasPrefix(raw, rawKeyPrefix+"_") {
		return ErrKeyInvalid
	}
	if len(raw) < len(rawKeyPrefix)+1+16 {
		return ErrKeyInvalid
	}
	return nil
}
