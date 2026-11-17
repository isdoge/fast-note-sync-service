package dao

import (
	"context"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBackupRepository_DeleteHistoryOnlyRemovesSelectedRecord(t *testing.T) {
	daoInst, cleanup := setupCountByFIDsTestEnv(t)
	defer cleanup()
	repo := NewBackupRepository(daoInst)
	ctx := context.Background()
	for _, name := range []string{"failed.zip", "deleted.zip"} {
		_, err := repo.CreateHistory(ctx, &domain.BackupHistory{UID: 1, ConfigID: 2, Type: "full", FilePath: name, CreatedAt: time.Now().AddDate(0, 0, -8)}, 1)
		require.NoError(t, err)
	}
	list, err := repo.ListOldHistory(ctx, 1, 2, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, list, 2)
	selected := list[1].ID
	require.NoError(t, repo.DeleteHistory(ctx, 2, selected))
	unchanged, err := repo.ListOldHistory(ctx, 1, 2, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, unchanged, 2)
	require.NoError(t, repo.DeleteHistory(ctx, 1, selected))
	remaining, err := repo.ListOldHistory(ctx, 1, 2, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, list[0].ID, remaining[0].ID)
}
