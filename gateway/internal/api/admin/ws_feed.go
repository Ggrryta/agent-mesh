package admin

import (
	"net/http"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/internal/middleware"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const (
	wsPingInterval = 30 * time.Second
	wsWriteWait    = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func (h *Handler) handleWSFeed(w http.ResponseWriter, r *http.Request) {
	if h.feed == nil {
		http.Error(w, "feed not available", http.StatusServiceUnavailable)
		return
	}

	uid, ok := middleware.UIDFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", zap.Error(err))
		return
	}
	defer conn.Close()

	sub := h.feed.Subscribe(uid)
	defer h.feed.Unsubscribe(sub)

	// 读 goroutine：只处理 close 和 pong。
	go func() {
		defer conn.Close()
		conn.SetReadLimit(512)
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})
		for {
			if _, _, err := conn.NextReader(); err != nil {
				break
			}
		}
	}()

	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-sub.Ch:
			if !ok {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-sub.Done():
			return
		}
	}
}
