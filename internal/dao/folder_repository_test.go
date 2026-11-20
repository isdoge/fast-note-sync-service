package dao

import (
	"context"
	"fmt"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/stretchr/testify/require"
)

// TestFolderRepository_Update_WritesZeroFIDAndLevel 覆盖 P1-08：目录移回根目录时
// FID=0、Level=0 属于真实状态，GORM 的 Updates(struct) 默认跳过零值，必须显式 Select。
// Moving a folder back to the root (FID=0, Level=0) must actually persist those zeros.
func TestFolderRepository_Update_WritesZeroFIDAndLevel(t *testing.T) {
	daoInst, cleanup := setupSyncWindowBatchEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)
	require.NoError(t, daoInst.ResolveDB("user_"+fmt.Sprint(uid)).AutoMigrate(&model.Folder{}))

	repo := NewFolderRepository(daoInst)
	created, err := repo.Create(ctx, &domain.Folder{
		VaultID: batchVaultID, Path: "parent/child", PathHash: "ph-child",
		Level: 2, FID: 42, Action: domain.FolderActionCreate,
	}, uid)
	require.NoError(t, err)
	require.NotEmpty(t, created.Revision)

	created.FID = 0
	created.Level = 0
	created.Path = "child"
	created.PathHash = "ph-root"
	updated, err := repo.Update(ctx, created, uid)
	require.NoError(t, err)
	require.Equal(t, int64(0), updated.FID)
	require.Equal(t, int64(0), updated.Level)

	got, err := repo.GetByID(ctx, created.ID, uid)
	require.NoError(t, err)
	require.Equal(t, int64(0), got.FID, "FID 必须真正写回 0，而不是被零值跳过")
	require.Equal(t, int64(0), got.Level)
	require.Equal(t, "child", got.Path)
	require.NotEqual(t, created.Revision, got.Revision)
}

// TestFolderRepository_UpdateIfRevision_WritesZeroFIDAndLevel 同一约束适用于 CAS 更新。
func TestFolderRepository_UpdateIfRevision_WritesZeroFIDAndLevel(t *testing.T) {
	daoInst, cleanup := setupSyncWindowBatchEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)
	require.NoError(t, daoInst.ResolveDB("user_"+fmt.Sprint(uid)).AutoMigrate(&model.Folder{}))

	repo := NewFolderRepository(daoInst)
	created, err := repo.Create(ctx, &domain.Folder{
		VaultID: batchVaultID, Path: "a/b", PathHash: "ph-ab",
		Level: 2, FID: 7, Action: domain.FolderActionCreate,
	}, uid)
	require.NoError(t, err)

	created.FID = 0
	created.Level = 0
	ok, err := repo.UpdateIfRevision(ctx, created, created.Revision, uid)
	require.NoError(t, err)
	require.True(t, ok)

	got, err := repo.GetByID(ctx, created.ID, uid)
	require.NoError(t, err)
	require.Equal(t, int64(0), got.FID)
	require.Equal(t, int64(0), got.Level)
}

// TestFolderRepository_ListByPathsMeta_IncludesTombstones 批量元数据必须包含墓碑，
// 否则窗口外的删除意图会被当成"服务端不存在"而误判。
func TestFolderRepository_ListByPathsMeta_IncludesTombstones(t *testing.T) {
	daoInst, cleanup := setupSyncWindowBatchEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)
	require.NoError(t, daoInst.ResolveDB("user_"+fmt.Sprint(uid)).AutoMigrate(&model.Folder{}))

	repo := NewFolderRepository(daoInst)
	_, err := repo.Create(ctx, &domain.Folder{
		VaultID: batchVaultID, Path: "live", PathHash: "ph-live", Action: domain.FolderActionCreate,
	}, uid)
	require.NoError(t, err)
	tomb, err := repo.Create(ctx, &domain.Folder{
		VaultID: batchVaultID, Path: "gone", PathHash: "ph-gone", Action: domain.FolderActionCreate,
	}, uid)
	require.NoError(t, err)
	tomb.Action = domain.FolderActionDelete
	_, err = repo.Update(ctx, tomb, uid)
	require.NoError(t, err)

	meta, err := repo.ListByPathsMeta(ctx, []string{"live", "gone", "absent"}, batchVaultID, uid)
	require.NoError(t, err)
	require.Len(t, meta, 2)
	require.Equal(t, domain.FolderActionCreate, meta["live"].Action)
	require.Equal(t, domain.FolderActionDelete, meta["gone"].Action)
	require.NotContains(t, meta, "absent")
}
