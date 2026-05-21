// Package auth 集中处理 token 的签发与校验。
//
// 为了简单用 HS256 —— 适合单 issuer 单 audience 的场景，也避免密钥轮换
// 复杂度。两种 token 类型并存：
//
//   - UserToken  带用户 uid。登录时签发，admin API 校验。
//   - AgentToken 带 agent_id + owner uid。agent 创建/重签时产生，mesh API 校验。
//
// 两种 token 都带标准 JWT claims（iss, iat, exp, sub）便于通用工具解析；
// 服务端通过 Kind 字段区分。
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenKind 在 claim 层面区分 user / agent token，防止盗用的 user token
// 绕过 mesh API 检查，反之亦然。
type TokenKind string

const (
	KindUser  TokenKind = "user"
	KindAgent TokenKind = "agent"
)

const issuer = "agent-mesh-gateway"

// Claims 在标准 jwt.RegisteredClaims 基础上加了应用特定字段。
type Claims struct {
	Kind    TokenKind `json:"kind"`
	UID     int64     `json:"uid,omitempty"`      // user id（两种 kind 都带 owner uid）
	AgentID string    `json:"agent_id,omitempty"` // 仅 KindAgent 使用
	KeyID   int64     `json:"kid,omitempty"`      // 仅 KindAgent：签出这个 JWT 的 api_key.id
	jwt.RegisteredClaims
}

// Signer 用共享的 HS256 密钥签发 / 校验 token。
// 两种 token kind 的 TTL 分开配：user token 偏长，agent token 偏短。
type Signer struct {
	secret   []byte
	userTTL  time.Duration
	agentTTL time.Duration
}

// NewSigner 校验密钥并返回可直接使用的 Signer。
//
//   - userTTL：user JWT 的有效期（admin 登录，常见 24h）。
//   - agentTTL：agent JWT 的有效期（API Key 换出来的短期 token，建议 1h）。
//
// 任一 TTL <= 0 时走合理默认值。
func NewSigner(secret string, userTTL, agentTTL time.Duration) (*Signer, error) {
	if len(secret) < 16 {
		return nil, errors.New("auth: JWT secret must be at least 16 bytes")
	}
	if userTTL <= 0 {
		userTTL = 24 * time.Hour
	}
	if agentTTL <= 0 {
		agentTTL = 1 * time.Hour
	}
	return &Signer{secret: []byte(secret), userTTL: userTTL, agentTTL: agentTTL}, nil
}

// IssueUser 签发一个带 uid 的 user token。
func (s *Signer) IssueUser(uid int64) (string, error) {
	return s.issue(Claims{
		Kind: KindUser,
		UID:  uid,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: fmt.Sprintf("user:%d", uid),
		},
	}, s.userTTL)
}

// IssueAgent 签发一个 agent token。
//
// 参数：
//   - agentID：本次要代表的 agent 身份（来自 /v1/mesh/auth/token 请求体）。
//   - ownerUID：API Key 的归属用户。
//   - keyID：本次校验通过的 api_key.id。审计用；吊销 key 时可以追溯受影响的 JWT。
//
// agent JWT 的 TTL 固定用 agentTTL；调用方不能自己延长。
func (s *Signer) IssueAgent(agentID string, ownerUID, keyID int64) (string, error) {
	return s.issue(Claims{
		Kind:    KindAgent,
		UID:     ownerUID,
		AgentID: agentID,
		KeyID:   keyID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: fmt.Sprintf("agent:%s", agentID),
		},
	}, s.agentTTL)
}

// AgentTTL 暴露 agent token 的 TTL，供 handler 回 expires_in 用。
func (s *Signer) AgentTTL() time.Duration { return s.agentTTL }

// UserTTL 暴露 user token 的 TTL，供 handler 回 expires_in 用。
func (s *Signer) UserTTL() time.Duration { return s.userTTL }

// Verify 解析并校验 token，成功返回 Claims。
// Kind 字段由调用方按 user / agent scope 自行判定。
func (s *Signer) Verify(raw string) (*Claims, error) {
	c := &Claims{}
	t, err := jwt.ParseWithClaims(raw, c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method %q", t.Header["alg"])
		}
		return s.secret, nil
	}, jwt.WithIssuer(issuer), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	if !t.Valid {
		return nil, errors.New("auth: token invalid")
	}
	return c, nil
}

func (s *Signer) issue(c Claims, ttl time.Duration) (string, error) {
	now := time.Now()
	c.Issuer = issuer
	c.IssuedAt = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return tok.SignedString(s.secret)
}
