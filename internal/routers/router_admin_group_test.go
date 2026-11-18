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
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/locales/en"
	"github.com/go-playground/locales/zh"
	internalapp "github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/require"
)

// 管理接口的路由归属守卫：/api/admin/upgrade、/api/admin/ws_clients、
// /api/admin/ws_client/:traceId 必须位于 WebGUI 鉴权组（登录令牌 + webgui 客户端），
// 手动 API 令牌（即使 p:rest 通过普通鉴权）必须被挡在门外；
// /api/admin/check 也在 WebGUI 组但只报告身份，非管理员拿到 isAdmin=false 而不是 403。
// 本测试构建最小 App 并直接调 registerAPIRoutes，验证的是路由层分组而非 handler 内部。

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
func (s *adminGroupTokenService) RecordAccessLog(ctx context.Context, log *domain.AuthTokenLog) error { return nil }
func (s *adminGroupTokenService) ListLogs(ctx context.Context, uid, tokenID int64, page, pageSize int) ([]*dto.TokenLogResponse, int64, error) {
	return nil, 0, errors.New("not implemented")
}
func (s *adminGroupTokenService) UpdateLastUsedAt(ctx context.Context, tokenID int64) error { return nil }
func (s *adminGroupTokenService) SetSyncHandler(handler func(uid int64, tokenID int64, scope string, kick bool)) {
}
func (s *adminGroupTokenService) GetRecentClients(ctx context.Context, uid int64, duration time.Duration) (map[int64][]string, error) {
	return nil, nil
}
func (s *adminGroupTokenService) CleanExpired(ctx context.Context, uid int64, issueType int) error { return nil }

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

// adminGroupJWT 用固定密钥签一张测试 JWT（uid=2，tokenID=5）。
func adminGroupJWT(t *testing.T, secretKey, nonce string) string {
	t.Helper()
	tokenManager := app.NewTokenManager(app.TokenConfig{SecretKey: secretKey, Expiry: time.Hour})
	token, err := tokenManager.Generate(2, "", "", 5, nonce)
	require.NoError(t, err)
	return token
}

// 手动令牌（issue_type=2）即使带 webgui 头也必须被 RequireWebGUI 挡在管理接口外。
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
		"/api/admin/upgrade?version=latest",
		"/api/admin/ws_clients",
	} {
		rec := runAdminGroupRequest(t, testApp, adminGroupJWT(t, secretKey, "nonce-a"), "webgui", http.MethodGet, target)
		require.Equal(t, http.StatusOK, rec.Code, "response uses business code envelope")
		var res app.Res
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		require.NotEqual(t, int64(1), int64(res.Code), "manual token must not pass: %s", target)
	}
}

// 登录令牌（issue_type=1，ClientType=webgui）可到达 /api/admin/check，
// 且 AdminUID=0 多用户场景下非最小 UID 用户拿到 isAdmin=false 而不是错误码。
func TestAdminCheckReachableByLoginToken(t *testing.T) {
	secretKey := "admin-group-test-secret"
	tokenSvc := &adminGroupTokenService{activeToken: &domain.AuthToken{
		ID: 5, UID: 2, TokenString: "nonce-a", Status: 1,
		Scope: "p:rest c:webgui f:*", IssueType: 1, ClientType: "webgui",
		ExpiredAt: time.Now().Add(time.Hour),
	}}
	testApp := newAdminGroupTestApp(t, tokenSvc)
	testApp.Config().Security.AuthTokenKey = secretKey

	rec := runAdminGroupRequest(t, testApp, adminGroupJWT(t, secretKey, "nonce-a"), "webgui", http.MethodGet, "/api/admin/check")
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
