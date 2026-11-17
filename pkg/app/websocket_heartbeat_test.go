package app

import (
	"github.com/gin-gonic/gin"
	"github.com/lxzan/gws"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWSHeartbeatGraceAndRecovery(t *testing.T) {
	start := time.Unix(1000, 0)
	var h wsHeartbeat
	require.False(t, h.expired(start.Add(25*time.Second), start, 60*time.Second, 25*time.Second))
	require.False(t, h.expired(start.Add(75*time.Second), start, 60*time.Second, 25*time.Second), "first timeout must send a probe and allow one interval")
	require.False(t, h.expired(start.Add(99*time.Second), start, 60*time.Second, 25*time.Second))
	require.True(t, h.expired(start.Add(100*time.Second), start, 60*time.Second, 25*time.Second), "dead peers must eventually close")

	// A delayed pong or inbound application heartbeat recovers the connection.
	last := start.Add(90 * time.Second)
	require.False(t, h.expired(start.Add(100*time.Second), last, 60*time.Second, 25*time.Second))
	require.False(t, h.expired(start.Add(175*time.Second), last, 60*time.Second, 25*time.Second), "a later outage gets its own grace period")
	require.True(t, h.expired(start.Add(200*time.Second), last, 60*time.Second, 25*time.Second))
}

func TestWSHeartbeatConfiguredTimeout(t *testing.T) {
	start := time.Unix(1000, 0)
	var h wsHeartbeat
	require.False(t, h.expired(start.Add(100*time.Second), start, 180*time.Second, 25*time.Second))
	require.False(t, h.expired(start.Add(200*time.Second), start, 180*time.Second, 25*time.Second))
	require.True(t, h.expired(start.Add(225*time.Second), start, 180*time.Second, 25*time.Second))
}

// Exercise the real socket deadline as well as PingLoop: a read timeout must not
// close an idle connection before the final probe has had time to return.
func TestWSHeartbeatIdleSocketGrace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := NewWebsocketServer(WSConfig{PingInterval: 1, PingWait: 1}, &websocketSecurityApp{})
	w.UseWithoutAuth("HeartbeatFixture", func(c *WebsocketClient, _ *WebSocketMessage) {
		go c.PingLoop(1)
	})
	router := gin.New()
	router.GET("/sync", w.Run())
	server := httptest.NewServer(router)
	defer server.Close()
	events := &heartbeatProbeClient{pings: make(chan struct{}, 8), closed: make(chan struct{})}
	conn, _, err := gws.NewClient(events, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http") + "/sync"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.NetConn().Close(); w.WaitAllClosed(time.Second) })
	go conn.ReadLoop()
	require.NoError(t, conn.WriteMessage(gws.OpcodeText, []byte("HeartbeatFixture|{}")))
	for i := 0; i < 2; i++ {
		select {
		case <-events.pings:
		case <-events.closed:
			t.Fatal("socket closed before grace probe")
		case <-time.After(3 * time.Second):
			t.Fatal("no ping")
		}
	}
	// A pong received during grace restores normal operation.
	require.NoError(t, conn.WritePong(nil))
	select {
	case <-events.pings:
	case <-events.closed:
		t.Fatal("pong during grace did not recover the connection")
	case <-time.After(3 * time.Second):
		t.Fatal("no ping after recovery")
	}
	// Stop responding: even a writable TCP connection must be reclaimed.
	select {
	case <-events.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("dead connection was not reclaimed")
	}
}

type heartbeatProbeClient struct {
	gws.BuiltinEventHandler
	pings  chan struct{}
	closed chan struct{}
}

func (c *heartbeatProbeClient) OnPing(_ *gws.Conn, _ []byte) { c.pings <- struct{}{} }
func (c *heartbeatProbeClient) OnClose(_ *gws.Conn, _ error) { close(c.closed) }
