package dao

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupFileRepoTestEnv sets up a temporary workspace for a *Dao backed by SQLite, with the
// write queue disabled so ExecuteWrite runs synchronously without needing a writequeue.Manager.
// setupFileRepoTestEnv 为 *Dao（SQLite 后端）搭建临时工作区，关闭写队列以便 ExecuteWrite
// 同步执行，无需注入 writequeue.Manager。
func setupFileRepoTestEnv(t *testing.T) (domain.FileRepository, func()) {
	tempDir, err := os.MkdirTemp("", "fast-note-sync-service-filerepo-test-*")
	require.NoError(t, err)

	origWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tempDir))

	require.NoError(t, os.MkdirAll(filepath.Join("storage", "database"), 0755))

	logger := zap.NewNop()

	dbPath := filepath.Join("storage", "database", "db.sqlite3")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)

	dbCfg := &config.DatabaseConfig{
		Type:             "sqlite",
		Path:             dbPath,
		EnableWriteQueue: util.Ptr(false),
	}
	daoInst := New(db, context.Background(),
		WithConfig(dbCfg),
		WithUserDatabaseConfig(dbCfg),
		WithLogger(logger),
	)

	fileRepo := NewFileRepository(daoInst)

	cleanup := func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = os.Chdir(origWd)
		_ = os.RemoveAll(tempDir)
	}

	return fileRepo, cleanup
}

// TestFileRepository_Create_RenameFailure_PropagatesErrorAndCleansUp verifies the P0 fix:
// when the temp-file rename fails, Create must return the error (not silently succeed) and
// must not leave an orphaned DB row that claims the file exists.
// TestFileRepository_Create_RenameFailure_PropagatesErrorAndCleansUp 验证 P0 修复：
// 临时文件 rename 失败时，Create 必须返回错误（不能静默成功），且不能残留一条声称文件
// 存在的孤儿数据库记录。
func TestFileRepository_Create_RenameFailure_PropagatesErrorAndCleansUp(t *testing.T) {
	fileRepo, cleanup := setupFileRepoTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	f := &domain.File{
		VaultID:     1,
		Path:        "a.md",
		PathHash:    "hash-a",
		ContentHash: "content-hash-a",
		// Non-existent temp path makes os.Rename fail deterministically.
		// 不存在的临时路径，使 os.Rename 必然失败。
		SavePath: filepath.Join(os.TempDir(), "does-not-exist-fast-note-sync-test", "tmp.dat"),
		Size:     10,
		Mtime:    1000,
		Ctime:    1000,
		Action:   domain.FileActionCreate,
	}

	created, err := fileRepo.Create(ctx, f, uid)
	assert.Error(t, err, "Create must return an error when the rename fails")
	assert.Nil(t, created)

	// The row must not remain in the DB claiming the file exists.
	// 数据库中不能残留一条声称文件存在的记录。
	got, getErr := fileRepo.GetByPathHash(ctx, "hash-a", 1, uid)
	assert.Error(t, getErr, "expected record-not-found, orphaned row should have been cleaned up")
	assert.Nil(t, got)
}

// TestFileRepository_Update_RenameFailure_AbortsBeforeDBWrite verifies the P0 fix: when the
// temp-file rename fails during Update, the DB row must be left untouched (pointing at the
// previous, still-valid content) instead of being updated to claim new content landed.
// TestFileRepository_Update_RenameFailure_AbortsBeforeDBWrite 验证 P0 修复：Update 过程中
// rename 失败时，数据库行必须保持不变（仍指向此前有效的内容），而不是被更新为声称新内容已落地。
func TestFileRepository_Update_RenameFailure_AbortsBeforeDBWrite(t *testing.T) {
	fileRepo, cleanup := setupFileRepoTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	// First create a valid file (no SavePath, so no rename happens; a pure metadata row).
	// 先创建一条不带 SavePath 的合法记录（不触发 rename，作为基线元数据行）。
	f := &domain.File{
		VaultID:     1,
		Path:        "b.md",
		PathHash:    "hash-b",
		ContentHash: "content-hash-b-v1",
		Size:        10,
		Mtime:       1000,
		Ctime:       1000,
		Action:      domain.FileActionCreate,
	}
	created, err := fileRepo.Create(ctx, f, uid)
	require.NoError(t, err)
	require.NotNil(t, created)

	// Now attempt an update that provides a new (non-existent) temp file, forcing rename to fail.
	// 现在尝试更新并提供一个不存在的新临时文件，强制 rename 失败。
	created.ContentHash = "content-hash-b-v2"
	created.SavePath = filepath.Join(os.TempDir(), "does-not-exist-fast-note-sync-test", "tmp2.dat")

	updated, err := fileRepo.Update(ctx, created, uid)
	assert.Error(t, err, "Update must return an error when the rename fails")
	assert.Nil(t, updated)

	// The DB row must still reflect the old content hash, not the failed update.
	// 数据库行必须仍反映旧的 content hash，而不是失败的更新。
	got, getErr := fileRepo.GetByPathHash(ctx, "hash-b", 1, uid)
	require.NoError(t, getErr)
	require.NotNil(t, got)
	assert.Equal(t, "content-hash-b-v1", got.ContentHash, "DB row must not have been overwritten when rename failed")
}

// TestFileRepository_Update_UploadMovesTempFile 验证非删除更新仍将上传临时文件移动到标准路径。
func TestFileRepository_Update_UploadMovesTempFile(t *testing.T) {
	fileRepo, cleanup := setupFileRepoTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	created, err := fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: "upload.png", PathHash: "upload-hash",
		ContentHash: "old-hash", Action: domain.FileActionCreate,
	}, 1)
	require.NoError(t, err)
	finalPath := created.SavePath
	tempPath := filepath.Join(t.TempDir(), "upload.dat")
	require.NoError(t, os.WriteFile(tempPath, []byte("new content"), 0600))
	created.SavePath = tempPath
	created.Action = domain.FileActionModify
	created.ContentHash = "new-hash"

	updated, err := fileRepo.Update(ctx, created, 1)
	require.NoError(t, err)
	require.Equal(t, finalPath, updated.SavePath)
	content, err := os.ReadFile(finalPath)
	require.NoError(t, err)
	require.Equal(t, "new content", string(content))
	_, err = os.Stat(tempPath)
	require.True(t, os.IsNotExist(err), "临时文件应已移动")
	stored, err := fileRepo.GetByPathHash(ctx, created.PathHash, 1, 1)
	require.NoError(t, err)
	require.Equal(t, "new-hash", stored.ContentHash)
	require.Equal(t, domain.FileActionModify, stored.Action)
}

// TestFileRepository_UpdateIfRevision_ConflictKeepsDiskPayload 覆盖 P0-05：CAS 失败时
// 绝不触碰磁盘上现有的 file.dat——旧附件必须保持逐字节不变，临时文件原样留给上层清理。
// CAS failure must never touch the existing file.dat: the previous payload stays
// byte-identical and the temp file is left for the caller to clean up.
func TestFileRepository_UpdateIfRevision_ConflictKeepsDiskPayload(t *testing.T) {
	fileRepo, cleanup := setupFileRepoTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	tmpDir := t.TempDir()
	initialPayload := filepath.Join(tmpDir, "initial.dat")
	require.NoError(t, os.WriteFile(initialPayload, []byte("ORIGINAL-BYTES"), 0644))

	created, err := fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: "a.png", PathHash: "ph-a", ContentHash: "H1",
		SavePath: initialPayload, Size: 14, Mtime: 1000, Ctime: 1000,
		Action: domain.FileActionCreate,
	}, uid)
	require.NoError(t, err)
	require.NotEmpty(t, created.Revision)

	// 读取落盘后的真实路径与字节，作为"冲突后必须原样保留"的基准。
	stored, err := fileRepo.GetByID(ctx, created.ID, uid)
	require.NoError(t, err)
	beforeBytes, err := os.ReadFile(stored.SavePath)
	require.NoError(t, err)
	require.Equal(t, []byte("ORIGINAL-BYTES"), beforeBytes)

	newPayload := filepath.Join(tmpDir, "new.dat")
	require.NoError(t, os.WriteFile(newPayload, []byte("REPLACEMENT-BYTES"), 0644))

	// 用过期令牌提交：必须返回 false，且磁盘文件不变。
	ok, err := fileRepo.UpdateIfRevision(ctx, &domain.File{
		ID: created.ID, VaultID: 1, Path: "a.png", PathHash: "ph-a", ContentHash: "H2",
		SavePath: newPayload, Size: 17, Mtime: 2000, Ctime: 1000, Action: domain.FileActionCreate,
	}, "stale-revision-token", uid)
	require.NoError(t, err)
	require.False(t, ok, "CAS 失败必须返回 false")

	afterBytes, err := os.ReadFile(stored.SavePath)
	require.NoError(t, err)
	require.Equal(t, beforeBytes, afterBytes, "CAS 失败后原磁盘 file.dat 字节必须 100%% 未变")

	// 临时文件没有被搬走（仍由调用方清理），内容也未污染正式位置。
	require.FileExists(t, newPayload)

	// 数据库行同样未被改动：内容哈希与令牌仍是旧值。
	still, err := fileRepo.GetByID(ctx, created.ID, uid)
	require.NoError(t, err)
	require.Equal(t, "H1", still.ContentHash)
	require.Equal(t, created.Revision, still.Revision)
}

// TestFileRepository_UpdateIfRevision_MatchReplacesPayload CAS 成功时才把临时文件搬进
// 正式位置并换新令牌。
func TestFileRepository_UpdateIfRevision_MatchReplacesPayload(t *testing.T) {
	fileRepo, cleanup := setupFileRepoTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	tmpDir := t.TempDir()
	initialPayload := filepath.Join(tmpDir, "initial.dat")
	require.NoError(t, os.WriteFile(initialPayload, []byte("V1"), 0644))
	created, err := fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: "b.png", PathHash: "ph-b", ContentHash: "H1",
		SavePath: initialPayload, Size: 2, Mtime: 1000, Ctime: 1000, Action: domain.FileActionCreate,
	}, uid)
	require.NoError(t, err)
	stored, err := fileRepo.GetByID(ctx, created.ID, uid)
	require.NoError(t, err)

	newPayload := filepath.Join(tmpDir, "new.dat")
	require.NoError(t, os.WriteFile(newPayload, []byte("V2-longer"), 0644))

	ok, err := fileRepo.UpdateIfRevision(ctx, &domain.File{
		ID: created.ID, VaultID: 1, Path: "b.png", PathHash: "ph-b", ContentHash: "H2",
		SavePath: newPayload, Size: 9, Mtime: 2000, Ctime: 1000, Action: domain.FileActionCreate,
	}, created.Revision, uid)
	require.NoError(t, err)
	require.True(t, ok)

	afterBytes, err := os.ReadFile(stored.SavePath)
	require.NoError(t, err)
	require.Equal(t, []byte("V2-longer"), afterBytes)

	updated, err := fileRepo.GetByID(ctx, created.ID, uid)
	require.NoError(t, err)
	require.Equal(t, "H2", updated.ContentHash)
	require.NotEqual(t, created.Revision, updated.Revision, "成功提交必须换新令牌")
}

// TestFileRepository_UpdateIfRevision_RenameFailureRollsBackRow 覆盖 B-03：CAS 命中后
// os.Rename 失败必须触发真实 SQL 回滚，数据库里的 revision 与 content_hash 保持原值，
// 而不是留下"新哈希已写入、磁盘还是旧内容"的库盘不一致。
// A rename failure after a successful CAS must roll the row back: revision and
// content_hash keep their previous values instead of claiming the new payload landed.
func TestFileRepository_UpdateIfRevision_RenameFailureRollsBackRow(t *testing.T) {
	fileRepo, cleanup := setupFileRepoTestEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	tmpDir := t.TempDir()
	initialPayload := filepath.Join(tmpDir, "initial.dat")
	require.NoError(t, os.WriteFile(initialPayload, []byte("ORIGINAL-BYTES"), 0644))
	created, err := fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: "c.png", PathHash: "ph-c", ContentHash: "H1",
		SavePath: initialPayload, Size: 14, Mtime: 1000, Ctime: 1000, Action: domain.FileActionCreate,
	}, uid)
	require.NoError(t, err)
	stored, err := fileRepo.GetByID(ctx, created.ID, uid)
	require.NoError(t, err)
	require.Equal(t, "H1", stored.ContentHash)

	// 不存在的临时路径：CAS 成功之后 os.Rename 必然失败，用于验证事务回滚。
	// A missing temp path makes os.Rename fail deterministically after the CAS hits.
	missing := filepath.Join(tmpDir, "missing-dir", "never.dat")
	ok, err := fileRepo.UpdateIfRevision(ctx, &domain.File{
		ID: created.ID, VaultID: 1, Path: "c.png", PathHash: "ph-c", ContentHash: "H2",
		SavePath: missing, Size: 17, Mtime: 2000, Ctime: 1000, Action: domain.FileActionCreate,
	}, created.Revision, uid)
	require.Error(t, err, "os.Rename 失败必须向调用方返回错误")
	require.False(t, ok, "回滚后不得声称更新成功")

	still, err := fileRepo.GetByID(ctx, created.ID, uid)
	require.NoError(t, err)
	require.Equal(t, "H1", still.ContentHash, "Rename 失败后 content_hash 必须回滚为原值")
	require.Equal(t, created.Revision, still.Revision, "Rename 失败后 revision 必须回滚为原值")

	// 磁盘上的旧附件保持原样。
	afterBytes, err := os.ReadFile(stored.SavePath)
	require.NoError(t, err)
	require.Equal(t, []byte("ORIGINAL-BYTES"), afterBytes)
}
