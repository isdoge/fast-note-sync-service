package model

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// mysqlPathIndexSpec describes one MySQL index containing a path column.
// mysqlPathIndexSpec 描述一个包含 path 列的 MySQL 索引。
type mysqlPathIndexSpec struct {
	table   string
	index   string
	column  string
	columns []string
}

var mysqlPathIndexSpecs = []mysqlPathIndexSpec{
	{table: "note", index: "idx_vault_id_path", column: "path", columns: []string{"vault_id", "path(255)"}},
	{table: "note", index: "idx_vault_id_path_hash", column: "path_hash", columns: []string{"vault_id", "path_hash(255)"}},
	{table: "file", index: "idx_file_vault_id_path", column: "path", columns: []string{"vault_id", "path(255)"}},
	{table: "file", index: "idx_file_vault_id_path_hash", column: "path_hash", columns: []string{"vault_id", "path_hash(255)"}},
	{table: "folder", index: "idx_folder_vault_id_level_path", column: "path", columns: []string{"vault_id", "level", "path(255)"}},
	{table: "folder", index: "idx_folder_vault_id_fid_path", column: "path", columns: []string{"vault_id", "fid", "path(255)"}},
	{table: "folder", index: "idx_folder_vault_id_path", column: "path", columns: []string{"vault_id", "path(255)"}},
	{table: "folder", index: "idx_folder_vault_id_path_hash", column: "path_hash", columns: []string{"vault_id", "path_hash(255)"}},
	{table: "setting", index: "idx_setting_id_path", column: "path", columns: []string{"id", "path(255)"}},
	{table: "setting", index: "idx_setting_id_path_hash", column: "path_hash", columns: []string{"id", "path_hash(255)"}},
	{table: "note_link", index: "idx_target_path_hash", column: "target_path_hash", columns: []string{"target_path_hash(255)", "vault_id", "uid"}},
}

// PrepareMySQLPathIndexes replaces only existing full-column path indexes and
// widens their columns in the same ALTER, preserving each column's current
// nullability and default. Once converted, later calls execute no ALTER.
// Missing indexes and NULL values are harmless.
// PrepareMySQLPathIndexes 只替换已存在的 path 全列索引，并在同一条 ALTER 中
// 扩列，同时保留该列当前的可空性和默认值。转换后再次调用不执行 ALTER。
// 索引缺失和既有 NULL 值都不报错。
func PrepareMySQLPathIndexes(db *gorm.DB) error {
	if db == nil || db.Dialector.Name() != "mysql" {
		return nil
	}
	for _, spec := range mysqlPathIndexSpecs {
		needed, err := mysqlFullPathIndexExists(db, spec)
		if err != nil {
			return err
		}
		if !needed {
			continue
		}
		def, err := mysqlColumnDefinition(db, spec.table, spec.column)
		if err != nil {
			return err
		}
		sql := fmt.Sprintf("ALTER TABLE `%s` MODIFY COLUMN `%s` varchar(1024)%s, DROP INDEX `%s`, ADD INDEX `%s` (%s)",
			spec.table, spec.column, def, spec.index, spec.index, strings.Join(spec.columns, ", "))
		if err := db.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}

func mysqlFullPathIndexExists(db *gorm.DB, spec mysqlPathIndexSpec) (bool, error) {
	var count int64
	err := db.Raw(`SELECT COUNT(*) FROM information_schema.statistics
		WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?
		AND column_name = ? AND sub_part IS NULL`, spec.table, spec.index, spec.column).Scan(&count).Error
	return count > 0, err
}

func mysqlColumnDefinition(db *gorm.DB, table, column string) (string, error) {
	var nullable string
	var hasDefault bool
	var defaultValue string
	err := db.Raw(`SELECT is_nullable, column_default IS NOT NULL, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).
		Row().Scan(&nullable, &hasDefault, &defaultValue)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if strings.EqualFold(nullable, "NO") {
		b.WriteString(" NOT NULL")
	}
	if hasDefault {
		b.WriteString(" DEFAULT ")
		b.WriteString(mysqlQuoteDefault(defaultValue))
	}
	return b.String(), nil
}

func mysqlQuoteDefault(value string) string {
	if strings.EqualFold(value, "NULL") || strings.Contains(value, "(") || strings.Contains(value, ")") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
