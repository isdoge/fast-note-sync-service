package dao

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/stretchr/testify/require"
)

// TestFileRepository_GetByPathHash_PrefersLiveRowOverTombstone 验证：同一
// (vault_id, path_hash) 并存"改名留下的 delete 墓碑行"与"后建的活跃行"时
// （a.png 改名 b.png 后又新建 a.png），GetByPathHash 必须返回活跃行，
// 而不是无序 First() 命中的旧墓碑行。
// TestFileRepository_GetByPathHash_PrefersLiveRowOverTombstone verifies that when a
// rename tombstone and a later live row coexist for the same (vault_id, path_hash)
// (a.png renamed to b.png, then a.png re-created), GetByPathHash returns the live row
// instead of the old tombstone an unordered First() may hit.
func TestFileRepository_GetByPathHash_PrefersLiveRowOverTombstone(t *testing.T) {
	fileRepo, cleanup := setupFileRepoTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	// 1. 创建 a.png 活跃行
	// 1. Create the original a.png live row
	first, err := fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: "a.png", PathHash: "hash-a",
		ContentHash: "content-a-v1", Size: 10, Mtime: 1000, Ctime: 1000,
		Action: domain.FileActionCreate,
	}, uid)
	require.NoError(t, err)

	// 2. 模拟 fileService.Rename：旧行标记为 delete 墓碑（Rename=1）
	// 2. Simulate fileService.Rename: mark the old row as a delete tombstone (Rename=1)
	first.Action = domain.FileActionDelete
	first.Rename = 1
	_, err = fileRepo.Update(ctx, first, uid)
	require.NoError(t, err)

	// 3. 再新建同名 a.png，产生同 path_hash 的后建活跃行（id 更大）
	// 3. Re-create a.png, producing a later live row with the same path_hash (larger id)
	second, err := fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: "a.png", PathHash: "hash-a",
		ContentHash: "content-a-v2", Size: 20, Mtime: 2000, Ctime: 2000,
		Action: domain.FileActionCreate,
	}, uid)
	require.NoError(t, err)
	require.Greater(t, second.ID, first.ID)

	got, err := fileRepo.GetByPathHash(ctx, "hash-a", 1, uid)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, second.ID, got.ID, "并存时必须返回活跃行而不是墓碑行")
	require.NotEqual(t, domain.FileActionDelete, got.Action)
	require.Equal(t, "content-a-v2", got.ContentHash)
}

// TestFileRepository_GetByPathHash_PrefersNewestLiveRow 验证多个活跃行并存时
// 返回 id 较新者（与"优先活跃行"的第二优先级一致）。
// TestFileRepository_GetByPathHash_PrefersNewestLiveRow verifies that among multiple
// live rows the newest (largest id) wins, matching the second priority of the rule.
func TestFileRepository_GetByPathHash_PrefersNewestLiveRow(t *testing.T) {
	fileRepo, cleanup := setupFileRepoTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	older, err := fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: "a.png", PathHash: "hash-a",
		ContentHash: "older", Action: domain.FileActionCreate,
	}, uid)
	require.NoError(t, err)
	newer, err := fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: "a.png", PathHash: "hash-a",
		ContentHash: "newer", Action: domain.FileActionCreate,
	}, uid)
	require.NoError(t, err)
	require.Greater(t, newer.ID, older.ID)

	got, err := fileRepo.GetByPathHash(ctx, "hash-a", 1, uid)
	require.NoError(t, err)
	require.Equal(t, newer.ID, got.ID, "多个活跃行并存时返回 id 较新者")
}

// TestFileRepository_GetByPathHash_TombstoneOnlyKeepsOldBehavior 验证只有墓碑行时
// 行为与修复前一致：仍返回墓碑行（按主键升序的第一条）。
// TestFileRepository_GetByPathHash_TombstoneOnlyKeepsOldBehavior verifies that with
// tombstones only, the behavior is unchanged: a tombstone row is still returned
// (the first one by primary key ascending order).
func TestFileRepository_GetByPathHash_TombstoneOnlyKeepsOldBehavior(t *testing.T) {
	fileRepo, cleanup := setupFileRepoTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	first, err := fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: "a.png", PathHash: "hash-a",
		ContentHash: "content-a-v1", Action: domain.FileActionCreate,
	}, uid)
	require.NoError(t, err)
	first.Action = domain.FileActionDelete
	first.Rename = 1
	_, err = fileRepo.Update(ctx, first, uid)
	require.NoError(t, err)

	got, err := fileRepo.GetByPathHash(ctx, "hash-a", 1, uid)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, first.ID, got.ID, "只有墓碑行时仍返回墓碑行")
	require.Equal(t, domain.FileActionDelete, got.Action)
}
