package service

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGitSyncService_UpdateConfig_PreservesBlankPassword(t *testing.T) {
	for _, tc := range []struct{ name, submitted, want string }{
		{"unchanged", "", "stored-secret"},
		{"replace", "new-secret", "new-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := new(domainmocks.MockGitSyncRepository)
			conf := &domain.GitSyncConfig{ID: 1, UID: 2, VaultID: 3, Password: "stored-secret"}
			repo.On("GetByID", mock.Anything, int64(1), int64(2)).Return(conf, nil)
			repo.On("Save", mock.Anything, mock.Anything, int64(2)).Run(func(args mock.Arguments) {
				require.Equal(t, tc.want, args.Get(1).(*domain.GitSyncConfig).Password)
			}).Return(conf, nil)
			svc := newTestGitSyncService(repo)
			defer svc.Shutdown(context.Background())
			result, err := svc.UpdateConfig(context.Background(), 2, &dto.GitSyncConfigRequest{
				ID: 1, RepoURL: "https://example.com/repo.git", Password: tc.submitted, Delay: 30,
			})
			require.NoError(t, err)
			require.Equal(t, int64(1), result.ID)
			require.Equal(t, tc.want, conf.Password)
			repo.AssertExpectations(t)
		})
	}
}
