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
