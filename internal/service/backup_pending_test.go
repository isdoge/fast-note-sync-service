package service

import (
	"context"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type pendingBackupRepository struct {
	domain.BackupRepository
	configs  []*domain.BackupConfig
	finished chan int64
}

func (r *pendingBackupRepository) ListEnabledConfigs(context.Context) ([]*domain.BackupConfig, error) {
	return r.configs, nil
}
func (r *pendingBackupRepository) SaveConfig(_ context.Context, c *domain.BackupConfig, _ int64) (*domain.BackupConfig, error) {
	if c.LastStatus != domain.BackupStatusRunning {
		r.finished <- c.ID
	}
	return c, nil
}

func TestBackupService_PendingChangesReachEveryMirror(t *testing.T) {
	future := time.Now().Add(time.Hour)
	repo := &pendingBackupRepository{finished: make(chan int64, 2), configs: []*domain.BackupConfig{
		{ID: 1, UID: 7, Type: "full", IsEnabled: true, NextRunTime: future},
		{ID: 2, UID: 7, Type: "sync", IsEnabled: true, NextRunTime: future, StorageIds: "invalid"},
		{ID: 3, UID: 7, Type: "sync", IsEnabled: true, NextRunTime: future, StorageIds: "invalid"},
	}}
	svc := &backupService{backupRepo: repo, ctx: context.Background(), tempPath: t.TempDir(), logger: zap.NewNop(), runningTasks: make(map[int64]context.CancelFunc)}
	svc.pendingSyncs.Store(int64(7), true)
	require.NoError(t, svc.ExecuteTaskBackups(context.Background()))
	ids := make([]int64, 0, 2)
	for range 2 {
		select {
		case id := <-repo.finished:
			ids = append(ids, id)
		case <-time.After(time.Second):
			t.Fatal("pending changes did not trigger every mirror")
		}
	}
	svc.wg.Wait()
	require.ElementsMatch(t, []int64{2, 3}, ids)
	_, pending := svc.pendingSyncs.Load(int64(7))
	require.False(t, pending)
}
