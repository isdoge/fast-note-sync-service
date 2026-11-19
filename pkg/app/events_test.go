package app

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/require"
)

func TestExtractBroadcastMeta(t *testing.T) {
	fixture := struct {
		Path             string
		PathHash         string
		OldPath          string
		OldPathHash      string
		ContentHash      string
		Ctime            int64
		Mtime            int64
		UpdatedTimestamp int64
		Content          string
		SavePath         string
	}{"new.md", "new-hash", "old.md", "old-hash", "content-hash", 10, 20, 30, "private body", "private storage"}
	want := BroadcastMeta{"new.md", "new-hash", "old.md", "old-hash", "content-hash", 10, 20, 30}
	require.Equal(t, want, extractBroadcastMeta(fixture))
	require.Equal(t, want, extractBroadcastMeta(&fixture))
	for _, data := range []any{nil, (*BroadcastMeta)(nil), "body", []byte("body"), map[string]string{"Path": "a", "Content": "body"}, struct{ Path int }{1}} {
		require.Equal(t, BroadcastMeta{}, extractBroadcastMeta(data))
	}
	payload, err := json.Marshal(BroadcastEvent{BroadcastMeta: extractBroadcastMeta(fixture)})
	require.NoError(t, err)
	require.NotContains(t, string(payload), "private body")
	require.NotContains(t, string(payload), `"content":`)
	require.NotContains(t, string(payload), "private storage")
}

func TestEventHubVaultFilters(t *testing.T) {
	for _, test := range []struct {
		name, allowed, vault string
		declared             []string
		want                 bool
	}{
		{name: "all", vault: "A", want: true},
		{name: "unrestricted empty", want: true},
		{name: "restricted", allowed: "A, B", vault: "B", want: true},
		{name: "outside token", allowed: "A", vault: "B"},
		{name: "restricted empty", allowed: "A"},
		{name: "declared", vault: "A", declared: []string{"A", "B"}, want: true},
		{name: "undeclared", vault: "C", declared: []string{"A", "B"}},
		{name: "token wins", allowed: "A", vault: "B", declared: []string{"B"}},
		{name: "token literal star", allowed: "*", vault: "A"},
		{name: "declaration literal star", vault: "A", declared: []string{"*"}},
		{name: "exact star", allowed: "*", vault: "*", declared: []string{"*"}, want: true},
		{name: "exact name", vault: "A-other", declared: []string{"A"}},
		{name: "spaces significant", vault: " A ", declared: []string{"A"}},
		{name: "declared empty rejected", declared: []string{"A"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := NewEventHub()
			sub, err := hub.Subscribe("1", test.allowed, test.declared)
			require.NoError(t, err)
			hub.Publish("2", test.vault, "NoteSyncModify", nil)
			require.Empty(t, sub.ch)
			hub.Publish("1", test.vault, "UserRefresh", nil)
			require.Empty(t, sub.ch)
			hub.Publish("1", test.vault, "NoteSyncModify", nil)
			require.Equal(t, test.want, len(sub.ch) == 1)
		})
	}
}

func TestEventHubLimitsOverflowAndClose(t *testing.T) {
	hub := NewEventHub()
	var subs []*EventSubscriber
	for i := 0; i < maxUserEventStreams; i++ {
		sub, err := hub.Subscribe("1", "", nil)
		require.NoError(t, err)
		subs = append(subs, sub)
	}
	_, err := hub.Subscribe("1", "", nil)
	require.ErrorIs(t, err, ErrEventStreamLimit)
	_, err = hub.Subscribe("2", "", nil)
	require.NoError(t, err)
	for i := 0; i < eventBufferSize+1; i++ {
		hub.Publish("1", "A", "NoteSyncModify", nil)
	}
	require.Len(t, subs[0].ch, eventBufferSize)
	require.True(t, subs[0].Overflowed())
	require.False(t, subs[0].Overflowed())
	hub.Unsubscribe("1", subs[0])
	hub.Unsubscribe("1", subs[0])
	select {
	case <-subs[0].Done():
	default:
		t.Fatal("订阅未关闭")
	}
	_, err = hub.Subscribe("1", "", nil)
	require.NoError(t, err)
	hub.Close()
	hub.Close()
	require.Empty(t, hub.users)
	for _, sub := range subs {
		select {
		case <-sub.Done():
		default:
			t.Fatal("中心关闭未通知订阅")
		}
	}
	_, err = hub.Subscribe("1", "", nil)
	require.ErrorIs(t, err, ErrEventHubClosed)
	hub.Publish("1", "A", "NoteSyncModify", nil)
}

func TestEventHubConcurrentPublishUnsubscribe(t *testing.T) {
	hub := NewEventHub()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				sub, err := hub.Subscribe(uid, "A", []string{"A"})
				if err != nil {
					return
				}
				hub.Publish(uid, "A", "NoteSyncModify", nil)
				sub.Overflowed()
				hub.Unsubscribe(uid, sub)
			}
		}(fmt.Sprint(i % 2))
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			hub.Publish("1", "A", "NoteSyncDelete", nil)
		}
	}()
	wg.Wait()
	require.Empty(t, hub.users)
}

func TestBroadcastEventsWithoutWebsocketConnections(t *testing.T) {
	w := NewWebsocketServer(WSConfig{}, &websocketSecurityApp{})
	sub, err := w.SubscribeEvents("1", "", nil)
	require.NoError(t, err)
	defer w.UnsubscribeEvents("1", sub)
	c := &WebsocketClient{Server: w, User: &UserEntity{}}
	c.User.ID = "1"
	payload := code.Success.WithVault("A").WithData(struct{ Path, Content string }{"a.md", "private body"})
	c.BroadcastResponse(payload, true, "NoteSyncModify")
	require.Len(t, sub.ch, 1)
	event := <-sub.Events()
	require.Equal(t, "a.md", event.Path)
	require.Equal(t, "NoteSyncModify", event.Action)
	data, err := json.Marshal(event)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private body")
	require.NotContains(t, string(data), `"content":`)
	require.InDelta(t, time.Now().UnixMilli(), event.TS, 1000)
	w.BroadcastToUser(1, payload, "NoteSyncDelete")
	require.Len(t, sub.ch, 1)
	require.Equal(t, "NoteSyncDelete", (<-sub.Events()).Action)
	w.CloseAllConnections()
	select {
	case <-sub.Done():
	default:
		t.Fatal("关闭服务未注销事件订阅")
	}
}
