package api_router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/middleware"
	"github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func decodeShareBaseURL(t *testing.T, w *httptest.ResponseRecorder) (dto.ShareBaseURLResponse, int64) {
	t.Helper()
	var resp struct {
		Code int64                    `json:"code"`
		Data dto.ShareBaseURLResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "body: %s", w.Body.String())
	return resp.Data, int64(resp.Code)
}

func newShareBaseURLHandler(t *testing.T) (*ShareBaseURLHandler, *app.App) {
	t.Helper()
	mockUserSvc := new(mocks.MockUserService)
	testApp := app.NewTestApp(&app.Services{UserService: mockUserSvc})
	testApp.Config().User.AdminUID = 1
	mockUserSvc.On("GetAllUIDs", mock.Anything).Return([]int64{1, 2}, nil).Maybe()
	return NewShareBaseURLHandler(testApp), testApp
}

func TestShareBaseURL_AdminUpdateAndGet(t *testing.T) {
	handler, testApp := newShareBaseURLHandler(t)

	c, w := newAdminTestContext(http.MethodPost, "/api/admin/share-base-url", `{"baseUrl":"https://share.example.com:12345/"}`, 1)
	handler.Update(c)
	data, rc := decodeShareBaseURL(t, w)
	require.Equal(t, int64(code.Success.Code()), rc, w.Body.String())
	assert.Equal(t, "https://share.example.com:12345", data.BaseURL, "去尾部斜杠")
	assert.Equal(t, app.ShareBaseURLSourceOverride, data.Source)

	c, w = newAdminTestContext(http.MethodGet, "/api/admin/share-base-url", "", 1)
	handler.Get(c)
	data, rc = decodeShareBaseURL(t, w)
	require.Equal(t, int64(code.Success.Code()), rc)
	assert.Equal(t, "https://share.example.com:12345", data.BaseURL)
	assert.Equal(t, app.ShareBaseURLSourceOverride, data.Source)

	// The effective value must flow into share link generation.
	assert.Equal(t, "https://share.example.com:12345", testApp.ShareBaseURLStore().Override())
}

func TestShareBaseURL_NonAdminRejected(t *testing.T) {
	handler, _ := newShareBaseURLHandler(t)

	c, w := newAdminTestContext(http.MethodPost, "/api/admin/share-base-url", `{"baseUrl":"https://x.example.com"}`, 2)
	handler.Update(c)
	_, rc := decodeShareBaseURL(t, w)
	assert.Equal(t, int64(code.ErrorUserIsNotAdmin.Code()), rc)

	c, w = newAdminTestContext(http.MethodGet, "/api/admin/share-base-url", "", 2)
	handler.Get(c)
	_, rc = decodeShareBaseURL(t, w)
	assert.Equal(t, int64(code.ErrorUserIsNotAdmin.Code()), rc)
}

func TestShareBaseURL_StaticTokenBypassesAdmin(t *testing.T) {
	handler, testApp := newShareBaseURLHandler(t)
	testApp.Config().Security.ShareBaseURLToken = "s3cret-static-token"

	// Simulate the middleware having authenticated via the static token.
	c, w := newAdminTestContext(http.MethodPost, "/api/admin/share-base-url", `{"baseUrl":"https://tunnel.example.com"}`, 0)
	c.Set("share_base_url_static_auth", true)
	handler.Update(c)
	data, rc := decodeShareBaseURL(t, w)
	require.Equal(t, int64(code.Success.Code()), rc, w.Body.String())
	assert.Equal(t, "https://tunnel.example.com", data.BaseURL)
}

func TestShareBaseURL_InvalidURLRejected(t *testing.T) {
	handler, _ := newShareBaseURLHandler(t)

	for _, body := range []string{
		`{"baseUrl":"ftp://example.com"}`,
		`{"baseUrl":"example.com"}`,
		`{"baseUrl":"https://example.com?x=1"}`,
		`{"baseUrl":"https://example.com#frag"}`,
		`{"baseUrl":"https://exa\nmple.com"}`,
		`{"baseUrl":"https://"}`,
		`{"baseUrl":"https://user:pass@example.com"}`,
	} {
		c, w := newAdminTestContext(http.MethodPost, "/api/admin/share-base-url", body, 1)
		handler.Update(c)
		_, rc := decodeShareBaseURL(t, w)
		assert.Equal(t, int64(code.ErrorInvalidParams.Code()), rc, "body %q should be rejected: %s", body, w.Body.String())
	}
}

func TestShareBaseURL_EmptyClearsOverride(t *testing.T) {
	handler, testApp := newShareBaseURLHandler(t)

	c, w := newAdminTestContext(http.MethodPost, "/api/admin/share-base-url", `{"baseUrl":"https://share.example.com"}`, 1)
	handler.Update(c)
	require.Equal(t, "https://share.example.com", testApp.ShareBaseURLStore().Override())

	c, w = newAdminTestContext(http.MethodPost, "/api/admin/share-base-url", `{"baseUrl":""}`, 1)
	handler.Update(c)
	data, rc := decodeShareBaseURL(t, w)
	require.Equal(t, int64(code.Success.Code()), rc)
	assert.Empty(t, data.BaseURL)
	assert.Equal(t, app.ShareBaseURLSourceConfig, data.Source, "清除后回落到配置来源")
	assert.Empty(t, testApp.ShareBaseURLStore().Override())
}

// 持久化到文件后，用同一路径新建的 store 必须能重新加载（模拟重启）。
func TestShareBaseURL_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "share_base_url")

	store := app.NewShareBaseURLStore(path)
	require.NoError(t, store.Set("https://reload.example.com:8443"))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "持久化文件权限必须为 0600")

	reloaded := app.NewShareBaseURLStore(path)
	require.NoError(t, reloaded.Load())
	assert.Equal(t, "https://reload.example.com:8443", reloaded.Override())

	// Clearing removes the file so a fresh store loads nothing.
	require.NoError(t, reloaded.Set(""))
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "清除覆盖应删除持久化文件")
	fresh := app.NewShareBaseURLStore(path)
	require.NoError(t, fresh.Load())
	assert.Empty(t, fresh.Override())
}

func TestShareBaseURL_GetReportsConfigSourceWhenNoOverride(t *testing.T) {
	handler, testApp := newShareBaseURLHandler(t)
	// The configured value only takes effect with both ports configured.
	// 配置值只在两个端口都配置时生效。
	testApp.Config().Server.WebGuiPort = "8080"
	testApp.Config().Server.SharePort = "9100"
	testApp.Config().Server.ShareBaseUrl = "https://config.example.com"

	c, w := newAdminTestContext(http.MethodGet, "/api/admin/share-base-url", "", 1)
	handler.Get(c)
	data, rc := decodeShareBaseURL(t, w)
	require.Equal(t, int64(code.Success.Code()), rc)
	assert.Equal(t, "https://config.example.com", data.BaseURL)
	assert.Equal(t, app.ShareBaseURLSourceConfig, data.Source)
}

// 静态令牌认证中间件：正确令牌放行，错误令牌落到用户令牌路径并被拒。
func TestShareBaseURLAuthMiddleware_StaticToken(t *testing.T) {
	staticToken := "static-token-abc"
	middlewareFn := middleware.ShareBaseURLAuth("", nil, false, staticToken)

	newCtx := func(header, value string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/share-base-url", nil)
		if header != "" {
			c.Request.Header.Set(header, value)
		}
		return c, w
	}

	// Correct token via the dedicated header.
	c, _ := newCtx("X-Share-Base-Url-Token", staticToken)
	middlewareFn(c)
	assert.True(t, middleware.ShareBaseURLStaticAuthorized(c), "正确静态令牌应放行")

	// Correct token via Bearer.
	c, _ = newCtx("Authorization", "Bearer "+staticToken)
	middlewareFn(c)
	assert.True(t, middleware.ShareBaseURLStaticAuthorized(c))

	// Wrong token falls through to user-token auth, which aborts (no token svc).
	c, w := newCtx("X-Share-Base-Url-Token", "wrong")
	middlewareFn(c)
	assert.False(t, middleware.ShareBaseURLStaticAuthorized(c))
	assert.True(t, c.IsAborted(), "错误令牌必须被拒绝")
	var res pkgapp.Res
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.Equal(t, int64(code.ErrorNotUserAuthToken.Code()), int64(res.Code))
}

// 未配置静态令牌时，仅凭任意静态头不能通过。
func TestShareBaseURLAuthMiddleware_NoStaticTokenConfigured(t *testing.T) {
	middlewareFn := middleware.ShareBaseURLAuth("", nil, false, "")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/share-base-url", nil)
	c.Request.Header.Set("X-Share-Base-Url-Token", "anything")
	middlewareFn(c)
	assert.True(t, c.IsAborted(), "未配置静态令牌时静态头无效")
	assert.False(t, middleware.ShareBaseURLStaticAuthorized(c))
}

// 管理接口报告的值必须与分享链接实际采用的配置值一致：端口没配齐时配置值
// 不生效，两边都不能声称用了它。
func TestShareBaseURL_ConfigValueConsistentWithShareLinks(t *testing.T) {
	shareHandler := newTestShareHandler(nil, nil)
	testApp := shareHandler.App
	testApp.Config().Server.ShareBaseUrl = "https://config.example.com"

	c, _ := newShareTestContext(http.MethodPost, "/api/share", "", 1)
	c.Request.Host = "sync.example.com:9000"

	// One port missing: links fall back to the request address and the admin
	// value reports "no fixed base URL".
	// 端口没配齐：链接回落到请求地址，管理接口报告“没有固定基址”。
	assert.NotEqual(t, "https://config.example.com", shareHandler.getShareBaseUrl(c))
	value, source := testApp.ShareBaseURLValue()
	assert.Empty(t, value)
	assert.Equal(t, app.ShareBaseURLSourceConfig, source)

	testApp.Config().Server.WebGuiPort = "8080"
	testApp.Config().Server.SharePort = "9100"
	assert.Equal(t, "https://config.example.com", shareHandler.getShareBaseUrl(c))
	value, source = testApp.ShareBaseURLValue()
	assert.Equal(t, "https://config.example.com", value)
	assert.Equal(t, app.ShareBaseURLSourceConfig, source)
}

// 分享链接生成优先使用运行时覆盖值。
func TestShareBaseURL_FlowsIntoShareLinks(t *testing.T) {
	handler, testApp := newShareBaseURLHandler(t)
	require.NoError(t, testApp.ShareBaseURLStore().Set("https://runtime.example.com:1234"))
	testApp.Config().Server.ShareBaseUrl = "https://config.example.com"

	shareSvc := new(svcmocks.MockShareService)
	shareSvc.On("ShareGenerate", mock.Anything, int64(1), "main", "test.md", "hash_123", "", int64(0)).
		Return(&dto.ShareCreateResponse{ID: 1, Token: "st"}, nil)
	shareHandler := newTestShareHandler(shareSvc, nil)
	// newTestShareHandler builds its own app; copy the runtime store over.
	shareHandler.App.SetShareBaseURLStore(testApp.ShareBaseURLStore())
	shareHandler.App.Config().Server.ShareBaseUrl = "https://config.example.com"

	c, w := newShareTestContext(http.MethodPost, "/api/share", `{"vault":"main","path":"test.md","pathHash":"hash_123","password":""}`, 1)
	c.Request.Host = "sync.example.com:9000"
	shareHandler.Create(c)

	res := decodeRes(t, w)
	data, _ := res["data"].(map[string]any)
	baseURL, _ := data["baseUrl"].(string)
	assert.Equal(t, "https://runtime.example.com:1234", baseURL, "运行时覆盖值应优先于配置")
	_ = handler
}
