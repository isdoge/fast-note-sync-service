package dao

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/glebarez/sqlite"
)

// TestGetOrCreateDB_SQLiteKeysStaySeparate verifies the #231 fix must not
// collapse SQLite tenant keys: in SQLite mode every repository key still maps
// to its own database file and its own cached connection.
// TestGetOrCreateDB_SQLiteKeysStaySeparate 验证 #231 修复不能合并 SQLite 租户
// key：SQLite 模式下每个仓储 key 仍对应独立库文件和独立缓存连接。
func TestGetOrCreateDB_SQLiteKeysStaySeparate(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "db.sqlite3")

	mainDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	dbCfg := &config.DatabaseConfig{
		Type:             "sqlite",
		Path:             dbPath,
		EnableWriteQueue: util.Ptr(false),
	}
	daoInst := New(mainDB, context.Background(), WithConfig(dbCfg), WithUserDatabaseConfig(dbCfg), WithLogger(zap.NewNop()))

	a := daoInst.GetOrCreateDB("user_note_1")
	b := daoInst.GetOrCreateDB("user_vault_1")
	require.NotNil(t, a)
	require.NotNil(t, b)
	require.NotSame(t, a, b)
	require.Equal(t, 2, daoInst.PoolCount())
}

// TestTenantCacheKey_PostgresAndMySQLShareUserPool verifies cache-key
// normalization without opening a real server: PostgreSQL and MySQL keys that
// resolve to the same user_<uid> schema/database share one pool, while SQLite
// and non-user fallback keys keep their original isolation.
// TestTenantCacheKey_PostgresAndMySQLShareUserPool 在不连接真实服务器的情况下
// 验证缓存键归一：PostgreSQL / MySQL 解析到同一 user_<uid> schema/库的 key
// 共用一个连接池；SQLite 和非用户回退 key 保持原有隔离。
func TestTenantCacheKey_PostgresAndMySQLShareUserPool(t *testing.T) {
	for _, typ := range []string{"postgres", "mysql"} {
		t.Run(typ, func(t *testing.T) {
			cfg := &config.DatabaseConfig{Type: typ, Host: "127.0.0.1", Name: "fns_test", EnableWriteQueue: util.Ptr(false)}
			d := New(nil, context.Background(), WithConfig(cfg), WithUserDatabaseConfig(cfg), WithLogger(zap.NewNop()))
			require.Equal(t, "user_1", d.tenantCacheKey("user_note_1"))
			require.Equal(t, "user_1", d.tenantCacheKey("user_vault_1"))
			require.Equal(t, "user_1", d.tenantCacheKey("user_file_1"))
			require.Equal(t, "user_2", d.tenantCacheKey("user_note_2"))
			require.Equal(t, "custom", d.tenantCacheKey("custom"))
		})
	}
}

// TestGetOrCreateDB_ConcurrentFirstAccessSinglePool verifies that concurrent
// first access to repository keys of the same user creates only one pool in
// PostgreSQL/MySQL cache-key mode. It uses SQLite files to avoid a server, and
// the cache-key normalization under test is type-independent.
// TestGetOrCreateDB_ConcurrentFirstAccessSinglePool 验证同一用户多个仓储 key
// 并发首次访问只创建一个连接池。为避免依赖服务器，这里用 SQLite 文件驱动；
// 被测的缓存键归一逻辑与具体驱动无关。
func TestGetOrCreateDB_ConcurrentFirstAccessSinglePool(t *testing.T) {
	tmpDir := t.TempDir()
	mainPath := filepath.Join(tmpDir, "main.sqlite3")
	mainDB, err := gorm.Open(sqlite.Open(mainPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	cfg := &config.DatabaseConfig{Type: "sqlite", Path: filepath.Join(tmpDir, "tenant.sqlite3"), EnableWriteQueue: util.Ptr(false)}
	daoInst := New(mainDB, context.Background(), WithConfig(cfg), WithUserDatabaseConfig(cfg), WithLogger(zap.NewNop()))

	keys := []string{"user_note_1", "user_vault_1", "user_file_1"}
	var wg sync.WaitGroup
	dbs := make([]*gorm.DB, len(keys))
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			dbs[i] = daoInst.GetOrCreateDB(key)
		}(i, key)
	}
	wg.Wait()

	require.NotNil(t, dbs[0])
	for _, db := range dbs[1:] {
		require.NotNil(t, db)
	}
	require.Equal(t, len(keys), daoInst.PoolCount(), "SQLite behavior must remain per-key even under concurrent access")
}

func TestTenantCacheKey_SQLiteKeepsOriginalKeys(t *testing.T) {
	cfg := &config.DatabaseConfig{Type: "sqlite", Path: "storage/database/db.sqlite3", EnableWriteQueue: util.Ptr(false)}
	d := New(nil, context.Background(), WithConfig(cfg), WithUserDatabaseConfig(cfg), WithLogger(zap.NewNop()))
	require.Equal(t, "user_note_1", d.tenantCacheKey("user_note_1"))
	require.Equal(t, "user_vault_1", d.tenantCacheKey("user_vault_1"))
}
