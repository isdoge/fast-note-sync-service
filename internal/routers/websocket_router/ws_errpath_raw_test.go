package websocket_router

// 占并发名额的动作：服务层看到的路径是归一化后的（反斜杠折叠为正斜杠），
// 但回给客户端的 data.path 必须是客户端发来的原始路径，否则插件 releaseSlot
// 用的键与 waitForSlot 时登记的键不一致，名额泄漏。

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestErrPath_BackslashEchoesClientRawPath(t *testing.T) {
	m := newErrPathMocks(t)
	// 服务层仍拿到归一化后的路径，保证既有同步契约不变。
	m.noteSvc.On("Delete", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.NoteDeleteRequest) bool {
		return req.Path == "a/b.md"
	})).Return(nil, code.ErrorDBQuery).Once()

	c, messages := newSyncDeletePeer(t, m.testApp)
	NewNoteWSHandler(m.testApp).NoteDelete(c, wsMsg(NoteReceiveDelete, map[string]any{
		"vault": "v", "path": `a\b.md`, "pathHash": "h",
	}))

	res := syncDeleteMessage(t, messages, NoteReceiveDelete)
	require.Greater(t, res["code"], float64(299))
	require.Equal(t, `a\b.md`, res["data"].(map[string]any)["path"])
}

func TestErrPath_InvalidPathCarriesClientRawPath(t *testing.T) {
	m := newErrPathMocks(t)
	c, messages := newSyncDeletePeer(t, m.testApp)
	NewNoteWSHandler(m.testApp).NoteDelete(c, wsMsg(NoteReceiveDelete, map[string]any{
		"vault": "v", "path": "a/../b.md", "pathHash": "h",
	}))

	select {
	case raw := <-messages:
		var res struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &res), raw)
		require.Greater(t, res.Code, 299)
		require.Equal(t, "a/../b.md", res.Data["path"])
	case <-time.After(time.Second):
		t.Fatal("非法路径未回错误响应")
	}
}

func TestClientSlotPath_ProtobufRawPathAndJSONFallback(t *testing.T) {
	inner, err := proto.Marshal(&v1.NoteDeleteRequest{
		Vault: "v", Path: `a\b.md`, PathHash: "h",
	})
	require.NoError(t, err)
	c := &pkgapp.WebsocketClient{Server: &pkgapp.WebsocketServer{}}
	require.Equal(t, `a\b.md`, clientSlotPath(c, &pkgapp.WebSocketMessage{Type: NoteReceiveDelete, Data: inner}, "a/b.md"))
	require.Equal(t, `a\b.md`, clientSlotPath(c, &pkgapp.WebSocketMessage{Type: NoteReceiveDelete, Data: []byte(`{"path":"a\\b.md"}`)}, "a/b.md"))
	require.Equal(t, "a/b.md", clientSlotPath(c, &pkgapp.WebSocketMessage{Type: NoteReceiveDelete, Data: []byte(`{}`)}, "a/b.md"))
}
