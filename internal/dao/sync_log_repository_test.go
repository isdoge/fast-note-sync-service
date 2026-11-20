package dao

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupSyncLogTestEnv 为 *Dao 搭建全新的临时 SQLite 用户库（关闭写队列，
// ExecuteWrite 同步执行），不预先创建 sync_log 表。
// setupSyncLogTestEnv builds a *Dao on a brand-new temporary SQLite user database
// (write queue disabled so ExecuteWrite runs synchronously) without pre-creating
// the sync_log table.
func setupSyncLogTestEnv(t *testing.T) (*Dao, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "db.sqlite3")
	mainDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	cfg := &config.DatabaseConfig{
		Type:             "sqlite",
		Path:             dbPath,
		EnableWriteQueue: util.Ptr(false),
	}
	daoInst := New(mainDB, context.Background(),
		WithConfig(cfg),
		WithUserDatabaseConfig(cfg),
		WithLogger(zap.NewNop()),
	)

	cleanup := func() {
		if sqlDB, err := mainDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		daoInst.mu.Lock()
		for _, entry := range daoInst.KeyDb {
			if sqlDB, err := entry.db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		daoInst.mu.Unlock()
	}
	return daoInst, cleanup
}

// TestSyncLogRepository_ConcurrentFirstUseInitializesTable 验证全新用户库中
// sync_log 的首次并发读取/写入不会在建表完成前放行查询。
// 修复前 db(uid) 先 LoadOrStore 标记再 AutoMigrate，其它 goroutine 看到标记后
// 立即查询未建出的表，报 no such table: sync_log。
// TestSyncLogRepository_ConcurrentFirstUseInitializesTable verifies that concurrent
// first reads/writes on a fresh user database cannot run before sync_log exists.
// Before the fix db(uid) marked the key as initialized before AutoMigrate finished,
// so other goroutines queried the missing table and failed with "no such table".
func TestSyncLogRepository_ConcurrentFirstUseInitializesTable(t *testing.T) {
	daoInst, cleanup := setupSyncLogTestEnv(t)
	defer cleanup()

	repo := NewSyncLogRepository(daoInst)
	const uid = int64(7)
	const workers = 32

	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				_, _, err := repo.List(context.Background(), uid, "", "", 1, 20)
				errs <- err
				return
			}
			errs <- repo.Create(context.Background(), &domain.SyncLog{
				UID:      uid,
				VaultID:  1,
				Type:     domain.SyncLogTypeNote,
				Action:   domain.SyncLogActionCreate,
				Path:     fmt.Sprintf("note-%d.md", i),
				PathHash: fmt.Sprintf("hash-%d", i),
				Status:   1,
			}, uid)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	failures := 0
	for err := range errs {
		if err != nil {
			failures++
			t.Logf("concurrent sync_log call failed: %v", err)
		}
	}
	require.Zero(t, failures, "all concurrent first-use calls must succeed")

	_, total, err := repo.List(context.Background(), uid, "", "", 1, workers)
	require.NoError(t, err)
	require.EqualValues(t, workers/2, total)
}
