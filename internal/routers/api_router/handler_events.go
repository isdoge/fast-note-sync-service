package api_router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"go.uber.org/zap"
)

// EventHandler serves metadata-only WebGUI invalidations.
// EventHandler 提供仅含元数据的 WebGUI 失效通知。
type EventHandler struct {
	*Handler
	heartbeatInterval time.Duration
	maxLifetime       time.Duration
	closeGrace        time.Duration
	shutdown          <-chan struct{}
}

func NewEventHandler(a *app.App, wss *pkgapp.WebsocketServer) *EventHandler {
	return &EventHandler{Handler: NewHandlerWithWSS(a, wss), heartbeatInterval: 20 * time.Second, maxLifetime: 30 * time.Minute, closeGrace: 2 * time.Second, shutdown: a.ShutdownCh()}
}

func (h *EventHandler) Stream(c *gin.Context) {
	// Log every panic without logging query tokens or broadcast contents.
	// 记录所有 panic，不记录查询串令牌或广播内容。
	defer func() {
		if recover() != nil {
			h.App.Logger().Error("WebGUI event stream panic", zap.String("path", c.Request.URL.Path), zap.ByteString("stack", debug.Stack()))
			if !c.Writer.Written() {
				c.Status(http.StatusInternalServerError)
			}
			c.Abort()
		}
	}()
	uid := strconv.FormatInt(pkgapp.GetUID(c), 10)
	// Each repeated vault= parameter is one complete vault name; commas are legal in vault names.
	// 每个重复的 vault= 参数都是一个完整的库名（库名可以包含逗号），因此不按逗号拆分。
	var vaults []string
	for _, vault := range c.QueryArray("vault") {
		if vault != "" {
			vaults = append(vaults, vault)
		}
	}
	sub, err := h.WSS.SubscribeEvents(uid, pkgapp.GetTokenID(c), c.GetString("vaults"), vaults)
	if err != nil {
		if errors.Is(err, pkgapp.ErrEventStreamLimit) {
			// Tell the client how long to wait before reconnecting; the WebGUI defaults to 60s without it.
			// 告知客户端重连前需要等待的秒数；前端在没有该头时默认退避 60 秒。
			c.Header("Retry-After", "60")
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	defer h.WSS.UnsubscribeEvents(uid, sub)
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	// Limit each stream so reconnecting clients authenticate again.
	// 限制单条流的存活时间，让客户端重连时重新鉴权。
	streamContext, cancel := context.WithTimeout(c.Request.Context(), h.maxLifetime)
	defer cancel()
	// Interrupt stalled writes on cancellation, expiry or shutdown; join before Gin reuses c.
	// 取消、到期或关闭时中断卡住的写入；退出前等待监视协程结束，避免 Gin 复用 c 时发生竞争。
	finished, watcherDone := make(chan struct{}), make(chan struct{})
	requestDone := streamContext.Done()
	go func() {
		defer close(watcherDone)
		select {
		case <-finished:
			return
		case <-requestDone:
		case <-h.shutdown:
		case <-sub.Done():
		}
		_ = controller.SetWriteDeadline(time.Now())
	}()
	defer func() {
		close(finished)
		<-watcherDone
		// The watcher expires the write deadline on exit; restore a grace period so the terminating chunk can be written.
		// 监视协程退出时会把写截止时间设为当前时间，这里重设宽限期，让分块传输的结束块有机会写出。
		_ = controller.SetWriteDeadline(time.Now().Add(h.closeGrace))
	}()
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	// The handler overrides this connection's write deadline, so HTTP/1.x connections must not be reused.
	// With server.write-timeout: 0, net/http does not reset the deadline before the next request, so a stale
	// deadline would break later responses on the same connection; "Connection: close" closes it after this one.
	// HTTP/2 deadlines are per-stream and its server turns "Connection: close" into a whole-connection graceful
	// shutdown, so the header is omitted there.
	// 处理函数改过这条连接的写截止时间；服务端配置 server.write-timeout: 0 时 net/http 不会在下一个请求开始前
	// 重置写截止时间，复用连接会让后续响应写失败，因此 HTTP/1.x 用 Connection: close 让服务端在本响应后关闭连接。
	// HTTP/2 的写截止时间按流生效，且其服务端会把 Connection: close 当作整条连接的优雅关闭，不能写。
	if c.Request.ProtoMajor == 1 {
		c.Header("Connection", "close")
	}
	c.Header("X-Accel-Buffering", "no")
	write := func(kind string, data any) bool {
		payload, err := json.Marshal(data)
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", kind, payload); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	signal := func(reason string) map[string]any {
		return map[string]any{"v": 1, "reason": reason, "ts": time.Now().UnixMilli()}
	}
	if !write("hello", map[string]any{"v": 1, "ts": time.Now().UnixMilli()}) {
		return
	}
	if !write("resync", signal("reconnect")) {
		return
	}
	ticker := time.NewTicker(h.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-requestDone:
			return
		case <-h.shutdown:
			return
		case <-sub.Done():
			return
		default:
		}
		if sub.Overflowed() && !write("resync", signal("overflow")) {
			return
		}
		select {
		case <-requestDone:
			return
		case <-h.shutdown:
			return
		case <-sub.Done():
			return
		case event := <-sub.Events():
			if !write("change", event) {
				return
			}
		case <-ticker.C:
			if _, err := c.Writer.WriteString(": ping\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
		}
	}
}
