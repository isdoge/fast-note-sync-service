package app

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/lxzan/gws"
	"github.com/stretchr/testify/require"
)

func TestAcceptsBroadcast(t *testing.T) {
	for _, test := range []struct {
		name, allowedVault, broadcastVault string
		declaredVaults                     []string
		want                               bool
	}{
		{name: "unrestricted without declarations rejects vault-specific broadcast", broadcastVault: "vault-a"},
		{name: "unrestricted without declarations accepts empty vault", want: true},
		{name: "unrestricted accepts declared vault", broadcastVault: "vault-a", declaredVaults: []string{"vault-a"}, want: true},
		{name: "unrestricted rejects other vault", broadcastVault: "vault-b", declaredVaults: []string{"vault-a"}},
		{name: "unrestricted with declarations still accepts empty vault", declaredVaults: []string{"vault-a"}, want: true},
		{name: "restricted rejects allowed vault before declaration", allowedVault: "vault-a", broadcastVault: "vault-a"},
		{name: "restricted accepts allowed declared vault", allowedVault: "vault-a", broadcastVault: "vault-a", declaredVaults: []string{"vault-a"}, want: true},
		{name: "restricted rejects other vault without declarations", allowedVault: "vault-a", broadcastVault: "vault-b"},
		{name: "restricted rejects other vault despite declarations", allowedVault: "vault-a", broadcastVault: "vault-b", declaredVaults: []string{"vault-b"}},
		{name: "restricted rejects allowed but undeclared vault", allowedVault: "vault-a", broadcastVault: "vault-a", declaredVaults: []string{"vault-b"}},
		{name: "restricted rejects empty vault", allowedVault: "vault-a"},
		{name: "multiple allowed vaults", allowedVault: "vault-a, vault-b", broadcastVault: "vault-b", declaredVaults: []string{"vault-b"}, want: true},
		{name: "vault names are exact not prefixes", allowedVault: "vault-a", broadcastVault: "vault-a-other"},
		{name: "asterisk is a literal name", allowedVault: "vault-*", broadcastVault: "vault-a"},
		{name: "spaces are significant inside vault names", allowedVault: "vault-a", broadcastVault: " vault-a "},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := &WebsocketClient{Vaults: test.allowedVault}
			for _, vault := range test.declaredVaults {
				c.recordBoundVault(&vaultBindReq{Vault: vault})
			}
			if got := c.acceptsBroadcast(test.broadcastVault); got != test.want {
				t.Fatalf("acceptsBroadcast(%q)=%v, want %v (allowed=%q, declared=%v)", test.broadcastVault, got, test.want, test.allowedVault, test.declaredVaults)
			}
		})
	}
}

func TestDeclaredVaultsConcurrentAccess(t *testing.T) {
	c := &WebsocketClient{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.recordBoundVault(&vaultBindReq{Vault: "vault-a"})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = c.acceptsBroadcast("vault-a")
			}
		}()
	}
	wg.Wait()
}

type broadcastTestEvents struct {
	gws.BuiltinEventHandler
	messages chan string
}

func (e *broadcastTestEvents) OnMessage(_ *gws.Conn, message *gws.Message) {
	defer message.Close()
	e.messages <- message.Data.String()
}

type broadcastTestClient struct {
	client   *WebsocketClient
	messages <-chan string
}

// TestWebsocketBroadcastVaultIsolation drives both fan-out paths through real sockets.
// TestWebsocketBroadcastVaultIsolation 用真实 WebSocket 连接验证两条广播路径的库隔离。
func TestWebsocketBroadcastVaultIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := NewWebsocketServer(WSConfig{}, &websocketSecurityApp{})
	clients := make(chan *WebsocketClient, 8)
	w.UseWithoutAuth("BroadcastFixture", func(c *WebsocketClient, _ *WebSocketMessage) {
		c.User = &UserEntity{UID: 1}
		c.User.ID = "1"
		c.UserClients = w.AddUserClient(c)
		clients <- c
	})
	router := gin.New()
	router.GET("/sync", w.Run())
	server := httptest.NewServer(router)
	defer server.Close()

	connect := func(t *testing.T) broadcastTestClient {
		t.Helper()
		events := &broadcastTestEvents{messages: make(chan string, 16)}
		conn, _, err := gws.NewClient(events, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/sync"})
		require.NoError(t, err)
		go conn.ReadLoop()
		require.NoError(t, conn.WriteMessage(gws.OpcodeText, []byte("BroadcastFixture|{}")))
		t.Cleanup(func() { _ = conn.NetConn().Close() })
		select {
		case c := <-clients:
			return broadcastTestClient{client: c, messages: events.messages}
		case <-time.After(time.Second):
			t.Fatal("未取得广播测试连接")
			return broadcastTestClient{}
		}
	}
	t.Cleanup(func() { w.WaitAllClosed(time.Second) })

	sender := connect(t)
	receiver := connect(t)
	restricted := connect(t)
	otherVault := connect(t)
	restricted.client.Vaults = "vault-a"
	sender.client.recordBoundVault(&vaultBindReq{Vault: "vault-a"})
	receiver.client.recordBoundVault(&vaultBindReq{Vault: "vault-a"})
	receiver.client.Vaults = "vault-a"
	otherVault.client.Vaults = "vault-b"
	restricted.client.recordBoundVault(&vaultBindReq{Vault: "vault-a"})
	otherVault.client.recordBoundVault(&vaultBindReq{Vault: "vault-b"})

	t.Run("websocket broadcast", func(t *testing.T) {
		sender.client.BroadcastResponse(code.Success.WithData(map[string]string{"content": "secret"}).WithVault("vault-a"), true, "NoteSyncModify")
		assertBroadcastReceived(t, receiver.messages, "NoteSyncModify", "vault-a")
		assertBroadcastReceived(t, restricted.messages, "NoteSyncModify", "vault-a")
		assertNoBroadcast(t, otherVault.messages, "WS 广播投给声明其他库的连接")
		assertNoBroadcast(t, sender.messages, "WS 广播未排除自己")
	})

	t.Run("REST and MCP broadcast", func(t *testing.T) {
		w.BroadcastToUser(1, code.Success.WithData(map[string]string{"content": "rest-secret"}).WithVault("vault-a"), "NoteSyncModify")
		assertBroadcastReceived(t, sender.messages, "NoteSyncModify", "vault-a")
		assertBroadcastReceived(t, receiver.messages, "NoteSyncModify", "vault-a")
		assertBroadcastReceived(t, restricted.messages, "NoteSyncModify", "vault-a")
		assertNoBroadcast(t, otherVault.messages, "REST/MCP 广播投给声明其他库的连接")
	})

	t.Run("token allowlist wins", func(t *testing.T) {
		otherVault.client.Vaults = "vault-b"
		otherVault.client.broadcastVaultsMu.Lock()
		otherVault.client.declaredVaults = nil
		otherVault.client.broadcastVaultsMu.Unlock()
		w.BroadcastToUser(1, code.Success.WithData(map[string]string{"content": "wrong-token"}).WithVault("vault-a"), "NoteSyncModify")
		assertBroadcastReceived(t, sender.messages, "NoteSyncModify", "vault-a")
		assertBroadcastReceived(t, receiver.messages, "NoteSyncModify", "vault-a")
		assertBroadcastReceived(t, restricted.messages, "NoteSyncModify", "vault-a")
		assertNoBroadcast(t, otherVault.messages, "广播投给令牌白名单外的连接")
	})

	t.Run("missing vault only reaches unrestricted connections", func(t *testing.T) {
		otherVault.client.Vaults = ""
		sender.client.BroadcastResponse(code.Success.WithData(map[string]string{"content": "missing-vault"}), true, "UserRefresh")
		assertBroadcastReceived(t, otherVault.messages, "UserRefresh", "")
		assertNoBroadcast(t, receiver.messages, "无库名广播投给受限连接")
		assertNoBroadcast(t, restricted.messages, "无库名广播投给受限连接")
	})
}

// Check action and vault explicitly so an unrelated response cannot pass the isolation assertions.
// 明确检查动作和库名，避免无关响应让隔离断言误通过。
func assertBroadcastReceived(t *testing.T, messages <-chan string, action, vault string) {
	t.Helper()
	select {
	case message := <-messages:
		prefix, payload, ok := strings.Cut(message, "|")
		require.True(t, ok)
		require.Equal(t, action, prefix)
		if vault == "" {
			require.NotContains(t, payload, `"vault"`)
		} else {
			require.Contains(t, payload, `"vault":"`+vault+`"`)
		}
	case <-time.After(time.Second):
		t.Fatalf("未收到 %s 的库 %s 广播", action, vault)
	}
}

func assertNoBroadcast(t *testing.T, messages <-chan string, description string) {
	t.Helper()
	select {
	case message := <-messages:
		t.Fatalf("%s：意外收到 %q", description, message)
	case <-time.After(100 * time.Millisecond):
	}
}
