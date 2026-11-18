package dao

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestGetOrCreateDB_ConcurrentLastUsedNoRace 验证多个 goroutine 并发对同一个 key
// 调用 GetOrCreateDB 时没有对 dbEntry.lastUsed 的数据竞争（须以 -race 运行方能检出）。
// 修复前：读锁下直接写 entry.lastUsed = time.Now()，-race 报 DATA RACE。
// TestGetOrCreateDB_ConcurrentLastUsedNoRace verifies that concurrent GetOrCreateDB calls
// on the same key from multiple goroutines produce no data race on dbEntry.lastUsed
// (only detectable under -race).
// Before the fix: writing entry.lastUsed = time.Now() under a read lock, -race reports
// DATA RACE.
func TestGetOrCreateDB_ConcurrentLastUsedNoRace(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fast-note-sync-service-dbpool-race-*")
	require.NoError(t, err)
	origWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tempDir))
	defer func() {
		_ = os.Chdir(origWd)
		_ = os.RemoveAll(tempDir)
	}()
	require.NoError(t, os.MkdirAll(filepath.Join("storage", "database"), 0755))

	dbPath := filepath.Join("storage", "database", "db.sqlite3")
	mainDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	defer func() {
		if sqlDB, err := mainDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	dbCfg := &config.DatabaseConfig{
		Type:             "sqlite",
		Path:             dbPath,
		EnableWriteQueue: util.Ptr(false),
	}
	daoInst := New(mainDB, context.Background(),
		WithConfig(dbCfg),
		WithUserDatabaseConfig(dbCfg),
		WithLogger(zap.NewNop()),
	)

	// 先建好该 key 的缓存条目，随后并发取用，制造对同一条目 lastUsed 的并发写。
	// Warm the cache entry first, then hammer it concurrently to create concurrent
	// writes to the same entry's lastUsed.
	require.NotNil(t, daoInst.GetOrCreateDB("tenant-race"))

	const goroutines = 16
	const iterations = 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if db := daoInst.GetOrCreateDB("tenant-race"); db == nil {
					t.Error("GetOrCreateDB returned nil")
					return
				}
			}
		}()
	}
	wg.Wait()
}
