package api_router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
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
	shutdown          <-chan struct{}
}

func NewEventHandler(a *app.App, wss *pkgapp.WebsocketServer) *EventHandler {
	return &EventHandler{Handler: NewHandlerWithWSS(a, wss), heartbeatInterval: 20 * time.Second, shutdown: a.ShutdownCh()}
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
	var vaults []string
	for _, vault := range strings.Split(c.Query("vault"), ",") {
		if vault != "" {
			vaults = append(vaults, vault)
		}
	}
	sub, err := h.WSS.SubscribeEvents(uid, c.GetString("vaults"), vaults)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, pkgapp.ErrEventStreamLimit) {
			status = http.StatusTooManyRequests
		}
		c.AbortWithStatus(status)
		return
	}
	defer h.WSS.UnsubscribeEvents(uid, sub)
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	// Interrupt a stalled network write on cancellation or shutdown; join before Gin reuses c.
	// 取消或关闭时中断卡住的网络写入；退出前等待监视协程结束，避免 Gin 复用 c 时发生竞争。
	finished, watcherDone := make(chan struct{}), make(chan struct{})
	requestDone := c.Request.Context().Done()
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
	defer func() { close(finished); <-watcherDone }()
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
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
