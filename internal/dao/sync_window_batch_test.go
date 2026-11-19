package dao

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupSyncWindowBatchEnv 为 *Dao 搭建 SQLite 临时工作区（关闭写队列，同步执行）。
// setupSyncWindowBatchEnv sets up a temporary SQLite workspace for *Dao with the
// write queue disabled so ExecuteWrite runs synchronously.
func setupSyncWindowBatchEnv(t *testing.T) (*Dao, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "fast-note-sync-service-syncwindow-test-*")
	require.NoError(t, err)

	origWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tempDir))
	require.NoError(t, os.MkdirAll(filepath.Join("storage", "database"), 0755))

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
		WithLogger(zap.NewNop()),
	)

	cleanup := func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = os.Chdir(origWd)
		_ = os.RemoveAll(tempDir)
	}
	return daoInst, cleanup
}

const batchVaultID = int64(7)

// seedFileRows 直接向用户库插入附件元数据行（绕过 Create 的文件搬移）。
// 租户库 key 与 fileRepository.GetKey 一致（user_file_<uid>）。
func seedFileRows(t *testing.T, daoInst *Dao, uid int64, rows []model.File) {
	t.Helper()
	userDB := daoInst.ResolveDB("user_file_" + fmt.Sprint(uid))
	require.NoError(t, userDB.AutoMigrate(&model.File{}))
	require.NoError(t, userDB.Create(&rows).Error)
}

// TestFileRepository_ListByPathsMeta_ChunkedOver500 验证附件批量元数据查询在超过
// 500 条时分块执行且结果完整：1201 条路径全部命中，同一路径多行时保留
// updated_timestamp 最新的一条（第 8 条）。
func TestFileRepository_ListByPathsMeta_ChunkedOver500(t *testing.T) {
	daoInst, cleanup := setupSyncWindowBatchEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	const total = 1201 // 覆盖 500+500+201 三块
	rows := make([]model.File, 0, total)
	for i := 0; i < total; i++ {
		path := fmt.Sprintf("dir/file-%04d.bin", i)
		rows = append(rows, model.File{
			VaultID:          batchVaultID,
			Action:           "create",
			Path:             path,
			PathHash:         util.EncodeHash32(path),
			ContentHash:      fmt.Sprintf("hash-%04d", i),
			Size:             int64(i),
			Ctime:            1000,
			Mtime:            2000,
			UpdatedTimestamp: int64(10000 + i),
		})
	}
	// 同一路径的旧重复行：updated_timestamp 更小，不得胜出。
	rows = append(rows, model.File{
		VaultID:          batchVaultID,
		Action:           "modify",
		Path:             "dir/file-0000.bin",
		PathHash:         util.EncodeHash32("dir/file-0000.bin"),
		ContentHash:      "stale-row",
		UpdatedTimestamp: 1,
	})
	// 另一 vault 的同路径行：不得串库。
	rows = append(rows, model.File{
		VaultID:          batchVaultID + 1,
		Action:           "create",
		Path:             "dir/file-0000.bin",
		PathHash:         util.EncodeHash32("dir/file-0000.bin"),
		ContentHash:      "other-vault",
		UpdatedTimestamp: 99999999,
	})
	seedFileRows(t, daoInst, uid, rows)

	fileRepo := NewFileRepository(daoInst)
	paths := make([]string, 0, total)
	for i := 0; i < total; i++ {
		paths = append(paths, fmt.Sprintf("dir/file-%04d.bin", i))
	}
	got, err := fileRepo.ListByPathsMeta(ctx, paths, batchVaultID, uid)
	require.NoError(t, err)
	require.Len(t, got, total)
	for i := 0; i < total; i++ {
		path := fmt.Sprintf("dir/file-%04d.bin", i)
		f, ok := got[path]
		require.True(t, ok, path)
		require.Equal(t, fmt.Sprintf("hash-%04d", i), f.ContentHash)
		require.Equal(t, domain.FileActionCreate, f.Action)
	}
	// 旧重复行未胜出、别的库未串入。
	require.Equal(t, "hash-0000", got["dir/file-0000.bin"].ContentHash)
}

// TestFileRepository_ListByPathsMeta_TombstoneIncluded 验证批量查询包含墓碑行
// （含所有状态），调用方据此把墓碑判为"不一致"。
func TestFileRepository_ListByPathsMeta_TombstoneIncluded(t *testing.T) {
	daoInst, cleanup := setupSyncWindowBatchEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)
	seedFileRows(t, daoInst, uid, []model.File{{
		VaultID: batchVaultID, Action: "delete", Path: "gone.bin",
		PathHash: util.EncodeHash32("gone.bin"), ContentHash: "h", UpdatedTimestamp: 500,
	}})

	fileRepo := NewFileRepository(daoInst)
	got, err := fileRepo.ListByPathsMeta(ctx, []string{"gone.bin"}, batchVaultID, uid)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, domain.FileActionDelete, got["gone.bin"].Action)
}

// TestSettingRepository_ListByPathHashesMeta_ChunkedOver500 验证配置批量元数据查询
// 分块正确、不读正文文件（无 content.txt 时也不报错），同哈希多行保留最新一条（第 8 条）。
func TestSettingRepository_ListByPathHashesMeta_ChunkedOver500(t *testing.T) {
	daoInst, cleanup := setupSyncWindowBatchEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	const total = 1201
	userDB := daoInst.ResolveDB("user_setting_" + fmt.Sprint(uid))
	require.NoError(t, userDB.AutoMigrate(&model.Setting{}))
	rows := make([]model.Setting, 0, total)
	hashes := make([]string, 0, total)
	for i := 0; i < total; i++ {
		path := fmt.Sprintf(".obsidian/plugin-%04d.json", i)
		hash := util.EncodeHash32(path)
		hashes = append(hashes, hash)
		rows = append(rows, model.Setting{
			VaultID:          batchVaultID,
			Action:           "create",
			Path:             path,
			PathHash:         hash,
			ContentHash:      fmt.Sprintf("hash-%04d", i),
			Size:             int64(i),
			Ctime:            1000,
			Mtime:            2000,
			UpdatedTimestamp: int64(10000 + i),
		})
	}
	// 同一哈希的旧重复行：updated_timestamp 更小，不得胜出。
	rows = append(rows, model.Setting{
		VaultID:          batchVaultID,
		Action:           "modify",
		Path:             ".obsidian/plugin-0000.json",
		PathHash:         hashes[0],
		ContentHash:      "stale-row",
		UpdatedTimestamp: 1,
	})
	require.NoError(t, userDB.Create(&rows).Error)

	settingRepo := NewSettingRepository(daoInst)
	got, err := settingRepo.ListByPathHashesMeta(ctx, hashes, batchVaultID, uid)
	require.NoError(t, err)
	require.Len(t, got, total)
	for i, hash := range hashes {
		s, ok := got[hash]
		require.True(t, ok, hash)
		require.Equal(t, fmt.Sprintf("hash-%04d", i), s.ContentHash)
		require.Equal(t, domain.SettingActionCreate, s.Action)
	}
	require.Equal(t, "hash-0000", got[hashes[0]].ContentHash)
}
