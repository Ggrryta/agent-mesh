package handler

import (
	"context"
	"time"

	"agent-gateway/config"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/jwtclaims"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuthHandler struct {
	repo *repo.ConsumerRepo
	cfg  *config.JWTConfig
}

func NewAuthHandler(r *repo.ConsumerRepo, cfg *config.JWTConfig) *AuthHandler {
	return &AuthHandler{repo: r, cfg: cfg}
}

// Token POST /auth/token
func (h *AuthHandler) Token(ctx context.Context, c *app.RequestContext) {
	var req struct {
		AppID    string `json:"app_id"`
		Secret   string `json:"secret"`
		Password string `json:"password"` // 兼容旧字段
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid request body: "+err.Error()))
		return
	}
	if req.Secret == "" {
		req.Secret = req.Password
	}
	if req.AppID == "" || req.Secret == "" {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "app_id and secret are required"))
		return
	}

	consumer, err := h.repo.GetByAppID(ctx, req.AppID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "invalid app_id or secret"))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "get consumer failed: "+err.Error()))
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(consumer.SecretHash), []byte(req.Secret)); err != nil {
		c.JSON(consts.StatusUnauthorized, resp.Err(resp.CodeUnauthorized, "invalid app_id or secret"))
		return
	}

	expireHours := h.cfg.ExpireHours
	if expireHours <= 0 {
		expireHours = 24
	}

	claims := jwtclaims.Claims{
		AppID: consumer.AppID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(expireHours) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(h.cfg.Secret))
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "sign token failed"))
		return
	}

	c.JSON(consts.StatusOK, resp.OK(map[string]any{
		"token":      signed,
		"expires_in": expireHours * 3600,
	}))
}
