package service

import (
	"sync"

	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
)

// MonitorEventKind 监控流事件类型
type MonitorEventKind string

const (
	MonitorEventMessage     MonitorEventKind = "message"      // task 内新消息
	MonitorEventTaskCreated MonitorEventKind = "task_created" // 新 task 诞生
	MonitorEventTaskClosed  MonitorEventKind = "task_closed"  // task 关闭
	MonitorEventPing        MonitorEventKind = "ping"
)

// MonitorEvent 发给监控订阅者的事件
//
// Data 是松散结构:
//   - message:      {task_id, seq, sender_agent_id, message_id, content, created_at}
//   - task_created: {task_id, title, creator_agent_id, members, created_at}
//   - task_closed:  {task_id, status, closed_at}
type MonitorEvent struct {
	Kind MonitorEventKind `json:"kind"`
	Data any              `json:"data"`
}

// MonitorSession 一个 Web 用户的 SSE 订阅会话
// 和 InboxSession 的区别:同一个 OwnerAppID 允许多个并发会话(多 tab 浏览)
type MonitorSession struct {
	ID         string // 会话唯一 ID
	OwnerAppID string // 订阅者属于哪个账号
	MemberOf   map[string]struct{} // 该账号下的 agent_id 集合,收到事件前用于过滤
	Events     chan *MonitorEvent
	Done       chan struct{}
}

// MonitorHub 维护 app_id → 多个订阅会话 的映射
//
// 设计考虑:
//  1. 同账号多 tab:用 sessions[appID] 存 slice
//  2. 事件路由:事件带 task_id + 参与 agent 列表,hub 判断"哪些账号的 agent 在这个 task 里",只推给相关订阅者
//  3. 非阻塞:channel 满就丢事件(监控页面丢一两条不影响功能,主消息流有 /monitor/tasks/:id/messages 兜底)
type MonitorHub struct {
	mu       sync.RWMutex
	sessions map[string]map[string]*MonitorSession // appID → sessionID → session
	bufSize  int
}

func NewMonitorHub() *MonitorHub {
	return &MonitorHub{
		sessions: make(map[string]map[string]*MonitorSession),
		bufSize:  64,
	}
}

// Subscribe 创建一个新会话。caller 持有返回的 session,通过读 session.Events 获取事件,
// 退出时调 Unsubscribe。
func (h *MonitorHub) Subscribe(sessionID, ownerAppID string, memberAgentIDs []string) *MonitorSession {
	memberSet := make(map[string]struct{}, len(memberAgentIDs))
	for _, a := range memberAgentIDs {
		memberSet[a] = struct{}{}
	}
	sess := &MonitorSession{
		ID:         sessionID,
		OwnerAppID: ownerAppID,
		MemberOf:   memberSet,
		Events:     make(chan *MonitorEvent, h.bufSize),
		Done:       make(chan struct{}),
	}
	h.mu.Lock()
	if _, ok := h.sessions[ownerAppID]; !ok {
		h.sessions[ownerAppID] = make(map[string]*MonitorSession)
	}
	h.sessions[ownerAppID][sessionID] = sess
	h.mu.Unlock()
	logger.Info("monitor subscribed",
		zap.String("session_id", sessionID),
		zap.String("app_id", ownerAppID),
		zap.Int("my_agents", len(memberSet)))
	return sess
}

// Unsubscribe 移除会话
func (h *MonitorHub) Unsubscribe(sess *MonitorSession) {
	if sess == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := h.sessions[sess.OwnerAppID]; ok {
		delete(m, sess.ID)
		if len(m) == 0 {
			delete(h.sessions, sess.OwnerAppID)
		}
	}
	select {
	case <-sess.Done:
		// 已 closed
	default:
		close(sess.Done)
	}
}

// PublishTaskEvent 分发一个 task 相关事件给所有 relevant 订阅者
//
// taskMembers 是该 task 的参与 agent_id 列表。hub 据此筛选:
// 只要某个会话的 MemberOf 集合和 taskMembers 有交集,这个会话就会收到事件。
func (h *MonitorHub) PublishTaskEvent(taskMembers []string, event *MonitorEvent) {
	if len(taskMembers) == 0 || event == nil {
		return
	}
	memberSet := make(map[string]struct{}, len(taskMembers))
	for _, a := range taskMembers {
		memberSet[a] = struct{}{}
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, accountSessions := range h.sessions {
		for _, sess := range accountSessions {
			// 这个账号的 agent 是否有参与该 task
			hasIntersect := false
			for a := range sess.MemberOf {
				if _, ok := memberSet[a]; ok {
					hasIntersect = true
					break
				}
			}
			if !hasIntersect {
				continue
			}
			select {
			case sess.Events <- event:
			default:
				// 缓冲满,丢该事件(页面会通过 REST 查询补足)
			}
		}
	}
}

// BroadcastPing 定时 ping(让 SSE 长连接保活)
func (h *MonitorHub) BroadcastPing() {
	event := &MonitorEvent{Kind: MonitorEventPing, Data: map[string]string{"ts": "now"}}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, accountSessions := range h.sessions {
		for _, sess := range accountSessions {
			select {
			case sess.Events <- event:
			default:
			}
		}
	}
}
