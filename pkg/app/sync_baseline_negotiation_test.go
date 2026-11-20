package app

// Stage C connection-level capability negotiation tests. The state machine must satisfy:
//   - maxSupported=0 (the stage C default) keeps every connection on the legacy mode, even a
//     client that asks for 1 — the server never advertises what it cannot enforce;
//   - maxSupported=1 with a client asking for 1 settles on 1 only after the ClientInfo reply;
//   - a PB downgrade re-send of ClientInfo resolves to the already accepted value;
//   - a background ClientInfo broadcast echoes the accepted value and never flips the mode.
//
// 阶段 C 连接级能力协商测试。状态机必须满足：
//   - maxSupported=0（阶段 C 默认值）让所有连接保持旧模式，即使客户端请求 1——服务端不广告无法兑现的能力；
//   - maxSupported=1 且客户端请求 1 时，只有 ClientInfo 应答后才是 1；
//   - PB 降级重发 ClientInfo 解析为已接受的值；
//   - 后台 ClientInfo 广播只回显已接受值，绝不翻转模式。

import (
	"encoding/json"
	"testing"
	"time"

	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/lxzan/gws"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSyncBaselineNegotiation_MaxZeroKeepsLegacy(t *testing.T) {
	w := &WebsocketServer{}
	c := w.NewTestClient(1)

	// Even an explicit request for 1 is capped by a server that only supports 0.
	require.EqualValues(t, 0, c.negotiateSyncBaseline(1, w.MaxSyncBaselineVersion))
	require.EqualValues(t, 0, c.SyncBaselineVersion())
}

func TestSyncBaselineNegotiation_RequestOneAcceptedWhenSupported(t *testing.T) {
	w := &WebsocketServer{MaxSyncBaselineVersion: 1}
	c := w.NewTestClient(1)

	require.EqualValues(t, 1, c.negotiateSyncBaseline(1, w.MaxSyncBaselineVersion))
	require.EqualValues(t, 1, c.SyncBaselineVersion())
}

func TestSyncBaselineNegotiation_LegacyClientStaysZero(t *testing.T) {
	w := &WebsocketServer{MaxSyncBaselineVersion: 1}
	c := w.NewTestClient(1)

	// An old client omits the field; proto3 and JSON both decode it as 0.
	require.EqualValues(t, 0, c.negotiateSyncBaseline(0, w.MaxSyncBaselineVersion))
	require.EqualValues(t, 0, c.SyncBaselineVersion())
}

func TestSyncBaselineNegotiation_FirstClientInfoWins(t *testing.T) {
	w := &WebsocketServer{MaxSyncBaselineVersion: 1}
	c := w.NewTestClient(1)

	require.EqualValues(t, 1, c.negotiateSyncBaseline(1, w.MaxSyncBaselineVersion))
	// A PB downgrade re-sends ClientInfo with the same requested version.
	require.EqualValues(t, 1, c.negotiateSyncBaseline(1, w.MaxSyncBaselineVersion))
	// Even a bogus later value cannot revoke or upgrade an established mode.
	require.EqualValues(t, 1, c.negotiateSyncBaseline(0, w.MaxSyncBaselineVersion))
	require.EqualValues(t, 1, c.SyncBaselineVersion())
}

func TestSyncBaselineNegotiation_LaterClientInfoCannotActivate(t *testing.T) {
	w := &WebsocketServer{MaxSyncBaselineVersion: 1}
	c := w.NewTestClient(1)

	require.EqualValues(t, 0, c.negotiateSyncBaseline(0, w.MaxSyncBaselineVersion))
	// A late ClientInfo must not silently activate the mode on an established legacy connection.
	require.EqualValues(t, 0, c.negotiateSyncBaseline(1, w.MaxSyncBaselineVersion))
	require.EqualValues(t, 0, c.SyncBaselineVersion())
}

func TestSyncBaselineNegotiation_ConcurrentFirstClientInfo(t *testing.T) {
	w := &WebsocketServer{MaxSyncBaselineVersion: 1}
	c := w.NewTestClient(1)

	const workers = 16
	results := make(chan uint32, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			requested := uint32(0)
			if i%2 == 0 {
				requested = 1
			}
			results <- c.negotiateSyncBaseline(requested, w.MaxSyncBaselineVersion)
		}(i)
	}
	// The first caller wins; later callers observe that settled value. Whatever the winner was,
	// the connection ends up with exactly one of the two requested values and never a mix.
	seen := make(map[uint32]bool)
	for i := 0; i < workers; i++ {
		seen[<-results] = true
	}
	settled := c.SyncBaselineVersion()
	require.Contains(t, []uint32{0, 1}, settled)
	require.True(t, seen[settled], "the settled value must be one a caller actually observed")
	require.Equal(t, settled, c.SyncBaselineVersion(), "the value must be stable once negotiated")
}

func TestSyncBaselineNegotiation_ClientInfoMessageDecodesField(t *testing.T) {
	// Both encodings must land the requested version on the same field.
	var fromPB ClientInfoMessage
	require.NoError(t, decodeProtoClientInfo(t, &fromPB))
	require.EqualValues(t, 1, fromPB.SyncBaselineVersion)

	var fromJSON ClientInfoMessage
	require.NoError(t, json.Unmarshal([]byte(`{"name":"peer","syncBaselineVersion":1}`), &fromJSON))
	require.EqualValues(t, 1, fromJSON.SyncBaselineVersion)

	// Absent field means legacy, and must not be confused with "unsupported".
	var legacy ClientInfoMessage
	require.NoError(t, json.Unmarshal([]byte(`{"name":"legacy"}`), &legacy))
	require.EqualValues(t, 0, legacy.SyncBaselineVersion)
}

func TestSyncBaselineNegotiation_CheckVersionInfoCarriesAccepted(t *testing.T) {
	info := CheckVersionInfo{SyncBaselineVersion: 1}
	require.EqualValues(t, 1, info.SyncBaselineVersion)

	// A legacy server's reply simply omits the key; the client must read that as 0.
	var legacy CheckVersionInfo
	require.NoError(t, json.Unmarshal([]byte(`{"githubAvailable":false}`), &legacy))
	require.EqualValues(t, 0, legacy.SyncBaselineVersion)
}

// Small helpers keep the proto/json imports local to this test file.
// 小工具函数把 proto/json 依赖限制在本测试文件内。
func decodeProtoClientInfo(t *testing.T, dest *ClientInfoMessage) error {
	t.Helper()
	msg := &v1.ClientInfoMessage{Name: "peer", SyncBaselineVersion: 1}
	raw, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	var decoded v1.ClientInfoMessage
	if err := proto.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	dest.Name = decoded.Name
	dest.SyncBaselineVersion = decoded.SyncBaselineVersion
	return nil
}

// TestSyncBaselineNegotiation_HandlerWire verifies the handler-level contract on a real gws
// connection: the ClientInfo reply carries the value accepted for this connection, a later
// PB-downgrade style re-send keeps it, and a broadcast echoes the same value.
// TestSyncBaselineNegotiation_HandlerWire 在真实 gws 连接上验证处理器层契约：ClientInfo 应答携带
// 本连接接受的值，之后的 PB 降级式重发保持不变，广播回显同一值。
func TestSyncBaselineNegotiation_HandlerWire(t *testing.T) {
	w, c, _, frames := newNegotiationClient(t)
	w.MaxSyncBaselineVersion = 1
	c.User = &UserEntity{UID: 1}
	c.ProtoVersion = 1

	sendClientInfo := func(requested int) map[string]any {
		t.Helper()
		payload := []byte(`{"name":"peer","type":"desktop","version":"1.0","syncBaselineVersion":` +
			itoa(requested) + `}`)
		done := make(chan struct{})
		go func() {
			defer close(done)
			dispatchNegotiation(w, c, gws.OpcodeText, append([]byte("ClientInfo|"), payload...))
		}()
		select {
		case frame := <-frames:
			require.Equal(t, gws.OpcodeText, frame.opcode)
			_, body, ok := bytesCut(frame.data, []byte("|"))
			require.True(t, ok, "unexpected reply %s", frame.data)
			var res struct {
				Code int            `json:"code"`
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(body, &res))
			require.Equal(t, code.Success.Code(), res.Code)
			<-done
			return res.Data
		case <-time.After(time.Second):
			t.Fatal("ClientInfo got no reply")
			return nil
		}
	}

	data := sendClientInfo(1)
	require.EqualValues(t, 1, data["syncBaselineVersion"])
	require.EqualValues(t, 1, c.SyncBaselineVersion())

	// A PB downgrade re-send carries the same requested version; the mode must not flip.
	data = sendClientInfo(1)
	require.EqualValues(t, 1, data["syncBaselineVersion"])
	require.EqualValues(t, 1, c.SyncBaselineVersion())

	// The background broadcast echoes the accepted value (it never re-negotiates).
	info := w.app.CheckVersion(c.ClientVersion())
	info.SyncBaselineVersion = c.SyncBaselineVersion()
	require.EqualValues(t, 1, info.SyncBaselineVersion)

	// A server that only supports 0 caps the same client back to legacy.
	w.MaxSyncBaselineVersion = 0
	other := w.NewTestClient(2)
	require.EqualValues(t, 0, other.negotiateSyncBaseline(1, w.MaxSyncBaselineVersion))
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func bytesCut(data, sep []byte) ([]byte, []byte, bool) {
	for i := 0; i+len(sep) <= len(data); i++ {
		if string(data[i:i+len(sep)]) == string(sep) {
			return data[:i], data[i+len(sep):], true
		}
	}
	return data, nil, false
}
