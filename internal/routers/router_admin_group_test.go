package routers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/locales/en"
	"github.com/go-playground/locales/zh"
	ut "github.com/go-playground/universal-translator"
	internalapp "github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/require"
)

// 管理接口的路由归属守卫：
// - /api/admin/ws_clients、/api/admin/ws_client/:traceId 位于 WebGUI 鉴权组
//   （登录令牌 + webgui 客户端 + 生效管理员），手动 API 令牌被 RequireWebGUI 挡住；
// - /api/admin/check 位于普通鉴权组、无功能点（任何已认证令牌可查询身份）；
// - /api/admin/upgrade 位于普通鉴权组、归类为 config_w，handler 内部要求生效
//   管理员：WebGUI 登录令牌与管理员名下含 config_w 的手动令牌可用（插件场景），
//   仅笔记/附件权限的受限令牌与非管理员一律拒绝。
// 本测试构建最小 App 并直接调 registerAPIRoutes，验证路由层分组与归类。

// adminGroupTokenService 实现 service.TokenService，返回一张指定 scope 的令牌。
type adminGroupTokenService struct {
	activeToken *domain.AuthToken
}

func (s *adminGroupTokenService) Create(ctx context.Context, uid int64, params *dto.TokenIssueRequest) (*dto.TokenCreateResponse, error) {
	return nil, errors.New("not implemented")
}
func (s *adminGroupTokenService) CreateForLogin(ctx context.Context, uid int64, clientType, ip, userAgent string) (*domain.AuthToken, string, error) {
	return nil, "", errors.New("not implemented")
}
func (s *adminGroupTokenService) ListByUser(ctx context.Context, uid int64) ([]*dto.TokenResponse, error) {
	return nil, errors.New("not implemented")
}
func (s *adminGroupTokenService) Update(ctx context.Context, uid int64, tokenID int64, params *dto.TokenUpdateRequest) error {
	return errors.New("not implemented")
}
func (s *adminGroupTokenService) Revoke(ctx context.Context, uid int64, tokenID int64) error {
	return errors.New("not implemented")
}
func (s *adminGroupTokenService) Rotate(ctx context.Context, uid int64, tokenID int64) (*dto.TokenCreateResponse, error) {
	return nil, errors.New("not implemented")
}
func (s *adminGroupTokenService) RotateForLogin(ctx context.Context, uid int64, tokenID int64, ip, userAgent string) (*domain.AuthToken, string, error) {
	return nil, "", errors.New("not implemented")
}
func (s *adminGroupTokenService) GetActiveToken(ctx context.Context, uid int64, tokenID int64) (*domain.AuthToken, error) {
	if s.activeToken != nil && s.activeToken.UID == uid {
		return s.activeToken, nil
	}
	return nil, errors.New("token not found")
}
func (s *adminGroupTokenService) RecordAccessLog(ctx context.Context, log *domain.AuthTokenLog) error {
	return nil
}
func (s *adminGroupTokenService) ListLogs(ctx context.Context, uid, tokenID int64, page, pageSize int) ([]*dto.TokenLogResponse, int64, error) {
	return nil, 0, errors.New("not implemented")
}
func (s *adminGroupTokenService) UpdateLastUsedAt(ctx context.Context, tokenID int64) error {
	return nil
}
func (s *adminGroupTokenService) SetSyncHandler(handler func(uid int64, tokenID int64, scope string, kick bool)) {
}
func (s *adminGroupTokenService) GetRecentClients(ctx context.Context, uid int64, duration time.Duration) (map[int64][]string, error) {
	return nil, nil
}
func (s *adminGroupTokenService) CleanExpired(ctx context.Context, uid int64, issueType int) error {
	return nil
}

var _ service.TokenService = (*adminGroupTokenService)(nil)

func newAdminGroupTestApp(t *testing.T, tokenSvc service.TokenService) *internalapp.App {
	t.Helper()
	gin.SetMode(gin.TestMode)
	testApp := internalapp.NewTestApp(&internalapp.Services{TokenService: tokenSvc})
	// NewTestApp 不初始化 Repositories / 指针配置；registerAPIRoutes 注册
	// MCP 路由时会读取 UserRepo 与 Tracer.Enabled，补上最小可用值。
	testApp.Repositories = &internalapp.Repositories{}
	cfg := testApp.Config()
	tracerEnabled := false
	cfg.Tracer.Enabled = &tracerEnabled
	return testApp
}

func runAdminGroupRequest(t *testing.T, testApp *internalapp.App, token, clientHeader, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	uni := ut.New(en.New(), en.New(), zh.New())
	registerAPIRoutes(r, testApp, nil, uni)

	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if clientHeader != "" {
		req.Header.Set("x-client", clientHeader)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// adminGroupJWT 用固定密钥签一张测试 JWT（uid 与 tokenID 由调用方指定）。
func adminGroupJWT(t *testing.T, secretKey, nonce string, uid, tokenID int64) string {
	t.Helper()
	tokenManager := app.NewTokenManager(app.TokenConfig{SecretKey: secretKey, Expiry: time.Hour})
	token, err := tokenManager.Generate(uid, "", "", tokenID, nonce)
	require.NoError(t, err)
	return token
}

// ws_clients / ws_client 位于 WebGUI 鉴权组：手动令牌即使带 webgui 头也被
// RequireWebGUI 挡住；/api/admin/upgrade 不再要求 WebGUI，改按功能点校验。
func TestAdminRoutesBlockManualToken(t *testing.T) {
	secretKey := "admin-group-test-secret"
	tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{
		ID: 5, UID: 2, TokenString: "nonce-a", Status: 1,
		Scope: "p:rest c:* f:*", IssueType: 2,
		ExpiredAt: time.Now().Add(time.Hour),
	}}
	testApp := newAdminGroupTestApp(t, tokenSvc)
	testApp.Config().Security.AuthTokenKey = secretKey

	for _, target := range []string{
		"/api/admin/ws_clients",
	} {
		rec := runAdminGroupRequest(t, testApp, adminGroupJWT(t, secretKey, "nonce-a", 2, 5), "webgui", http.MethodGet, target)
		require.Equal(t, http.StatusOK, rec.Code, "response uses business code envelope")
		var res app.Res
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		require.NotEqual(t, int64(1), int64(res.Code), "manual token must not pass: %s", target)
	}
}

// /api/admin/check 移回普通鉴权组且无功能点：任何已认证令牌（含受限令牌）
// 都可调用并得到 isAdmin 报告；AdminUID=0 多用户场景下非最小 UID 用户拿到
// isAdmin=false 而不是错误码。库限制（vaults）不影响该接口。
func TestAdminCheckReachableByLoginToken(t *testing.T) {
	secretKey := "admin-group-test-secret"
	tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{
		ID: 5, UID: 2, TokenString: "nonce-a", Status: 1,
		Scope: "p:rest c:webgui f:*", IssueType: 1, ClientType: "webgui",
		ExpiredAt: time.Now().Add(time.Hour),
	}}
	testApp := newAdminGroupTestApp(t, tokenSvc)
	testApp.Config().Security.AuthTokenKey = secretKey

	rec := runAdminGroupRequest(t, testApp, adminGroupJWT(t, secretKey, "nonce-a", 2, 5), "webgui", http.MethodGet, "/api/admin/check")
	require.Equal(t, http.StatusOK, rec.Code)

	var res struct {
		Code int64 `json:"code"`
		Data struct {
			IsAdmin bool `json:"isAdmin"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	// NewTestApp 没有用户服务数据，AdminUID=0 时无用户 → 无人是管理员 → isAdmin=false。
	require.Equal(t, int64(1), res.Code, "check must succeed, not 403: %s", rec.Body.String())
	require.False(t, res.Data.IsAdmin, "no users configured means nobody is admin")
}

// /api/admin/check 对受限令牌同样可达（无功能点、不查库）。
func TestAdminCheckReachableByRestrictedToken(t *testing.T) {
	secretKey := "admin-group-test-secret"
	tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{
		ID: 6, UID: 2, TokenString: "nonce-a", Status: 1,
		Scope: "p:rest c:CLI f:note_r", IssueType: 2, Vaults: "vault-a",
		ExpiredAt: time.Now().Add(time.Hour),
	}}
	testApp := newAdminGroupTestApp(t, tokenSvc)
	testApp.Config().Security.AuthTokenKey = secretKey

	rec := runAdminGroupRequest(t, testApp, adminGroupJWT(t, secretKey, "nonce-a", 2, 6), "CLI", http.MethodGet, "/api/admin/check")
	require.Equal(t, http.StatusOK, rec.Code)
	var res app.Res
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, int64(1), int64(res.Code), "受限令牌也应能查询管理员身份（无库参数）: %s", rec.Body.String())
}

// /api/admin/upgrade 按功能点归类为 config_w，叠加 handler 内的生效管理员校验：
// 受限令牌（仅 note 权限）即使令牌主人是管理员也被功能点挡住。
func TestAdminUpgradeRestrictedTokenBlockedByFunction(t *testing.T) {
	secretKey := "admin-group-test-secret"
	tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{
		ID: 7, UID: 1, TokenString: "nonce-a", Status: 1,
		Scope: "p:rest c:CLI f:note_rw", IssueType: 2,
		ExpiredAt: time.Now().Add(time.Hour),
	}}
	testApp := newAdminGroupTestApp(t, tokenSvc)
	testApp.Config().Security.AuthTokenKey = secretKey
	testApp.Config().User.AdminUID = 1

	rec := runAdminGroupRequest(t, testApp, adminGroupJWT(t, secretKey, "nonce-a", 1, 7), "CLI", http.MethodGet, "/api/admin/upgrade?version=latest")
	require.Equal(t, http.StatusOK, rec.Code)
	var res app.Res
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.NotEqual(t, int64(1), int64(res.Code), "仅笔记权限的令牌不得触发升级")
}

// 管理员名下含 config_w 的手动令牌（插件场景：同步令牌通常为 f:* 或含 config 维度）
// 可通过升级接口的路由层；进入 handler 后由生效管理员校验把关。
func TestAdminUpgradeAdminTokenPassesRouting(t *testing.T) {
	secretKey := "admin-group-test-secret"
	tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{
		ID: 8, UID: 1, TokenString: "nonce-a", Status: 1,
		Scope: "p:rest c:ObsidianPlugin f:config_w,note_rw", IssueType: 2,
		ExpiredAt: time.Now().Add(time.Hour),
	}}
	testApp := newAdminGroupTestApp(t, tokenSvc)
	testApp.Config().Security.AuthTokenKey = secretKey
	testApp.Config().User.AdminUID = 1

	// 请求进入 handler：管理员 + config_w 通过路由与功能点校验。
	// Upgrade handler 随后会做版本检查（无版本服务返回错误），此断言只关心
	// 不被功能点拒绝（业务码不等于 315 Scope restricted）。
	rec := runAdminGroupRequest(t, testApp, adminGroupJWT(t, secretKey, "nonce-a", 1, 8), "ObsidianPlugin", http.MethodGet, "/api/admin/upgrade?version=v9.9.9")
	require.Equal(t, http.StatusOK, rec.Code)
	var res app.Res
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.NotEqual(t, int64(315), int64(res.Code), "管理员 config_w 令牌不得被功能点挡住: %s", rec.Body.String())
}

// WebGUI 登录令牌（f:*）通过升级接口的普通鉴权（不再要求 RequireWebGUI）。
func TestAdminUpgradeWebGuiLoginTokenPassesRouting(t *testing.T) {
	secretKey := "admin-group-test-secret"
	tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{
		ID: 9, UID: 1, TokenString: "nonce-a", Status: 1,
		Scope: "p:rest c:webgui f:*", IssueType: 1, ClientType: "webgui",
		ExpiredAt: time.Now().Add(time.Hour),
	}}
	testApp := newAdminGroupTestApp(t, tokenSvc)
	testApp.Config().Security.AuthTokenKey = secretKey
	testApp.Config().User.AdminUID = 1

	rec := runAdminGroupRequest(t, testApp, adminGroupJWT(t, secretKey, "nonce-a", 1, 9), "webgui", http.MethodGet, "/api/admin/upgrade?version=v9.9.9")
	require.Equal(t, http.StatusOK, rec.Code)
	var res app.Res
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.NotEqual(t, int64(315), int64(res.Code), "WebGUI 登录令牌不得被功能点挡住: %s", rec.Body.String())
	require.NotEqual(t, int64(307), int64(res.Code), "登录令牌不得被 RequireWebGUI 挡住（已移回普通组）: %s", rec.Body.String())
}

// 非管理员即使持有全权限令牌（f:*）也过不了 handler 内的管理员校验。
func TestAdminUpgradeNonAdminBlocked(t *testing.T) {
	secretKey := "admin-group-test-secret"
	tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{
		ID: 10, UID: 2, TokenString: "nonce-a", Status: 1,
		Scope: "p:rest c:* f:*", IssueType: 2,
		ExpiredAt: time.Now().Add(time.Hour),
	}}
	testApp := newAdminGroupTestApp(t, tokenSvc)
	testApp.Config().Security.AuthTokenKey = secretKey
	testApp.Config().User.AdminUID = 1

	rec := runAdminGroupRequest(t, testApp, adminGroupJWT(t, secretKey, "nonce-a", 2, 10), "webgui", http.MethodGet, "/api/admin/upgrade?version=v9.9.9")
	require.Equal(t, http.StatusOK, rec.Code)
	var res app.Res
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, int64(411), int64(res.Code), "非管理员必须被 handler 内的管理员校验拒绝")
}
