package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMiddlewareTokenService struct {
	activeToken *domain.AuthToken
	activeErr   error
}

func (s *fakeMiddlewareTokenService) Create(ctx context.Context, uid int64, params *dto.TokenIssueRequest) (*dto.TokenCreateResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeMiddlewareTokenService) CreateForLogin(ctx context.Context, uid int64, clientType, ip, userAgent string) (*domain.AuthToken, string, error) {
	return nil, "", errors.New("not implemented")
}

func (s *fakeMiddlewareTokenService) ListByUser(ctx context.Context, uid int64) ([]*dto.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeMiddlewareTokenService) Update(ctx context.Context, uid int64, tokenID int64, params *dto.TokenUpdateRequest) error {
	return errors.New("not implemented")
}

func (s *fakeMiddlewareTokenService) Revoke(ctx context.Context, uid int64, tokenID int64) error {
	return errors.New("not implemented")
}

func (s *fakeMiddlewareTokenService) Rotate(ctx context.Context, uid int64, tokenID int64) (*dto.TokenCreateResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeMiddlewareTokenService) RotateForLogin(ctx context.Context, uid int64, tokenID int64, ip, userAgent string) (*domain.AuthToken, string, error) {
	return nil, "", errors.New("not implemented")
}

func (s *fakeMiddlewareTokenService) GetActiveToken(ctx context.Context, uid int64, tokenID int64) (*domain.AuthToken, error) {
	return s.activeToken, s.activeErr
}

func (s *fakeMiddlewareTokenService) RecordAccessLog(ctx context.Context, log *domain.AuthTokenLog) error {
	return nil
}

func (s *fakeMiddlewareTokenService) ListLogs(ctx context.Context, uid, tokenID int64, page, pageSize int) ([]*dto.TokenLogResponse, int64, error) {
	return nil, 0, errors.New("not implemented")
}

func (s *fakeMiddlewareTokenService) UpdateLastUsedAt(ctx context.Context, tokenID int64) error {
	return errors.New("not implemented")
}

func (s *fakeMiddlewareTokenService) SetSyncHandler(handler func(uid int64, tokenID int64, scope string, kick bool)) {
}

func (s *fakeMiddlewareTokenService) GetRecentClients(ctx context.Context, uid int64, duration time.Duration) (map[int64][]string, error) {
	return nil, nil
}

func (s *fakeMiddlewareTokenService) CleanExpired(ctx context.Context, uid int64, issueType int) error {
	return nil
}

func newMiddlewareJWT(t *testing.T, secretKey, nonce string) string {
	t.Helper()
	tokenManager := app.NewTokenManager(app.TokenConfig{
		SecretKey: secretKey,
		Expiry:    time.Hour,
	})
	token, err := tokenManager.Generate(1, "", "", 2, nonce)
	require.NoError(t, err)
	return token
}

func runUserAuthMiddleware(t *testing.T, tokenService *fakeMiddlewareTokenService, token string) app.Res {
	return runUserAuthMiddlewareWithRequest(t, tokenService, token, http.MethodGet, "/api/note/list?path=test.md", func(req *http.Request) {
		req.Header.Set("x-client", "ObsidianPlugin")
		req.Header.Set("User-Agent", "Obsidian")
	})
}

func runUserAuthMiddlewareWithRequest(t *testing.T, tokenService *fakeMiddlewareTokenService, token string, method string, target string, configure func(*http.Request)) app.Res {
	t.Helper()
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(UserAuthTokenWithConfig("test-secret", tokenService))
	router.GET("/api/note/list", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": code.Success.Code(), "status": true})
	})
	router.GET("/api/file", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": code.Success.Code(), "status": true})
	})
	router.POST("/api/file", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": code.Success.Code(), "status": true})
	})
	router.GET("/api/vault", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": code.Success.Code(), "status": true})
	})
	router.GET("/api/share", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": code.Success.Code(), "status": true})
	})
	router.POST("/api/share", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": code.Success.Code(), "status": true})
	})
	router.GET("/api/shares", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": code.Success.Code(), "status": true})
	})
	router.GET("/api/some/new/route", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": code.Success.Code(), "status": true})
	})

	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if configure != nil {
		configure(req)
	}
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	var res app.Res
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &res))
	return res
}

func TestUserAuthTokenWithConfig_AllowsValidManualTokenScope(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	res := runUserAuthMiddleware(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          2,
		UID:         1,
		TokenString: "nonce-ok",
		Status:      1,
		Scope:       "p:rest,ws c:ObsidianPlugin f:note_rw,file_rw,config_rw",
		IssueType:   2,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}, token)

	assert.Equal(t, code.Success.Code(), res.Code)
}

func TestUserAuthTokenWithConfig_PropagatesInvalidStatefulToken(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "stale-nonce")
	res := runUserAuthMiddleware(t, &fakeMiddlewareTokenService{
		activeErr: code.ErrorInvalidUserAuthToken.WithDetails("Token has been revoked or no longer exists"),
	}, token)

	assert.Equal(t, code.ErrorInvalidUserAuthToken.Code(), res.Code)
	assert.Contains(t, res.Details, "revoked")
}

func TestUserAuthTokenWithConfig_PropagatesExpiredToken(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "expired-nonce")
	res := runUserAuthMiddleware(t, &fakeMiddlewareTokenService{
		activeErr: code.ErrorTokenExpired,
	}, token)

	assert.Equal(t, code.ErrorTokenExpired.Code(), res.Code)
}

func TestUserAuthTokenWithConfig_RejectsNonceMismatch(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "old-nonce")
	res := runUserAuthMiddleware(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          2,
		UID:         1,
		TokenString: "new-nonce",
		Status:      1,
		Scope:       "p:rest,ws c:ObsidianPlugin f:note_rw,file_rw,config_rw",
		IssueType:   2,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}, token)

	assert.Equal(t, code.ErrorInvalidUserAuthToken.Code(), res.Code)
	assert.Contains(t, res.Details, "rotated")
}

func TestUserAuthTokenWithConfig_RejectsScopeRestrictedToken(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	res := runUserAuthMiddleware(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          2,
		UID:         1,
		TokenString: "nonce-ok",
		Status:      1,
		Scope:       "p:ws c:ObsidianPlugin f:note_rw",
		IssueType:   2,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}, token)

	assert.Equal(t, code.ErrorAuthTokenScopeRestricted.Code(), res.Code)
	assert.Contains(t, res.Details, "Permission denied")
}

func TestUserAuthTokenWithConfig_AllowsVaultListWithNoteReadScope(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	res := runUserAuthMiddlewareWithRequest(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          2,
		UID:         1,
		TokenString: "nonce-ok",
		Status:      1,
		Scope:       "p:rest c:ObsidianPlugin f:note_r",
		IssueType:   2,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}, token, http.MethodGet, "/api/vault", func(req *http.Request) {
		req.Header.Set("x-client", "ObsidianPlugin")
	})

	assert.Equal(t, code.Success.Code(), res.Code)
}

// 库发现改为任一读权限即可：仅有 file_r 的受限只读令牌也能列库（任务要求
// 不能让现有受限只读令牌突然列不了库），但没有任何读权限的令牌仍被拒绝。
func TestUserAuthTokenWithConfig_VaultListAcceptsAnyReadScope(t *testing.T) {
	for _, scope := range []string{"f:note_r", "f:file_r", "f:vault_r"} {
		token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
		res := runUserAuthMiddlewareWithRequest(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
			ID:          2,
			UID:         1,
			TokenString: "nonce-ok",
			Status:      1,
			Scope:       "p:rest c:ObsidianPlugin " + scope,
			IssueType:   2,
			ExpiredAt:   time.Now().Add(time.Hour),
		}}, token, http.MethodGet, "/api/vault", func(req *http.Request) {
			req.Header.Set("x-client", "ObsidianPlugin")
		})
		assert.Equal(t, code.Success.Code(), res.Code, "scope %s 应可列库", scope)
	}
}

func TestUserAuthTokenWithConfig_RejectsVaultListWithoutAnyReadScope(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	res := runUserAuthMiddlewareWithRequest(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          2,
		UID:         1,
		TokenString: "nonce-ok",
		Status:      1,
		Scope:       "p:rest c:ObsidianPlugin f:config_r",
		IssueType:   2,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}, token, http.MethodGet, "/api/vault", func(req *http.Request) {
		req.Header.Set("x-client", "ObsidianPlugin")
	})

	assert.Equal(t, code.ErrorAuthTokenScopeRestricted.Code(), res.Code)
}

// B2：分享的新建/修改/取消要求 note_w，查询/列表要求 note_r；
// 只有 config 权限的令牌不得创建公开分享。
func TestUserAuthTokenWithConfig_ShareRequiresNoteScope(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	mk := func(scope string) *fakeMiddlewareTokenService {
		return &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
			ID: 2, UID: 1, TokenString: "nonce-ok", Status: 1,
			Scope: "p:rest c:ObsidianPlugin " + scope, IssueType: 2,
			ExpiredAt: time.Now().Add(time.Hour),
		}}
	}

	res := runUserAuthMiddlewareWithRequest(t, mk("f:config_rw"), token, http.MethodPost, "/api/share", func(req *http.Request) {
		req.Header.Set("x-client", "ObsidianPlugin")
	})
	assert.Equal(t, code.ErrorAuthTokenScopeRestricted.Code(), res.Code, "config 权限不得创建分享")

	res = runUserAuthMiddlewareWithRequest(t, mk("f:config_rw"), token, http.MethodGet, "/api/share", func(req *http.Request) {
		req.Header.Set("x-client", "ObsidianPlugin")
	})
	assert.Equal(t, code.ErrorAuthTokenScopeRestricted.Code(), res.Code, "config 权限不得查询分享")

	res = runUserAuthMiddlewareWithRequest(t, mk("f:note_rw"), token, http.MethodPost, "/api/share", func(req *http.Request) {
		req.Header.Set("x-client", "ObsidianPlugin")
	})
	assert.Equal(t, code.Success.Code(), res.Code, "note_w 应可创建分享")

	res = runUserAuthMiddlewareWithRequest(t, mk("f:note_r"), token, http.MethodGet, "/api/shares", func(req *http.Request) {
		req.Header.Set("x-client", "ObsidianPlugin")
	})
	assert.Equal(t, code.Success.Code(), res.Code, "note_r 应可列出分享")
}

// 未归类路径（普通鉴权组内新增路由却没进 restRouteFunction 表）必须拒绝。
func TestUserAuthTokenWithConfig_UnclassifiedRouteFailsClosed(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	res := runUserAuthMiddlewareWithRequest(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID: 2, UID: 1, TokenString: "nonce-ok", Status: 1,
		Scope: "p:rest c:ObsidianPlugin f:*", IssueType: 2,
		ExpiredAt: time.Now().Add(time.Hour),
	}}, token, http.MethodGet, "/api/some/new/route", func(req *http.Request) {
		req.Header.Set("x-client", "ObsidianPlugin")
	})
	assert.Equal(t, code.ErrorAuthTokenScopeRestricted.Code(), res.Code)
}

func TestUserAuthTokenWithConfig_AllowsLoginTokenWithoutClientHeader(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	res := runUserAuthMiddlewareWithRequest(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          2,
		UID:         1,
		TokenString: "nonce-ok",
		Status:      1,
		Scope:       "p:rest c:WebGui f:*",
		ClientType:  "WebGui",
		IssueType:   1,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}, token, http.MethodGet, "/api/file?vault=main&path=image.png", func(req *http.Request) {
		req.Header.Set("User-Agent", "Mozilla/5.0")
	})

	assert.Equal(t, code.Success.Code(), res.Code)
}

func TestUserAuthTokenWithConfig_AllowsUnboundUALoginTokenAfterProxyRewrite(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	res := runUserAuthMiddlewareWithRequest(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          2,
		UID:         1,
		TokenString: "nonce-ok",
		Status:      1,
		Scope:       "p:rest c:WebGui f:*",
		ClientType:  "WebGui",
		IssueType:   1,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}, token, http.MethodGet, "/api/file?vault=main&path=image.png", func(req *http.Request) {
		req.Header.Set("User-Agent", "Rewritten by proxy")
	})

	assert.Equal(t, code.Success.Code(), res.Code)
}

func TestUserAuthTokenWithConfig_RejectsHeaderlessLoginTokenWrite(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	res := runUserAuthMiddlewareWithRequest(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          2,
		UID:         1,
		TokenString: "nonce-ok",
		Status:      1,
		Scope:       "p:rest c:WebGui f:*",
		ClientType:  "WebGui",
		IssueType:   1,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}, token, http.MethodPost, "/api/file?vault=main&path=image.png", func(req *http.Request) {
		req.Header.Set("User-Agent", "Mozilla/5.0")
	})

	assert.Equal(t, code.ErrorAuthTokenClientRestricted.Code(), res.Code)
}

func TestUserAuthTokenWithConfig_RejectsManualTokenWithoutClientHeader(t *testing.T) {
	token := newMiddlewareJWT(t, "test-secret", "nonce-ok")
	res := runUserAuthMiddlewareWithRequest(t, &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          2,
		UID:         1,
		TokenString: "nonce-ok",
		Status:      1,
		Scope:       "p:rest c:WebGui f:file_r",
		ClientType:  "WebGui",
		IssueType:   2,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}, token, http.MethodGet, "/api/file?vault=main&path=image.png", nil)

	assert.Equal(t, code.ErrorAuthTokenScopeRestricted.Code(), res.Code)
}

// TestUserAuthTokenWithConfig_InjectsTokenContextAttributes verifies that UserAuthTokenWithConfig
// correctly injects token_issue_type and token_client_type into gin.Context after successful authentication.
// These context values are consumed by middleware.RequireWebGUI for multi-factor verification.
// 验证 UserAuthTokenWithConfig 在认证成功后将 token_issue_type 和 token_client_type
// 正确注入到 gin.Context，这些值供 RequireWebGUI 进行联合校验防止请求头伪造
func TestUserAuthTokenWithConfig_InjectsTokenContextAttributes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tokenSvc := &fakeMiddlewareTokenService{activeToken: &domain.AuthToken{
		ID:          3,
		UID:         1,
		TokenString: "nonce-inject",
		Status:      1,
		Scope:       "p:rest c:WebGui f:*",
		ClientType:  "WebGui",
		IssueType:   1,
		ExpiredAt:   time.Now().Add(time.Hour),
	}}

	var capturedIssueType interface{}
	var capturedClientType interface{}

	router := gin.New()
	router.Use(UserAuthTokenWithConfig("test-secret", tokenSvc))
	router.GET("/api/note/list", func(c *gin.Context) {
		capturedIssueType, _ = c.Get("token_issue_type")
		capturedClientType, _ = c.Get("token_client_type")
		c.JSON(http.StatusOK, gin.H{"code": code.Success.Code()})
	})

	token, err := app.NewTokenManager(app.TokenConfig{
		SecretKey: "test-secret",
		Expiry:    time.Hour,
	}).Generate(1, "", "", 1, "nonce-inject")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/note/list?path=test.md", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-client", "WebGui")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	// Verify token_issue_type injected correctly // 验证 token_issue_type 注入正确
	assert.Equal(t, 1, capturedIssueType, "token_issue_type should be 1 (Login)")
	// Verify token_client_type injected correctly // 验证 token_client_type 注入正确
	assert.Equal(t, "WebGui", capturedClientType, "token_client_type should match dbToken.ClientType")
}
