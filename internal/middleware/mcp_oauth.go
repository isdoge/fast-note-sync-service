package middleware

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	internaloauth "github.com/haierkeys/fast-note-sync-service/internal/oauth"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
)

type MCPOAuthVerifier interface {
	VerifyBearerToken(ctx context.Context, token string, clientType string) (*MCPOAuthClaims, error)
}

type MCPOAuthScopeMapper func(*MCPOAuthClaims) string

type MCPOAuthClaims struct {
	UID           int64
	UserID        int64
	Scope         string
	FNSScope      string
	OAuthScopes   []string
	ClientID      string
	ClientName    string
	ClientVersion string
}

type MCPMixedAuthConfig struct {
	OAuthEnabled         bool
	Resource             string
	AllowStaticFNSToken  bool
	Verifier             MCPOAuthVerifier
	ScopeMapper          MCPOAuthScopeMapper
	DefaultClient        string
	DefaultClientName    string
	DefaultClientVersion string
	// StrictEmptyProtocol rejects scopes with an empty protocol dimension
	// instead of treating them as wildcards (security.strict-empty-protocol).
	// StrictEmptyProtocol 拒绝协议维度为空的 scope，不再当通配处理
	// （security.strict-empty-protocol）。
	StrictEmptyProtocol bool
	// OAuthVaults is the vault allowlist injected for OAuth-authenticated
	// requests (oauth.vaults). Empty means unrestricted.
	// OAuthVaults 是 OAuth 鉴权请求注入的库白名单（oauth.vaults）。空 = 不限制。
	OAuthVaults string
}

func MCPOAuthWithConfig(oauthCfg config.OAuthConfig, secretKey string, tokenService service.TokenService, userRepo domain.UserRepository) gin.HandlerFunc {
	return MCPOAuthWithConfigStrict(oauthCfg, secretKey, tokenService, userRepo, false)
}

// MCPOAuthWithConfigStrict is MCPOAuthWithConfig with the strict
// empty-protocol flag forwarded to scope verification.
// MCPOAuthWithConfigStrict 是带严格空协议开关的 MCPOAuthWithConfig。
func MCPOAuthWithConfigStrict(oauthCfg config.OAuthConfig, secretKey string, tokenService service.TokenService, userRepo domain.UserRepository, strictEmptyProtocol bool) gin.HandlerFunc {
	oauthCfg.Normalize()
	return MCPMixedAuthWithConfig(secretKey, tokenService, MCPMixedAuthConfig{
		OAuthEnabled:         oauthCfg.Enabled,
		Resource:             oauthCfg.Resource,
		AllowStaticFNSToken:  oauthCfg.AllowStaticFNSToken,
		Verifier:             newOAuthVerifierAdapter(oauthCfg, userRepo),
		DefaultClient:        oauthCfg.DefaultClient,
		DefaultClientName:    oauthCfg.DefaultClientName,
		DefaultClientVersion: oauthCfg.DefaultClientVersion,
		StrictEmptyProtocol:  strictEmptyProtocol,
		OAuthVaults:          strings.Join(oauthCfg.Vaults, ","),
	})
}

// mcpPlainTokenAuth 在 OAuth 未启用时承担 /api/mcp 的认证。
//
// 与 UserAuthTokenWithConfig 的唯一差别在失败路径：本函数返回 MCP 规范形态
// （401 + WWW-Authenticate + {"error":...}，复用 writeOAuthChallenge），
// 而不是 response.ToResponse(appErr) —— 后者因 pkg/code 的 Code.StatusCode()
// 恒返回 http.StatusOK，会产出 HTTP 200 + {"code":307,"尚未登录"}，
// MCP 客户端据此把响应读成「零 tool」并拦停整个 job（kvpac-ymehx）。
func mcpPlainTokenAuth(secretKey string, tokenService service.TokenService, resource string, strictEmptyProtocol bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, scope, vaults, dbToken, appErr := AuthenticateUserTokenStrict(c, secretKey, tokenService, strictEmptyProtocol)
		if appErr != nil {
			writeOAuthChallenge(c, http.StatusUnauthorized, resource, "invalid_token")
			return
		}
		c.Set("user_token", user)
		c.Set("scope", scope)
		c.Set("vaults", vaults)
		// 与 UserAuthTokenWithConfig 保持一致：注入服务端 Token 属性供下游中间件联合校验
		c.Set("token_issue_type", dbToken.IssueType)
		c.Set("token_client_type", dbToken.ClientType)
		c.Next()
	}
}

func MCPMixedAuthWithConfig(secretKey string, tokenService service.TokenService, cfg MCPMixedAuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !cfg.OAuthEnabled {
			// MCP 规范形态：认证失败应返回 401 + WWW-Authenticate。
			// 原实现走 UserAuthTokenWithConfig → response.ToResponse(appErr)，而 pkg/code 的
			// Code.StatusCode() 恒返回 http.StatusOK，故产出 HTTP 200 + {"code":307,"尚未登录"}。
			// MCP 客户端会把 200 当作成功去解析 JSON-RPC，失败后判「零 tool」，
			// 冷启动首连即拦停整个 job（见 kvpac-ymehx）。
			mcpPlainTokenAuth(secretKey, tokenService, cfg.Resource, cfg.StrictEmptyProtocol)(c)
			return
		}

		if cfg.AllowStaticFNSToken && ExtractUserAuthToken(c) != "" {
			if user, scope, vaults, _, err := AuthenticateUserTokenStrict(c, secretKey, tokenService, cfg.StrictEmptyProtocol); err == nil {
				c.Set("user_token", user)
				c.Set("scope", scope)
				c.Set("vaults", vaults)
				c.Next()
				return
			}
		}

		token := extractOAuthBearerToken(c)
		if token == "" || cfg.Verifier == nil {
			log.Printf("mcp oauth rejected: reason=missing_bearer_token path=%s client=%q verifier_configured=%t", c.Request.URL.Path, c.GetHeader("X-Client"), cfg.Verifier != nil)
			writeOAuthChallenge(c, http.StatusUnauthorized, cfg.Resource, "invalid_token")
			return
		}

		ensureMCPClientHeaders(c, cfg, nil)
		claims, err := cfg.Verifier.VerifyBearerToken(c.Request.Context(), token, c.GetHeader("X-Client"))
		if err != nil || claims == nil {
			log.Printf("mcp oauth rejected: reason=verify_failed path=%s client=%q insufficient_scope=%t err=%q", c.Request.URL.Path, c.GetHeader("X-Client"), errors.Is(err, internaloauth.ErrInsufficientScope), err)
			if errors.Is(err, internaloauth.ErrInsufficientScope) {
				writeOAuthChallenge(c, http.StatusForbidden, cfg.Resource, "insufficient_scope")
				return
			}
			writeOAuthChallenge(c, http.StatusUnauthorized, cfg.Resource, "invalid_token")
			return
		}

		uid := claims.UID
		if uid == 0 {
			uid = claims.UserID
		}
		scope := claims.FNSScope
		if cfg.ScopeMapper != nil {
			scope = cfg.ScopeMapper(claims)
		}
		if scope == "" {
			scope = claims.Scope
		}

		ensureMCPClientHeaders(c, cfg, claims)
		if !pkgapp.VerifyPermissionsWithStrictEmptyProtocol(scope, "mcp", c.GetHeader("X-Client"), "", cfg.StrictEmptyProtocol) {
			log.Printf("mcp oauth rejected: reason=fns_permission_failed path=%s uid=%d client=%q fns_scope=%q oauth_scopes=%q", c.Request.URL.Path, uid, c.GetHeader("X-Client"), scope, strings.Join(claims.OAuthScopes, " "))
			writeOAuthChallenge(c, http.StatusForbidden, cfg.Resource, "insufficient_scope")
			return
		}

		c.Set("user_token", &pkgapp.UserEntity{UID: uid})
		c.Set("scope", scope)
		// Inject the configured OAuth vault allowlist; empty means all vaults
		// of the mapped user (existing connectors keep working).
		// 注入配置的 OAuth 库白名单；空表示映射用户的全部库
		// （现有连接器不受影响）。
		c.Set("vaults", cfg.OAuthVaults)
		// Keep token attributes consistent with the static-token branch (B8):
		// OAuth tokens are not database rows, so issue type 0 marks
		// "not a login/manual DB token" and the client type comes from the
		// verified headers above.
		// 与静态令牌分支保持一致的令牌属性注入（B8）：OAuth 令牌不是数据库
		// 行，issue type 用 0 表示"非登录/手动库内令牌"，客户端类型来自上方
		// 已校验的请求头。
		c.Set("token_issue_type", 0)
		c.Set("token_client_type", c.GetHeader("X-Client"))
		c.Next()
	}
}

func extractOAuthBearerToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	return ""
}

func ensureMCPClientHeaders(c *gin.Context, cfg MCPMixedAuthConfig, claims *MCPOAuthClaims) {
	var claimsClientID, claimsClientName, claimsClientVersion string
	if claims != nil {
		claimsClientID = claims.ClientID
		claimsClientName = claims.ClientName
		claimsClientVersion = claims.ClientVersion
	}
	clientType := firstNonEmpty(c.GetHeader("X-Client"), claimsClientID, cfg.DefaultClient, "MCP")
	clientName := firstNonEmpty(c.GetHeader("X-Client-Name"), claimsClientName, cfg.DefaultClientName, clientType)
	clientVersion := firstNonEmpty(c.GetHeader("X-Client-Version"), claimsClientVersion, cfg.DefaultClientVersion)

	c.Request.Header.Set("X-Client", clientType)
	c.Request.Header.Set("X-Client-Name", clientName)
	if clientVersion != "" {
		c.Request.Header.Set("X-Client-Version", clientVersion)
	}
}

func writeOAuthChallenge(c *gin.Context, status int, resource string, authErr string) {
	challenge := `Bearer`
	if resource != "" {
		challenge += ` resource_metadata="` + escapeAuthParam(oauthProtectedResourceMetadataURL(resource)) + `"`
		challenge += ` resource="` + escapeAuthParam(resource) + `"`
	}
	if authErr != "" {
		challenge += `, error="` + escapeAuthParam(authErr) + `"`
	}
	c.Header("WWW-Authenticate", challenge)
	c.AbortWithStatusJSON(status, gin.H{
		"error": authErr,
		"_meta": gin.H{
			"mcp/www_authenticate": challenge,
		},
	})
}

func oauthProtectedResourceMetadataURL(resource string) string {
	u, err := url.Parse(resource)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return resource
	}
	resourcePath := strings.TrimRight(u.Path, "/")
	u.Path = "/.well-known/oauth-protected-resource" + resourcePath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func escapeAuthParam(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type oauthVerifierAdapter struct {
	jwtVerifier   *internaloauth.JWTVerifier
	subjectMapper *internaloauth.SubjectMapper
	client        string
	defaultScope  string
}

func newOAuthVerifierAdapter(cfg config.OAuthConfig, userRepo domain.UserRepository) *oauthVerifierAdapter {
	audience := cfg.Resource
	if len(cfg.Audience) > 0 {
		audience = cfg.Audience[0]
	}
	return &oauthVerifierAdapter{
		jwtVerifier: internaloauth.NewJWTVerifier(internaloauth.JWTVerifierConfig{
			Issuer:         cfg.Issuer,
			Audience:       audience,
			JWKSURL:        cfg.JWKSURL,
			RequiredScopes: requiredOAuthScopes(cfg),
		}),
		subjectMapper: internaloauth.NewSubjectMapper(userRepo, internaloauth.SubjectMapperConfig{
			Mode:       cfg.SubjectMapping.Mode,
			EmailClaim: cfg.SubjectMapping.Claim,
			FixedUID:   cfg.SubjectMapping.FixedUID,
		}),
		client:       cfg.DefaultClient,
		defaultScope: cfg.DefaultFNSScope,
	}
}

func requiredOAuthScopes(cfg config.OAuthConfig) []string {
	if strings.TrimSpace(cfg.DefaultFNSScope) != "" {
		return nil
	}
	return cfg.RequiredScopes
}

func (v *oauthVerifierAdapter) VerifyBearerToken(ctx context.Context, token string, clientType string) (*MCPOAuthClaims, error) {
	claims, err := v.jwtVerifier.Verify(ctx, token)
	if err != nil {
		return nil, err
	}
	uid, err := v.subjectMapper.Map(ctx, claims.Raw)
	if err != nil {
		return nil, err
	}
	fnsScope := v.defaultScope
	if fnsScope == "" {
		client := firstNonEmpty(clientType, v.client)
		fnsScope, err = internaloauth.MapOAuthScopesToFNS(client, claims.Scopes)
		if err != nil {
			return nil, err
		}
	}
	return &MCPOAuthClaims{
		UID:         uid,
		Scope:       fnsScope,
		FNSScope:    fnsScope,
		OAuthScopes: claims.Scopes,
	}, nil
}
