package service

import (
	"context"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestTokenService_RejectsOverflowingExpiredDays(t *testing.T) {
	for _, days := range []int{106752, 3650000000000000, -1} {
		repo := &tokenWriteRepository{}
		token := &domain.AuthToken{ID: 1, UID: 2, IssueType: 2, ExpiredAt: time.Now().Add(time.Hour)}
		repo.getByIDToken = token
		before := token.ExpiredAt
		svc := NewTokenService(repo, nil, app.NewTokenManager(app.TokenConfig{SecretKey: "test-secret", Expiry: time.Hour}), nil, TokenServiceConfig{})
		_, err := svc.Create(context.Background(), 2, &dto.TokenIssueRequest{ExpiredDays: days})
		require.Equal(t, code.ErrorInvalidParams.Code(), tokenErrorCode(t, err))
		require.Contains(t, err.Error(), "106751")
		require.Nil(t, repo.created)
		err = svc.Update(context.Background(), 2, 1, &dto.TokenUpdateRequest{ExpiredDays: days})
		require.Equal(t, code.ErrorInvalidParams.Code(), tokenErrorCode(t, err))
		require.Equal(t, before, token.ExpiredAt)
	}
}
