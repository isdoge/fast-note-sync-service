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
		response := app.NewResponse(c)
		user, scope, vaults, dbToken, appErr := AuthenticateUserTokenStrict(c, secretKey, tokenService, strictEmptyProtocol)
		if appErr != nil {
			response.ToResponse(appErr)
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
		c.Next()
	}
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
		if targetVault != "" && !util.VerifyVaultAccess(dbToken.Vaults, targetVault) {
			return nil, "", "", nil, code.ErrorAuthTokenScopeRestricted.WithDetails("Vault access restricted: " + targetVault)
		}
	}

	go func() {
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
		_ = tokenService.RecordAccessLog(context.Background(), log)
	}()

	return user, dbToken.Scope, dbToken.Vaults, dbToken, nil
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
