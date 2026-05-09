package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
	"gorm.io/datatypes"
)

// ErrForbiddenNotOwner 调用方不是 Agent 的 owner
var ErrForbiddenNotOwner = errors.New("forbidden: not the owner")

// AgentCardInput A2A AgentCard 的注册请求结构（对应 A2A spec v0.2.5）
type AgentCardInput struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	URL          string            `json:"url"`
	Version      string            `json:"version"`
	Capabilities AgentCapabilities `json:"capabilities"`
	Skills       []AgentSkillInput `json:"skills"`
}

type AgentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
}

type AgentSkillInput struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	InputModes  []string `json:"inputModes"`
	OutputModes []string `json:"outputModes"`
}

// RegisterAgentRequest 注册请求
type RegisterAgentRequest struct {
	AgentID      string              `json:"agent_id"`
	AgentCard    AgentCardInput      `json:"agent_card"`
	DeliveryMode *model.DeliveryMode `json:"delivery_mode,omitempty"` // 0=push (默认), 1=pull (GAS)
}

// agentRepoIface 供测试注入 stub
type agentRepoIface interface {
	Create(ctx context.Context, a *model.Agent) error
	GetByAgentID(ctx context.Context, agentID string) (*model.Agent, error)
	UpdateAgentCard(ctx context.Context, agentID string, updates map[string]any) error
	UpdateStatus(ctx context.Context, agentID string, status model.AgentStatus) error
	UpdateHeartbeat(ctx context.Context, agentID string) error
	Delete(ctx context.Context, agentID string) error
	List(ctx context.Context, f repo.AgentFilter) ([]*model.Agent, int64, error)
	ListStaleAgents(ctx context.Context, before time.Time) ([]*model.Agent, error)
}

// agentSkillRepoIface 供测试注入 stub
type agentSkillRepoIface interface {
	ReplaceByAgentID(ctx context.Context, agentID string, skills []*model.AgentSkill) error
	ListByAgentID(ctx context.Context, agentID string) ([]*model.AgentSkill, error)
}

// AgentService Agent 注册/发现业务逻辑
type AgentService struct {
	agentRepo      agentRepoIface
	agentSkillRepo agentSkillRepoIface
	cache          *AgentCache
	notifier       AgentRegistryNotifier
}

func NewAgentService(agentRepo *repo.AgentRepo, agentSkillRepo *repo.AgentSkillRepo, cache *AgentCache, notifier AgentRegistryNotifier) *AgentService {
	return &AgentService{
		agentRepo:      agentRepo,
		agentSkillRepo: agentSkillRepo,
		cache:          cache,
		notifier:       notifier,
	}
}

// Register 注册或更新 Agent
func (s *AgentService) Register(ctx context.Context, ownerAppID string, req RegisterAgentRequest) (*model.Agent, error) {
	req.AgentID = NormalizeAgentID(req.AgentID)
	if err := validateRegisterRequest(req); err != nil {
		return nil, err
	}

	cardJSON, err := json.Marshal(req.AgentCard)
	if err != nil {
		return nil, fmt.Errorf("marshal agent card: %w", err)
	}

	now := time.Now()
	mode := model.DeliveryModePull
	if req.DeliveryMode != nil {
		mode = *req.DeliveryMode
	}
	agent := &model.Agent{
		AgentID:                   req.AgentID,
		Name:                      req.AgentCard.Name,
		Description:               req.AgentCard.Description,
		URL:                       req.AgentCard.URL,
		Version:                   req.AgentCard.Version,
		OwnerAppID:                ownerAppID,
		Status:                    model.AgentStatusActive,
		DeliveryMode:              mode,
		SupportsStreaming:         req.AgentCard.Capabilities.Streaming,
		SupportsPushNotifications: req.AgentCard.Capabilities.PushNotifications,
		AgentCardJSON:             datatypes.JSON(cardJSON),
		LastHeartbeatAt:           &now,
	}

	err = s.agentRepo.Create(ctx, agent)
	if errors.Is(err, repo.ErrDuplicateAgentID) {
		existing, getErr := s.agentRepo.GetByAgentID(ctx, req.AgentID)
		if getErr != nil {
			return nil, getErr
		}
		if existing.OwnerAppID != ownerAppID {
			return nil, ErrForbiddenNotOwner
		}
		// 已存在则更新
		updates := map[string]any{
			"name":                        agent.Name,
			"description":                 agent.Description,
			"url":                         agent.URL,
			"version":                     agent.Version,
			"status":                      model.AgentStatusActive,
			"delivery_mode":               agent.DeliveryMode,
			"supports_streaming":          agent.SupportsStreaming,
			"supports_push_notifications": agent.SupportsPushNotifications,
			"agent_card_json":             agent.AgentCardJSON,
			"last_heartbeat_at":           now,
		}
		if err := s.agentRepo.UpdateAgentCard(ctx, req.AgentID, updates); err != nil {
			return nil, err
		}
		agent, err = s.agentRepo.GetByAgentID(ctx, req.AgentID)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	// 同步 skills
	if err := s.syncSkills(ctx, req.AgentID, req.AgentCard.Skills); err != nil {
		logger.Warn("sync agent skills failed", zap.String("agent_id", req.AgentID), zap.Error(err))
	}

	s.cache.Set(agent)
	if s.notifier != nil {
		if err := s.notifier.RegisterAgent(ctx, agent); err != nil {
			logger.Warn("notify agent register failed", zap.String("agent_id", req.AgentID), zap.Error(err))
		}
	}
	logger.Info("agent registered", zap.String("agent_id", req.AgentID), zap.String("owner", ownerAppID))
	return agent, nil
}

// Deregister 注销 Agent（软删除）
func (s *AgentService) Deregister(ctx context.Context, agentID, callerAppID string) error {
	agent, err := s.agentRepo.GetByAgentID(ctx, agentID)
	if errors.Is(err, repo.ErrAgentNotFound) {
		return repo.ErrAgentNotFound
	}
	if err != nil {
		return err
	}
	if agent.OwnerAppID != callerAppID {
		return ErrForbiddenNotOwner
	}

	if err := s.agentRepo.Delete(ctx, agentID); err != nil {
		return err
	}
	s.cache.Delete(agentID)
	if s.notifier != nil {
		if err := s.notifier.DeregisterAgent(ctx, agentID, agent.URL); err != nil {
			logger.Warn("notify agent deregister failed", zap.String("agent_id", agentID), zap.Error(err))
		}
	}
	logger.Info("agent deregistered", zap.String("agent_id", agentID))
	return nil
}

// Drain 将 Agent 置为 Draining 状态（优雅下线）
// Draining 状态下 Gateway 不再路由新请求，但不强制断开已有连接
func (s *AgentService) Drain(ctx context.Context, agentID, callerAppID string) error {
	agent, err := s.agentRepo.GetByAgentID(ctx, agentID)
	if errors.Is(err, repo.ErrAgentNotFound) {
		return repo.ErrAgentNotFound
	}
	if err != nil {
		return err
	}
	if agent.OwnerAppID != callerAppID {
		return ErrForbiddenNotOwner
	}
	if agent.Status == model.AgentStatusDraining {
		return nil // 幂等
	}

	if err := s.agentRepo.UpdateStatus(ctx, agentID, model.AgentStatusDraining); err != nil {
		return err
	}
	s.cache.Delete(agentID)
	if s.notifier != nil {
		if err := s.notifier.DeregisterAgent(ctx, agentID, agent.URL); err != nil {
			logger.Warn("notify agent drain failed", zap.String("agent_id", agentID), zap.Error(err))
		}
	}
	logger.Info("agent draining", zap.String("agent_id", agentID))
	return nil
}

// Get 获取 Agent 详情
func (s *AgentService) Get(ctx context.Context, agentID string) (*model.Agent, error) {
	return s.agentRepo.GetByAgentID(ctx, agentID)
}

// List 发现 Agent
func (s *AgentService) List(ctx context.Context, f repo.AgentFilter) ([]*model.Agent, int64, error) {
	return s.agentRepo.List(ctx, f)
}

// GetSkills 获取 Agent 的 Skill 列表
func (s *AgentService) GetSkills(ctx context.Context, agentID string) ([]*model.AgentSkill, error) {
	return s.agentSkillRepo.ListByAgentID(ctx, agentID)
}

func (s *AgentService) syncSkills(ctx context.Context, agentID string, inputs []AgentSkillInput) error {
	skills := make([]*model.AgentSkill, 0, len(inputs))
	for _, in := range inputs {
		tagsJSON, _ := json.Marshal(in.Tags)
		inputModesJSON, _ := json.Marshal(in.InputModes)
		outputModesJSON, _ := json.Marshal(in.OutputModes)
		skills = append(skills, &model.AgentSkill{
			AgentID:     agentID,
			SkillID:     in.ID,
			Name:        in.Name,
			Description: in.Description,
			Tags:        datatypes.JSON(tagsJSON),
			InputModes:  datatypes.JSON(inputModesJSON),
			OutputModes: datatypes.JSON(outputModesJSON),
		})
	}
	return s.agentSkillRepo.ReplaceByAgentID(ctx, agentID, skills)
}

func validateRegisterRequest(req RegisterAgentRequest) error {
	if req.AgentID == "" {
		return errors.New("agent_id is required")
	}
	if !ValidateAgentID(req.AgentID) {
		return errors.New("agent_id must be 3-64 chars, lowercase alphanumeric with . _ - (will be normalized)")
	}
	if req.AgentCard.Name == "" {
		return errors.New("agent_card.name is required")
	}
	// pull 模式(默认)下 URL 可空;push 模式下必须提供
	mode := model.DeliveryModePull
	if req.DeliveryMode != nil {
		mode = *req.DeliveryMode
	}
	if mode == model.DeliveryModePush {
		if req.AgentCard.URL == "" {
			return errors.New("agent_card.url is required for push mode")
		}
		return validateAgentURL(req.AgentCard.URL)
	}
	// pull 模式下 Gateway 不会主动访问 agent,URL 纯属元信息,不校验 host 白名单
	return nil
}
