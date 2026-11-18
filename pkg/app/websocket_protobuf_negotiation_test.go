package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/lxzan/gws"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/protobuf/proto"
)

type negotiationApp struct {
	websocketSecurityApp
	logger      *zap.Logger
	pingStopped chan struct{}
	waitPing    bool
}

func (a *negotiationApp) Logger() *zap.Logger { return a.logger }

func (*negotiationApp) GetAuthTokenKey() string              { return "negotiation-test" }
func (*negotiationApp) Version() VersionInfo                 { return VersionInfo{} }
func (*negotiationApp) CheckVersion(string) CheckVersionInfo { return CheckVersionInfo{} }
func (*negotiationApp) SyncChunkNums() (int, int)            { return 1, 1 }
func (*negotiationApp) PipelineWindows() (int, int)          { return 1, 1 }
func (*negotiationApp) Validator() ValidatorInterface        { return nil }

type negotiationFrame struct {
	opcode gws.Opcode
	data   []byte
}
type negotiationEvents struct {
	gws.BuiltinEventHandler
	frames chan negotiationFrame
}

func (e *negotiationEvents) OnMessage(_ *gws.Conn, m *gws.Message) {
	defer m.Close()
	e.frames <- negotiationFrame{m.Opcode, append([]byte(nil), m.Data.Bytes()...)}
}

type negotiationWriter struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (w *negotiationWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

// Hold the auth Write's return after bytes reach the peer, reproducing the real race window.
// 鉴权字节到达对端后暂缓 Write 返回，确定性重现原来的竞态窗口。
type negotiationConn struct {
	net.Conn
	written chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *negotiationConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if bytes.Contains(p, []byte("Authorization|")) {
		c.once.Do(func() { close(c.written) })
		<-c.release
	}
	return n, err
}

// Build a real gws connection over net.Pipe, with dispatch driven directly (no listening port).
// 用 net.Pipe 构造真实 gws 连接，直接驱动分发，无需监听端口。
func newNegotiationClient(t *testing.T) (*WebsocketServer, *WebsocketClient, *negotiationConn, <-chan negotiationFrame) {
	t.Helper()
	raw, peerConn := net.Pipe()
	held := &negotiationConn{Conn: raw, written: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { _ = raw.Close(); _ = peerConn.Close() })
	testApp := &negotiationApp{pingStopped: make(chan struct{})}
	testApp.logger = zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(io.Discard), zapcore.InfoLevel), zap.Hooks(func(entry zapcore.Entry) error {
		if entry.Message == "WS Client Close Ping" {
			close(testApp.pingStopped)
		}
		return nil
	}))
	w := NewWebsocketServer(WSConfig{PingInterval: 3600}, testApp)
	result := make(chan *gws.Conn, 1)
	upgradeErr := make(chan error, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(held))
		if err != nil {
			upgradeErr <- err
			return
		}
		conn, err := gws.NewUpgrader(&gws.BuiltinEventHandler{}, nil).Upgrade(&negotiationWriter{httptest.NewRecorder(), held}, req)
		upgradeErr <- err
		result <- conn
	}()
	events := &negotiationEvents{frames: make(chan negotiationFrame, 8)}
	peer, _, err := gws.NewClientFromConn(events, &gws.ClientOption{Addr: "ws://negotiation.test/sync"}, peerConn)
	require.NoError(t, err)
	require.NoError(t, <-upgradeErr)
	c := w.NewTestClient(1)
	c.User = nil
	c.conn = <-result
	c.done = make(chan struct{})
	c.Protocol = "protobuf"
	c.ProtoVersion = 2
	c.PbEnabled = true
	w.AddClient(c)
	go peer.ReadLoop()
	t.Cleanup(func() {
		close(c.done)
		c.WsCancel()
		if testApp.waitPing {
			select {
			case <-testApp.pingStopped:
			case <-time.After(time.Second):
				t.Error("PingLoop did not stop")
			}
		}
	})
	return w, c, held, events.frames
}

func dispatchNegotiation(w *WebsocketServer, c *WebsocketClient, opcode gws.Opcode, data []byte) {
	w.OnMessage(c.conn, &gws.Message{Opcode: opcode, Data: bytes.NewBuffer(data)})
}

func negotiationPacket(t *testing.T) []byte {
	t.Helper()
	inner, err := proto.Marshal(&v1.ClientInfoMessage{Name: "test", Type: "desktop", Version: "1.0", Protobuf: true})
	require.NoError(t, err)
	envelope, err := proto.Marshal(&v1.WSMessage{Type: "ClientInfo", Data: inner})
	require.NoError(t, err)
	return append([]byte("pb"), envelope...)
}

func installNegotiationHooks(w *WebsocketServer, encoded chan<- string) {
	w.EnvelopeDecoder = func(data []byte) (string, []byte, error) {
		var env v1.WSMessage
		err := proto.Unmarshal(data, &env)
		return env.Type, env.Data, err
	}
	w.ProtobufDecoder = func(action string, data []byte, obj any) (bool, error) {
		var info v1.ClientInfoMessage
		if err := proto.Unmarshal(data, &info); err != nil {
			return false, err
		}
		*obj.(*ClientInfoMessage) = ClientInfoMessage{Name: info.Name, Type: info.Type, Version: info.Version, Protobuf: info.Protobuf}
		return true, nil
	}
	w.ProtobufEncoder = func(action string, _ *Res) ([]byte, error) {
		encoded <- action
		data, err := proto.Marshal(&v1.WSMessage{Type: action})
		return append([]byte("pb"), data...), err
	}
}

func TestWebsocketProtobufNegotiation_AuthWriteWindow(t *testing.T) {
	w, c, held, frames := newNegotiationClient(t)
	encoded := make(chan string, 4)
	installNegotiationHooks(w, encoded)
	w.UseUserVerify(func(*WebsocketClient, int64) (*UserSelectEntity, error) { return &UserSelectEntity{UID: 1}, nil })
	token, err := NewTokenManager(TokenConfig{SecretKey: "negotiation-test"}).Generate(1, "test", "", 0, "")
	require.NoError(t, err)
	var release sync.Once
	unblock := func() { release.Do(func() { close(held.release) }) }
	t.Cleanup(unblock)
	w.app.(*negotiationApp).waitPing = true
	authDone := make(chan struct{})
	go func() { defer close(authDone); w.Authorization(c, &WebSocketMessage{Data: []byte(token)}) }()
	select {
	case <-held.written:
	case <-time.After(time.Second):
		t.Fatal("auth response did not reach the pipe")
	}
	// This checkpoint is after auth bytes were written but before outbound switching.
	// 此时鉴权字节已写出，但出站开关尚未切换。
	require.False(t, c.UseProtobuf())
	select {
	case frame := <-frames:
		require.Equal(t, gws.OpcodeText, frame.opcode)
		require.True(t, bytes.HasPrefix(frame.data, []byte("Authorization|")))
		var response struct {
			Data struct {
				Ack bool `json:"protobufAck"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(bytes.TrimPrefix(frame.data, []byte("Authorization|")), &response))
		require.True(t, response.Data.Ack)
	case <-time.After(time.Second):
		t.Fatal("missing auth response")
	}
	infoDone := make(chan struct{})
	packet := negotiationPacket(t)
	go func() { defer close(infoDone); dispatchNegotiation(w, c, gws.OpcodeBinary, packet) }()
	select {
	case action := <-encoded:
		require.Equal(t, "ClientInfo", action)
	case <-time.After(time.Second):
		t.Fatal("negotiated pb ClientInfo was dropped or failed to decode before outbound switch")
	}
	unblock()
	select {
	case <-authDone:
	case <-time.After(time.Second):
		t.Fatal("auth handler stuck")
	}
	select {
	case <-infoDone:
	case <-time.After(time.Second):
		t.Fatal("ClientInfo handler stuck")
	}
	select {
	case frame := <-frames:
		require.Equal(t, gws.OpcodeBinary, frame.opcode)
		require.True(t, bytes.HasPrefix(frame.data, []byte("pb")))
		var env v1.WSMessage
		require.NoError(t, proto.Unmarshal(frame.data[2:], &env))
		require.Equal(t, "ClientInfo", env.Type)
	case <-time.After(time.Second):
		t.Fatal("missing pb ClientInfo response")
	}
	require.Equal(t, "test", c.ClientName())
	// Explicit JSON guarantee also holds if ClientInfo won before auth serialization.
	// 即使 ClientInfo 先完成升级，鉴权序列化也仍必须使用 JSON。
	go c.ToResponse(code.Success, "Authorization")
	select {
	case frame := <-frames:
		require.Equal(t, gws.OpcodeText, frame.opcode)
	case <-time.After(time.Second):
		t.Fatal("missing repeated auth response")
	}
}

func TestWebsocketProtobufNegotiation_RejectUnnegotiated(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous", true: "authenticated"}[authenticated], func(t *testing.T) {
			w, c, _, _ := newNegotiationClient(t)
			if authenticated {
				c.User = &UserEntity{UID: 1}
			} else {
				c.setAcceptProtobuf(true)
			}
			calls := 0
			w.EnvelopeDecoder = func([]byte) (string, []byte, error) { calls++; return "ClientInfo", nil, nil }
			w.UseWithoutAuth("ClientInfo", func(*WebsocketClient, *WebSocketMessage) { calls++ })
			dispatchNegotiation(w, c, gws.OpcodeBinary, negotiationPacket(t))
			require.Zero(t, calls, "pb must be rejected before decoding or dispatch")
		})
	}
}

func TestWebsocketProtobufNegotiation_LegacyClientInfo(t *testing.T) {
	w, c, _, frames := newNegotiationClient(t)
	c.User = &UserEntity{UID: 1}
	c.ProtoVersion = 1
	encoded := make(chan string, 4)
	installNegotiationHooks(w, encoded)
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatchNegotiation(w, c, gws.OpcodeText, []byte(`ClientInfo|{"name":"legacy","type":"desktop","version":"1.0","protobuf":true}`))
	}()
	select {
	case frame := <-frames:
		require.Equal(t, gws.OpcodeBinary, frame.opcode)
	case <-time.After(time.Second):
		t.Fatal("legacy ClientInfo did not upgrade")
	}
	<-done
	require.True(t, c.UseProtobuf())
	require.Equal(t, "legacy", c.ClientName())
}

// TestWebsocketProtobufNegotiation_JSONTextFrameOnProtobufConnection 已协商 Protobuf 的连接上，
// 客户端降级时发来的 JSON 文本帧必须按 JSON 处理：ClientInfo(protobuf=false) 要能关闭本连接的
// Protobuf 并以文本帧应答，而不是被当成 Protobuf 解码失败拒绝。
func TestWebsocketProtobufNegotiation_JSONTextFrameOnProtobufConnection(t *testing.T) {
	w, c, _, frames := newNegotiationClient(t)
	c.User = &UserEntity{UID: 1}
	c.setAcceptProtobuf(true)
	c.setUseProtobuf(true)
	encoded := make(chan string, 4)
	installNegotiationHooks(w, encoded)
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatchNegotiation(w, c, gws.OpcodeText, []byte(`ClientInfo|{"name":"fallback","type":"desktop","version":"1.0","protobuf":false}`))
	}()
	select {
	case frame := <-frames:
		require.Equal(t, gws.OpcodeText, frame.opcode, "downgrade must be answered as a JSON text frame: %s", frame.data)
		_, payload, ok := bytes.Cut(frame.data, []byte("|"))
		require.True(t, ok, "unexpected reply %s", frame.data)
		var res struct {
			Code int `json:"code"`
		}
		require.NoError(t, json.Unmarshal(payload, &res))
		require.Equal(t, code.Success.Code(), res.Code)
	case <-time.After(time.Second):
		t.Fatal("ClientInfo downgrade got no reply")
	}
	<-done
	require.False(t, c.UseProtobuf())
	require.False(t, c.acceptsProtobuf())
	require.Equal(t, "fallback", c.ClientName())
	require.Empty(t, encoded, "nothing may be Protobuf-encoded after the downgrade")
}

func TestIsJSONObjectPayload(t *testing.T) {
	inner, err := proto.Marshal(&v1.ClientInfoMessage{Name: "{\"a\":1}", Protobuf: true})
	require.NoError(t, err)
	for _, tc := range []struct {
		data []byte
		want bool
	}{
		{[]byte(`{"protobuf":false}`), true},
		{[]byte(`{}`), true},
		{[]byte(`{not json`), false},
		{[]byte(`[1,2]`), false},
		{nil, false},
		{inner, false},
	} {
		require.Equal(t, tc.want, isJSONObjectPayload(tc.data), "%q", tc.data)
	}
}
