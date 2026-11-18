package routers

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/locales/en"
	"github.com/go-playground/locales/zh"
	"github.com/haierkeys/fast-note-sync-service/internal/middleware"
)

// 路由归类守卫：普通鉴权组（手动令牌可到达）下的每条路由都必须在
// middleware.restRouteFunction 的归类表里。新增路由而未归类时，
// AuthenticateUserToken 会在运行时按“未归类路径拒绝”处理；本测试在注册期
// 提前发现，避免上线后才发现新路由被挡或漏挡。
func TestPlainAuthGroupRoutesAreClassified(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	// 与 registerAPIRoutes 相同的注册路径，但只为收集路由表，不发起请求。
	// 通过一个探针中间件在注册完成后遍历路由树。
	type routeKey struct {
		method string
		path   string
	}
	collected := make(map[routeKey]bool)

	// registerAPIRoutes 需要完整 App；这里用与 admin group 测试相同的构建方式。
	testApp := newAdminGroupTestApp(t, nil)
	uni := ut.New(en.New(), en.New(), zh.New())
	registerAPIRoutes(r, testApp, nil, uni)

	// 仅统计挂在 /api 下、经过 UserAuthToken 中间件的路由。
	// 通过注册后的路由树遍历；无需发请求。
	for _, info := range r.Routes() {
		if !strings.HasPrefix(info.Path, "/api/") {
			continue
		}
		if isPublicAPIPath(info.Path) {
			continue
		}
		collected[routeKey{info.Method, info.Path}] = true
	}

	if len(collected) == 0 {
		t.Fatal("未收集到任何 /api 路由，路由树构建失败")
	}

	// 对收集到的每条路由调用归类函数，未归类的路径必须为空错误（fail closed）。
	for key := range collected {
		if !routeClassificationExists(key.method, key.path) {
			t.Errorf("路由 %s %s 未在归类表中（将按拒绝处理）；请在 middleware.restRouteFunction 中归类或加入公开路径白名单", key.method, key.path)
		}
	}
}

// isPublicAPIPath 列出 /api 下不经过 UserAuthToken 的公开路由。
func isPublicAPIPath(path string) bool {
	public := []string{
		"/api/version", "/api/support", "/api/health",
		"/api/user/register", "/api/user/login",
		"/api/user/auth/oidc/config", "/api/webgui/config",
		"/api/user/auth/oidc/start", "/api/user/auth/oidc/callback",
		"/api/user/sync",
	}
	for _, p := range public {
		if path == p {
			return true
		}
	}
	// OIDC 回调带参数化路径 / 前缀。
	if strings.HasPrefix(path, "/api/user/auth/oidc/") {
		return true
	}
	// MCP 由独立中间件链处理。
	if path == "/api/mcp" || strings.HasPrefix(path, "/api/mcp/") {
		return true
	}
	// 分享公开读接口使用 ShareAuthToken。
	if path == "/api/share/note" || path == "/api/share/file" {
		return true
	}
	return false
}

// routeClassificationExists 通过 middleware 包导出的归类检查函数验证。
func routeClassificationExists(method, path string) bool {
	return middleware.RouteFunctionClassified(method, path)
}
