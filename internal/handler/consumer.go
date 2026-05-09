package handler

import (
	"context"
	"errors"
	"regexp"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"golang.org/x/crypto/bcrypt"
)

// app_id 允许小写字母、数字、点、下划线、短横线，3-128 位。
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,127}$`)

const minSecretLen = 12

type ConsumerHandler struct {
	repo consumerRepo
}

type consumerRepo interface {
	Create(ctx context.Context, c *model.Consumer) error
}

func NewConsumerHandler(r *repo.ConsumerRepo) *ConsumerHandler {
	return &ConsumerHandler{repo: r}
}

// Register POST /register
// 自助注册 Consumer。Agent 调用权限通过 /agents/:agent_id/apply 申请，由 owner 审批。
//
// TODO(安全): 后续补充防刷机制 —— IP 维度限流、邮箱验证、captcha。
func (h *ConsumerHandler) Register(ctx context.Context, c *app.RequestContext) {
	var req struct {
		AppID       string `json:"app_id"`
		Secret      string `json:"secret"`
		Description string `json:"description"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid request body: "+err.Error()))
		return
	}
	if !appIDPattern.MatchString(req.AppID) {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "invalid app_id: must be 3-128 chars, lowercase alphanumeric with . _ -"))
		return
	}
	if len(req.Secret) < minSecretLen {
		c.JSON(consts.StatusBadRequest, resp.Err(resp.CodeBadRequest, "secret too short: at least 12 chars required"))
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Secret), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "hash secret failed"))
		return
	}

	consumer := &model.Consumer{
		AppID:       req.AppID,
		SecretHash:  string(hash),
		Description: req.Description,
	}
	if err := h.repo.Create(ctx, consumer); err != nil {
		if errors.Is(err, repo.ErrDuplicateAppID) {
			c.JSON(consts.StatusConflict, resp.Err(resp.CodeBadRequest, "app_id already exists: "+req.AppID))
			return
		}
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "create consumer failed: "+err.Error()))
		return
	}

	c.JSON(consts.StatusOK, resp.OK(map[string]any{
		"app_id":      consumer.AppID,
		"description": consumer.Description,
	}))
}
