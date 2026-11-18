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

// tombstoneAndLiveFile 在真实 SQLite 仓储中构造"a.png 改名 b.png 后又新建 a.png"
// 的最终状态：同 path_hash 下一条 rename 墓碑行 + 一条后建活跃行。
// tombstoneAndLiveFile builds the end state of "a.png renamed to b.png, then a.png
// re-created" in a real SQLite repository: one rename tombstone plus one later live
// row under the same path_hash.
func tombstoneAndLiveFile(t *testing.T) (domain.FileRepository, *domain.File, *domain.File) {
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
	ctx := context.Background()

	// 1. a.png 原始活跃行
	// 1. The original a.png live row
	tombstone, err := repo.Create(ctx, &domain.File{
		VaultID: 1, Path: "a.png", PathHash: "hash-a",
		ContentHash: "content-a-v1", Size: 10, Action: domain.FileActionCreate,
	}, 1)
	require.NoError(t, err)

	// 2. 改名留下的墓碑行（同 fileService.Rename：Action=delete、Rename=1）
	// 2. The rename tombstone (as fileService.Rename writes: Action=delete, Rename=1)
	tombstone.Action = domain.FileActionDelete
	tombstone.Rename = 1
	_, err = repo.Update(ctx, tombstone, 1)
	require.NoError(t, err)

	// 3. 后建的 a.png 活跃行
	// 3. The re-created a.png live row
	live, err := repo.Create(ctx, &domain.File{
		VaultID: 1, Path: "a.png", PathHash: "hash-a",
		ContentHash: "content-a-v2", Size: 20, Action: domain.FileActionCreate,
	}, 1)
	require.NoError(t, err)
	require.Greater(t, live.ID, tombstone.ID)
	return repo, tombstone, live
}

// TestFileService_Delete_TombstoneAndLiveRows 验证：同 path_hash 并存墓碑行与活跃行时，
// 删除新 a.png 必须真正软删活跃行（返回 Unchanged=false），而不是命中旧墓碑行判成
// "已删除/Unchanged"，导致附件删不掉。
// TestFileService_Delete_TombstoneAndLiveRows verifies that when a tombstone and a live
// row coexist for the same path_hash, deleting the new a.png must actually soft-delete
// the live row (Unchanged=false) instead of hitting the old tombstone and reporting
// "already deleted / Unchanged", which would leave the attachment undeletable.
func TestFileService_Delete_TombstoneAndLiveRows(t *testing.T) {
	repo, tombstone, live := tombstoneAndLiveFile(t)
	vaultRepo := new(domainmocks.MockVaultRepository)
	vaultRepo.On("GetByName", mock.Anything, "vault", int64(1)).Return(&domain.Vault{ID: 1, Name: "vault"}, nil)
	vaultSvc := NewVaultService(vaultRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	svc := NewFileService(nil, repo, nil, vaultSvc, nil, nil, nil, nil, nil).(*fileService)

	got, err := svc.Delete(context.Background(), 1, &dto.FileDeleteRequest{
		Vault: "vault", Path: live.Path, PathHash: live.PathHash,
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

	require.False(t, got.Unchanged, "并存时删除必须作用于活跃行，不能判成 Unchanged")
	require.Equal(t, string(domain.FileActionDelete), got.Action)

	// 活跃行已被软删；墓碑行保持不变。
	// The live row is soft-deleted; the tombstone row is untouched.
	rows, err := repo.ListByPathHash(context.Background(), live.PathHash, 1, 1)
	require.NoError(t, err)
	for _, row := range rows {
		if row.ID == live.ID {
			require.Equal(t, domain.FileActionDelete, row.Action, "活跃行必须被软删")
			require.Zero(t, row.Rename)
		}
		if row.ID == tombstone.ID {
			require.Equal(t, domain.FileActionDelete, row.Action)
			require.Equal(t, int64(1), row.Rename, "旧墓碑行不应被改写")
		}
	}
	vaultRepo.AssertExpectations(t)
}

// TestFileService_Delete_TombstoneOnly_Idempotent 验证只有墓碑行时删除仍幂等
// （Unchanged=true），保持既有行为。
// TestFileService_Delete_TombstoneOnly_Idempotent verifies that deleting with only a
// tombstone row stays idempotent (Unchanged=true), preserving existing behavior.
func TestFileService_Delete_TombstoneOnly_Idempotent(t *testing.T) {
	repo, tombstone, live := tombstoneAndLiveFile(t)
	// 物理删掉活跃行，只留墓碑行
	// Physically remove the live row so only the tombstone remains
	require.NoError(t, repo.Delete(context.Background(), live.ID, 1))

	vaultRepo := new(domainmocks.MockVaultRepository)
	vaultRepo.On("GetByName", mock.Anything, "vault", int64(1)).Return(&domain.Vault{ID: 1, Name: "vault"}, nil)
	vaultSvc := NewVaultService(vaultRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	svc := NewFileService(nil, repo, nil, vaultSvc, nil, nil, nil, nil, nil).(*fileService)

	got, err := svc.Delete(context.Background(), 1, &dto.FileDeleteRequest{
		Vault: "vault", Path: tombstone.Path, PathHash: tombstone.PathHash,
	})
	require.NoError(t, err)
	require.True(t, got.Unchanged, "只有墓碑行时删除必须保持幂等 Unchanged=true")
	vaultRepo.AssertExpectations(t)
}
