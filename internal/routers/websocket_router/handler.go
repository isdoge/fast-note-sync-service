// Package websocket_router provides WebSocket router handlers
// Package websocket_router 提供 WebSocket 路由处理器
package websocket_router

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"go.uber.org/zap"
)

// WSHandler WebSocket base Handler struct, encapsulates App Container
// All WebSocket Handlers should embed this struct to gain dependency injection capability
// WSHandler WebSocket 基础 Handler 结构体，封装 App Container
// 所有 WebSocket Handler 都应该嵌入此结构体以获得依赖注入能力
type WSHandler struct {
	App *app.App
}

// NewWSHandler creates WebSocket base Handler instance
// NewWSHandler 创建 WebSocket 基础 Handler 实例
func NewWSHandler(a *app.App) *WSHandler {
	return &WSHandler{App: a}
}

// logError records error log, including Trace ID
// Directly use WebsocketClient.TraceID field, avoiding fetching from potentially invalid HTTP context
// logError 记录错误日志，包含 Trace ID
// 直接使用 WebsocketClient.TraceID 字段，避免从可能失效的 HTTP context 获取
func (h *WSHandler) logError(c *pkgapp.WebsocketClient, method string, err error) {
	traceID := ""
	if c != nil {
		traceID = c.TraceID
	}

	// If connection closed error and context canceled, downgrade log level
	// 如果是连接关闭导致的错误且 context 已取消，降级日志级别
	if isNetworkClosedError(err) && c != nil && c.Context().Err() != nil {
		h.logDebug(c, method, zap.Error(err))
		return
	}

	h.App.Logger().Error(method,
		zap.Error(err),
		zap.String("traceId", traceID),
	)
}

// logDebug records debug log, including Trace ID
// logDebug 记录调试日志，包含 Trace ID
func (h *WSHandler) logDebug(c *pkgapp.WebsocketClient, method string, fields ...zap.Field) {
	traceID := ""
	if c != nil {
		traceID = c.TraceID
	}
	allFields := append([]zap.Field{zap.String("traceId", traceID)}, fields...)
	h.App.Logger().Debug(method, allFields...)
}

// logInfo records info log, including Trace ID
// Directly use WebsocketClient.TraceID field, avoiding fetching from potentially invalid HTTP context
// logInfo 记录信息日志，包含 Trace ID
// 直接使用 WebsocketClient.TraceID 字段，避免从可能失效的 HTTP context 获取
func (h *WSHandler) logInfo(c *pkgapp.WebsocketClient, method string, fields ...zap.Field) {
	traceID := ""
	if c != nil {
		traceID = c.TraceID
	}
	allFields := append([]zap.Field{zap.String("traceId", traceID)}, fields...)
	h.App.Logger().Info(method, allFields...)
}

// logWarn records warning log, including Trace ID
// Directly use WebsocketClient.TraceID field, avoiding fetching from potentially invalid HTTP context
// logWarn 记录警告日志，包含 Trace ID
// 直接使用 WebsocketClient.TraceID 字段，避免从可能失效的 HTTP context 获取
func (h *WSHandler) logWarn(c *pkgapp.WebsocketClient, method string, fields ...zap.Field) {
	traceID := ""
	if c != nil {
		traceID = c.TraceID
	}
	allFields := append([]zap.Field{zap.String("traceId", traceID)}, fields...)
	h.App.Logger().Warn(method, allFields...)
}

// validWSPath validates a client-reported vault-relative path WITHOUT
// rewriting it (#342 S-8): the sync protocol requires the server to store the
// exact path the client reported (clients key their baseline and hashes by
// their own path), so the WebSocket entry only rejects — never strips the
// leading "/" nor appends ".md". The check itself is segment-wise: empty,
// NUL, or any segment (split on "/" and "\") equal to ".." is rejected;
// ordinary names like "a..b.md", "等等...md", "100%.md" pass.
// validWSPath 只校验、不改写客户端上报的库相对路径（#342 S-8）：同步协议
// 要求服务端按客户端上报的原样路径落库（客户端按自己的路径记基线与哈希），
// 因此 WebSocket 入口只做拒绝——不去前导 "/"、不补 ".md"。校验本身按段进行：
// 空路径、含 NUL、任一段（"/" 与 "\" 都算分隔符）等于 ".." 拒绝；
// "a..b.md"、"等等...md"、"100%.md" 等正常笔记名合法。
func validWSPath(path string) bool {
	if path == "" {
		return false
	}
	if strings.Contains(path, "\x00") {
		return false
	}
	for _, seg := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	return true
}

// extractMsgMeta extracts context/vault/path from raw WebSocket JSON payload for error tracing.
// extractMsgMeta 从原始 WebSocket JSON 载荷中提取 context/vault/path，用于错误定位。
func extractMsgMeta(data []byte) (msgCtx, vault, path string) {
	if len(data) == 0 {
		return
	}
	var meta struct {
		Context string `json:"context"`
		Vault   string `json:"vault"`
		Path    string `json:"path"`
	}
	_ = json.Unmarshal(data, &meta) // Ignore error; unrecognized fields are simply empty // 忽略错误，未识别字段为空即可
	return meta.Context, meta.Vault, meta.Path
}

// respondError unified error response method
// Records error log and sends error response with Details to client
// respondError 统一错误响应方法
// 记录错误日志并发送包含 Details 的错误响应给客户端
func (h *WSHandler) respondError(c *pkgapp.WebsocketClient, codeErr *code.Code, err error, method string, msg ...*pkgapp.WebSocketMessage) {
	h.logError(c, method, err)

	// 若传入了 msg，则自动提取请求元数据并附加到错误响应中。
	if len(msg) > 0 && msg[0] != nil {
		m := msg[0]
		msgCtx, vault, path := extractMsgMeta(m.Data)
		if cErr, ok := err.(*code.Code); ok {
			enriched := cErr
			if msgCtx != "" {
				enriched = enriched.WithContext(msgCtx)
			}
			if vault != "" {
				enriched = enriched.WithVault(vault)
			}
			if path != "" {
				enriched = enriched.WithPath(path)
			}
			c.ToResponse(enriched, m.Type)
			return
		}
		enriched := codeErr.WithDetails(err.Error())
		if msgCtx != "" {
			enriched = enriched.WithContext(msgCtx)
		}
		if vault != "" {
			enriched = enriched.WithVault(vault)
		}
		if path != "" {
			enriched = enriched.WithPath(path)
		}
		c.ToResponse(enriched, m.Type)
		return
	}

	// Original logic (backward compatible) // 原有逻辑（向后兼容）
	if cErr, ok := err.(*code.Code); ok {
		c.ToResponse(cErr)
		return
	}
	c.ToResponse(codeErr.WithDetails(err.Error()))
}

// respondErrorWithData unified error response method with data
// Records error log and sends error response with Details and Data to client
// respondErrorWithData 带数据的统一错误响应方法
// 记录错误日志并发送包含 Details 和 Data 的错误响应给客户端
func (h *WSHandler) respondErrorWithData(c *pkgapp.WebsocketClient, codeErr *code.Code, err error, data interface{}, method string, msg ...*pkgapp.WebSocketMessage) {
	h.logError(c, method, err)

	// 若传入了 msg，则自动提取请求元数据并附加到错误响应中。
	if len(msg) > 0 && msg[0] != nil {
		m := msg[0]
		msgCtx, vault, path := extractMsgMeta(m.Data)
		enriched := codeErr.WithData(data)
		if msgCtx != "" {
			enriched = enriched.WithContext(msgCtx)
		}
		if vault != "" {
			enriched = enriched.WithVault(vault)
		}
		if path != "" {
			enriched = enriched.WithPath(path)
		}
		c.ToResponse(enriched, m.Type)
		return
	}

	// Original logic (backward compatible) // 原有逻辑（向后兼容）
	c.ToResponse(codeErr.WithDetails(err.Error()).WithData(data))
}

// GetTraceID retrieves Trace ID from WebSocket client
// Directly use WebsocketClient.TraceID field, avoiding fetching from potentially invalid HTTP context
// GetTraceID 从 WebSocket 客户端获取 Trace ID
// 直接使用 WebsocketClient.TraceID 字段，避免从可能失效的 HTTP context 获取
func GetTraceID(c *pkgapp.WebsocketClient) string {
	if c == nil {
		return ""
	}
	return c.TraceID
}

// LogErrorWithLogger records error log, including Trace ID (uses injected logger)
// LogErrorWithLogger 记录错误日志，包含 Trace ID（使用注入的 logger）
func LogErrorWithLogger(logger *zap.Logger, c *pkgapp.WebsocketClient, method string, err error) {
	traceID := GetTraceID(c)

	// If connection closed error and context canceled, downgrade log level
	// 如果是连接关闭导致的错误且 context 已取消，降级日志级别
	if isNetworkClosedError(err) && c != nil && c.Context().Err() != nil {
		allFields := []zap.Field{zap.String("traceId", traceID), zap.Error(err)}
		logger.Debug(method, allFields...)
		return
	}

	logger.Error(method,
		zap.Error(err),
		zap.String("traceId", traceID),
	)
}

// LogInfoWithLogger records info log, including Trace ID (uses injected logger)
// LogInfoWithLogger 记录信息日志，包含 Trace ID（使用注入的 logger）
func LogInfoWithLogger(logger *zap.Logger, c *pkgapp.WebsocketClient, method string, fields ...zap.Field) {
	traceID := GetTraceID(c)
	allFields := append([]zap.Field{zap.String("traceId", traceID)}, fields...)
	logger.Info(method, allFields...)
}

// LogWarnWithLogger records warning log, including Trace ID (uses injected logger)
// LogWarnWithLogger 记录警告日志，包含 Trace ID（使用注入的 logger）
func LogWarnWithLogger(logger *zap.Logger, c *pkgapp.WebsocketClient, method string, fields ...zap.Field) {
	traceID := GetTraceID(c)
	allFields := append([]zap.Field{zap.String("traceId", traceID)}, fields...)
	logger.Warn(method, allFields...)
}

// isNetworkClosedError checks if the error is related to network closure
// isNetworkClosedError 检查是否为网络关闭相关的错误
func isNetworkClosedError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "broken pipe") ||
		err == context.Canceled
}
