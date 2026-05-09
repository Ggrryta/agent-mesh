package service

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"agent-gateway/pkg/logger"

	"go.uber.org/zap"
)

// InboxEventKind Inbox SSE 事件类型
type InboxEventKind string

const (
	InboxEventTaskMessage  InboxEventKind = "task_message"  // 新消息
	InboxEventTaskCreated  InboxEventKind = "task_created"  // 被拉入新 task
	InboxEventTaskClosed   InboxEventKind = "task_closed"   // task 关闭
	InboxEventFriendReq    InboxEventKind = "friend_request"
	InboxEventFriendAccept InboxEventKind = "friend_accept"
	InboxEventFriendRevoke InboxEventKind = "friend_revoke"
	InboxEventPing         InboxEventKind = "ping"
)

// InboxEvent 发给订阅者的事件
type InboxEvent struct {
	Kind InboxEventKind `json:"kind"`
	Data any            `json:"data"`
	Seq  uint64         `json:"seq"`
}

// InboxSession 一个 agent 的 SSE 订阅会话
type InboxSession struct {
	AgentID string
	Events  chan *InboxEvent
	Done    chan struct{}
}

// InboxHub 维护 agent_id → 订阅会话 的映射,以及事件分发
//
// 设计考虑:
//   - 每个 agent_id 同一时刻只允许一个活跃会话(多机抢占由 OnlineRegistry 兜底)
//   - 新会话接入时踢掉旧会话(旧会话 Done chan 关闭)
//   - 每个会话有自己的 buffered events chan,满了就丢最老
//   - 发送方永不阻塞,宁可丢 ping 也不卡调用链
type InboxHub struct {
	mu       sync.RWMutex
	sessions map[string]*InboxSession
	seq      uint64
	bufSize  int
}

func NewInboxHub() *InboxHub {
	return &InboxHub{
		sessions: make(map[string]*InboxSession),
		bufSize:  64,
	}
}

// Subscribe 建立订阅。若 agent_id 已有会话,踢掉旧的。
// 返回的 session 上的 Events 需要被调用方消费,Done 用于感知被踢下线。
func (h *InboxHub) Subscribe(agentID string) *InboxSession {
	h.mu.Lock()
	defer h.mu.Unlock()

	if old, ok := h.sessions[agentID]; ok {
		close(old.Done)
		logger.Info("inbox: evicting previous session", zap.String("agent_id", agentID))
	}
	s := &InboxSession{
		AgentID: agentID,
		Events:  make(chan *InboxEvent, h.bufSize),
		Done:    make(chan struct{}),
	}
	h.sessions[agentID] = s
	logger.Info("inbox: subscribed", zap.String("agent_id", agentID),
		zap.Int("total_sessions", len(h.sessions)))
	return s
}

// Unsubscribe 清理会话。幂等。
func (h *InboxHub) Unsubscribe(agentID string, s *InboxSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, ok := h.sessions[agentID]
	if !ok || cur != s {
		return
	}
	delete(h.sessions, agentID)
	logger.Info("inbox: unsubscribed", zap.String("agent_id", agentID),
		zap.Int("total_sessions", len(h.sessions)))
}

// Publish 向指定 agent 推送事件。非阻塞。
// 返回 true 表示 agent 在线且送入队列;false 表示离线或队列已满。
func (h *InboxHub) Publish(agentID string, kind InboxEventKind, data any) bool {
	h.mu.RLock()
	s, ok := h.sessions[agentID]
	sessionCount := len(h.sessions)
	h.mu.RUnlock()
	if !ok {
		logger.Warn("inbox: publish target not subscribed",
			zap.String("agent_id", agentID),
			zap.String("kind", string(kind)),
			zap.Int("total_sessions", sessionCount))
		return false
	}

	h.mu.Lock()
	h.seq++
	seq := h.seq
	h.mu.Unlock()

	evt := &InboxEvent{Kind: kind, Data: data, Seq: seq}
	select {
	case s.Events <- evt:
		return true
	default:
		logger.Warn("inbox: session buffer full, dropping event",
			zap.String("agent_id", agentID), zap.String("kind", string(kind)))
		return false
	}
}

// IsConnected 查询某 agent 的 SSE 会话是否活跃(本机 Hub 维度)
func (h *InboxHub) IsConnected(agentID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.sessions[agentID]
	return ok
}

// StartPingLoop 启动周期性 ping,保持 SSE 连接不被中间 LB 切断
func (h *InboxHub) StartPingLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.broadcastPing()
			}
		}
	}()
}

func (h *InboxHub) broadcastPing() {
	h.mu.RLock()
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	h.mu.RUnlock()
	for _, id := range ids {
		h.Publish(id, InboxEventPing, map[string]any{"ts": time.Now().UnixMilli()})
	}
}

// EncodeSSE 把 InboxEvent 格式化成 SSE 一帧
func EncodeSSE(e *InboxEvent) []byte {
	data, _ := json.Marshal(e)
	out := make([]byte, 0, len(data)+32)
	out = append(out, "event: "...)
	out = append(out, e.Kind...)
	out = append(out, "\ndata: "...)
	out = append(out, data...)
	out = append(out, "\n\n"...)
	return out
}
