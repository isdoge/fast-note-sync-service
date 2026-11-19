package api_router

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalapp "github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/require"
)

type eventRecorder struct {
	*httptest.ResponseRecorder
	onFlush    func(string)
	writeErr   bool
	panicWrite bool
}

func (w *eventRecorder) Flush() {
	w.ResponseRecorder.Flush()
	if w.onFlush != nil {
		w.onFlush(w.Body.String())
	}
}
func (w *eventRecorder) Write(data []byte) (int, error) {
	if w.panicWrite {
		panic("test event writer panic")
	}
	if w.writeErr {
		return 0, errors.New("test write failed")
	}
	return w.ResponseRecorder.Write(data)
}

func eventTestHandler(t *testing.T) (*EventHandler, *pkgapp.WebsocketServer, *gin.Engine, chan struct{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	a := internalapp.NewTestApp(&internalapp.Services{})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, a)
	h := NewEventHandler(a, wss)
	h.heartbeatInterval = time.Millisecond
	shutdown := make(chan struct{})
	h.shutdown = shutdown
	router := gin.New()
	router.GET("/api/webgui/events", func(c *gin.Context) {
		c.Set("user_token", &pkgapp.UserEntity{UID: 1, TokenID: 5})
		c.Set("vaults", "A")
		h.Stream(c)
	})
	return h, wss, router, shutdown
}

func assertEventSubscriptionsRemoved(t *testing.T, wss *pkgapp.WebsocketServer) {
	t.Helper()
	for i := 0; i < 16; i++ {
		sub, err := wss.SubscribeEvents("1", 5, "", nil)
		require.NoError(t, err, "请求结束后仍然占用订阅名额")
		t.Cleanup(func() { wss.UnsubscribeEvents("1", sub) })
	}
}

func TestEventStreamHelloHeartbeatAndCancellation(t *testing.T) {
	_, wss, router, _ := eventTestHandler(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rec := &eventRecorder{ResponseRecorder: httptest.NewRecorder()}
	rec.onFlush = func(body string) {
		if strings.Contains(body, ": ping\n\n") {
			cancel()
		}
	}
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/webgui/events", nil).WithContext(ctx))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	require.True(t, strings.HasPrefix(rec.Body.String(), "event: hello\ndata: {"))
	require.Contains(t, rec.Body.String(), `"reason":"reconnect"`)
	require.Contains(t, rec.Body.String(), ": ping\n\n")
	assertEventSubscriptionsRemoved(t, wss)
}

func TestEventStreamVaultIsolationAndOverflow(t *testing.T) {
	_, wss, router, _ := eventTestHandler(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rec := &eventRecorder{ResponseRecorder: httptest.NewRecorder()}
	var once sync.Once
	rec.onFlush = func(body string) {
		once.Do(func() {
			wss.BroadcastToUser(1, code.Success.WithVault("B").WithData(struct{ Path string }{"forbidden"}), "NoteSyncModify")
			for i := 0; i < 65; i++ {
				wss.BroadcastToUser(1, code.Success.WithVault("A").WithData(struct{ Path string }{"allowed"}), "NoteSyncModify")
			}
		})
		if strings.Contains(body, "event: change") {
			cancel()
		}
	}
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/webgui/events?vault=A", nil).WithContext(ctx))
	require.NotContains(t, rec.Body.String(), "forbidden")
	require.Contains(t, rec.Body.String(), `"path":"allowed"`)
	require.Equal(t, 1, strings.Count(rec.Body.String(), `"reason":"overflow"`))
	assertEventSubscriptionsRemoved(t, wss)
}

func TestEventStreamExitAndCleanup(t *testing.T) {
	for _, mode := range []string{"shutdown", "hub close", "write error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			_, wss, router, shutdown := eventTestHandler(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			rec := &eventRecorder{ResponseRecorder: httptest.NewRecorder(), writeErr: mode == "write error", panicWrite: mode == "panic"}
			var once sync.Once
			rec.onFlush = func(string) {
				once.Do(func() {
					if mode == "shutdown" {
						close(shutdown)
					}
					if mode == "hub close" {
						wss.CloseAllConnections()
					}
				})
			}
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/webgui/events", nil).WithContext(ctx))
			require.NoError(t, ctx.Err(), "处理器只在测试兜底超时后退出")
			if mode != "hub close" {
				assertEventSubscriptionsRemoved(t, wss)
			}
		})
	}
}

func TestEventStreamLimit(t *testing.T) {
	_, wss, router, _ := eventTestHandler(t)
	for i := 0; i < 16; i++ {
		sub, err := wss.SubscribeEvents("1", 5, "", nil)
		require.NoError(t, err)
		defer wss.UnsubscribeEvents("1", sub)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/webgui/events", nil))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotContains(t, rec.Body.String(), "event: hello")
}

func TestEventStreamMaxLifetime(t *testing.T) {
	h, wss, router, _ := eventTestHandler(t)
	require.Equal(t, 30*time.Minute, h.maxLifetime)
	h.maxLifetime = 20 * time.Millisecond
	h.heartbeatInterval = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	start := time.Now()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/webgui/events", nil).WithContext(ctx))
	require.NoError(t, ctx.Err(), "处理器只在请求兜底超时后退出")
	require.GreaterOrEqual(t, time.Since(start), h.maxLifetime)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: hello\n"))
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: resync\n"))
	require.Equal(t, 2, strings.Count(rec.Body.String(), "event: "))
	assertEventSubscriptionsRemoved(t, wss)
}

// A real HTTP server is required to verify net/http's connection write deadline.
// 必须使用真实 HTTP 服务验证 net/http 的连接写截止时间；禁止监听的沙箱内跳过。
func TestEventStreamSurvivesHTTPWriteTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听回环端口：%v", err)
	}
	_, _, router, shutdown := eventTestHandler(t)
	server := &http.Server{Handler: router, WriteTimeout: time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	defer func() { close(shutdown); _ = server.Close(); <-served }()
	client := &http.Client{Timeout: 6 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/api/webgui/events")
	require.NoError(t, err)
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	start := time.Now()
	for scanner.Scan() {
		if time.Since(start) > 3*time.Second && scanner.Text() == ": ping" {
			return
		}
	}
	t.Fatalf("事件流提前中断：%v", scanner.Err())
}

type stalledEventWriter struct {
	*httptest.ResponseRecorder
	started     chan struct{}
	interrupted chan struct{}
	once        sync.Once
}

func (w *stalledEventWriter) Write([]byte) (int, error) {
	close(w.started)
	<-w.interrupted
	return 0, errors.New("write interrupted")
}
func (w *stalledEventWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		w.once.Do(func() { close(w.interrupted) })
	}
	return nil
}

func TestEventStreamCancellationInterruptsStalledWrite(t *testing.T) {
	for _, mode := range []string{"cancel", "shutdown", "kick token", "max lifetime"} {
		t.Run(mode, func(t *testing.T) {
			h, wss, router, shutdown := eventTestHandler(t)
			if mode == "max lifetime" {
				h.maxLifetime = 20 * time.Millisecond
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rec := &stalledEventWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), interrupted: make(chan struct{})}
			done := make(chan struct{})
			go func() {
				defer close(done)
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/webgui/events", nil).WithContext(ctx))
			}()
			select {
			case <-rec.started:
			case <-time.After(time.Second):
				t.Fatal("未进入写入")
			}
			switch mode {
			case "cancel":
				cancel()
			case "shutdown":
				close(shutdown)
			case "kick token":
				wss.KickToken(1, 5)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("取消、关闭、吊销或到期未中断卡住的写入")
			}
			assertEventSubscriptionsRemoved(t, wss)
		})
	}
}
