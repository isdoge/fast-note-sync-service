package model

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	gormPostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type captureLogger struct {
	stmts []string
}

func (l *captureLogger) LogMode(logger.LogLevel) logger.Interface { return l }
func (l *captureLogger) Info(context.Context, string, ...interface{})  {}
func (l *captureLogger) Warn(context.Context, string, ...interface{})  {}
func (l *captureLogger) Error(context.Context, string, ...interface{}) {}

func (l *captureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.stmts = append(l.stmts, sql)
}

// TestPathIndexPrefixLengthSQLiteNoop verifies that adding GORM index length
// metadata for the MySQL key-length fix does not alter existing SQLite
// databases. The project SQLite driver ignores schema index lengths when
// building indexes, and the model column type remains varchar(1024).
// TestPathIndexPrefixLengthSQLiteNoop 验证为修复 MySQL 索引键长而增加的
// GORM 索引长度元数据不会改变既有 SQLite 库：项目使用的 SQLite 驱动生成
// 索引时忽略 schema 里的 length，模型列类型也仍是 varchar(1024)。
func TestPathIndexPrefixLengthSQLiteNoop(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "fns.sqlite3")

	before, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	// Seed the table and indexes with the model shape from the parent commit of
	// b2411461, before the index length fix. AutoMigrate may still add columns
	// introduced after that version, so the no-change assertion is restricted to
	// the pre-existing indexed path columns and their existing indexes.
	// 使用 b2411461 父提交、索引长度修复之前的模型形态建表与索引。
	// AutoMigrate 仍可能补充后续版本新增列，因此“零变化”断言只针对既有
	// 受索引影响的 path 列和既有索引。
	oldNoteDDL := "CREATE TABLE \"note\"  (id INTEGER PRIMARY KEY AUTOINCREMENT,vault_id integer NOT NULL DEFAULT 0,`path` varchar(1024) DEFAULT \"\",`path_hash` varchar(1024) DEFAULT \"\",`action` varchar(255) DEFAULT \"\",`rename` integer DEFAULT 0,`fid` integer DEFAULT 0, `content` text DEFAULT \"\", `content_hash` text DEFAULT \"\", `content_last_snapshot` text NOT NULL DEFAULT \"\", `content_last_snapshot_hash` text NOT NULL DEFAULT \"\", `version` integer DEFAULT 0, `client_name` text NOT NULL DEFAULT \"\", `client_type` text NOT NULL DEFAULT \"\", `client_version` text NOT NULL DEFAULT \"\", `size` integer DEFAULT 0, `ctime` integer DEFAULT 0, `mtime` integer DEFAULT 0, `updated_timestamp` integer DEFAULT 0, `created_at` datetime DEFAULT NULL, `updated_at` datetime DEFAULT NULL)"
	require.NoError(t, before.Exec(oldNoteDDL).Error)
	require.NoError(t, before.Exec("CREATE INDEX `idx_vault_id_action_fid` ON `note`(`vault_id`,`action`,`fid`)").Error)
	require.NoError(t, before.Exec("CREATE INDEX `idx_vault_id_action_rename` ON `note`(`vault_id`,`action`,`rename`)").Error)
	require.NoError(t, before.Exec("CREATE INDEX `idx_vault_id_path` ON `note`(`vault_id`,`path`)").Error)
	require.NoError(t, before.Exec("CREATE INDEX `idx_vault_id_path_hash` ON `note`(`vault_id`,`path_hash`)").Error)
	require.NoError(t, before.Exec("CREATE INDEX `idx_vault_id_rename` ON `note`(`vault_id`,`rename`)").Error)
	require.NoError(t, before.Exec("CREATE INDEX `idx_vault_id_updated_at` ON `note`(`vault_id`,`updated_at`)").Error)
	require.NoError(t, before.Exec("CREATE INDEX `idx_vault_id_updated_timestamp` ON `note`(`vault_id`,`updated_timestamp`)").Error)

	beforeSQL := sqliteMasterDump(t, before)
	sqlDB, err := before.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	capture := &captureLogger{}
	after, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: capture})
	require.NoError(t, err)
	require.NoError(t, after.AutoMigrate(&Note{}))

	afterSQL := sqliteMasterDump(t, after)
	require.Equal(t, beforeSQL, afterSQL, "SQLite schema must remain byte-for-byte identical")

	for _, stmt := range capture.stmts {
		upper := strings.ToUpper(stmt)
		require.NotContains(t, upper, "ALTER TABLE", "AutoMigrate must not alter the existing SQLite table: %s", stmt)
		require.NotContains(t, upper, "CREATE INDEX", "AutoMigrate must not recreate an existing SQLite index: %s", stmt)
		require.NotContains(t, upper, "CREATE UNIQUE INDEX", "AutoMigrate must not recreate an existing SQLite index: %s", stmt)
	}
}

func sqliteMasterDump(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	rows, err := db.Raw("SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name, tbl_name, sql").Rows()
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var typ, name, table, sql string
		require.NoError(t, rows.Scan(&typ, &name, &table, &sql))
		out = append(out, typ+"|"+name+"|"+table+"|"+sql)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestPathIndexPrefixLengthPostgresIgnored verifies that PostgreSQL ignores
// GORM's MySQL-only index length option. It runs only when FNS_TEST_PG_DSN is
// provided by the real PostgreSQL validation step.
// TestPathIndexPrefixLengthPostgresIgnored 验证 PostgreSQL 驱动忽略 GORM 的
// MySQL 专用索引长度选项。只有真实 PostgreSQL 验证步骤提供 FNS_TEST_PG_DSN
// 时才执行。
func TestPathIndexPrefixLengthPostgresIgnored(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_PG_DSN not set")
	}

	db, err := gorm.Open(gormPostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	require.NoError(t, db.Exec("DROP TABLE IF EXISTS note").Error)
	require.NoError(t, db.Exec("CREATE TABLE note (id bigint PRIMARY KEY, vault_id bigint NOT NULL DEFAULT 0, path varchar(1024) NOT NULL DEFAULT '', path_hash varchar(1024) NOT NULL DEFAULT '')").Error)
	require.NoError(t, db.Exec("CREATE INDEX idx_vault_id_path ON note (vault_id, path)").Error)
	require.NoError(t, db.Exec("CREATE INDEX idx_vault_id_path_hash ON note (vault_id, path_hash)").Error)

	before := pgIndexDump(t, db)
	capture := &captureLogger{}
	db.Config.Logger = capture
	require.NoError(t, db.AutoMigrate(&Note{}))

	after := pgIndexDump(t, db)
	for _, want := range before {
		require.Contains(t, after, want, "pre-existing PostgreSQL index definition must not change")
	}
	for _, stmt := range capture.stmts {
		upper := strings.ToUpper(stmt)
		require.NotContains(t, upper, "DROP INDEX", "PostgreSQL must not drop an existing index: %s", stmt)
		require.NotContains(t, upper, "ALTER TABLE note", "PostgreSQL must not alter the pre-created note table: %s", stmt)
		if strings.Contains(upper, "CREATE INDEX") {
			require.Contains(t, upper, "IF NOT EXISTS", "PostgreSQL index creation must remain idempotent: %s", stmt)
		}
	}
}

func pgIndexDump(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	rows, err := db.Raw("SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'note' ORDER BY indexname").Rows()
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name, def string
		require.NoError(t, rows.Scan(&name, &def))
		out = append(out, name+"|"+def)
	}
	require.NoError(t, rows.Err())
	return out
}
