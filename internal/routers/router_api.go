package routers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	ut "github.com/go-playground/universal-translator"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	appconfig "github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/middleware"
	"github.com/haierkeys/fast-note-sync-service/internal/routers/api_router"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
)

func registerAPIRoutes(r *gin.Engine, appContainer *app.App, wss *pkgapp.WebsocketServer, uni *ut.UniversalTranslator) {
	cfg := appContainer.Config()
	api := r.Group("/api")
	{
		api.Use(middleware.AppInfoWithConfig(app.Name, appContainer.Version().Version))
		// #342 S-18：gin.Logger() 会打印完整查询串，鉴权又支持 ?token=，
		// 会把令牌明文写入访问日志。脱敏的 AccessLogWithLogger 在下方统一记录，此处移除。
		// #342 S-18: gin.Logger() prints the full query string, and auth accepts ?token=,
		// leaking plaintext tokens into access logs. The sanitized AccessLogWithLogger
		// below already covers logging; remove it here.
		api.Use(middleware.TraceMiddlewareWithConfig(*cfg.Tracer.Enabled, cfg.Tracer.Header)) // Trace ID middleware
		// Trace ID 中间件
		api.Use(middleware.RateLimiter(methodLimiters))
		api.Use(middleware.FailureRateLimiter(globalLimiters))

		// MCP routes
		registerMCPRoutes(api, appContainer, wss)

		// Event streams bypass ContextTimeout and explicitly restore language/access logging.
		// 事件流绕过 ContextTimeout，并显式补齐语言与访问日志中间件。
		events := api.Group("")
		events.Use(middleware.LangWithTranslator(uni))
		events.Use(middleware.AccessLogWithLogger(appContainer.Logger()))
		events.Use(middleware.UserAuthTokenWithConfigStrict(cfg.Security.AuthTokenKey, appContainer.TokenService, cfg.Security.StrictEmptyProtocol))
		events.Use(middleware.RequireWebGUI())
		events.GET("/webgui/events", api_router.NewEventHandler(appContainer, wss).Stream)

		api.Use(middleware.ContextTimeout(time.Duration(cfg.App.DefaultContextTimeout) * time.Second))
		api.Use(middleware.LangWithTranslator(uni))
		api.Use(middleware.AccessLogWithLogger(appContainer.Logger()))
		api.Use(middleware.RecoveryWithLogger(appContainer.Logger()))

		// Create Handlers (injected App Container)
		// 创建 Handlers（注入 App Container）
		userHandler := api_router.NewUserHandler(appContainer)
		vaultHandler := api_router.NewVaultHandler(appContainer)
		noteHandler := api_router.NewNoteHandler(appContainer, wss)
		folderHandler := api_router.NewFolderHandler(appContainer, wss)
		fileHandler := api_router.NewFileHandler(appContainer, wss)
		noteHistoryHandler := api_router.NewNoteHistoryHandler(appContainer, wss)
		versionHandler := api_router.NewVersionHandler(appContainer)
		adminControlHandler := api_router.NewAdminControlHandler(appContainer, wss)
		shareHandler := api_router.NewShareHandler(appContainer, wss)
		storageHandler := api_router.NewStorageHandler(appContainer)
		backupHandler := api_router.NewBackupHandler(appContainer)
		gitSyncHandler := api_router.NewGitSyncHandler(appContainer)
		settingHandler := api_router.NewSettingHandler(appContainer, wss)
		syncLogHandler := api_router.NewSyncLogHandler(appContainer)
		tokenHandler := api_router.NewTokenHandler(appContainer)
		stytchOAuthHandler := api_router.NewStytchOAuthHandler(appContainer)
		oidcHandler := api_router.NewOIDCHandler(appContainer)
		userCapabilityHandler := api_router.NewUserCapabilityHandler(appContainer)
		shareBaseURLHandler := api_router.NewShareBaseURLHandler(appContainer)
		cliUpgradeHandler := api_router.NewCLIUpgradeHandler(appContainer)

		// No-auth WebGUI restricted routes
		// 免认证但仅限 WebGUI 访问的路由组
		noAuthWebgui := api.Group("")
		noAuthWebgui.Use(middleware.RequireWebGUI())
		{
			noAuthWebgui.POST("/user/register", userHandler.Register)
			noAuthWebgui.POST("/user/login", userHandler.Login)
			noAuthWebgui.GET("/user/auth/oidc/config", oidcHandler.Config)
			noAuthWebgui.GET("/webgui/config", adminControlHandler.Config)
		}
		api.GET("/user/auth/oidc/start", oidcHandler.Start)
		api.GET("/user/auth/oidc/start/:providerID", oidcHandler.Start)
		for _, route := range oidcCallbackRoutes(cfg.OIDC) {
			api.GET(route, oidcHandler.Callback)
		}
		api.GET("/user/sync", wss.Run())

		// Add server version interface (no auth required)
		// 添加服务端版本号接口（无需认证）
		api.GET("/version", versionHandler.ServerVersion)
		api.GET("/support", versionHandler.Support)

		// Health check interface (no auth required)
		// 健康检查接口（无需认证）
		healthHandler := api_router.NewHealthHandler(appContainer)
		api.GET("/health", healthHandler.Check)

		// Share routing group (controlled read-only access)
		// 分享路由组 (受控的只读访问)
		share := api.Group("/share")
		share.Use(middleware.ShareAuthToken(appContainer.ShareService))
		{
			share.GET("/note", shareHandler.NoteGet) // Get shared note
			// 获取分享的笔记
			share.GET("/file", shareHandler.FileGet) // Get shared file content
			// 获取分享的文件内容
		}

		// Auth routing group (authentication required)
		// 需要认证的路由组
		auth := api.Group("/")
		auth.Use(middleware.UserAuthTokenWithConfigStrict(cfg.Security.AuthTokenKey, appContainer.TokenService, cfg.Security.StrictEmptyProtocol))
		{
			// Create share
			// 创建分享
			auth.POST("/auth/logout", userHandler.Logout)
			auth.POST("/share", shareHandler.Create)
			auth.POST("/share/password", shareHandler.UpdatePassword)
			auth.GET("/share", shareHandler.Query)
			auth.DELETE("/share", shareHandler.Cancel)
			auth.POST("/share/short_link", shareHandler.CreateShortLink)
			auth.GET("/shares", shareHandler.List)

			// Version source latency probe (auth required: triggers real outbound requests)
			// 版本源延迟探测（需认证：会触发真实的外部网络请求）
			auth.GET("/version/probe", versionHandler.ProbeSources)

			// Admin status check: any authenticated token may call; reports
			// whether the caller is the effective admin so clients (Obsidian
			// plugin's upgrade dialog) can decide whether to show admin-only
			// features. Vault restrictions do not apply (user-level route).
			// 管理员状态查询：任何已认证令牌均可调用，报告调用者是否为生效
			// 管理员，供客户端（Obsidian 插件的升级对话框）决定是否展示
			// 管理员专属功能。不适用库限制（用户级路由）。
			auth.GET("/admin/check", adminControlHandler.CheckAdmin)

			// Online client list: read-only and, like /api/admin/check, reports
			// data only after the handler's effective-admin check. It stays in
			// the plain auth group so the Obsidian plugin's "online clients"
			// menu works with the user's own API token, as before 3.8.0.
			// 在线客户端列表：只读，且 handler 内部要求生效管理员。保留在普通
			// 鉴权组，使官方插件用用户自己的 API 令牌也能打开“在线客户端”，
			// 与 3.8.0 之前一致。
			auth.GET("/admin/ws_clients", adminControlHandler.GetWSClients)

			// Server self-upgrade: stays in the plain auth group for client
			// compatibility (WebGUI login tokens and admin-owned manual tokens
			// with config_w both work). The handler requires the effective
			// admin; the route function is classified as config write.
			// 服务端自升级：为兼容既有客户端保留在普通鉴权组（WebGUI 登录令牌
			// 与管理员名下含 config_w 的手动令牌均可使用）。handler 内部要求
			// 生效管理员；路由功能点归类为配置写。
			auth.GET("/admin/upgrade", adminControlHandler.Upgrade)

			// Runtime share base URL management: accepts either an effective
			// admin user token or the configured static token (for external
			// dynamic-address notifiers), so it uses its own auth middleware
			// instead of the plain user-token group.
			// 运行时分享基址管理：接受生效管理员用户令牌或配置的静态令牌
			//（供外部动态地址通知脚本），因此使用独立鉴权中间件而非普通用户令牌组。
			shareBaseURLGroup := api.Group("/admin")
			shareBaseURLGroup.Use(middleware.ShareBaseURLAuth(cfg.Security.AuthTokenKey, appContainer.TokenService, cfg.Security.StrictEmptyProtocol, cfg.Security.ShareBaseURLToken))
			{
				shareBaseURLGroup.GET("/share-base-url", shareBaseURLHandler.Get)
				shareBaseURLGroup.POST("/share-base-url", shareBaseURLHandler.Update)
			}

			auth.GET("/user/info", userHandler.UserInfo)
			auth.POST("/oauth/stytch/authorize/start", stytchOAuthHandler.AuthorizeStart)
			auth.POST("/oauth/stytch/authorize/submit", stytchOAuthHandler.AuthorizeSubmit)

			// Effective token capabilities: any authenticated user token (full or
			// restricted) may query its own scope, so clients can adapt their UI
			// to read-only / vault-limited tokens. No function point required.
			// 令牌有效能力查询：任何已认证用户令牌（全权或受限）都可查询自己的
			// scope，客户端据此适配只读 / 限定库令牌的界面。无需功能点。
			auth.GET("/user/token-capabilities", userCapabilityHandler.TokenCapabilities)

			// CLI upgrade relay: requires an authenticated user token to prevent
			// unauthenticated SSRF-style proxying, but needs no function point.
			// CLI 升级中转：需要已登录用户令牌以防未授权的代理滥用，但无需功能点。
			auth.GET("/upgrade/cli", cliUpgradeHandler.CLI)

			auth.GET("/note", noteHandler.Get)
			auth.POST("/note", noteHandler.CreateOrUpdate)
			auth.DELETE("/note", noteHandler.Delete)
			auth.PUT("/note/restore", noteHandler.Restore)
			auth.POST("/note/rename", noteHandler.Rename)
			auth.GET("/notes", noteHandler.List)
			auth.DELETE("/note/recycle-clear", noteHandler.RecycleClear)
			auth.GET("/notes/share-paths", shareHandler.NoteSharePaths)

			auth.GET("/folder", folderHandler.Get)
			auth.POST("/folder", folderHandler.Create)
			auth.DELETE("/folder", folderHandler.Delete)
			auth.GET("/folders", folderHandler.List)
			auth.GET("/folder/notes", folderHandler.ListNotes)
			auth.GET("/folder/files", folderHandler.ListFiles)
			auth.GET("/folder/tree", folderHandler.Tree)

			// Note edit operations
			auth.PATCH("/note/frontmatter", noteHandler.PatchFrontmatter)
			auth.POST("/note/append", noteHandler.Append)
			auth.POST("/note/prepend", noteHandler.Prepend)
			auth.POST("/note/replace", noteHandler.Replace)

			// Note link operations
			auth.GET("/note/backlinks", noteHandler.GetBacklinks)
			auth.GET("/note/outlinks", noteHandler.GetOutlinks)

			auth.GET("/file", fileHandler.GetInfo)
			auth.POST("/file", fileHandler.Upload)
			auth.OPTIONS("/file", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			auth.GET("/file/info", fileHandler.Get)
			auth.OPTIONS("/file/info", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			auth.DELETE("/file", fileHandler.Delete)
			auth.PUT("/file/restore", fileHandler.Restore)
			auth.POST("/file/rename", fileHandler.Rename)
			auth.GET("/files", fileHandler.List)
			auth.DELETE("/file/recycle-clear", fileHandler.RecycleClear)
			auth.OPTIONS("/files", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			auth.GET("/note/history", noteHistoryHandler.Get)
			auth.GET("/note/histories", noteHistoryHandler.List)
			auth.PUT("/note/history/restore", noteHistoryHandler.Restore)

			auth.GET("/setting", settingHandler.Get)
			auth.POST("/setting", settingHandler.CreateOrUpdate)
			auth.DELETE("/setting", settingHandler.Delete)
			auth.POST("/setting/rename", settingHandler.Rename)
			auth.GET("/settings", settingHandler.List)

			// Vault discovery is required before REST clients can address note,
			// file, and folder APIs. Mutating vault operations remain WebGUI-only.
			auth.GET("/vault", vaultHandler.List)

			// Client-safe git sync status/trigger for vault-scoped tokens (Obsidian plugin).
			// These two routes are classified as note_r / note_w; every other
			// /api/git-sync/* route stays in the WebGUI group below.
			// 面向限定库客户端令牌的 Git 同步状态/触发接口（Obsidian 插件）。
			// 仅这两条归类为 note_r / note_w，其余 /api/git-sync/* 仍在下方 WebGUI 组。
			auth.GET("/git-sync/status", gitSyncHandler.GetVaultStatus)
			auth.POST("/git-sync/trigger", gitSyncHandler.TriggerVaultSync)

			// WebGUI restricted routes
			// 仅限 WebGUI 访问的路由组
			webguiGroup := auth.Group("")
			webguiGroup.Use(middleware.RequireWebGUI())
			{
				// User management routes
				// 用户管理接口
				webguiGroup.POST("/user/change_password", userHandler.UserChangePassword)

				// Vault management routes
				// 笔记库管理接口
				webguiGroup.POST("/vault", vaultHandler.CreateOrUpdate)
				webguiGroup.DELETE("/vault", vaultHandler.Delete)
				webguiGroup.POST("/vault/rebuild-index", vaultHandler.RebuildIndex)
				webguiGroup.POST("/vault/force-delete-item", vaultHandler.ForceDeleteDataItem)

				// Admin config interface (effective admin required)
				// 管理员配置接口（需生效管理员）
				webguiGroup.GET("/admin/config", adminControlHandler.GetConfig)
				webguiGroup.POST("/admin/config", adminControlHandler.UpdateConfig)
				webguiGroup.GET("/admin/config/user_database", adminControlHandler.GetUserDatabaseConfig)
				webguiGroup.POST("/admin/config/user_database", adminControlHandler.UpdateUserDatabaseConfig)
				webguiGroup.POST("/admin/config/user_database/test", adminControlHandler.ValidateUserDatabaseConfig)
				webguiGroup.GET("/admin/config/cloudflare", adminControlHandler.GetCloudflareConfig)
				webguiGroup.POST("/admin/config/cloudflare", adminControlHandler.UpdateCloudflareConfig)
				webguiGroup.GET("/admin/systeminfo", adminControlHandler.GetSystemInfo)
				webguiGroup.GET("/admin/restart", adminControlHandler.Restart)
				webguiGroup.GET("/admin/gc", adminControlHandler.GC)
				webguiGroup.GET("/admin/cloudflared_tunnel_download", adminControlHandler.CloudflaredTunnelDownload)

				// Admin user managment
				webguiGroup.GET("/admin/users/list", adminControlHandler.GetUsers)
				webguiGroup.POST("/admin/users/create", adminControlHandler.CreateUser)
				webguiGroup.POST("/admin/users/update", adminControlHandler.UpdateUser)

				// Admin WS control that mutates state stays WebGUI-only: manual
				// API tokens (p:rest) must not kick connections. The read-only
				// client list moved to the plain auth group (effective-admin
				// check in the handler), see the comment there.
				// 会改变状态的管理写操作仍仅限 WebGUI：手动 API 令牌（p:rest）
				// 不得踢连接。只读的在线客户端列表已移回普通鉴权组（handler
				// 内部校验生效管理员），见上方注释。
				webguiGroup.DELETE("/admin/ws_client/:traceId", adminControlHandler.KickWSClient)

				// Storage management routes
				// 存储配置接口
				webguiGroup.GET("/storage", storageHandler.List)
				webguiGroup.POST("/storage", storageHandler.CreateOrUpdate)
				webguiGroup.GET("/storage/enabled_types", storageHandler.EnabledTypes)
				webguiGroup.POST("/storage/validate", storageHandler.Validate)
				webguiGroup.DELETE("/storage", storageHandler.Delete)

				// Backup routes
				// 本地备份接口
				webguiGroup.GET("/backup/configs", backupHandler.GetConfigs)
				webguiGroup.POST("/backup/config", backupHandler.UpdateConfig)
				webguiGroup.DELETE("/backup/config", backupHandler.DeleteConfig)
				webguiGroup.GET("/backup/historys", backupHandler.ListHistory)
				webguiGroup.POST("/backup/execute", backupHandler.Execute)

				// Git sync routes
				// Git 同步接口
				webguiGroup.GET("/git-sync/configs", gitSyncHandler.GetConfigs)
				webguiGroup.POST("/git-sync/config", gitSyncHandler.UpdateConfig)
				webguiGroup.DELETE("/git-sync/config", gitSyncHandler.DeleteConfig)
				webguiGroup.POST("/git-sync/validate", gitSyncHandler.Validate)
				webguiGroup.DELETE("/git-sync/config/clean", gitSyncHandler.CleanWorkspace)
				webguiGroup.POST("/git-sync/config/execute", gitSyncHandler.Execute)
				webguiGroup.GET("/git-sync/histories", gitSyncHandler.GetHistories)

				// Sync log routes
				// 同步日志路由
				webguiGroup.GET("/sync-logs", syncLogHandler.List)

				// Token management routes
				// 令牌管理路由
				webguiGroup.GET("/tokens", tokenHandler.List)
				webguiGroup.DELETE("/tokens/clean-expired", tokenHandler.CleanExpired)
				webguiGroup.POST("/token", tokenHandler.Create)
				webguiGroup.PUT("/token/:id", tokenHandler.Update)
				webguiGroup.DELETE("/token/:id", tokenHandler.Revoke)
				webguiGroup.POST("/token/:id/rotate", tokenHandler.Rotate)
				webguiGroup.GET("/token/:id/logs", tokenHandler.ListLogs)
			}
		}
	}
}

func oidcCallbackRoute(callbackPath string) string {
	callbackPath = strings.TrimSpace(callbackPath)
	if callbackPath == "" {
		return "/user/auth/oidc/callback"
	}
	if strings.HasPrefix(callbackPath, "/api/") {
		return strings.TrimPrefix(callbackPath, "/api")
	}
	if strings.HasPrefix(callbackPath, "/") {
		return callbackPath
	}
	return "/" + callbackPath
}

func oidcCallbackRoutes(cfg appconfig.OIDCConfig) []string {
	routes := []string{}
	seen := map[string]struct{}{}
	add := func(route string) {
		if _, ok := seen[route]; ok {
			return
		}
		seen[route] = struct{}{}
		routes = append(routes, route)
	}

	add(oidcCallbackRoute(cfg.CallbackPath))
	for _, provider := range cfg.Providers {
		route := oidcCallbackRoute(provider.CallbackPath)
		if route == oidcDefaultProviderCallbackRoute(provider.ID) || strings.Contains(route, ":") {
			continue
		}
		add(route)
	}
	add("/user/auth/oidc/callback/:providerID")
	return routes
}

func oidcDefaultProviderCallbackRoute(providerID string) string {
	if providerID == "" || providerID == "default" {
		return "/user/auth/oidc/callback"
	}
	return "/user/auth/oidc/callback/" + providerID
}
