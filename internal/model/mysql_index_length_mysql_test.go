package model

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	gormMysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestPathIndexPrefixLengthMySQLUpgrade verifies the #275 upgrade path: the
// generated model tags must emit MySQL prefix indexes, and the MySQL-specific
// pre-migration cleanup must remove an existing over-long full-column index
// before AutoMigrate widens the column.
// TestPathIndexPrefixLengthMySQLUpgrade 验证 #275 升级路径：生成的模型标签
// 必须生成 MySQL 前缀索引，而 MySQL 专用预迁移清理必须在 AutoMigrate 扩列前
// 删除已有的超长全列索引。
func TestPathIndexPrefixLengthMySQLUpgrade(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_MYSQL_DSN not set")
	}

	db, err := gorm.Open(gormMysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	require.NoError(t, db.Exec("DROP TABLE IF EXISTS note").Error)
	require.NoError(t, db.Exec("CREATE TABLE note (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, vault_id bigint NOT NULL DEFAULT 0, path varchar(1024) NOT NULL DEFAULT '', path_hash varchar(1024) NOT NULL DEFAULT '', KEY idx_vault_id_path (vault_id, path(255)), KEY idx_vault_id_path_hash (vault_id, path_hash(255))) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4").Error)

	require.NoError(t, PrepareMySQLPathIndexes(db))
	subPartsBefore := mysqlIndexSubParts(t, db, "note")
	require.Equal(t, "255", subPartsBefore["idx_vault_id_path:path"])
	require.Equal(t, "255", subPartsBefore["idx_vault_id_path_hash:path_hash"])
	require.NoError(t, db.AutoMigrate(&Note{}))

	subParts := mysqlIndexSubParts(t, db, "note")
	require.Equal(t, "255", subParts["idx_vault_id_path:path"])
	require.Equal(t, "255", subParts["idx_vault_id_path_hash:path_hash"])
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS note").Error)
}

// TestPrepareMySQLPathIndexesBeforeAutoMigrate proves that without the
// pre-migration cleanup the current model cannot upgrade a pre-#275 table with
// full-column varchar(255) path indexes: MySQL rejects widening the indexed
// path columns to varchar(1024) with Error 1071. It runs against a real MySQL
// instance when FNS_TEST_MYSQL_DSN is set.
// TestPrepareMySQLPathIndexesBeforeAutoMigrate 证明没有预迁移清理时，当前
// 模型无法升级 #275 前带 varchar(255) 全列 path 索引的表：MySQL 会以
// Error 1071 拒绝把索引列扩为 varchar(1024)。设置 FNS_TEST_MYSQL_DSN 时在
// 真实 MySQL 上执行。
func TestPrepareMySQLPathIndexesBeforeAutoMigrate(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_MYSQL_DSN not set")
	}

	db, err := gorm.Open(gormMysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	require.NoError(t, db.Exec("DROP TABLE IF EXISTS note").Error)
	require.NoError(t, db.Exec("CREATE TABLE note (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, vault_id bigint NOT NULL DEFAULT 0, path varchar(255) NOT NULL DEFAULT '', path_hash varchar(255) NOT NULL DEFAULT '', KEY idx_vault_id_path (vault_id, path), KEY idx_vault_id_path_hash (vault_id, path_hash)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4").Error)

	err = db.AutoMigrate(&Note{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Error 1071")

	require.NoError(t, db.Exec("DROP TABLE note").Error)
	require.NoError(t, db.Exec("CREATE TABLE note (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, vault_id bigint NOT NULL DEFAULT 0, path varchar(255) NOT NULL DEFAULT '', path_hash varchar(255) NOT NULL DEFAULT '', KEY idx_vault_id_path (vault_id, path), KEY idx_vault_id_path_hash (vault_id, path_hash)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4").Error)
	require.NoError(t, PrepareMySQLPathIndexes(db))
	subParts := mysqlIndexSubParts(t, db, "note")
	require.Equal(t, "255", subParts["idx_vault_id_path:path"])
	require.Equal(t, "255", subParts["idx_vault_id_path_hash:path_hash"])
	require.NoError(t, db.AutoMigrate(&Note{}))
}

// TestPrepareMySQLPathIndexesIdempotentAndNullable verifies that only legacy
// full-column indexes are rewritten: the second call executes no ALTER, a
// missing *_path_hash index is harmless, and existing NULL values survive.
// TestPrepareMySQLPathIndexesIdempotentAndNullable 验证只重写旧全列索引：
// 第二次调用不执行 ALTER，缺少 *_path_hash 索引不报错，既有 NULL 值不受影响。
func TestPrepareMySQLPathIndexesIdempotentAndNullable(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_MYSQL_DSN not set")
	}
	capture := &captureLogger{}
	db, err := gorm.Open(gormMysql.Open(dsn), &gorm.Config{Logger: capture})
	require.NoError(t, err)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS note").Error)
	require.NoError(t, db.Exec("CREATE TABLE note (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, vault_id bigint NOT NULL DEFAULT 0, path varchar(255) DEFAULT NULL, path_hash varchar(255) DEFAULT NULL, KEY idx_vault_id_path (vault_id, path)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4").Error)
	require.NoError(t, db.Exec("INSERT INTO note(vault_id, path, path_hash) VALUES (1, NULL, NULL)").Error)

	require.NoError(t, PrepareMySQLPathIndexes(db))
	require.NoError(t, db.AutoMigrate(&Note{}))
	capture.stmts = nil
	require.NoError(t, PrepareMySQLPathIndexes(db))
	for _, stmt := range capture.stmts {
		require.NotContains(t, stmt, "ALTER TABLE", "second preparation must be read-only: %s", stmt)
	}
	var pathIsNull bool
	require.NoError(t, db.Raw("SELECT path IS NULL FROM note WHERE id = 1").Scan(&pathIsNull).Error)
	require.True(t, pathIsNull)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS note").Error)
}

func mysqlIndexSubParts(t *testing.T, db *gorm.DB, table string) map[string]string {
	t.Helper()
	rows, err := db.Raw("SHOW INDEX FROM " + table).Rows()
	require.NoError(t, err)
	defer rows.Close()

	subParts := map[string]string{}
	for rows.Next() {
		cols, err2 := rows.Columns()
		require.NoError(t, err2)
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		row := map[string]any{}
		for i, col := range cols {
			row[col] = values[i]
		}
		keyName := mysqlValueString(row["Key_name"])
		colName := mysqlValueString(row["Column_name"])
		subParts[keyName+":"+colName] = mysqlValueString(row["Sub_part"])
	}
	require.NoError(t, rows.Err())
	return subParts
}

func mysqlValueString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return fmt.Sprint(x)
	}
}
