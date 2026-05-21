package mesh

import (
	"net/http"
	"strconv"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
)

// handleGetRoster mesh 端 —— agent 通过此接口发现群内队友的能力。
// 鉴权：调用方必须是该群成员。
func (h *Handler) handleGetRoster(w http.ResponseWriter, r *http.Request) {
	if h.groups == nil || h.agents == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "group/agent domain not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	groupID := r.PathValue("group_id")

	isMember, err := h.groups.IsMember(r.Context(), groupID, claims.AgentID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	if !isMember {
		httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "not a group member")
		return
	}

	members, err := h.groups.ListMembers(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}

	agentIDs := make([]string, 0, len(members))
	for _, m := range members {
		agentIDs = append(agentIDs, m.AgentID)
	}
	skillsMap := map[string][]map[string]any{}
	if h.skills != nil && len(agentIDs) > 0 {
		raw, _ := h.skills.ListByAgentIDs(r.Context(), agentIDs)
		for aid, ss := range raw {
			for _, s := range ss {
				skillsMap[aid] = append(skillsMap[aid], map[string]any{
					"skill_id": s.SkillID, "name": s.Name, "description": s.Description,
				})
			}
		}
	}

	roster := make([]map[string]any, 0, len(members))
	for _, m := range members {
		a, err := h.agents.Get(r.Context(), m.AgentID)
		if err != nil {
			continue
		}
		roster = append(roster, map[string]any{
			"agent_id":    a.AgentID,
			"name":        a.Name,
			"description": a.Description,
			"role":        string(m.Role),
			"status":      string(a.Status),
			"skills":      skillsMap[m.AgentID],
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"roster": roster})
}

// handleGetTimeline mesh 端 —— agent 拉取 context 下的元数据时间轴。
// 鉴权简化为：调用方必须提供合法 agent JWT；具体的群组成员校验由入口检查保证
// （agent 只能拉到自己参与过的 context 的 timeline，通过 task 鉴权传递）。
func (h *Handler) handleGetTimeline(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "task domain not wired")
		return
	}
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	contextID := r.PathValue("context_id")
	sinceID, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	entries, err := h.tasks.ListTimeline(r.Context(), contextID, sinceID, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
	})
}
