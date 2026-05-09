package handler

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-gateway/internal/repo"
	"agent-gateway/internal/service"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const gatewayVersion = "1.0.0"

// GatewayCardHandler 暴露 Gateway 自身的 A2A AgentCard
type GatewayCardHandler struct {
	agentRepo      *repo.AgentRepo
	agentSkillRepo *repo.AgentSkillRepo
	cache          *service.AgentCache
	gatewayName    string
	gatewayURL     string // Gateway 对外地址，用于 AgentCard.url
}

func NewGatewayCardHandler(
	agentRepo *repo.AgentRepo,
	agentSkillRepo *repo.AgentSkillRepo,
	cache *service.AgentCache,
	gatewayName, gatewayURL string,
) *GatewayCardHandler {
	return &GatewayCardHandler{
		agentRepo:      agentRepo,
		agentSkillRepo: agentSkillRepo,
		cache:          cache,
		gatewayName:    gatewayName,
		gatewayURL:     gatewayURL,
	}
}

// agentCardSkill A2A spec AgentCard.skills[] 元素
type agentCardSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
	// 扩展字段：标明来自哪个 Agent
	AgentID   string `json:"x-agent-id,omitempty"`
	AgentName string `json:"x-agent-name,omitempty"`
}

type gatewayCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
}

type gatewayAgentCard struct {
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	URL          string               `json:"url"`
	Version      string               `json:"version"`
	Capabilities gatewayCapabilities  `json:"capabilities"`
	Skills       []agentCardSkill     `json:"skills"`
}

// GetCard GET /.well-known/agent-card.json
// 聚合所有 Active Agent 的 Skill，返回 Gateway 自身的 AgentCard
func (h *GatewayCardHandler) GetCard(ctx context.Context, c *app.RequestContext) {
	agents, _, err := h.agentRepo.List(ctx, repo.AgentFilter{})
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var skills []agentCardSkill
	for _, agent := range agents {
		agentSkills, err := h.agentSkillRepo.ListByAgentID(ctx, agent.AgentID)
		if err != nil {
			continue
		}
		for _, s := range agentSkills {
			skill := agentCardSkill{
				ID:          fmt.Sprintf("%s/%s", agent.AgentID, s.SkillID),
				Name:        s.Name,
				Description: s.Description,
				AgentID:     agent.AgentID,
				AgentName:   agent.Name,
			}
			if len(s.Tags) > 0 {
				_ = json.Unmarshal(s.Tags, &skill.Tags)
			}
			if len(s.InputModes) > 0 {
				_ = json.Unmarshal(s.InputModes, &skill.InputModes)
			}
			if len(s.OutputModes) > 0 {
				_ = json.Unmarshal(s.OutputModes, &skill.OutputModes)
			}
			skills = append(skills, skill)
		}
	}

	card := gatewayAgentCard{
		Name:        h.gatewayName,
		Description: "A2A Agent Gateway — unified entry point for all registered agents",
		URL:         h.gatewayURL,
		Version:     gatewayVersion,
		Capabilities: gatewayCapabilities{
			Streaming:         true,
			PushNotifications: false,
		},
		Skills: skills,
	}

	c.JSON(consts.StatusOK, card)
}
