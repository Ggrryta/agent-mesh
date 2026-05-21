package feed

import "sync"

const recentBufferSize = 100

// RecentBuffer 为每个 uid 保留最近 N 条事件，供断线重连时补推。
// 环形缓冲，固定内存，不会无限增长。
type RecentBuffer struct {
	mu      sync.RWMutex
	buffers map[int64]*ringBuffer
}

type ringBuffer struct {
	events [recentBufferSize]*FeedEvent
	head   int
	count  int
}

func NewRecentBuffer() *RecentBuffer {
	return &RecentBuffer{
		buffers: make(map[int64]*ringBuffer),
	}
}

// Append 追加一条事件到 uid 的环形缓冲。
func (rb *RecentBuffer) Append(uid int64, event *FeedEvent) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	buf, ok := rb.buffers[uid]
	if !ok {
		buf = &ringBuffer{}
		rb.buffers[uid] = buf
	}
	idx := (buf.head + buf.count) % recentBufferSize
	if buf.count == recentBufferSize {
		buf.head = (buf.head + 1) % recentBufferSize
	} else {
		buf.count++
	}
	buf.events[idx] = event
}

// Since 返回 lastEventID 之后的所有事件（不含 lastEventID 本身）。
// lastEventID 为空时返回空（不补推所有历史）。
func (rb *RecentBuffer) Since(uid int64, lastEventID string) []*FeedEvent {
	if lastEventID == "" {
		return nil
	}
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	buf, ok := rb.buffers[uid]
	if !ok {
		return nil
	}

	foundIdx := -1
	for i := 0; i < buf.count; i++ {
		idx := (buf.head + i) % recentBufferSize
		if buf.events[idx].EventID == lastEventID {
			foundIdx = i
			break
		}
	}
	if foundIdx == -1 {
		return nil
	}

	var result []*FeedEvent
	for i := foundIdx + 1; i < buf.count; i++ {
		idx := (buf.head + i) % recentBufferSize
		result = append(result, buf.events[idx])
	}
	return result
}
