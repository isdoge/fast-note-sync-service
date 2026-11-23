package middleware

import (
	"crypto/subtle"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
)

// contextKeyShareBaseURLStaticAuth marks a request authenticated by the static
// share-base-url token instead of a user token.
// contextKeyShareBaseURLStaticAuth 标记请求由分享基址静态令牌认证，而非用户令牌。
const contextKeyShareBaseURLStaticAuth = "share_base_url_static_auth"

// ShareBaseURLTokenHeader is the dedicated static-token header for the share
// base URL admin endpoints.
// ShareBaseURLTokenHeader 是分享基址管理接口的专用静态令牌请求头。
const ShareBaseURLTokenHeader = "X-Share-Base-Url-Token"

// ShareBaseURLAuth guards the runtime share base URL admin endpoints. A request
// is accepted when either:
//   - it carries the configured static token (Authorization: Bearer <token> or
//     X-Share-Base-Url-Token), compared in constant time; or
//   - it passes normal user-token authentication (the handler then requires the
//     effective admin).
//
// When no static token is configured, only the user-token path is available.
// ShareBaseURLAuth 保护运行时分享基址管理接口。请求满足以下任一条件即放行：
//   - 携带配置的静态令牌（Authorization: Bearer <token> 或
//     X-Share-Base-Url-Token），按常量时间比较；
//   - 通过普通用户令牌鉴权（handler 随后要求生效管理员）。
//
// 未配置静态令牌时只有用户令牌路径可用。
func ShareBaseURLAuth(secretKey string, tokenService service.TokenService, strictEmptyProtocol bool, staticToken string) gin.HandlerFunc {
	staticToken = strings.TrimSpace(staticToken)

	return func(c *gin.Context) {
		if staticToken != "" {
			presented := ExtractShareBaseURLToken(c)
			if presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(staticToken)) == 1 {
				c.Set(contextKeyShareBaseURLStaticAuth, true)
				c.Next()
				return
			}
		}
		if tokenService == nil {
			// No token service means user-token auth cannot run (e.g. a
			// misconfigured test app); fail closed.
			// 没有令牌服务时用户令牌鉴权无法运行（例如配置不全的测试 App），
			// 直接拒绝。
			pkgapp.NewResponse(c).ToResponse(code.ErrorNotUserAuthToken)
			c.Abort()
			return
		}
		UserAuthTokenWithConfigStrict(secretKey, tokenService, strictEmptyProtocol)(c)
	}
}

// ShareBaseURLStaticAuthorized reports whether the request was authenticated by
// the static share-base-url token.
// ShareBaseURLStaticAuthorized 报告请求是否由分享基址静态令牌认证。
func ShareBaseURLStaticAuthorized(c *gin.Context) bool {
	value, ok := c.Get(contextKeyShareBaseURLStaticAuth)
	if !ok {
		return false
	}
	authorized, _ := value.(bool)
	return authorized
}

// ExtractShareBaseURLToken reads the static token from the Authorization
// bearer header or the dedicated header.
// ExtractShareBaseURLToken 从 Authorization Bearer 头或专用头读取静态令牌。
func ExtractShareBaseURLToken(c *gin.Context) string {
	if auth := c.GetHeader("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
	}
	return strings.TrimSpace(c.GetHeader(ShareBaseURLTokenHeader))
}
