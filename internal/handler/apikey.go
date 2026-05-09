package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/ctxkey"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const apiKeyRandomBytes = 32

type APIKeyHandler struct {
	repo *repo.APIKeyRepo
}

func NewAPIKeyHandler(r *repo.APIKeyRepo) *APIKeyHandler {
	return &APIKeyHandler{repo: r}
}

// Generate POST /api-keys/generate
// 生成或覆盖当前账号的 API Key，明文只返回一次
func (h *APIKeyHandler) Generate(ctx context.Context, c *app.RequestContext) {
	appID := c.GetString(ctxkey.AppID)

	raw := make([]byte, apiKeyRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "generate key failed"))
		return
	}
	// 格式：agw_ + base64url（无填充），约 47 字符
	keyPlain := model.APIKeyPrefix + base64.RawURLEncoding.EncodeToString(raw)
	// 前缀取 agw_ + 前4位随机字符，共8位，用于快速定位
	keyPrefix := keyPlain[:8]

	hash, err := bcrypt.GenerateFromPassword([]byte(keyPlain), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "hash key failed"))
		return
	}

	if err := h.repo.Upsert(ctx, appID, string(hash), keyPrefix); err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "save key failed: "+err.Error()))
		return
	}

	c.JSON(consts.StatusOK, resp.OK(map[string]any{
		"key":    keyPlain,
		"prefix": keyPrefix,
		"tip":    "API Key 只显示一次，请立即复制保存",
	}))
}

// Get GET /api-keys
// 查询当前账号的 Key 信息（不含明文）
func (h *APIKeyHandler) Get(ctx context.Context, c *app.RequestContext) {
	appID := c.GetString(ctxkey.AppID)

	key, err := h.repo.GetByAppID(ctx, appID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(consts.StatusOK, resp.OK(nil))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "get key failed"))
		return
	}

	c.JSON(consts.StatusOK, resp.OK(map[string]any{
		"prefix":       key.KeyPrefix,
		"created_at":   key.CreatedAt,
		"last_used_at": key.LastUsedAt,
	}))
}

// Delete DELETE /api-keys
// 吊销当前账号的 API Key
func (h *APIKeyHandler) Delete(ctx context.Context, c *app.RequestContext) {
	appID := c.GetString(ctxkey.AppID)

	if err := h.repo.DeleteByAppID(ctx, appID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "no api key found"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "delete key failed"))
		return
	}

	c.JSON(consts.StatusOK, resp.OK(nil))
}
