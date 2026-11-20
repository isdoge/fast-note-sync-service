package middleware

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"

	"github.com/gin-gonic/gin"
)

func UserAuthTokenWithConfig(secretKey string, tokenService service.TokenService) gin.HandlerFunc {
	return UserAuthTokenWithConfigStrict(secretKey, tokenService, false)
}

// UserAuthTokenWithConfigStrict is UserAuthTokenWithConfig with the strict
// empty-protocol flag forwarded to permission checks (see
// security.strict-empty-protocol).
// UserAuthTokenWithConfigStrict 是带严格空协议开关的 UserAuthTokenWithConfig
// （见 security.strict-empty-protocol）。
func UserAuthTokenWithConfigStrict(secretKey string, tokenService service.TokenService, strictEmptyProtocol bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, scope, vaults, dbToken, appErr := AuthenticateUserTokenStrict(c, secretKey, tokenService, strictEmptyProtocol)
		if appErr != nil {
			respondUserAuthError(c, appErr)
			c.Abort()
			return
		}
		c.Set("user_token", user)
		c.Set("scope", scope)
		c.Set("vaults", vaults)
		// Inject server-side token attributes for downstream middleware (e.g. RequireWebGUI)
		// 注入服务端 Token 属性，供下游中间件（如 RequireWebGUI）进行联合校验，防止请求头伪造绕过
		c.Set("token_issue_type", dbToken.IssueType)
		c.Set("token_client_type", dbToken.ClientType)
		// Keep the entry-time client and strictness so handler-level checks that
		// depend on the request body (e.g. whole-recycle-bin clear) decide
		// permissions exactly like the route-level check did.
		// 记录入口时的客户端与严格开关，供依赖请求体的 handler 级功能点检查
		//（例如整库清空回收站）与路由级检查保持一致。
		c.Set(contextKeyStrictEmptyProtocol, strictEmptyProtocol)
		c.Next()
	}
}

// contextKeyStrictEmptyProtocol / contextKeyAuthRestClient hold the entry-time
// authentication inputs used by RestFunctionAllowed.
// contextKeyStrictEmptyProtocol / contextKeyAuthRestClient 保存入口鉴权时的
// 输入，供 RestFunctionAllowed 复用。
const (
	contextKeyStrictEmptyProtocol = "auth_strict_empty_protocol"
	contextKeyAuthRestClient      = "auth_rest_client"
)

// RestFunctionAllowed reports whether the authenticated request's token scope
// admits the given REST function point. The decision reuses the same
// protocol/client/strictness the entry middleware applied, so handlers can
// enforce a different function point for a body-dependent code path (such as
// a whole-recycle-bin clear versus a single-path clear).
// RestFunctionAllowed 报告已认证请求的令牌 scope 是否包含指定 REST 功能点。
// 判定复用入口中间件的协议/客户端/严格开关，供 handler 对依赖请求体的分支
// （例如整库清空回收站与单条清理）追加不同的功能点校验。
func RestFunctionAllowed(c *gin.Context, function string) bool {
	strict, _ := c.Get(contextKeyStrictEmptyProtocol)
	strictEmptyProtocol, _ := strict.(bool)
	return verifyRestFunction(
		c.GetString("scope"),
		"rest",
		c.GetString(contextKeyAuthRestClient),
		function,
		strictEmptyProtocol,
	)
}

func ExtractUserAuthToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	if authHeader := c.GetHeader("Token"); authHeader != "" {
		return authHeader
	}
	if authHeader := c.GetHeader("token"); authHeader != "" {
		return authHeader
	}

	return c.Query("token")
}

func AuthenticateUserToken(c *gin.Context, secretKey string, tokenService service.TokenService) (*app.UserEntity, string, string, *domain.AuthToken, *code.Code) {
	return AuthenticateUserTokenStrict(c, secretKey, tokenService, false)
}

// AuthenticateUserTokenStrict is AuthenticateUserToken with the strict
// empty-protocol flag for scope verification.
// AuthenticateUserTokenStrict 是带严格空协议开关的 AuthenticateUserToken。
func AuthenticateUserTokenStrict(c *gin.Context, secretKey string, tokenService service.TokenService, strictEmptyProtocol bool) (*app.UserEntity, string, string, *domain.AuthToken, *code.Code) {
	token := ExtractUserAuthToken(c)
	if token == "" {
		return nil, "", "", nil, code.ErrorNotUserAuthToken
	}

	user, err := app.ParseTokenWithKey(token, secretKey)
	if err != nil {
		if appErr, ok := err.(*code.Code); ok {
			return nil, "", "", nil, appErr
		}
		return nil, "", "", nil, code.ErrorInvalidUserAuthToken
	}

	ctx := c.Request.Context()
	dbToken, err := tokenService.GetActiveToken(ctx, user.UID, user.TokenID)
	if err != nil || dbToken == nil {
		if appErr, ok := err.(*code.Code); ok {
			return nil, "", "", nil, appErr
		}
		return nil, "", "", nil, code.ErrorInvalidUserAuthToken
	}

	if dbToken.TokenString != "" && user.Nonce != dbToken.TokenString {
		return nil, "", "", nil, code.ErrorInvalidUserAuthToken.WithDetails("Token has been rotated")
	}

	reqClientType := c.GetHeader("x-client")
	if reqClientType == "" {
		reqClientType = c.Query("client")
	}
	if reqClientType == "" && dbToken.IssueType == 1 && isHeaderlessLoginTokenResourceRead(c) {
		reqClientType = dbToken.ClientType
	}
	c.Set(contextKeyAuthRestClient, reqClientType)

	if dbToken.IssueType == 1 && !app.MatchWildcard(dbToken.ClientType, reqClientType) {
		return nil, "", "", nil, code.ErrorAuthTokenClientRestricted.WithDetails("Client mismatch")
	}

	if dbToken.UserAgent != "" {
		if reqUserAgent := c.GetHeader("User-Agent"); !app.MatchWildcard(dbToken.UserAgent, reqUserAgent) {
			return nil, "", "", nil, code.ErrorAuthTokenUARestricted.WithDetails("User-Agent mismatch")
		}
	}

	if dbToken.BoundIP != "" {
		if reqIP := c.ClientIP(); !app.MatchWildcard(dbToken.BoundIP, reqIP) {
			return nil, "", "", nil, code.ErrorAuthTokenIPRestricted.WithDetails("IP mismatch")
		}
	}

	path := c.Request.URL.Path
	method := c.Request.Method
	isRead := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
	function, fnErr := restRouteFunction(path, method, isRead)
	if fnErr != nil {
		return nil, "", "", nil, fnErr
	}

	protocol := "rest"
	if strings.HasPrefix(path, "/api/mcp") {
		protocol = "mcp"
	}

	if path != "/api/health" && !verifyRestFunction(dbToken.Scope, protocol, reqClientType, function, strictEmptyProtocol) {
		resPath := c.Query("path")
		if resPath == "" {
			resPath = c.Query("name")
		}
		if resPath == "" {
			resPath = c.Query("file")
		}
		if resPath == "" {
			resPath = path
		}
		return nil, "", "", nil, code.ErrorAuthTokenScopeRestricted.WithDetails("Permission denied: " + resPath)
	}

	if dbToken.Vaults != "" {
		targetVault := app.RequestParam(c, "vault")
		targets := []string{targetVault}
		// The event endpoint takes one complete vault name per repeated vault= parameter
		// (commas are legal in vault names), so every value is checked individually.
		// 事件端点每个重复的 vault= 参数是一个完整库名（库名可含逗号），逐值校验，任一越权即整体拒绝。
		if path == "/api/webgui/events" {
			targets = c.QueryArray("vault")
			if len(targets) == 0 {
				targets = []string{targetVault}
			}
		}
		for _, vault := range targets {
			if vault != "" && !util.VerifyVaultAccess(dbToken.Vaults, vault) {
				return nil, "", "", nil, code.ErrorAuthTokenScopeRestricted.WithDetails("Vault access restricted: " + vault)
			}
		}
	}

	// Snapshot Gin fields before asynchronous logging; streams keep mutating the writer.
	// 异步记录前先快照 Gin 字段；事件流会持续修改响应 writer。
	clientName := c.GetHeader("x-client-name")
	if clientName != "" {
		if decoded, err := url.QueryUnescape(clientName); err == nil {
			clientName = decoded
		}
	}

	log := &domain.AuthTokenLog{
		TokenID:       dbToken.ID,
		UID:           dbToken.UID,
		Protocol:      protocol,
		Client:        reqClientType,
		ClientName:    clientName,
		ClientVersion: c.GetHeader("x-client-version"),
		IP:            c.ClientIP(),
		UA:            c.GetHeader("User-Agent"),
		StatusCode:    int64(c.Writer.Status()),
	}
	go func() { _ = tokenService.RecordAccessLog(context.Background(), log) }()

	return user, dbToken.Scope, dbToken.Vaults, dbToken, nil
}

// RevalidateUserToken re-checks a request's token after a delayed setup step
// (e.g. registering an SSE subscription). It applies the same validity checks
// as AuthenticateUserTokenStrict — signature, active database token and the
// rotation nonce — so a revocation that raced the entry middleware cannot leave
// an authenticated stream behind. Static per-request properties (client, UA,
// IP, scope, vault allowlist) are not repeated: they were enforced on entry.
// RevalidateUserToken 在延迟的初始化步骤（如注册 SSE 订阅）之后重新校验请求令牌，
// 判定与 AuthenticateUserTokenStrict 相同：签名、数据库中的有效令牌、轮转 nonce，
// 避免入口鉴权与订阅之间发生的吊销留下仍可用的流。客户端/UA/IP/scope/库白名单
// 属于请求的静态属性，入口已校验，不重复。
func RevalidateUserToken(c *gin.Context, secretKey string, tokenService service.TokenService) *code.Code {
	token := ExtractUserAuthToken(c)
	if token == "" {
		return code.ErrorNotUserAuthToken
	}

	user, err := app.ParseTokenWithKey(token, secretKey)
	if err != nil {
		if appErr, ok := err.(*code.Code); ok {
			return appErr
		}
		return code.ErrorInvalidUserAuthToken
	}

	dbToken, err := tokenService.GetActiveToken(c.Request.Context(), user.UID, user.TokenID)
	if err != nil || dbToken == nil {
		if appErr, ok := err.(*code.Code); ok {
			return appErr
		}
		return code.ErrorInvalidUserAuthToken
	}

	if dbToken.TokenString != "" && user.Nonce != dbToken.TokenString {
		return code.ErrorInvalidUserAuthToken.WithDetails("Token has been rotated")
	}
	return nil
}

func isHeaderlessLoginTokenResourceRead(c *gin.Context) bool {
	return c.Request.URL.Path == "/api/file" &&
		(c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead)
}

// verifyRestFunction checks the token scope against a route function point.
// The function may be an alternatives list joined with "|" (any match passes),
// used for read paths that several restricted read-only token shapes must
// keep working (e.g. vault discovery accepts vault_r / note_r / file_r).
// verifyRestFunction 按路由功能点校验令牌 scope。功能点可以是 "|" 连接的
// 多选列表（任一匹配即通过），用于多种受限只读令牌都必须可用的读路径
// （例如发现库接受 vault_r / note_r / file_r）。
func verifyRestFunction(scope, protocol, clientType, function string, strictEmptyProtocol bool) bool {
	if function == "" {
		return app.VerifyPermissionsWithStrictEmptyProtocol(scope, protocol, clientType, "", strictEmptyProtocol)
	}
	for _, candidate := range strings.Split(function, "|") {
		if app.VerifyPermissionsWithStrictEmptyProtocol(scope, protocol, clientType, candidate, strictEmptyProtocol) {
			return true
		}
	}
	return false
}

// RouteFunctionClassified reports whether the given route is classified in
// the REST route-function table. Exported for the route classification guard
// test: adding a route under the auth group without classifying it makes the
// guard fail at test time instead of the route silently failing closed (or
// worse, passing unchecked) in production.
// RouteFunctionClassified 报告给定路由是否已进入 REST 功能点归类表。
// 供路由归类守卫测试使用：在鉴权组下新增路由而未归类时，守卫在测试期失败，
// 而不是等到生产环境才发现新路由被静默拒绝（或更糟：未经校验放行）。
func RouteFunctionClassified(method, path string) bool {
	isRead := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
	_, appErr := restRouteFunction(path, method, isRead)
	return appErr == nil
}

// restRouteFunction maps a REST route under the plain auth group to the RBAC
// function point required to call it. Every route reachable with a manual
// token must be classified; unknown paths fail closed with an explicit error
// instead of passing with an empty (unchecked) function.
// restRouteFunction 把普通鉴权组内的 REST 路由映射到所需的 RBAC 功能点。
// 手动令牌能到达的每条路由都必须归类；未归类路径显式拒绝，而不是以空
// 功能点（等于放行）通过。
func restRouteFunction(path, method string, isRead bool) (string, *code.Code) {
	deny := func() (string, *code.Code) {
		return "", code.ErrorAuthTokenScopeRestricted.WithDetails("Permission denied: " + path)
	}

	switch {
	case path == "/api/webgui/events":
		return "note_r", nil

	// ---------------- Git sync (client-safe, vault-scoped) ----------------
	// Exactly these two routes are reachable with note_r/note_w manual tokens; the
	// vault allowlist check below then confines them to the token's vaults. Every
	// other /api/git-sync/* route stays config_r/config_w and WebGUI-only.
	// 仅这两条 Git 同步路由允许 note_r/note_w 手动令牌；下方库白名单校验再把它们
	// 限制在令牌授权的库内。其余 /api/git-sync/* 仍是 config_r/config_w 且仅限 WebGUI。
	case path == "/api/git-sync/status":
		return "note_r", nil
	case path == "/api/git-sync/trigger":
		return "note_w", nil
	// ---------------- Vault ----------------
	// Vault discovery is needed before REST clients can address note/file
	// APIs; any read capability suffices so restricted read-only tokens keep
	// working. Vault mutations stay WebGUI-only (route group), but classify
	// defensively in case routing changes.
	case path == "/api/vault":
		if isRead {
			// vault_r / note_r / file_r 任一：受限只读令牌仍能发现库
			return "vault_r|note_r|file_r", nil
		}
		return "vault_w", nil

	// ---------------- Recycle bin clear (body-dependent) ----------------
	// Both a single-path clear and a whole-recycle-bin clear reach the same
	// route, so the route-level check accepts either capability; the handler
	// then requires note_purge/file_purge for the whole-bin form (empty path
	// and path hash) and note_w/file_w otherwise, matching the MCP tools.
	// 单条清理与整库清空共用同一路由，路由层接受任一能力；handler 绑定参数后
	// 对整库清空（路径与路径哈希均为空）要求 note_purge/file_purge，其余仍为
	// note_w/file_w，与 MCP 工具一致。
	case path == "/api/note/recycle-clear":
		return "note_w|note_purge", nil
	case path == "/api/file/recycle-clear":
		return "file_w|file_purge", nil

	// ---------------- Note ----------------
	case strings.HasPrefix(path, "/api/note"), strings.HasPrefix(path, "/api/folder"):
		if isRead {
			return "note_r", nil
		}
		return "note_w", nil

	// ---------------- File ----------------
	case strings.HasPrefix(path, "/api/file"), strings.HasPrefix(path, "/api/storage"):
		if isRead {
			return "file_r", nil
		}
		return "file_w", nil

	// ---------------- Setting ----------------
	case strings.HasPrefix(path, "/api/setting"):
		if isRead {
			return "config_r", nil
		}
		return "config_w", nil

	// ---------------- Share (B2) ----------------
	// Creating/modifying/cancelling a share exposes note content publicly and
	// is treated as a note write; listing/querying is a note read. The
	// share-token public read endpoints are outside the auth group and
	// unaffected.
	case path == "/api/share", strings.HasPrefix(path, "/api/share/"), path == "/api/shares":
		if isRead {
			return "note_r", nil
		}
		return "note_w", nil

	// ---------------- MCP ----------------
	// /api/mcp routes are guarded by the MCP middleware chain; function points
	// are enforced per-tool inside the MCP handlers, so the middleware only
	// authenticates here.
	// /api/mcp 路由由 MCP 中间件链把关；功能点在 MCP 工具内部逐个校验，
	// 此处只做认证。
	case path == "/api/mcp" || strings.HasPrefix(path, "/api/mcp/"):
		return "", nil

	// ---------------- Version ----------------
	// Latency probe triggers outbound requests; harmless permission-wise,
	// requires no function point (authenticated caller is enough).
	case path == "/api/version/probe":
		return "", nil

	// ---------------- User ----------------
	case path == "/api/user/info", path == "/api/auth/logout":
		return "", nil
	case strings.HasPrefix(path, "/api/oauth/stytch/"):
		return "", nil

	// ---------------- Admin status & upgrade (plain group) ----------------
	// /api/admin/check only reports whether the caller is the effective admin:
	// no function point (any authenticated token may ask). /api/admin/upgrade
	// is classified as a config write: together with the effective-admin check
	// in the handler this admits WebGUI login tokens (f:*) and admin-owned
	// manual tokens carrying config_w, and rejects restricted tokens with only
	// note/file scopes. Vault restrictions do not apply to either route — the
	// upgrade handler does not touch vault data and check is user-level.
	// /api/admin/check 仅报告调用者是否为生效管理员：无功能点（任何已认证
	// 令牌均可查询）。/api/admin/upgrade 归类为配置写：配合 handler 内部的
	// 生效管理员校验，WebGUI 登录令牌（f:*）与管理员名下含 config_w 的手动
	// 令牌可用，仅笔记/附件权限的受限令牌被拒。两个接口均不适用库限制：
	// 升级 handler 不触碰库内数据，check 属用户级。
	case path == "/api/admin/check":
		return "", nil
	case path == "/api/admin/ws_clients":
		// Read-only online client list: the handler requires the effective
		// admin, so no function point is needed. Kept out of the config_r
		// classification below so an admin's own sync token (note/file scopes)
		// still works, matching the pre-3.8.0 client behavior.
		// 只读在线客户端列表：handler 要求生效管理员，无需功能点。不归入下方
		// 的 config_r 分类，管理员自己的同步令牌（笔记/附件权限）仍可用，
		// 与 3.8.0 之前的客户端行为一致。
		return "", nil
	case path == "/api/admin/upgrade":
		return "config_w", nil

	// ---------------- WebGUI group ----------------
	// Routes mounted under the WebGUI group are additionally guarded by
	// RequireWebGUI (login token + webgui client) and, for admin routes, the
	// effective-admin check. Login tokens carry f:*, so classifying them as
	// config keeps the function dimension non-empty without affecting any
	// legitimate WebGUI caller.
	// WebGUI 组下的路由另由 RequireWebGUI（登录令牌 + webgui 客户端）与管理员
	// 校验把关。登录令牌的 scope 是 f:*，这里归为 config 维度只是保证功能点
	// 非空，不影响任何合法的 WebGUI 调用方。
	case strings.HasPrefix(path, "/api/admin/"), strings.HasPrefix(path, "/api/user/change_password"),
		strings.HasPrefix(path, "/api/vault/"), path == "/api/tokens", strings.HasPrefix(path, "/api/token"),
		path == "/api/storage", strings.HasPrefix(path, "/api/storage/"),
		strings.HasPrefix(path, "/api/backup"), strings.HasPrefix(path, "/api/git-sync"),
		path == "/api/sync-logs", path == "/api/webgui/config":
		if isRead {
			return "config_r", nil
		}
		return "config_w", nil

	default:
		// Anything else under the auth group is unclassified. This includes
		// admin/config routes (WebGUI group enforces admin separately, but a
		// manual token must not slip through on function checks) — fail
		// closed and update the table when adding routes.
		// 鉴权组内其余路径均未归类（含 admin/config 等本就属于 WebGUI 组、
		// 由管理员校验单独把关的路由）。为避免手动令牌借空功能点放行，
		// 这里按拒绝处理；新增路由时请同步更新本表。
		_ = method
		return deny()
	}
}

// UserAuthToken user Token authentication middleware (no secret key, always fails)
// UserAuthToken 用户 Token 认证中间件（无密钥，始终失败）
// Deprecated: Use UserAuthTokenWithConfig instead
// Deprecated: 推荐使用 UserAuthTokenWithConfig
func UserAuthToken() gin.HandlerFunc {
	// Without token service this cannot work properly in 3D RBAC
	return UserAuthTokenWithConfig("", nil)
}

// respondUserAuthError uses HTTP authentication statuses for the streaming endpoint.
// respondUserAuthError 为事件流端点返回 HTTP 鉴权状态，保留其它 REST 接口的响应约定。
func respondUserAuthError(c *gin.Context, appErr *code.Code) {
	if c.Request.URL.Path != "/api/webgui/events" {
		app.NewResponse(c).ToResponse(appErr)
		return
	}
	status := http.StatusUnauthorized
	switch appErr.Code() {
	case code.ErrorAuthTokenClientRestricted.Code(), code.ErrorAuthTokenScopeRestricted.Code(), code.ErrorAuthTokenUARestricted.Code(), code.ErrorAuthTokenIPRestricted.Code():
		status = http.StatusForbidden
	}
	c.JSON(status, app.Res{Code: appErr.Code(), Status: false, Message: appErr.MsgIn(c.GetString("lang"))})
}
