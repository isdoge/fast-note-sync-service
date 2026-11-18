package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// missingStorageFile 创建真实数据库记录，保留 DAO 回填的标准 SavePath，但不创建存储文件。
func missingStorageFile(t *testing.T) (domain.FileRepository, *domain.File) {
	t.Helper()
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join("storage", "database"), 0755))
	dbPath := filepath.Join("storage", "database", "db.sqlite3")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	cfg := &config.DatabaseConfig{Type: "sqlite", Path: dbPath, EnableWriteQueue: util.Ptr(false)}
	daoInst := dao.New(db, context.Background(),
		dao.WithConfig(cfg), dao.WithUserDatabaseConfig(cfg), dao.WithLogger(zap.NewNop()))
	t.Cleanup(func() { daoInst.CleanupConnections(-time.Second) })
	repo := dao.NewFileRepository(daoInst)
	file, err := repo.Create(context.Background(), &domain.File{
		VaultID: 1, Path: "assets/missing.png", PathHash: "missing-hash",
		ContentHash: "content-hash", Action: domain.FileActionCreate, Size: 10,
	}, 1)
	require.NoError(t, err)
	require.NotEmpty(t, file.SavePath)
	_, err = os.Stat(file.SavePath)
	require.True(t, os.IsNotExist(err), "存储文件必须不存在")
	return repo, file
}

func TestFileService_Delete_MissingStorage(t *testing.T) {
	repo, file := missingStorageFile(t)
	vaultRepo := new(domainmocks.MockVaultRepository)
	vaultRepo.On("GetByName", mock.Anything, "vault", int64(1)).Return(&domain.Vault{ID: 1, Name: "vault"}, nil)
	vaultSvc := NewVaultService(vaultRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	svc := NewFileService(nil, repo, nil, vaultSvc, nil, nil, nil, nil, nil).(*fileService)

	got, err := svc.Delete(context.Background(), 1, &dto.FileDeleteRequest{
		Vault: "vault", Path: file.Path, PathHash: file.PathHash,
	})
	require.NoError(t, err)
	// 等待异步统计注册防抖定时器后停掉，避免测试结束后访问临时数据库。
	t.Cleanup(func() {
		require.Eventually(t, func() bool {
			timer, ok := svc.countTimers.Load("1_1")
			if ok {
				timer.(*time.Timer).Stop()
			}
			return ok
		}, time.Second, time.Millisecond)
	})
	require.Equal(t, string(domain.FileActionDelete), got.Action)
	require.Equal(t, file.Path, got.Path)
	require.Equal(t, file.PathHash, got.PathHash)
	require.False(t, got.Unchanged, "首次删除必须照常通知其他端")
	stored, err := repo.GetByPathHash(context.Background(), file.PathHash, 1, 1)
	require.NoError(t, err)
	require.Equal(t, domain.FileActionDelete, stored.Action)
	require.Zero(t, stored.Rename)

	vaultRepo.AssertExpectations(t)
}

func TestFolderService_DeleteTree_MissingAttachmentStorage(t *testing.T) {
	repo, file := missingStorageFile(t)
	folderRepo := new(domainmocks.MockFolderRepository)
	noteRepo := new(domainmocks.MockNoteRepository)
	vaultRepo := new(domainmocks.MockVaultRepository)
	vaultRepo.On("GetByName", mock.Anything, "vault", int64(1)).Return(&domain.Vault{ID: 1, Name: "vault"}, nil)
	vaultSvc := NewVaultService(vaultRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	root := &domain.Folder{ID: 1, VaultID: 1, Path: "assets", Action: domain.FolderActionCreate}
	folderRepo.On("GetAllByPathHash", mock.Anything, mock.Anything, int64(1), int64(1)).
		Return([]*domain.Folder{root}, nil)
	folderRepo.On("ListByPathPrefix", mock.Anything, "assets", int64(1), int64(1)).
		Return([]*domain.Folder{}, nil)
	noteRepo.On("ListByPathPrefix", mock.Anything, "assets", int64(1), int64(1)).
		Return([]*domain.Note{}, nil)
	folderRepo.On("Update", mock.Anything, root, int64(1)).Return(root, nil)
	svc := NewFolderService(folderRepo, noteRepo, repo, vaultSvc, nil, nil, nil, nil)

	_, err := svc.DeleteTree(context.Background(), 1, &dto.FolderDeleteRequest{Vault: "vault", Path: "assets"})
	require.NoError(t, err)
	stored, err := repo.GetByPathHash(context.Background(), file.PathHash, 1, 1)
	require.NoError(t, err)
	require.Equal(t, domain.FileActionDelete, stored.Action)
	require.Zero(t, stored.Rename)
	folderRepo.AssertExpectations(t)
	noteRepo.AssertExpectations(t)
	vaultRepo.AssertExpectations(t)
}
