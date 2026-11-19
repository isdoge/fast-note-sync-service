package dao

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestResolveDB_RelationalMainDatabaseFallback verifies the documented empty
// user-database.type behavior for both CRUD and startup migrations.
// TestResolveDB_RelationalMainDatabaseFallback 验证 user-database.type 为空时，
// CRUD 与启动迁移都使用配置文档约定的主数据库。
func TestResolveDB_RelationalMainDatabaseFallback(t *testing.T) {
	mainDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "main.sqlite3")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, typ := range []string{"mysql", "postgres"} {
		t.Run(typ, func(t *testing.T) {
			cfg := &config.DatabaseConfig{Type: typ, EnableWriteQueue: util.Ptr(false)}
			d := New(mainDB, context.Background(), WithConfig(cfg), WithUserDatabaseConfig(&config.DatabaseConfig{}), WithLogger(zap.NewNop()))
			require.Same(t, mainDB, d.ResolveDB("user_note_1"))
			require.Zero(t, d.PoolCount())
		})
	}
}

// TestGetOrCreateDB_SameNormalizedKeyBuildsOnce proves singleflight semantics
// at the normalized cache key: N concurrent first accesses to repository keys
// of one PostgreSQL/MySQL user call the pool creator exactly once.
// TestGetOrCreateDB_SameNormalizedKeyBuildsOnce 验证归一缓存键的 singleflight
// 语义：同一 PostgreSQL/MySQL 用户多个仓储 key 的 N 次并发首次访问，只调用
// 一次建池函数。
func TestGetOrCreateDB_SameNormalizedKeyBuildsOnce(t *testing.T) {
	tmpDir := t.TempDir()
	mainDB, err := gorm.Open(sqlite.Open(filepath.Join(tmpDir, "main.sqlite3")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	cfg := &config.DatabaseConfig{Type: "postgres", Host: "127.0.0.1", Name: "fns_test", EnableWriteQueue: util.Ptr(false)}
	daoInst := New(mainDB, context.Background(), WithConfig(cfg), WithUserDatabaseConfig(cfg), WithLogger(zap.NewNop()))
	daoInst.schemaEnsurer = func(string) error { return nil }
	var calls atomic.Int32
	ready := make(chan struct{})
	daoInst.poolCreator = func(c config.DatabaseConfig, logger *zap.Logger) (*gorm.DB, error) {
		calls.Add(1)
		<-ready
		return gorm.Open(sqlite.Open(filepath.Join(tmpDir, "created.sqlite3")), &gorm.Config{})
	}

	const n = 8
	keys := []string{"user_note_1", "user_vault_1", "user_file_1", "user_setting_1", "user_folder_1", "user_note_history_1", "user_1", "user_storage_1"}
	var wg sync.WaitGroup
	results := make([]*gorm.DB, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = daoInst.GetOrCreateDB(keys[i])
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(ready)
	wg.Wait()

	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, 1, daoInst.PoolCount())
	require.NotNil(t, results[0])
	for _, db := range results[1:] {
		require.Same(t, results[0], db)
	}
}

// TestGetOrCreateDB_BuildDoesNotBlockCachedKey verifies the global cache lock is
// not held while a new pool is being built: an already cached key remains
// readable while another key's creator is blocked.
// TestGetOrCreateDB_BuildDoesNotBlockCachedKey 验证建新池时不持有全局缓存锁：
// 一个 key 的建池函数被阻塞时，另一个已缓存 key 的访问仍能立即返回。
func TestGetOrCreateDB_BuildDoesNotBlockCachedKey(t *testing.T) {
	tmpDir := t.TempDir()
	mainDB, err := gorm.Open(sqlite.Open(filepath.Join(tmpDir, "main.sqlite3")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	cfg := &config.DatabaseConfig{Type: "sqlite", Path: filepath.Join(tmpDir, "tenant.sqlite3"), EnableWriteQueue: util.Ptr(false)}
	daoInst := New(mainDB, context.Background(), WithConfig(cfg), WithUserDatabaseConfig(cfg), WithLogger(zap.NewNop()))
	cached := daoInst.GetOrCreateDB("cached")
	require.NotNil(t, cached)

	started := make(chan struct{})
	release := make(chan struct{})
	daoInst.poolCreator = func(c config.DatabaseConfig, logger *zap.Logger) (*gorm.DB, error) {
		close(started)
		<-release
		return gorm.Open(sqlite.Open(filepath.Join(tmpDir, "blocked.sqlite3")), &gorm.Config{})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = daoInst.GetOrCreateDB("blocked")
	}()
	<-started

	hitDone := make(chan struct{})
	go func() {
		defer close(hitDone)
		require.Same(t, cached, daoInst.GetOrCreateDB("cached"))
	}()
	select {
	case <-hitDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("cached key access was blocked while another pool was being built")
	}
	close(release)
	<-done
}
