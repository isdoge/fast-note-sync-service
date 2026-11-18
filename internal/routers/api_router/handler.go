// Package api_router provides HTTP API router handlers
// Package api_router 提供 HTTP API 路由处理器
package api_router

import (
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
)

// Handler basic Handler struct, encapsulates App Container
// All API Handlers should embed this struct to gain dependency injection capability
// Handler 基础 Handler 结构体，封装 App Container
// 所有 API Handler 都应该嵌入此结构体以获得依赖注入能力
type Handler struct {
	App *app.App
	WSS *pkgapp.WebsocketServer
}

// NewHandler creates a new base Handler instance
// NewHandler 创建新的基础 Handler 实例
func NewHandler(a *app.App) *Handler {
	return &Handler{App: a}
}

// NewHandlerWithWSS creates Handler instance with WebSocket service
// NewHandlerWithWSS 创建带 WebSocket 服务的 Handler 实例
func NewHandlerWithWSS(a *app.App, wss *pkgapp.WebsocketServer) *Handler {
	return &Handler{App: a, WSS: wss}
}

// getClientInfo extracts client type, name and version from request headers
// getClientInfo 从请求头中提取客户端类型、名称和版本
func (h *Handler) getClientInfo(c *gin.Context) (string, string, string) {
	clientType := c.GetHeader("X-Client")
	clientName := c.GetHeader("X-Client-Name")
	clientVersion := c.GetHeader("X-Client-Version")

	// Decode clientName if it's URL-encoded
	// 如果 clientName 是 URL 编码的，则进行解码
	if clientName != "" {
		if decoded, err := url.QueryUnescape(clientName); err == nil {
			clientName = decoded
		}
	}

	return clientType, clientName, clientVersion
}

// cleanAPIPath normalizes a vault-relative path for REST write entry points:
// strips leading "/" and rejects empty, absolute, ".."-segment and NUL paths
// (segment-wise, no URL decoding; #342 S-8). It never appends ".md" — REST is
// also used by third-party clients writing non-note files (e.g. x.canvas).
// cleanAPIPath 为 REST 写入口规范化库相对路径：去掉前导 "/"，按路径段拒绝
// 空路径、绝对路径、整段 ".." 与 NUL（不做 URL 解码；#342 S-8）。
// 一律不补 ".md"——REST 也被第三方客户端用于写非笔记文件（如 x.canvas）。
func cleanAPIPath(path string) (string, bool) {
	return util.CleanVaultPath(path)
}
