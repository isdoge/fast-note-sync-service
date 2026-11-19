package websocket_router

// 受限库令牌在 JSON 与 Protobuf 两种真实上行帧下都必须正确取到库名并放行 / 拒绝，
// 拒绝响应带 data.path（改名再带 oldPath）；Protobuf 上行的 RBAC 拒绝仍能取到改名
// 回滚数据。这里通过真实 gws 连接把帧写进 OnMessage，覆盖从信封解码、前置拦截器到
// 响应编码的整条链路。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalapp "github.com/haierkeys/fast-note-sync-service/internal/app"
	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/lxzan/gws"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type interceptorFrame struct {
	opcode gws.Opcode
	data   []byte
}

type interceptorFrameEvents struct {
	gws.BuiltinEventHandler
	frames chan interceptorFrame
}

func (e *interceptorFrameEvents) OnMessage(_ *gws.Conn, msg *gws.Message) {
	defer msg.Close()
	e.frames <- interceptorFrame{opcode: msg.Opcode, data: append([]byte(nil), msg.Data.Bytes()...)}
}

type interceptorResponseWriter struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (w *interceptorResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

type interceptorPeer struct {
	conn    *gws.Conn
	frames  <-chan interceptorFrame
	handled chan struct{}
}

// newInterceptorPeer builds a real WebSocket connection (net.Pipe, no listening
// port), registers the Protobuf hooks and the production message interceptor,
// and drives the authenticated test client into JSON or Protobuf mode.
// newInterceptorPeer 用真实 WebSocket 连接（net.Pipe，不监听端口）注册 Protobuf 钩子与
// 生产消息拦截器，并把已认证的测试客户端驱动到 JSON 或 Protobuf 模式。
func newInterceptorPeer(t *testing.T, vaults, scope string, useProtobuf bool, allowedAction string) (*interceptorPeer, *pkgapp.WebsocketClient) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	testApp := internalapp.NewTestApp(&internalapp.Services{})
	rawServer, peerConn := net.Pipe()
	serverConn := net.Conn(rawServer)
	t.Cleanup(func() { _ = serverConn.Close(); _ = peerConn.Close() })

	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{PingWait: 60}, testApp)
	RegisterProtobufHooks(wss)

	clients := make(chan *pkgapp.WebsocketClient, 1)
	handled := make(chan struct{}, 4)
	wss.UseWithoutAuth("InterceptorFixture", func(c *pkgapp.WebsocketClient, _ *pkgapp.WebSocketMessage) {
		c.User = &pkgapp.UserEntity{UID: 1}
		c.Vaults = vaults
		c.Scope = scope
		if useProtobuf {
			c.SetClientInfoForTest("interceptor-test", "desktop", "1.0", nil, "", true)
		}
		clients <- c
	})
	if allowedAction != "" {
		wss.Use(allowedAction, func(*pkgapp.WebsocketClient, *pkgapp.WebSocketMessage) {
			handled <- struct{}{}
		})
	}
	wss.UseInterceptor(NewMessageInterceptor(testApp))

	upgradeDone := make(chan error, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(serverConn))
		if err == nil {
			ctx, _ := gin.CreateTestContext(&interceptorResponseWriter{httptest.NewRecorder(), serverConn})
			ctx.Request = req
			wss.Run()(ctx)
		}
		upgradeDone <- err
	}()

	events := &interceptorFrameEvents{frames: make(chan interceptorFrame, 16)}
	peer, _, err := gws.NewClientFromConn(events, &gws.ClientOption{Addr: "ws://interceptor.test/sync"}, peerConn)
	require.NoError(t, err)
	require.NoError(t, <-upgradeDone)
	go peer.ReadLoop()

	require.NoError(t, peer.WriteMessage(gws.OpcodeText, []byte("InterceptorFixture|{}")))
	var c *pkgapp.WebsocketClient
	select {
	case c = <-clients:
	case <-time.After(time.Second):
		t.Fatal("未取得拦截器测试客户端")
	}
	t.Cleanup(func() {
		c.WsCancel()
		_ = peerConn.Close()
		_ = serverConn.Close()
		wss.WaitAllClosed(time.Second)
	})
	return &interceptorPeer{conn: peer, frames: events.frames, handled: handled}, c
}

func (p *interceptorPeer) sendProtobuf(t *testing.T, action string, inner []byte) {
	t.Helper()
	envelope, err := proto.Marshal(&v1.WSMessage{Type: action, Data: inner})
	require.NoError(t, err)
	require.NoError(t, p.conn.WriteMessage(gws.OpcodeBinary, append([]byte("pb"), envelope...)))
}

func (p *interceptorPeer) sendJSON(t *testing.T, action string, payload map[string]any) {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, p.conn.WriteMessage(gws.OpcodeText, []byte(action+"|"+string(body))))
}

// readProtobufResponse decodes one Protobuf response frame into its action and
// the JSON-decoded data payload (unhandled response actions fall back to JSON
// bytes inside the envelope, matching the plugin's default branch).
// readProtobufResponse 解出一条 Protobuf 响应帧的动作名，以及经 JSON 解码的 data 载荷
// （未专门编码的响应动作在信封里是 JSON 字节，与插件 default 分支一致）。
func (p *interceptorPeer) readProtobufResponse(t *testing.T) (string, int, map[string]any) {
	t.Helper()
	select {
	case frame := <-p.frames:
		require.Equal(t, gws.OpcodeBinary, frame.opcode)
		require.True(t, bytes.HasPrefix(frame.data, []byte("pb")), "响应应为 pb 帧：%q", frame.data)
		var envelope v1.WSMessage
		require.NoError(t, proto.Unmarshal(frame.data[2:], &envelope))
		var response v1.WSResponse
		require.NoError(t, proto.Unmarshal(envelope.Data, &response))
		var data map[string]any
		if len(response.Data) > 0 {
			require.NoError(t, json.Unmarshal(response.Data, &data), "响应 data 应为 JSON：%s", response.Data)
		}
		return envelope.Type, int(response.Code), data
	case <-time.After(time.Second):
		t.Fatal("未收到响应帧")
		return "", 0, nil
	}
}

func (p *interceptorPeer) readJSONResponse(t *testing.T) (string, int, map[string]any) {
	t.Helper()
	select {
	case frame := <-p.frames:
		require.Equal(t, gws.OpcodeText, frame.opcode)
		action, payload, ok := strings.Cut(string(frame.data), "|")
		require.True(t, ok, "响应缺少动作名前缀：%q", frame.data)
		var res struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(payload), &res))
		return action, res.Code, res.Data
	case <-time.After(time.Second):
		t.Fatal("未收到响应帧")
		return "", 0, nil
	}
}

func (p *interceptorPeer) requireHandled(t *testing.T) {
	t.Helper()
	select {
	case <-p.handled:
	case <-time.After(time.Second):
		t.Fatal("允许的请求未进入业务处理器")
	}
}

func (p *interceptorPeer) requireNotHandled(t *testing.T) {
	t.Helper()
	select {
	case <-p.handled:
		t.Fatal("被拒绝的请求不应进入业务处理器")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestInterceptorVaultAccess_ProtobufFrames(t *testing.T) {
	for _, tc := range []struct {
		name        string
		vault       string
		wantAllowed bool
	}{
		{name: "allowed vault passes", vault: "vault-a", wantAllowed: true},
		{name: "other vault denied", vault: "vault-b"},
		{name: "missing vault denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer, _ := newInterceptorPeer(t, "vault-a", "", true, NoteReceiveModify)
			inner, err := proto.Marshal(&v1.NoteModifyOrCreateRequest{
				Vault: tc.vault, Path: "/notes/a.md", PathHash: "h1",
			})
			require.NoError(t, err)
			peer.sendProtobuf(t, NoteReceiveModify, inner)

			if tc.wantAllowed {
				peer.requireHandled(t)
				return
			}
			peer.requireNotHandled(t)
			action, codeValue, data := peer.readProtobufResponse(t)
			require.Equal(t, NoteReceiveModify+"Ack", action)
			require.Equal(t, 315, codeValue)
			require.Equal(t, "/notes/a.md", data["path"])
		})
	}
}

func TestInterceptorVaultAccess_JSONFrames(t *testing.T) {
	for _, tc := range []struct {
		name        string
		vault       string
		wantAllowed bool
	}{
		{name: "allowed vault passes", vault: "vault-a", wantAllowed: true},
		{name: "other vault denied", vault: "vault-b"},
		{name: "missing vault denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer, _ := newInterceptorPeer(t, "vault-a", "", false, NoteReceiveModify)
			payload := map[string]any{"path": "/notes/a.md", "pathHash": "h1"}
			if tc.vault != "" {
				payload["vault"] = tc.vault
			}
			peer.sendJSON(t, NoteReceiveModify, payload)

			if tc.wantAllowed {
				peer.requireHandled(t)
				return
			}
			peer.requireNotHandled(t)
			action, codeValue, data := peer.readJSONResponse(t)
			require.Equal(t, NoteReceiveModify+"Ack", action)
			require.Equal(t, 315, codeValue)
			require.Equal(t, "/notes/a.md", data["path"])
		})
	}
}

// TestInterceptorVaultDenial_ProtobufRenameCarriesOldPath 覆盖改名拒绝的 oldPath：
// 插件按新路径占名额、回滚需要旧路径，两者都必须能从 Protobuf 上行取到。
func TestInterceptorVaultDenial_ProtobufRenameCarriesOldPath(t *testing.T) {
	peer, _ := newInterceptorPeer(t, "vault-a", "", true, NoteReceiveRename)
	inner, err := proto.Marshal(&v1.NoteRenameRequest{
		Vault: "vault-b", Path: "/notes/new.md", PathHash: "h-new",
		OldPath: "/notes/old.md", OldPathHash: "h-old",
	})
	require.NoError(t, err)
	peer.sendProtobuf(t, NoteReceiveRename, inner)

	peer.requireNotHandled(t)
	action, codeValue, data := peer.readProtobufResponse(t)
	require.Equal(t, NoteReceiveRename+"Ack", action)
	require.Equal(t, 315, codeValue)
	require.Equal(t, "/notes/new.md", data["path"])
	require.Equal(t, "/notes/old.md", data["oldPath"])
}

// TestInterceptorRBACDenial_ProtobufRenameRollsBack 证明 Protobuf 上行时 RBAC 拒绝
// 仍能提取改名数据并发出反向改名（NoteSyncRename），而不只是回一条错误。
func TestInterceptorRBACDenial_ProtobufRenameRollsBack(t *testing.T) {
	peer, c := newInterceptorPeer(t, "", "f:share", true, NoteReceiveRename)
	c.Scope = "f:share" // share 功能点不含 note_w
	inner, err := proto.Marshal(&v1.NoteRenameRequest{
		Vault: "v", Path: "/notes/new.md", PathHash: "h-new",
		OldPath: "/notes/old.md", OldPathHash: "h-old",
	})
	require.NoError(t, err)
	peer.sendProtobuf(t, NoteReceiveRename, inner)

	// 先收到拒绝应答，再收到反向改名；两者都是 pb 帧。
	action, codeValue, _ := peer.readProtobufResponse(t)
	require.Equal(t, NoteReceiveRename+"Ack", action)
	require.Equal(t, 315, codeValue)

	select {
	case frame := <-peer.frames:
		require.Equal(t, gws.OpcodeBinary, frame.opcode)
		var envelope v1.WSMessage
		require.NoError(t, proto.Unmarshal(frame.data[2:], &envelope))
		require.Equal(t, string(NoteSyncRename), envelope.Type)
		var response v1.WSResponse
		require.NoError(t, proto.Unmarshal(envelope.Data, &response))
		var rename v1.NoteSyncRenameMessage
		require.NoError(t, proto.Unmarshal(response.Data, &rename))
		require.Equal(t, "/notes/old.md", rename.Path)
		require.Equal(t, "/notes/new.md", rename.OldPath)
	case <-time.After(time.Second):
		t.Fatal("Protobuf 改名被拒后未发出反向改名")
	}
}
