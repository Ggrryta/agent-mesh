package admin

import (
	"errors"
	"net/http"

	"github.com/Ggrryta/agent-mesh/gateway/internal/api/httpx"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/group"
	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"
)

// handleListMyGroups 列出当前用户的所有群组，带成员头像和能力标签聚合。
func (h *Handler) handleListMyGroups(w http.ResponseWriter, r *http.Request) {
	if h.groups == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "group domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}

	groups, err := h.groups.ListByOwner(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}

	type groupSummary struct {
		GroupID     string   `json:"group_id"`
		ContextID   string   `json:"context_id"`
		Name        string   `json:"name"`
		Members     []string `json:"members"`
		MemberCount int      `json:"member_count"`
		Skills      []string `json:"skills"`
	}

	out := make([]groupSummary, 0, len(groups))
	for _, g := range groups {
		members, err := h.groups.ListMembers(r.Context(), g.GroupID)
		if err != nil {
			continue
		}
		memberIDs := make([]string, 0, len(members))
		for _, m := range members {
			memberIDs = append(memberIDs, m.AgentID)
		}

		skillSet := map[string]bool{}
		if h.skills != nil && len(memberIDs) > 0 {
			if raw, err := h.skills.ListByAgentIDs(r.Context(), memberIDs); err == nil {
				for _, ss := range raw {
					for _, s := range ss {
						skillSet[s.Name] = true
					}
				}
			}
		}
		skills := make([]string, 0, len(skillSet))
		for s := range skillSet {
			skills = append(skills, s)
		}

		out = append(out, groupSummary{
			GroupID:     g.GroupID,
			ContextID:   g.ContextID,
			Name:        g.Name,
			Members:     memberIDs,
			MemberCount: len(memberIDs),
			Skills:      skills,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"groups": out})
}

// ─── 群组 API ────────────────────────────────────────────────────

type createGroupReq struct {
	GroupID string `json:"group_id"`
	Name    string `json:"name"`
	AgentID string `json:"agent_id"`
}

type addMemberReq struct {
	AgentID string `json:"agent_id"`
}

func (h *Handler) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if h.groups == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "group domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	var req createGroupReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid body")
		return
	}
	if req.GroupID == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "group_id required")
		return
	}

	g, err := h.groups.Create(r.Context(), req.GroupID, "", req.Name, uid, req.AgentID)
	if err != nil {
		if errors.Is(err, group.ErrGroupIDExists) {
			httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, "group_id already exists")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"group_id":   g.GroupID,
		"context_id": g.ContextID,
		"name":       g.Name,
	})
}

func (h *Handler) handleAddGroupMember(w http.ResponseWriter, r *http.Request) {
	if h.groups == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "group domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	groupID := r.PathValue("group_id")
	var req addMemberReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid body")
		return
	}
	if req.AgentID == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "agent_id required")
		return
	}

	if err := h.groups.AddMember(r.Context(), groupID, req.AgentID, uid); err != nil {
		switch {
		case errors.Is(err, group.ErrGroupNotFound):
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "group not found")
		case errors.Is(err, group.ErrNotGroupOwner):
			httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "not group owner")
		case errors.Is(err, group.ErrAlreadyMember):
			httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, "already a member")
		case errors.Is(err, group.ErrAgentNotAllowed):
			httpx.WriteError(w, http.StatusForbidden, httpx.CodeForbidden, "target agent must be your own or your friend")
		default:
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		}
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "added"})
}

func (h *Handler) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	if h.groups == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "group domain not wired")
		return
	}
	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeTokenInvalid, "missing auth")
		return
	}
	groupID := r.PathValue("group_id")
	agentID := r.PathValue("agent_id")

	if err := h.groups.RemoveMember(r.Context(), groupID, agentID, uid); err != nil {
		switch {
		case errors.Is(err, group.ErrGroupNotFound):
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "group not found")
		case errors.Is(err, group.ErrNotGroupOwner):
			httpx.WriteError(w, http.StatusForbidden, httpx.CodeNotOwner, "not group owner")
		case errors.Is(err, group.ErrNotMember):
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "not a member")
		default:
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		}
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (h *Handler) handleListGroupMembers(w http.ResponseWriter, r *http.Request) {
	if h.groups == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "group domain not wired")
		return
	}
	groupID := r.PathValue("group_id")
	members, err := h.groups.ListMembers(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}
	type memberResp struct {
		AgentID string `json:"agent_id"`
		Role    string `json:"role"`
	}
	out := make([]memberResp, 0, len(members))
	for _, m := range members {
		out = append(out, memberResp{AgentID: m.AgentID, Role: string(m.Role)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"members": out})
}

// handleGetRoster 返回群组 roster：成员列表 + 每人的 AgentCard + skills。
// Agent 通过这个接口发现队友能力。
func (h *Handler) handleGetRoster(w http.ResponseWriter, r *http.Request) {
	if h.groups == nil || h.agents == nil {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, "group/agent domain not wired")
		return
	}
	groupID := r.PathValue("group_id")
	members, err := h.groups.ListMembers(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
		return
	}

	type skillSummary struct {
		SkillID     string `json:"skill_id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	type rosterEntry struct {
		AgentID     string         `json:"agent_id"`
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Role        string         `json:"role"`
		Status      string         `json:"status"`
		Skills      []skillSummary `json:"skills"`
	}

	out := make([]rosterEntry, 0, len(members))
	agentIDs := make([]string, 0, len(members))
	for _, m := range members {
		agentIDs = append(agentIDs, m.AgentID)
	}

	// 批量拉 skills
	skillsMap, _ := func() (map[string][]skillSummary, error) {
		if h.skills == nil {
			return map[string][]skillSummary{}, nil
		}
		raw, err := h.skills.ListByAgentIDs(r.Context(), agentIDs)
		if err != nil {
			return nil, err
		}
		m := map[string][]skillSummary{}
		for aid, ss := range raw {
			for _, s := range ss {
				m[aid] = append(m[aid], skillSummary{
					SkillID: s.SkillID, Name: s.Name, Description: s.Description,
				})
			}
		}
		return m, nil
	}()

	for _, m := range members {
		a, err := h.agents.Get(r.Context(), m.AgentID)
		if err != nil {
			// agent 不存在时跳过（可能是刚删）
			continue
		}
		out = append(out, rosterEntry{
			AgentID:     a.AgentID,
			Name:        a.Name,
			Description: a.Description,
			Role:        string(m.Role),
			Status:      string(a.Status),
			Skills:      skillsMap[m.AgentID],
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"roster": out})
}
