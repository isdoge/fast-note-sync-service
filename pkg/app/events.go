package app

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/haierkeys/fast-note-sync-service/pkg/util"
)

const eventBufferSize = 64
const maxUserEventStreams = 16

var ErrEventStreamLimit = errors.New("event stream limit reached")
var ErrEventHubClosed = errors.New("event hub closed")

// BroadcastMeta is the explicit metadata allowlist; broadcast payloads are never marshaled.
// BroadcastMeta 是明确的元数据白名单；绝不序列化原始广播数据。
type BroadcastMeta struct {
	Path             string `json:"path"`
	PathHash         string `json:"pathHash"`
	OldPath          string `json:"oldPath"`
	OldPathHash      string `json:"oldPathHash"`
	ContentHash      string `json:"contentHash"`
	Ctime            int64  `json:"ctime"`
	Mtime            int64  `json:"mtime"`
	UpdatedTimestamp int64  `json:"updatedTimestamp"`
}

// BroadcastEvent invalidates WebGUI data without carrying its contents.
// BroadcastEvent 通知 WebGUI 数据失效，不携带正文内容。
type BroadcastEvent struct {
	V      int    `json:"v"`
	Vault  string `json:"vault"`
	Action string `json:"action"`
	BroadcastMeta
	TS int64 `json:"ts"`
}

func extractBroadcastMeta(data any) (meta BroadcastMeta) {
	value := reflect.ValueOf(data)
	for value.IsValid() && value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return
	}
	target := reflect.ValueOf(&meta).Elem()
	for i := 0; i < target.NumField(); i++ {
		field := value.FieldByName(target.Type().Field(i).Name)
		if field.IsValid() && field.Type() == target.Field(i).Type() {
			switch field.Kind() {
			case reflect.String:
				target.Field(i).SetString(field.String())
			case reflect.Int64:
				target.Field(i).SetInt(field.Int())
			}
		}
	}
	return
}

func isEventAction(action string) bool {
	switch action {
	case "NoteSyncModify", "NoteSyncDelete", "NoteSyncRename",
		"FolderSyncModify", "FolderSyncDelete", "FolderSyncRename",
		"FileSyncUpdate", "FileSyncDelete", "FileSyncRename",
		"SettingSyncModify", "SettingSyncDelete", "SettingSyncClear", "ShareSyncRefresh":
		return true
	}
	return false
}

// EventSubscriber owns an immutable filter and a bounded, never-closed event channel.
// EventSubscriber 保存不可变过滤条件及有界事件通道；事件通道永不关闭。
type EventSubscriber struct {
	ch          chan BroadcastEvent
	done        chan struct{}
	overflow    atomic.Bool
	tokenID     int64
	tokenVaults string
	vaults      map[string]struct{}
}

func (s *EventSubscriber) Events() <-chan BroadcastEvent { return s.ch }
func (s *EventSubscriber) Done() <-chan struct{}         { return s.done }
func (s *EventSubscriber) Overflowed() bool              { return s.overflow.Swap(false) }

func (s *EventSubscriber) accepts(vault string) bool {
	if !util.VerifyVaultAccess(s.tokenVaults, vault) {
		return false
	}
	if len(s.vaults) == 0 {
		return true
	}
	_, ok := s.vaults[vault]
	return ok
}

// EventHub isolates subscriptions by user and never waits for a consumer.
// EventHub 按用户隔离订阅，投递绝不等待消费者。
type EventHub struct {
	mu     sync.RWMutex
	users  map[string]map[*EventSubscriber]struct{}
	closed bool
}

func NewEventHub() *EventHub { return &EventHub{users: make(map[string]map[*EventSubscriber]struct{})} }

func (h *EventHub) Subscribe(uid string, tokenID int64, tokenVaults string, vaults []string) (*EventSubscriber, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrEventHubClosed
	}
	if len(h.users[uid]) >= maxUserEventStreams {
		return nil, ErrEventStreamLimit
	}
	sub := &EventSubscriber{ch: make(chan BroadcastEvent, eventBufferSize), done: make(chan struct{}), tokenID: tokenID, tokenVaults: tokenVaults, vaults: make(map[string]struct{})}
	for _, vault := range vaults {
		if vault != "" {
			sub.vaults[vault] = struct{}{}
		}
	}
	if h.users[uid] == nil {
		h.users[uid] = make(map[*EventSubscriber]struct{})
	}
	h.users[uid][sub] = struct{}{}
	return sub, nil
}

func (h *EventHub) Unsubscribe(uid string, sub *EventSubscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.users[uid][sub]; ok {
		delete(h.users[uid], sub)
		close(sub.done)
		if len(h.users[uid]) == 0 {
			delete(h.users, uid)
		}
	}
}

func (h *EventHub) Publish(uid, vault, action string, data any) {
	if h == nil || !isEventAction(action) {
		return
	}
	event := BroadcastEvent{V: 1, Vault: vault, Action: action, BroadcastMeta: extractBroadcastMeta(data), TS: time.Now().UnixMilli()}
	// This lock never nests with WebsocketServer.mu; sends cannot block or race with removal.
	// 此锁不与 WebsocketServer.mu 嵌套；发送不会阻塞，也不会与注销发生竞争。
	h.mu.RLock()
	defer h.mu.RUnlock()
	for sub := range h.users[uid] {
		if !sub.accepts(vault) {
			continue
		}
		select {
		case sub.ch <- event:
		default:
			sub.overflow.Store(true)
		}
	}
}

// KickToken removes matching subscriptions before notifying their handlers.
// KickToken 先移除匹配的订阅，再通知处理器退出。
func (h *EventHub) KickToken(uid string, tokenID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.users[uid] {
		if sub.tokenID == tokenID {
			delete(h.users[uid], sub)
			close(sub.done)
		}
	}
	if len(h.users[uid]) == 0 {
		delete(h.users, uid)
	}
}

func (h *EventHub) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, subs := range h.users {
		for sub := range subs {
			close(sub.done)
		}
	}
	clear(h.users)
}

// SubscribeEvents exposes the event hub without exposing its locks to handlers.
// SubscribeEvents 向处理器提供订阅接口，不暴露中心的锁。
func (w *WebsocketServer) SubscribeEvents(uid string, tokenID int64, tokenVaults string, vaults []string) (*EventSubscriber, error) {
	return w.events.Subscribe(uid, tokenID, tokenVaults, vaults)
}
func (w *WebsocketServer) UnsubscribeEvents(uid string, sub *EventSubscriber) {
	w.events.Unsubscribe(uid, sub)
}
