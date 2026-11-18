package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/storage"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestBackupService_RetentionKeepsHistoryUntilFileDeleted(t *testing.T) {
	for _, mode := range []string{"success", "missing-file", "delete-failed", "disabled", "missing-storage", "invalid-client", "list-failed"} {
		t.Run(mode, func(t *testing.T) {
			repo := new(domainmocks.MockBackupRepository)
			root := t.TempDir()
			st := &dto.StorageDTO{ID: 1, Type: storage.LOCAL, CustomPath: "backups", IsEnabled: mode != "disabled"}
			if mode == "invalid-client" {
				st.Type = "invalid"
			}
			storages := map[int64]*dto.StorageDTO{1: st}
			if mode == "missing-storage" {
				delete(storages, 1)
			}
			svc := newBackupSvc(repo, nil, &backupStorageStub{storages: storages})
			svc.ctx = context.Background()
			svc.storageConfig = &config.StorageConfig{LocalFS: config.StorageLocalFSConfig{SavePath: root}}
			cfg := &domain.BackupConfig{ID: 3, UID: 2, RetentionDays: 7}
			history := &domain.BackupHistory{ID: 4, UID: 2, ConfigID: 3, StorageID: 1, Type: "full", FilePath: "old.zip"}
			filePath := filepath.Join(root, "backups", history.FilePath)
			require.NoError(t, os.MkdirAll(filepath.Dir(filePath), 0755))
			if mode == "delete-failed" {
				require.NoError(t, os.Mkdir(filePath, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(filePath, "child"), []byte("keep"), 0600))
			} else if mode != "missing-file" {
				require.NoError(t, os.WriteFile(filePath, []byte("archive"), 0600))
			}
			repo.On("SaveConfig", mock.Anything, cfg, int64(2)).Return(cfg, nil)
			var listErr error
			if mode == "list-failed" {
				listErr = errors.New("query failed")
			}
			repo.On("ListOldHistory", mock.Anything, int64(2), int64(3), mock.Anything).Return([]*domain.BackupHistory{history}, listErr)
			// Retention must only remove the specific records whose files were deleted.
			repo.On("DeleteOldHistory", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Run(func(mock.Arguments) { t.Error("unsafe bulk history deletion") }).Return(nil).Maybe()
			if mode == "success" || mode == "missing-file" {
				repo.On("DeleteHistory", mock.Anything, int64(2), int64(4)).Return(nil).Once()
			}
			require.NoError(t, svc.finishTask(context.Background(), cfg, nil, 0, 0, time.Now()))
			if mode == "success" || mode == "missing-file" {
				_, err := os.Stat(filePath)
				require.True(t, os.IsNotExist(err))
			} else {
				repo.AssertNotCalled(t, "DeleteHistory", mock.Anything, mock.Anything, mock.Anything)
			}
			repo.AssertExpectations(t)
		})
	}
}
