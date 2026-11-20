package model

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	gormMysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	// precheckOldNoteDDL mirrors a <=3.7.0 note table: utf8mb4 varchar path
	// columns with prefix indexes. varchar(1024) holds up to 1024 characters
	// (4096 bytes), so rows can exceed the v3.8.0 varbinary(1024) target.
	// precheckOldNoteDDL 模拟 <=3.7.0 的 note 表：utf8mb4 varchar 路径列与
	// 前缀索引。varchar(1024) 最多存 1024 个字符（4096 字节），因此存量行
	// 可能超过 v3.8.0 的 varbinary(1024) 上限。
	precheckOldNoteDDL = "CREATE TABLE note (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, vault_id bigint NOT NULL DEFAULT 0, path varchar(1024) NOT NULL DEFAULT '', path_hash varchar(1024) NOT NULL DEFAULT '', KEY idx_vault_id_path (vault_id, path(255)), KEY idx_vault_id_path_hash (vault_id, path_hash(255))) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	// precheckOldVaultDDL mirrors a <=3.7.0 vault table: utf8mb4 varchar(255)
	// can hold 255 characters (1020 bytes), above varbinary(255).
	// precheckOldVaultDDL 模拟 <=3.7.0 的 vault 表：utf8mb4 varchar(255) 最多
	// 存 255 个字符（1020 字节），超过 varbinary(255) 上限。
	precheckOldVaultDDL = "CREATE TABLE vault (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, vault varchar(255) NOT NULL DEFAULT '') ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
)

func openPrecheckMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_MYSQL_DSN not set")
	}
	db, err := gorm.Open(gormMysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db
}

func dropPrecheckTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []string{"note", "file", "folder", "setting", "note_link", "note_history", "vault"} {
		require.NoError(t, db.Exec("DROP TABLE IF EXISTS `"+table+"`").Error)
	}
}

func mysqlColumnType(t *testing.T, db *gorm.DB, table, column string) (string, int64) {
	t.Helper()
	var dataType string
	var octetLength int64
	require.NoError(t, db.Raw(`SELECT data_type, COALESCE(character_octet_length, 0)
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
		table, column).Row().Scan(&dataType, &octetLength))
	return dataType, octetLength
}

// TestMySQLPrecheckBlocksOversizedNotePath verifies that a <=3.7.0 note row
// whose utf8mb4 path exceeds 1024 bytes aborts the upgrade before any ALTER.
// TestMySQLPrecheckBlocksOversizedNotePath 验证 <=3.7.0 的 note 行中 utf8mb4
// path 超过 1024 字节时，升级在任何 ALTER 之前终止。
func TestMySQLPrecheckBlocksOversizedNotePath(t *testing.T) {
	db := openPrecheckMySQL(t)
	dropPrecheckTables(t, db)
	t.Cleanup(func() { dropPrecheckTables(t, db) })
	require.NoError(t, db.Exec(precheckOldNoteDDL).Error)

	longPath := strings.Repeat("路", 400) // 400 characters = 1200 UTF-8 bytes
	require.NoError(t, db.Exec("INSERT INTO note(vault_id, path, path_hash) VALUES (1, ?, ?)", longPath, "hash").Error)

	err := AutoMigrate(db, "Note")
	require.Error(t, err)
	msg := err.Error()
	t.Logf("precheck error: %s", msg)
	require.Contains(t, msg, "mysql upgrade precheck failed")
	require.Contains(t, msg, "note.path")
	require.Contains(t, msg, "1200")
	require.Contains(t, msg, "sample id=1")

	// The failed migration must not leave a half-applied ALTER behind.
	// 失败的迁移不得留下半途而废的 ALTER。
	dataType, octetLength := mysqlColumnType(t, db, "note", "path")
	require.Equal(t, "varchar", dataType)
	require.EqualValues(t, 4096, octetLength)
	var contentColumns int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'note' AND column_name = 'content'`).Scan(&contentColumns).Error)
	require.Zero(t, contentColumns, "AutoMigrate must stop before adding columns")
	require.Equal(t, "255", mysqlIndexSubParts(t, db, "note")["idx_vault_id_path:path"])
}

// TestMySQLPrecheckBlocksOversizedVaultName verifies the vault.vault column:
// 100 CJK characters are 300 UTF-8 bytes, legal in varchar(255) utf8mb4 but too
// long for varbinary(255).
// TestMySQLPrecheckBlocksOversizedVaultName 验证 vault.vault：100 个中文字符
// 为 300 字节，在 varchar(255) utf8mb4 中合法，但超过 varbinary(255)。
func TestMySQLPrecheckBlocksOversizedVaultName(t *testing.T) {
	db := openPrecheckMySQL(t)
	dropPrecheckTables(t, db)
	t.Cleanup(func() { dropPrecheckTables(t, db) })
	require.NoError(t, db.Exec(precheckOldVaultDDL).Error)

	longVault := strings.Repeat("库", 100) // 100 characters = 300 UTF-8 bytes
	require.NoError(t, db.Exec("INSERT INTO vault(vault) VALUES (?)", longVault).Error)

	err := AutoMigrate(db, "Vault")
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "mysql upgrade precheck failed")
	require.Contains(t, msg, "vault.vault")
	require.Contains(t, msg, "300")
	require.Contains(t, msg, "sample id=1")

	dataType, octetLength := mysqlColumnType(t, db, "vault", "vault")
	require.Equal(t, "varchar", dataType)
	require.EqualValues(t, 1020, octetLength)
}

// TestMySQLPrecheckAllowsUpgradeAfterShortening verifies that once oversized
// rows fit the binary target, the same AutoMigrate call converts the columns.
// TestMySQLPrecheckAllowsUpgradeAfterShortening 验证超长行缩短到二进制目标
// 长度内后，同一个 AutoMigrate 调用可以完成列转换。
func TestMySQLPrecheckAllowsUpgradeAfterShortening(t *testing.T) {
	db := openPrecheckMySQL(t)
	dropPrecheckTables(t, db)
	t.Cleanup(func() { dropPrecheckTables(t, db) })
	require.NoError(t, db.Exec(precheckOldNoteDDL).Error)
	require.NoError(t, db.Exec(precheckOldVaultDDL).Error)

	require.NoError(t, db.Exec("INSERT INTO note(vault_id, path, path_hash) VALUES (1, ?, ?)", strings.Repeat("路", 400), "hash").Error)
	require.NoError(t, db.Exec("INSERT INTO vault(vault) VALUES (?)", strings.Repeat("库", 100)).Error)
	require.Error(t, AutoMigrate(db, "Note"))
	require.Error(t, AutoMigrate(db, "Vault"))

	require.NoError(t, db.Exec("UPDATE note SET path = ?, path_hash = ? WHERE id = 1", strings.Repeat("路", 300), "hash").Error)
	require.NoError(t, db.Exec("UPDATE vault SET vault = ? WHERE id = 1", strings.Repeat("库", 80)).Error)

	require.NoError(t, AutoMigrate(db, "Vault"))
	require.NoError(t, AutoMigrate(db, "Note"))

	for _, column := range []string{"path", "path_hash"} {
		dataType, octetLength := mysqlColumnType(t, db, "note", column)
		require.Equal(t, "varbinary", dataType, "note."+column)
		require.EqualValues(t, 1024, octetLength, "note."+column)
	}
	vaultType, vaultLength := mysqlColumnType(t, db, "vault", "vault")
	require.Equal(t, "varbinary", vaultType)
	require.EqualValues(t, 255, vaultLength)
}

// TestMySQLPrecheckIsNoopAfterMigration verifies the fast path: an
// already-migrated table triggers no business-table scan on the next start.
// TestMySQLPrecheckIsNoopAfterMigration 验证快速路径：已迁移完成的表在下次
// 启动时不会触发业务表扫描。
func TestMySQLPrecheckIsNoopAfterMigration(t *testing.T) {
	db := openPrecheckMySQL(t)
	dropPrecheckTables(t, db)
	t.Cleanup(func() { dropPrecheckTables(t, db) })
	require.NoError(t, db.Exec(precheckOldNoteDDL).Error)
	require.NoError(t, db.Exec("INSERT INTO note(vault_id, path, path_hash) VALUES (1, 'short', 'hash')").Error)
	require.NoError(t, AutoMigrate(db, "Note"))

	capture := &captureLogger{}
	db.Config.Logger = capture
	require.NoError(t, AutoMigrate(db, "Note"))
	for _, stmt := range capture.stmts {
		require.NotContains(t, stmt, "OCTET_LENGTH", "stable startup must not scan business tables: %s", stmt)
	}
	for _, column := range []string{"path", "path_hash"} {
		dataType, _ := mysqlColumnType(t, db, "note", column)
		require.Equal(t, "varbinary", dataType, "note."+column)
	}
}
