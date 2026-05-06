package http

import (
	"sync"

	"relay-flow/internal/event"
)

const sseSubscriberBufferSize = 64

// SSEHub 维护 Gateway 进程内的 SSE 订阅关系。
// 它只管理本机连接；Redis 负责历史事件，RabbitMQ event exchange 负责跨组件事件传递。
type SSEHub struct {
	mu          sync.Mutex
	subscribers map[string]map[*SSESubscription]struct{}
}

// SSESubscription 表示一个客户端对某个 run 的实时事件订阅。
// 每个订阅都有自己的 channel，HTTP Handler 从 channel 中读取事件并写回 SSE 连接。
type SSESubscription struct {
	runID string
	ch    chan event.RunEvent
	hub   *SSEHub
	once  sync.Once
}

// NewSSEHub 创建 Gateway 本机 SSE 订阅中心。
func NewSSEHub() *SSEHub {
	return &SSEHub{
		subscribers: make(map[string]map[*SSESubscription]struct{}),
	}
}

// Subscribe 为指定 run 注册一个本机 SSE 订阅者。
func (h *SSEHub) Subscribe(runID string) *SSESubscription {
	sub := &SSESubscription{
		runID: runID,
		ch:    make(chan event.RunEvent, sseSubscriberBufferSize),
		hub:   h,
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers[runID] == nil {
		h.subscribers[runID] = make(map[*SSESubscription]struct{})
	}
	h.subscribers[runID][sub] = struct{}{}
	return sub
}

// PublishRunEvent 把事件分发给当前 Gateway 进程内订阅同一个 run 的 SSE 连接。
func (h *SSEHub) PublishRunEvent(evt event.RunEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for sub := range h.subscribers[evt.RunID] {
		select {
		case sub.ch <- evt:
		default:
			// 不主动断开连接；如果连接临时来不及读取，本次实时推送跳过，历史事件仍可从 Redis 补齐。
		}
	}
}

// Events 返回订阅者的只读事件通道。
func (s *SSESubscription) Events() <-chan event.RunEvent {
	return s.ch
}

// Close 取消订阅并关闭连接私有 channel。
func (s *SSESubscription) Close() {
	s.once.Do(func() {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()

		subs := s.hub.subscribers[s.runID]
		if subs != nil {
			delete(subs, s)
			if len(subs) == 0 {
				delete(s.hub.subscribers, s.runID)
			}
		}
		close(s.ch)
	})
}
