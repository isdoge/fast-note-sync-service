package service

import (
	"context"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/require"
)

type tokenWriteRepository struct {
	stubAuthTokenRepository
	created *domain.AuthToken
}

func (r *tokenWriteRepository) Create(_ context.Context, token *domain.AuthToken) (*domain.AuthToken, error) {
	token.ID = 1
	r.created = token
	return token, nil
}
func (r *tokenWriteRepository) UpdateTokenString(context.Context, int64, string) error { return nil }
func (r *tokenWriteRepository) Update(context.Context, *domain.AuthToken) error        { return nil }

func TestTokenService_WebGUILoginTokenBindUA(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bindUA bool
	}{
		{name: "default binds UA", bindUA: true},
		{name: "disabled leaves UA empty", bindUA: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &tokenWriteRepository{}
			svc := NewTokenService(repo, nil, app.NewTokenManager(app.TokenConfig{SecretKey: "test-secret", Expiry: time.Hour}), nil, TokenServiceConfig{WebGUILoginTokenBindUA: tc.bindUA})

			created, _, err := svc.CreateForLogin(context.Background(), 1, "WebGui", "192.0.2.1", "Browser A")
			require.NoError(t, err)
			if tc.bindUA {
				require.Equal(t, "Browser A", created.UserAgent)
			} else {
				require.Empty(t, created.UserAgent)
			}

			repo.getByIDToken = created
			created.UserAgent = "Browser A"
			rotated, _, err := svc.RotateForLogin(context.Background(), 1, created.ID, "192.0.2.1", "Browser B")
			require.NoError(t, err)
			if tc.bindUA {
				require.Equal(t, "Browser B", rotated.UserAgent)
			} else {
				require.Empty(t, rotated.UserAgent)
			}
		})
	}
}

func TestTokenService_Create_ManualLegacyScopeIncludesMCP(t *testing.T) {
	for _, tc := range []struct{ name, scope, protocol, want string }{
		{"web-form", "rest,ws", "", "p:rest,ws,mcp c:ObsidianPlugin f:*"},
		{"already-present", "rest,ws,mcp", "", "p:rest,ws,mcp c:ObsidianPlugin f:*"},
		{"explicit-restricted", "rest,ws", "rest", "p:rest c:* f:*"},
		{"wildcard", "*", "", "p:* c:ObsidianPlugin f:*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &tokenWriteRepository{}
			svc := NewTokenService(repo, nil, app.NewTokenManager(app.TokenConfig{SecretKey: "test-secret", Expiry: time.Hour}), nil, TokenServiceConfig{})
			res, err := svc.Create(context.Background(), 1, &dto.TokenIssueRequest{Scope: tc.scope, Protocol: tc.protocol, ClientType: "ObsidianPlugin", ExpiredDays: 7})
			require.NoError(t, err)
			require.Equal(t, tc.want, res.Scope)
			require.Equal(t, tc.protocol == "", app.VerifyPermissions(res.Scope, "mcp", "ObsidianPlugin", "note_r"))
		})
	}
}
