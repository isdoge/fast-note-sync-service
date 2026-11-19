package service

import (
	"context"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/require"
)

// 可变桩：记录 Update 收到的令牌，供断言。
type recordingTokenRepository struct {
	stubAuthTokenRepository
	updated *domain.AuthToken
}

func (r *recordingTokenRepository) Update(ctx context.Context, token *domain.AuthToken) error {
	r.updated = token
	return nil
}

func newManualTokenForUpdate(vaults string) *domain.AuthToken {
	return &domain.AuthToken{
		ID:         3,
		UID:        1,
		Status:     1,
		Scope:      "p:rest,ws c:CLI f:note_r",
		ClientType: "CLI",
		Vaults:     vaults,
		IssueType:  2,
		ExpiredAt:  time.Now().Add(time.Hour),
	}
}

func strPtr(v string) *string { return &v }

// B3：登录令牌不允许通过更新接口改 scope（防止会话令牌被升级成全权限 API 令牌）。
func TestTokenService_Update_LoginTokenScopeImmutable(t *testing.T) {
	repo := &recordingTokenRepository{stubAuthTokenRepository: stubAuthTokenRepository{getByIDToken: &domain.AuthToken{
		ID: 4, UID: 1, Status: 1, Scope: "p:rest c:webgui f:*", IssueType: 1,
		ExpiredAt: time.Now().Add(time.Hour),
	}}}
	svc := NewTokenService(repo, nil, app.NewTokenManager(app.TokenConfig{SecretKey: "test-secret"}), nil, TokenServiceConfig{})

	err := svc.Update(context.Background(), 1, 4, &dto.TokenUpdateRequest{
		Scope: "p:* c:* f:*", ExpiredDays: 7,
	})
	require.Equal(t, code.ErrorInvalidParams.Code(), tokenErrorCode(t, err))

	err = svc.Update(context.Background(), 1, 4, &dto.TokenUpdateRequest{
		Protocol: "rest,ws,mcp", ExpiredDays: 7,
	})
	require.Equal(t, code.ErrorInvalidParams.Code(), tokenErrorCode(t, err))
	require.Nil(t, repo.updated, "登录令牌不得进入更新路径")
}

// B4：更新时只有请求携带 vaults 字段才覆盖库限制；省略字段保持原值。
func TestTokenService_Update_VaultsOnlyOverwrittenWhenPresent(t *testing.T) {
	t.Run("omit keeps restriction", func(t *testing.T) {
		repo := &recordingTokenRepository{stubAuthTokenRepository: stubAuthTokenRepository{getByIDToken: newManualTokenForUpdate("vault-a")}}
		svc := NewTokenService(repo, nil, app.NewTokenManager(app.TokenConfig{SecretKey: "test-secret"}), nil, TokenServiceConfig{})

		err := svc.Update(context.Background(), 1, 3, &dto.TokenUpdateRequest{ExpiredDays: 30})
		require.NoError(t, err)
		require.Equal(t, "vault-a", repo.updated.Vaults, "省略 vaults 不得清空限制")
	})

	t.Run("present empty lifts restriction", func(t *testing.T) {
		repo := &recordingTokenRepository{stubAuthTokenRepository: stubAuthTokenRepository{getByIDToken: newManualTokenForUpdate("vault-a")}}
		svc := NewTokenService(repo, nil, app.NewTokenManager(app.TokenConfig{SecretKey: "test-secret"}), nil, TokenServiceConfig{})

		err := svc.Update(context.Background(), 1, 3, &dto.TokenUpdateRequest{ExpiredDays: 30, Vaults: strPtr("")})
		require.NoError(t, err)
		require.Equal(t, "", repo.updated.Vaults, "显式空值应解除限制")
	})

	t.Run("present value overwrites", func(t *testing.T) {
		repo := &recordingTokenRepository{stubAuthTokenRepository: stubAuthTokenRepository{getByIDToken: newManualTokenForUpdate("vault-a")}}
		svc := NewTokenService(repo, nil, app.NewTokenManager(app.TokenConfig{SecretKey: "test-secret"}), nil, TokenServiceConfig{})

		err := svc.Update(context.Background(), 1, 3, &dto.TokenUpdateRequest{ExpiredDays: 30, Vaults: strPtr("vault-b,vault-c")})
		require.NoError(t, err)
		require.Equal(t, "vault-b,vault-c", repo.updated.Vaults)
	})
}

// #2：更新时拒绝产生空协议维度的 scope（空 p: 等同通配）。
func TestTokenService_Update_RejectsEmptyProtocolScope(t *testing.T) {
	repo := &recordingTokenRepository{stubAuthTokenRepository: stubAuthTokenRepository{getByIDToken: newManualTokenForUpdate("")}}
	svc := NewTokenService(repo, nil, app.NewTokenManager(app.TokenConfig{SecretKey: "test-secret"}), nil, TokenServiceConfig{})

	// 三维分支空协议默认 rest，不再产生空 p:
	err := svc.Update(context.Background(), 1, 3, &dto.TokenUpdateRequest{Client: "CLI", Function: "note_r", ExpiredDays: 7})
	require.NoError(t, err)
	require.Equal(t, "p:rest c:CLI f:note_r", repo.updated.Scope)
}

// 在线收紧（或放宽）令牌的库范围 / 权限时，必须通过既有同步钩子踢掉该令牌
// 的 WebSocket 连接与事件流，让客户端重连并按新范围鉴权；仅续期或值未变不踢。
func TestTokenService_Update_KicksWhenScopeOrVaultsChange(t *testing.T) {
	cases := []struct {
		name     string
		params   *dto.TokenUpdateRequest
		wantKick bool
	}{
		{"vault restriction tightened", &dto.TokenUpdateRequest{ExpiredDays: 30, Vaults: strPtr("vault-b")}, true},
		{"vault restriction lifted", &dto.TokenUpdateRequest{ExpiredDays: 30, Vaults: strPtr("")}, true},
		{"vault allowlist unchanged", &dto.TokenUpdateRequest{ExpiredDays: 30, Vaults: strPtr("vault-a")}, false},
		{"expiry only", &dto.TokenUpdateRequest{ExpiredDays: 30}, false},
		{"function changed", &dto.TokenUpdateRequest{ExpiredDays: 30, Client: "CLI", Function: "note_rw"}, true},
		{"scope values unchanged", &dto.TokenUpdateRequest{ExpiredDays: 30, Protocol: "rest,ws", Client: "CLI", Function: "note_r"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &recordingTokenRepository{stubAuthTokenRepository: stubAuthTokenRepository{getByIDToken: newManualTokenForUpdate("vault-a")}}
			svc := NewTokenService(repo, nil, app.NewTokenManager(app.TokenConfig{SecretKey: "test-secret"}), nil, TokenServiceConfig{})

			var called, kicked bool
			var kickedScope string
			svc.SetSyncHandler(func(uid, tokenID int64, scope string, kick bool) {
				called, kicked, kickedScope = true, kick, scope
			})

			require.NoError(t, svc.Update(context.Background(), 1, 3, tc.params))
			require.True(t, called, "每次更新都应通知在线连接同步 scope")
			require.Equal(t, tc.wantKick, kicked, "kick decision mismatch")
			require.Equal(t, repo.updated.Scope, kickedScope)
		})
	}
}
