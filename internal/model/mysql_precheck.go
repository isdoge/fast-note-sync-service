package model

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// mysqlVarbinaryColumnSpec describes one column that v3.8.0+ stores as a
// case-sensitive binary type on MySQL/MariaDB. Older releases stored these
// columns as utf8mb4 varchar/text, which can legally hold values longer than
// the binary target and would make ALTER TABLE fail halfway through an upgrade.
// mysqlVarbinaryColumnSpec 描述 v3.8.0+ 在 MySQL/MariaDB 上改为区分大小写
// 二进制类型的列。旧版本以 utf8mb4 varchar/text 存储这些列，可以合法容纳
// 超过二进制目标长度的值，直接升级会让 ALTER TABLE 在中途失败。
type mysqlVarbinaryColumnSpec struct {
	table    string
	column   string
	maxBytes int
}

// mysqlVarbinaryColumnSpecs lists every mysqlType:varbinary column in
// internal/model/*.gen.go. Keep it in sync with those tags.
// mysqlVarbinaryColumnSpecs 列出 internal/model/*.gen.go 中所有
// mysqlType:varbinary 列，需与这些标签保持一致。
var mysqlVarbinaryColumnSpecs = []mysqlVarbinaryColumnSpec{
	{table: "note", column: "path", maxBytes: 1024},
	{table: "note", column: "path_hash", maxBytes: 1024},
	{table: "file", column: "path", maxBytes: 1024},
	{table: "file", column: "path_hash", maxBytes: 1024},
	{table: "folder", column: "path", maxBytes: 1024},
	{table: "folder", column: "path_hash", maxBytes: 1024},
	{table: "setting", column: "path", maxBytes: 1024},
	{table: "setting", column: "path_hash", maxBytes: 1024},
	{table: "note_history", column: "path", maxBytes: 1024},
	{table: "note_link", column: "target_path", maxBytes: 1024},
	{table: "note_link", column: "target_path_hash", maxBytes: 1024},
	{table: "vault", column: "vault", maxBytes: 255},
}

// mysqlVarbinaryModelTables maps AutoMigrate keys to the tables above.
// mysqlVarbinaryModelTables 把 AutoMigrate 的 key 映射到上述表。
var mysqlVarbinaryModelTables = map[string]string{
	"Note":        "note",
	"File":        "file",
	"Folder":      "folder",
	"Setting":     "setting",
	"NoteHistory": "note_history",
	"NoteLink":    "note_link",
	"Vault":       "vault",
}

// mysqlPrecheckSweepTables are the tables PrepareMySQLPathIndexes may ALTER in
// a single call. When any of them is migrated, every one of them must pass the
// precheck before the first ALTER runs.
// mysqlPrecheckSweepTables 是 PrepareMySQLPathIndexes 一次可能修改的全部表。
// 迁移其中任一表时，所有表都必须先通过预检，才能执行第一条 ALTER。
var mysqlPrecheckSweepTables = map[string]bool{
	"note":      true,
	"file":      true,
	"folder":    true,
	"setting":   true,
	"note_link": true,
}

// PrecheckMySQLVarbinaryColumns refuses an upgrade that would truncate existing
// path or vault values when MySQL/MariaDB columns are widened into varbinary.
// It is a no-op outside MySQL and for models without binary columns. For an
// already-migrated table it only reads information_schema, so normal startup
// never scans business tables.
// PrecheckMySQLVarbinaryColumns 在 MySQL/MariaDB 把列收窄为 varbinary 时，
// 拒绝会截断存量路径或库名的升级。非 MySQL 与不含二进制列的模型直接跳过；
// 已迁移完成的表只读 information_schema，日常启动不会扫业务表。
func PrecheckMySQLVarbinaryColumns(db *gorm.DB, key string) error {
	if db == nil || db.Dialector.Name() != "mysql" {
		return nil
	}
	table, ok := mysqlVarbinaryModelTables[key]
	if !ok {
		return nil
	}
	var schema string
	if err := db.Raw("SELECT DATABASE()").Row().Scan(&schema); err != nil {
		return fmt.Errorf("mysql upgrade precheck: read current schema: %w", err)
	}
	for _, spec := range mysqlVarbinaryPrecheckSpecs(table) {
		if err := precheckMySQLVarbinaryColumn(db, schema, spec); err != nil {
			return err
		}
	}
	return nil
}

// mysqlVarbinaryPrecheckSpecs returns the columns that the current AutoMigrate
// call can ALTER: the current model's own columns plus, for the five
// path-index tables, every table PrepareMySQLPathIndexes sweeps.
// mysqlVarbinaryPrecheckSpecs 返回本次 AutoMigrate 可能修改的列：当前模型
// 自身的列，以及 path 索引涉及的 5 张表的全部列。
func mysqlVarbinaryPrecheckSpecs(table string) []mysqlVarbinaryColumnSpec {
	sweep := mysqlPrecheckSweepTables[table]
	specs := make([]mysqlVarbinaryColumnSpec, 0, len(mysqlVarbinaryColumnSpecs))
	for _, spec := range mysqlVarbinaryColumnSpecs {
		if spec.table == table || (sweep && mysqlPrecheckSweepTables[spec.table]) {
			specs = append(specs, spec)
		}
	}
	return specs
}

func precheckMySQLVarbinaryColumn(db *gorm.DB, schema string, spec mysqlVarbinaryColumnSpec) error {
	var dataType string
	var octetLength int64
	err := db.Raw(`SELECT data_type, COALESCE(character_octet_length, 0)
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
		spec.table, spec.column).Row().Scan(&dataType, &octetLength)
	if errors.Is(err, sql.ErrNoRows) {
		// A missing table or column is a fresh install or an older schema that
		// never had this column; ALTER cannot truncate anything.
		// 表或列不存在表示全新建表或该列从未存在，ALTER 不会截断任何数据。
		return nil
	}
	if err != nil {
		return fmt.Errorf("mysql upgrade precheck: inspect %s.%s: %w", spec.table, spec.column, err)
	}
	if strings.EqualFold(dataType, "varbinary") && octetLength > 0 && octetLength <= int64(spec.maxBytes) {
		// Already migrated; keep startup free of business-table scans.
		// 已完成迁移，日常启动不扫描业务表。
		return nil
	}

	var count, maxOctetLength int64
	query := fmt.Sprintf("SELECT COUNT(*), COALESCE(MAX(OCTET_LENGTH(`%s`)), 0) FROM `%s` WHERE OCTET_LENGTH(`%s`) > ?",
		spec.column, spec.table, spec.column)
	if err := db.Raw(query, spec.maxBytes).Row().Scan(&count, &maxOctetLength); err != nil {
		return fmt.Errorf("mysql upgrade precheck: scan %s.%s: %w", spec.table, spec.column, err)
	}
	if count == 0 {
		return nil
	}

	sampleID, preview := mysqlOversizedSample(db, spec)
	return fmt.Errorf("mysql upgrade precheck failed: %s.%s.%s has %d row(s) exceeding %d bytes (max=%d bytes, sample id=%s preview=%q); please shorten or remove oversized paths before upgrading to v3.8.0+",
		schema, spec.table, spec.column, count, spec.maxBytes, maxOctetLength, sampleID, preview)
}

// mysqlOversizedSample returns a best-effort primary key and readable preview
// for the first oversized row. Diagnostic failures must not hide the precheck
// error itself.
// mysqlOversizedSample 尽力返回首条超长行的主键与可读预览。诊断查询失败
// 不得掩盖预检本身的错误。
func mysqlOversizedSample(db *gorm.DB, spec mysqlVarbinaryColumnSpec) (string, string) {
	idColumn := "NULL"
	var hasID int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'id'`, spec.table).Scan(&hasID).Error; err == nil && hasID > 0 {
		idColumn = "`id`"
	}
	query := fmt.Sprintf("SELECT %s, LEFT(CAST(`%s` AS CHAR), 80) FROM `%s` WHERE OCTET_LENGTH(`%s`) > ? LIMIT 1",
		idColumn, spec.column, spec.table, spec.column)
	var id, preview sql.NullString
	if err := db.Raw(query, spec.maxBytes).Row().Scan(&id, &preview); err != nil {
		return "?", ""
	}
	if id.Valid {
		return id.String, preview.String
	}
	return "?", preview.String
}
