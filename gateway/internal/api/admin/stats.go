package admin

import (
	"net/http"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/friendship"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
)

// Dashboard 数据：Recent Activity（最近事件流）+ Action Items（待办）+ Continue（最近 task）。

type dashSummary struct {
	ActiveAgents   int    `json:"active_agents"`
	TotalAgents    int    `json:"total_agents"`
	PrimaryAgentID string `json:"primary_agent_id,omitempty"`
}

type dashActionItem struct {
	Kind    string `json:"kind"`     // friend_request | task_failed | agent_draining
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	RefID   string `json:"ref_id,omitempty"`   // 关联实体 ID（agent_id / task_id / friendship_id）
	Created string `json:"created_at,omitempty"`
}

type dashRecentTask struct {
	TaskID    string `json:"task_id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

type dashboardResp struct {
	Summary     dashSummary       `json:"summary"`
	ActionItems []dashActionItem  `json:"action_items"`
	RecentTasks []dashRecentTask  `json:"recent_tasks"`
}

// handleMyDashboard 聚合 Dashboard 数据。Actionable，不是装饰。
func (h *Handler) handleMyDashboard(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}

	resp := dashboardResp{
		ActionItems: []dashActionItem{},
		RecentTasks: []dashRecentTask{},
	}

	// 1) Summary + 收集 normal agents
	var normalAgents []*agent.Agent
	if h.agents != nil {
		ags, err := h.agents.ListByOwner(r.Context(), uid)
		if err == nil {
			for _, a := range ags {
				if a.Kind == agent.KindVirtualUser {
					continue
				}
				normalAgents = append(normalAgents, a)
				resp.Summary.TotalAgents++
				if a.Status == agent.StatusActive {
					resp.Summary.ActiveAgents++
					if resp.Summary.PrimaryAgentID == "" {
						resp.Summary.PrimaryAgentID = a.AgentID
					}
				}
				// Action item: draining 中的 agent
				if a.Status == agent.StatusDraining {
					resp.ActionItems = append(resp.ActionItems, dashActionItem{
						Kind:    "agent_draining",
						Title:   "Agent draining",
						Detail:  a.Name + " is shutting down — confirm or cancel.",
						RefID:   a.AgentID,
						Created: a.UpdatedAt.UTC().Format(time.RFC3339),
					})
				}
			}
		}
	}

	// 2) 待处理好友请求（聚合所有 normal agent 的 incoming pending）
	if h.friends != nil {
		for _, a := range normalAgents {
			fs, err := h.friends.ListIncomingPending(r.Context(), uid, a.AgentID)
			if err != nil {
				continue
			}
			for _, f := range fs {
				if f.Status != friendship.StatusPending {
					continue
				}
				resp.ActionItems = append(resp.ActionItems, dashActionItem{
					Kind:    "friend_request",
					Title:   "Friend request",
					Detail:  f.FromAgentID + " → " + a.AgentID,
					RefID:   formatID(f.ID),
					Created: f.CreatedAt.UTC().Format(time.RFC3339),
				})
			}
		}
	}

	// 3) 最近 task：用户名下所有 normal agent + 虚拟 user-agent 参与的 task
	if h.tasks != nil {
		agentIDs := make([]string, 0, len(normalAgents)+1)
		for _, a := range normalAgents {
			agentIDs = append(agentIDs, a.AgentID)
		}
		// 虚拟 user-agent 也参与（用户从前端下令的 from）
		agentIDs = append(agentIDs, "virtual-user-"+formatID(uid))

		tasks, err := h.tasks.ListRecentByAgents(r.Context(), agentIDs, 10)
		if err == nil {
			for _, t := range tasks {
				resp.RecentTasks = append(resp.RecentTasks, dashRecentTask{
					TaskID:    t.TaskID,
					From:      t.FromAgentID,
					To:        t.ToAgentID,
					Status:    string(t.Status),
					UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
				})
				// Action item: failed task
				if string(t.Status) == "failed" {
					resp.ActionItems = append(resp.ActionItems, dashActionItem{
						Kind:    "task_failed",
						Title:   "Task failed",
						Detail:  t.FromAgentID + " → " + t.ToAgentID,
						RefID:   t.TaskID,
						Created: t.UpdatedAt.UTC().Format(time.RFC3339),
					})
				}
			}
		}
	}

	httpx.WriteJSON(w, http.StatusOK, resp)
}

// handleMyStats 保留作为兼容（如有其它调用方）。
func (h *Handler) handleMyStats(w http.ResponseWriter, r *http.Request) {
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	stats := struct {
		ActiveAgents   int    `json:"active_agents"`
		TotalAgents    int    `json:"total_agents"`
		FriendCount    int    `json:"friend_count"`
		GroupCount     int    `json:"group_count"`
		PrimaryAgentID string `json:"primary_agent_id,omitempty"`
	}{}
	agents, err := h.agents.ListByOwner(r.Context(), uid)
	if err == nil {
		for _, a := range agents {
			if a.Kind == agent.KindVirtualUser {
				continue
			}
			stats.TotalAgents++
			if a.Status == agent.StatusActive {
				stats.ActiveAgents++
			}
			if stats.PrimaryAgentID == "" && a.Status == agent.StatusActive {
				stats.PrimaryAgentID = a.AgentID
			}
		}
	}
	if h.friends != nil && stats.PrimaryAgentID != "" {
		fs, err := h.friends.ListFriends(r.Context(), uid, stats.PrimaryAgentID, friendship.StatusAccepted)
		if err == nil {
			stats.FriendCount = len(fs)
		}
	}
	if h.groups != nil {
		gs, err := h.groups.ListByOwner(r.Context(), uid)
		if err == nil {
			stats.GroupCount = len(gs)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, stats)
}

func formatID(id int64) string {
	// 标准库 fmt 在 stats.go 已用过；这里避免重复 import 写个简版。
	const digits = "0123456789"
	if id == 0 {
		return "0"
	}
	neg := id < 0
	if neg {
		id = -id
	}
	var b [20]byte
	i := len(b)
	for id > 0 {
		i--
		b[i] = digits[id%10]
		id /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}