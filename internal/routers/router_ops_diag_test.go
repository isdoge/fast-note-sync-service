package routers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/locales/en"
	"github.com/go-playground/locales/zh"
	ut "github.com/go-playground/universal-translator"
	internalapp "github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/require"
)

// 路由级守卫：
// - GET /api/user/token-capabilities 在普通鉴权组、无功能点，受限令牌也能查自己；
// - GET/POST /api/admin/share-base-url 走静态令牌或管理员中间件；
// - GET /api/upgrade/cli 需要已登录用户令牌（未认证被拒）。
// 本文件构建最小 App 并直接调 registerAPIRoutes 验证真实路由链。

func newOpsRouter(t *testing.T, tokenSvc service.TokenService, configure func(*internalapp.AppConfig)) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	testApp := newAdminGroupTestApp(t, tokenSvc)
	if configure != nil {
		configure(testApp.Config())
	}
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	uni := ut.New(en.New(), en.New(), zh.New())
	wss := app.NewWebsocketServer(app.WSConfig{}, testApp)
	registerAPIRoutes(r, testApp, wss, uni)
	return r
}

func runOpsRequest(t *testing.T, router http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestTokenCapabilitiesRoute_RestrictedTokenReachable(t *testing.T) {
	secretKey := "ops-test-secret"
	tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{
		ID: 11, UID: 2, TokenString: "nonce-a", Status: 1,
		Scope: "p:rest c:CLI f:note_r,file_r", IssueType: 2, Vaults: "doge",
		ExpiredAt: time.Now().Add(time.Hour),
	}}
	router := newOpsRouter(t, tokenSvc, func(c *internalapp.AppConfig) { c.Security.AuthTokenKey = secretKey })

	rec := runOpsRequest(t, router, http.MethodGet, "/api/user/token-capabilities", "", map[string]string{
		"Authorization": "Bearer " + adminGroupJWT(t, secretKey, "nonce-a", 2, 11),
		"x-client":      "CLI",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var res struct {
		Code int64 `json:"code"`
		Data struct {
			UID         int64    `json:"uid"`
			TokenID     string   `json:"tokenId"`
			Protocol    string   `json:"protocol"`
			Restricted  bool     `json:"restricted"`
			Vaults      []string `json:"vaults"`
			ReadOnly    bool     `json:"readOnly"`
			Permissions struct {
				NoteRead  bool `json:"noteRead"`
				NoteWrite bool `json:"noteWrite"`
				FileRead  bool `json:"fileRead"`
			} `json:"permissions"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, int64(1), res.Code, rec.Body.String())
	require.Equal(t, int64(2), res.Data.UID)
	require.Equal(t, "11", res.Data.TokenID, "tokenId 来自中间件写入的数据库令牌 ID")
	require.Equal(t, "rest", res.Data.Protocol)
	require.True(t, res.Data.Restricted)
	require.Equal(t, []string{"doge"}, res.Data.Vaults)
	require.True(t, res.Data.ReadOnly)
	require.True(t, res.Data.Permissions.NoteRead)
	require.False(t, res.Data.Permissions.NoteWrite)
	require.True(t, res.Data.Permissions.FileRead)
}

func TestUpgradeCLIRoute_RequiresAuth(t *testing.T) {
	router := newOpsRouter(t, &adminGroupTokenService{}, nil)

	rec := runOpsRequest(t, router, http.MethodGet, "/api/upgrade/cli", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, "业务码响应")
	var res app.Res
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.NotEqual(t, int64(1), int64(res.Code), "未认证不得访问 CLI 中转接口: %s", rec.Body.String())
}

func TestShareBaseURLRoute_StaticTokenWithoutLogin(t *testing.T) {
	staticToken := "s3cret-static-token"
	router := newOpsRouter(t, &adminGroupTokenService{}, func(c *internalapp.AppConfig) {
		c.Security.ShareBaseURLToken = staticToken
	})

	// POST with the static token, no user token at all.
	rec := runOpsRequest(t, router, http.MethodPost, "/api/admin/share-base-url", `{"baseUrl":"https://tunnel.example.com"}`, map[string]string{
		"X-Share-Base-Url-Token": staticToken,
	})
	var res app.Res
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, int64(1), int64(res.Code), "静态令牌应放行: %s", rec.Body.String())

	// GET is admin-only: the static notifier token can push but not read back.
	rec = runOpsRequest(t, router, http.MethodGet, "/api/admin/share-base-url", "", map[string]string{
		"Authorization": "Bearer " + staticToken,
	})
	var got app.Res
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotEqual(t, int64(1), int64(got.Code), "静态令牌不得回读当前值: %s", rec.Body.String())

	// A wrong static token is rejected.
	rec = runOpsRequest(t, router, http.MethodPost, "/api/admin/share-base-url", `{"baseUrl":"https://evil.example.com"}`, map[string]string{
		"X-Share-Base-Url-Token": "wrong",
	})
	res = app.Res{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.NotEqual(t, int64(1), int64(res.Code), "错误令牌必须被拒: %s", rec.Body.String())
}
