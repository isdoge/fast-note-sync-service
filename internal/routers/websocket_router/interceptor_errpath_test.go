package websocket_router

// 拦截器两种拒绝（库访问受限、RBAC 权限不足）的响应附带 data.path（JSON 与 Protobuf
// 上行都能提取时），让插件释放占用的并发名额；写操作的补偿逻辑（回推 / 改名回滚）不变。
// Protobuf 上行的完整覆盖见 interceptor_protobuf_test.go；本文件保留 JSON 与
// 无路径 Protobuf 载荷的原有断言。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	internalapp "github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"go.uber.org/zap"
)

// deniedEvents 已删除：直接复用 newSyncDeletePeer 管道 fixture。

// gwsBuiltin 借用 ws_sync_delete_test.go 的管道 fixture：这里直接复用 newSyncDeletePeer
// 构造对端，在其 SyncDeleteFixture 回调里装好认证态后再驱动拦截器。
func TestInterceptorDeniedResponsesCarryPath(t *testing.T) {

	drive := func(t *testing.T, vaults, scope string, msgType string, payload []byte) map[string]any {
		t.Helper()
		testApp := internalapp.NewTestApp(&internalapp.Services{})
		c, messages := newSyncDeletePeer(t, testApp)
		c.User = &pkgapp.UserEntity{UID: 1}
		c.Vaults = vaults
		c.Scope = scope
		interceptor := NewMessageInterceptor(testApp)
		interceptor(c, &pkgapp.WebSocketMessage{Type: msgType, Data: payload})
		select {
		case msg := <-messages:
			prefix, payload, ok := strings.Cut(msg, "|")
			if !ok {
				t.Fatalf("消息缺少动作名前缀：%q", msg)
			}
			if prefix != msgType+"Ack" {
				t.Fatalf("拒绝响应动作名应为 %s，实际 %q", msgType+"Ack", prefix)
			}
			var res map[string]any
			if err := json.Unmarshal([]byte(payload), &res); err != nil {
				t.Fatalf("解析响应失败：%v", err)
			}
			return res
		case <-time.After(time.Second):
			t.Fatalf("未收到 %s 的拒绝响应", msgType)
			return nil
		}
	}

	t.Run("vault restricted response carries data.path", func(t *testing.T) {
		res := drive(t, "vault-a", "", NoteReceiveModify, []byte(`{"vault":"vault-b","path":"/notes/a.md"}`))
		if res["code"] != float64(315) {
			t.Fatalf("应为 315 权限受限，实际 %v", res["code"])
		}
		data, ok := res["data"].(map[string]any)
		if !ok {
			t.Fatalf("拒绝响应必须带 data 对象，实际 %T", res["data"])
		}
		if data["path"] != "/notes/a.md" {
			t.Fatalf("data.path 应为客户端上报路径，实际 %v", data["path"])
		}
	})

	t.Run("vault missing response carries data.path", func(t *testing.T) {
		res := drive(t, "vault-a", "", NoteReceiveModify, []byte(`{"path":"/notes/a.md"}`))
		if res["code"] != float64(315) {
			t.Fatalf("应为 315 权限受限，实际 %v", res["code"])
		}
		data, ok := res["data"].(map[string]any)
		if !ok {
			t.Fatalf("拒绝响应必须带 data 对象，实际 %T", res["data"])
		}
		if data["path"] != "/notes/a.md" {
			t.Fatalf("data.path 应为客户端上报路径，实际 %v", data["path"])
		}
	})

	t.Run("protobuf uplink without path field keeps no data", func(t *testing.T) {
		// Protobuf 上行：该载荷只有 vault 字段（field 1 = "abc"），没有 path 字段，
		// 库名不匹配被拒，但没有可回传的路径，data 缺省。
		res := drive(t, "vault-a", "", NoteReceiveModify, []byte{0x0a, 0x03, 'a', 'b', 'c'})
		if res["code"] != float64(315) {
			t.Fatalf("应为 315 权限受限，实际 %v", res["code"])
		}
		if _, has := res["data"]; has {
			t.Fatalf("无 path 字段的 Protobuf 载荷，data 应缺省，实际 %v", res["data"])
		}
	})
}

// TestHandlePermissionDenied_CarriesPathWithCompensation：RBAC 拒绝（读操作，无补偿触发面）
// 的响应带 data.path；编译期引用 handlePermissionDenied 保证签名连接。
func TestHandlePermissionDenied_CarriesPathWithCompensation(t *testing.T) {
	testApp := internalapp.NewTestApp(&internalapp.Services{})
	c, messages := newSyncDeletePeer(t, testApp)
	c.User = &pkgapp.UserEntity{UID: 1}
	c.Scope = "f:share" // 只有 share 功能点：note_r 拒绝（读操作，无写补偿）
	logger := zap.NewNop()

	msg := &pkgapp.WebSocketMessage{Type: NoteReceiveCheck, Data: []byte(`{"vault":"v","path":"/notes/a.md","pathHash":"h1"}`)}
	if checkRBAC(c, msg, logger, false) {
		t.Fatal("仅 share 功能点的 scope 不应放行 note_r")
	}
	select {
	case m := <-messages:
		if !strings.Contains(m, `"path":"/notes/a.md"`) {
			t.Fatalf("拒绝响应应带 data.path，实际 %q", m)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到拒绝响应")
	}
}
