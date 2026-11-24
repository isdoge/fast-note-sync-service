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
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
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

// validWSPath validates paths after the shared JSON/Protobuf binder normalizes
// backslashes and their hashes (#451). Requests without backslashes retain the
// existing protocol behavior: leading slashes and extensions are unchanged.
// validWSPath 在公共绑定处规范化路径和哈希后校验，不额外改写路径。
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

// errPathData builds the error-response data payload carrying the client-reported
// path as-is. The official plugin's generic error branch releases the concurrency
// slot reserved for the action only when data.path is a string, so every error
// return of a slot-reserving action must carry it.
// errPathData 构造携带客户端上报原样路径的出错响应 data 载荷。官方插件的通用出错分支
// 只有在 data.path 为字符串时才释放该动作占用的并发名额，因此占名额动作的出错返回都必须带上。
func errPathData(path string) map[string]string {
	return map[string]string{"path": path}
}

// writeRejectsSystemJunk reports whether a WebSocket write entry point should reject
// this path: the switch is on and the path contains a system junk segment. Only
// create/overwrite entry points and rename target paths may call this; delete,
// restore and read handlers must not, so legacy junk entries stay deletable.
// writeRejectsSystemJunk 返回 WebSocket 写入口是否应拒收该路径：开关开启且路径
// 含系统垃圾文件段。只有新建/覆盖入口与改名目标路径可以调用；删除、恢复与读取
// 处理器不得调用，以保证库里已有的垃圾条目仍可删除。
func (h *WSHandler) writeRejectsSystemJunk(path string) bool {
	return h != nil && h.App != nil && h.App.Config().App.SyncExcludeSystemJunkEnabled() && util.IsSystemJunkPath(path)
}

// respondSystemJunkPath rejects a junk write with the existing invalid-path code and
// echoes the client's raw slot path, so the plugin releases its concurrency slot and
// the sync round can finish. No new error code is introduced.
// respondSystemJunkPath 用既有非法路径错误码拒收垃圾写入，并回显客户端占用并发
// 名额的原始路径，让插件释放名额、结束同步轮；不新增错误码。
func (h *WSHandler) respondSystemJunkPath(c *pkgapp.WebsocketClient, slotPath string) {
	c.ToResponse(code.ErrorInvalidPath.WithDetails("system junk file is not synced").WithData(errPathData(slotPath)))
}

// clientSlotPath returns the path the client used to reserve its concurrency slot.
// normalizeBoundPaths rewrites backslashes in the decoded params before handlers
// run, so an error response carrying params.Path would name a different key than
// the one the plugin holds (e.g. "a\b.md" became "a/b.md"). Read the raw request
// field instead; JSON and Protobuf uplink are both supported.
// clientSlotPath 返回客户端占并发名额所用的原始路径。绑定阶段会把反斜杠归一化，
// 直接用 params.Path 回包会让插件对不上自己的槽位键（如 "a\b.md" 变成 "a/b.md"），
// 因此从原始请求载荷里取 path；JSON 与 Protobuf 上行都支持。
func clientSlotPath(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, normalized string) string {
	if msg == nil {
		return normalized
	}
	if meta, ok := requestMeta(msg); ok {
		if meta.Path != "" {
			return meta.Path
		}
		return normalized
	}
	var pathInfo struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(msg.Data, &pathInfo); err == nil && pathInfo.Path != "" {
		return pathInfo.Path
	}
	return normalized
}

// respondErrorWithPath mirrors respondError's msg branch but attaches the client-reported
// path in data, so the plugin can release the concurrency slot on error. The action name
// (msg.Type) is kept so the frame stays an action-scoped reply like the old behavior;
// error code, details and message semantics are unchanged, data only gains the path key.
// respondErrorWithPath 与 respondError 的 msg 分支一致，只是在 data 里附带客户端上报路径，
// 让插件出错时能释放并发名额。保留动作名（msg.Type），出错帧与旧行为一样是带动作名的应答；
// 错误码、details 与文案语义不变，data 仅新增 path 键。
func (h *WSHandler) respondErrorWithPath(c *pkgapp.WebsocketClient, codeErr *code.Code, err error, path, action, method string) {
	h.logError(c, method, err)
	if cErr, ok := err.(*code.Code); ok {
		c.ToResponse(cErr.WithData(errPathData(path)), action)
		return
	}
	c.ToResponse(codeErr.WithDetails(err.Error()).WithData(errPathData(path)), action)
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
