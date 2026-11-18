package model

import (
	"gorm.io/gorm"
)

// mysqlPathIndexSpecs lists the pre-#275 indexes that combine a path column
// with a leading integer column. Existing MySQL deployments created these as
// full-column indexes at varchar(255); widening the column to varchar(1024)
// under utf8mb4 would exceed InnoDB's 3072-byte key limit unless the index is
// first replaced by the prefix index declared in the generated model tags.
// mysqlPathIndexSpecs 列出 #275 之前由整型前导列和 path 列组成的索引。既有
// MySQL 部署按 varchar(255) 创建了全列索引；在 utf8mb4 下把列扩成
// varchar(1024) 会超过 InnoDB 3072 字节索引键上限，必须先按生成模型标签
// 声明的前缀索引重建。
var mysqlPathIndexSpecs = []struct {
	Table  string
	Column string
	SQL    string
}{
	{Table: "note", Column: "path", SQL: "ALTER TABLE note MODIFY COLUMN path varchar(1024) NOT NULL DEFAULT '', DROP INDEX idx_vault_id_path, ADD INDEX idx_vault_id_path (vault_id, path(255))"},
	{Table: "note", Column: "path_hash", SQL: "ALTER TABLE note MODIFY COLUMN path_hash varchar(1024) NOT NULL DEFAULT '', DROP INDEX idx_vault_id_path_hash, ADD INDEX idx_vault_id_path_hash (vault_id, path_hash(255))"},
	{Table: "file", Column: "path", SQL: "ALTER TABLE file MODIFY COLUMN path varchar(1024) NOT NULL DEFAULT '', DROP INDEX idx_file_vault_id_path, ADD INDEX idx_file_vault_id_path (vault_id, path(255))"},
	{Table: "file", Column: "path_hash", SQL: "ALTER TABLE file MODIFY COLUMN path_hash varchar(1024) NOT NULL DEFAULT '', DROP INDEX idx_file_vault_id_path_hash, ADD INDEX idx_file_vault_id_path_hash (vault_id, path_hash(255))"},
	{Table: "folder", Column: "path", SQL: "ALTER TABLE folder MODIFY COLUMN path varchar(1024) NOT NULL DEFAULT '', DROP INDEX idx_folder_vault_id_level_path, DROP INDEX idx_folder_vault_id_fid_path, DROP INDEX idx_folder_vault_id_path, ADD INDEX idx_folder_vault_id_level_path (vault_id, level, path(255)), ADD INDEX idx_folder_vault_id_fid_path (vault_id, fid, path(255)), ADD INDEX idx_folder_vault_id_path (vault_id, path(255))"},
	{Table: "folder", Column: "path_hash", SQL: "ALTER TABLE folder MODIFY COLUMN path_hash varchar(1024) NOT NULL DEFAULT '', DROP INDEX idx_folder_vault_id_path_hash, ADD INDEX idx_folder_vault_id_path_hash (vault_id, path_hash(255))"},
	{Table: "setting", Column: "path", SQL: "ALTER TABLE setting MODIFY COLUMN path varchar(1024) NOT NULL DEFAULT '', DROP INDEX idx_setting_id_path, ADD INDEX idx_setting_id_path (id, path(255))"},
	{Table: "setting", Column: "path_hash", SQL: "ALTER TABLE setting MODIFY COLUMN path_hash varchar(1024) NOT NULL DEFAULT '', DROP INDEX idx_setting_id_path_hash, ADD INDEX idx_setting_id_path_hash (id, path_hash(255))"},
	{Table: "note_link", Column: "target_path_hash", SQL: "ALTER TABLE note_link MODIFY COLUMN target_path_hash varchar(1024) NOT NULL DEFAULT '', DROP INDEX idx_target_path_hash, ADD INDEX idx_target_path_hash (target_path_hash(255), vault_id, uid)"},
}

// PrepareMySQLPathIndexes rewrites legacy full-column path indexes as MySQL
// prefix indexes before AutoMigrate widens path columns. It is intentionally
// MySQL-only: SQLite keeps full-length indexes, PostgreSQL has no such key
// limit and its driver ignores GORM index length metadata.
// PrepareMySQLPathIndexes 在 AutoMigrate 扩展 path 列前，把旧的全列 path
// 索引重建为 MySQL 前缀索引。该函数仅用于 MySQL：SQLite 保留完整索引，
// PostgreSQL 没有此键长限制且其驱动会忽略 GORM 索引长度元数据。
func PrepareMySQLPathIndexes(db *gorm.DB) error {
	if db == nil || db.Dialector.Name() != "mysql" {
		return nil
	}
	for _, spec := range mysqlPathIndexSpecs {
		if !mysqlTableExists(db, spec.Table) {
			continue
		}
		// The ALTER is atomic: the column is widened and its legacy full-column
		// indexes are replaced by prefix indexes in the same statement. 单条
		// ALTER 原子完成扩列和用前缀索引替换旧全列索引，避免中间态触发 1071。
		if err := db.Exec(spec.SQL).Error; err != nil {
			return err
		}
	}
	return nil
}

func mysqlTableExists(db *gorm.DB, table string) bool {
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table).Scan(&count).Error; err != nil {
		return false
	}
	return count > 0
}

