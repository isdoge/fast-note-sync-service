package service

import (
	"context"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestBackupService_ManualBackupReturnsBeforeWorkAndSurvivesRequestCancel(t *testing.T) {
	repo := new(domainmocks.MockBackupRepository)
	svc := newBackupSvc(repo, nil, &backupStorageStub{})
	svc.ctx = context.Background()
	svc.tempPath = t.TempDir()
	cfg := &domain.BackupConfig{ID: 1, UID: 2, IsEnabled: true, Type: "sync", StorageIds: "invalid"}
	repo.On("GetByID", mock.Anything, int64(1), int64(2)).Return(cfg, nil)
	release := make(chan struct{})
	started := make(chan struct{})
	taskError := make(chan error, 1)
	repo.On("SaveConfig", mock.Anything, mock.Anything, int64(2)).Run(func(args mock.Arguments) {
		if args.Get(1).(*domain.BackupConfig).LastStatus == domain.BackupStatusRunning {
			close(started)
			<-release
			taskError <- args.Get(0).(context.Context).Err()
		}
	}).Return(cfg, nil)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- svc.ExecuteUserBackup(requestCtx, 2, 1) }()
	released := false
	defer func() {
		if !released {
			close(release)
			<-returned
		}
		svc.wg.Wait()
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("backup did not start")
	}
	select {
	case err := <-returned:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("manual backup blocked the request on long-running work")
	}
	cancel()
	close(release)
	released = true
	require.NoError(t, <-taskError, "request cancellation must not cancel the running backup")
	svc.wg.Wait()
	require.Equal(t, domain.BackupStatusFailed, cfg.LastStatus)
	require.Contains(t, cfg.LastMessage, "Backup failed")
	repo.AssertExpectations(t)
}
