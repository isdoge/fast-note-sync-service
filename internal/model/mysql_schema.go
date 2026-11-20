package model

import (
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// mysqlModel returns a generated struct with the original fields and migration
// tags adjusted only for MySQL/MariaDB. The table name is supplied separately.
// mysqlModel 返回保留原字段、仅调整 MySQL/MariaDB 迁移标签的动态结构体。
// 表名单独提供。
func newMySQLModel(value any) (any, string, error) {
	typ := reflect.TypeOf(value)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, "", fmt.Errorf("expected model struct, got %T", value)
	}
	fields := make([]reflect.StructField, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		parts := strings.Split(field.Tag.Get("gorm"), ";")
		isText := false
		for j, part := range parts {
			lower := strings.ToLower(part)
			if lower == "type:text" || lower == "type:longtext" || lower == "type:mediumtext" {
				isText = true
			}
			if strings.HasPrefix(lower, "mysqltype:") {
				parts[j] = "type:" + part[len("mysqlType:"):]
			}
		}
		if isText {
			filtered := parts[:0]
			for _, part := range parts {
				if !strings.HasPrefix(strings.ToLower(part), "default:") {
					filtered = append(filtered, part)
				}
			}
			parts = filtered
		}
		field.Tag = reflect.StructTag(`gorm:"` + strings.Join(parts, ";") + `" json:"` + field.Tag.Get("json") + `" form:"` + field.Tag.Get("form") + `"`)
		field.Anonymous = false
		fields = append(fields, field)
	}
	table := ""
	if tabler, ok := value.(interface{ TableName() string }); ok {
		table = tabler.TableName()
	} else if tabler, ok := reflect.New(typ).Interface().(interface{ TableName() string }); ok {
		table = tabler.TableName()
	}
	return reflect.New(reflect.StructOf(fields)).Interface(), table, nil
}

// mysqlMigrator makes ALTER use the same custom types as CREATE. Stock MySQL
// AlterColumn uses only Go's generic types and would undo a varbinary path.
//
// These overrides are reached through GORM's dynamic dispatch, not through Go
// embedding: AutoMigrate calls execTx.Migrator().MigrateColumn, and the base
// MigrateColumn calls m.DB.Migrator().FullDataTypeOf / AlterColumn. Because the
// session's Dialector is mysqlDialector, all three resolve to this wrapper.
// TestMySQLMigratorOverridesDispatch pins that path down on a real server.
// mysqlMigrator 让 ALTER 与 CREATE 使用相同的自定义类型。标准 MySQL
// AlterColumn 只按 Go 通用类型生成 SQL，会把 varbinary 路径改回 longtext。
//
// 这些覆盖由 GORM 的动态派发调用，而不是 Go 嵌入：AutoMigrate 调用
// execTx.Migrator().MigrateColumn，基类 MigrateColumn 又调用
// m.DB.Migrator().FullDataTypeOf / AlterColumn；会话的 Dialector 是
// mysqlDialector，因此三者都会落到本包装器。真实服务器上的调用路径由
// TestMySQLMigratorOverridesDispatch 固定。
type mysqlMigrator struct {
	gorm.Migrator
	db    *gorm.DB
	table string
}

func (m mysqlMigrator) BuildIndexOptions(fields []schema.IndexOption, stmt *gorm.Statement) []interface{} {
	if builder, ok := m.Migrator.(interface {
		BuildIndexOptions([]schema.IndexOption, *gorm.Statement) []interface{}
	}); ok {
		return builder.BuildIndexOptions(fields, stmt)
	}
	return nil
}

func (m mysqlMigrator) FullDataTypeOf(field *schema.Field) clause.Expr {
	if strings.EqualFold(string(field.DataType), "text") {
		clone := *field
		clone.HasDefaultValue = false
		clone.DefaultValue = ""
		clone.DefaultValueInterface = nil
		return m.Migrator.FullDataTypeOf(&clone)
	}
	if strings.HasPrefix(string(field.DataType), "varbinary(") || strings.HasPrefix(string(field.DataType), "varchar(") {
		expr := m.Migrator.FullDataTypeOf(field)
		// MySQL's interpolation escapes placeholders inside clause.Expr. Keep the
		// type expression self-contained; defaults here are fixed SQL literals
		// from the migration tags, not user input.
		// MySQL 插值会转义 clause.Expr 内的占位符。这里的默认值是迁移标签中的
		// 固定 SQL 字面量而非用户输入，因此让类型表达式自包含。
		var b strings.Builder
		b.WriteString(expr.SQL)
		for _, v := range expr.Vars {
			b.WriteString(fmt.Sprint(v))
		}
		expr.SQL = b.String()
		expr.Vars = nil
		expr.WithoutParentheses = true
		return expr
	}
	return m.Migrator.FullDataTypeOf(field)
}

func (m mysqlMigrator) MigrateColumn(value any, field *schema.Field, column gorm.ColumnType) error {
	return m.Migrator.MigrateColumn(value, field, mysqlTextDefaultColumn{base: column, field: field})
}

type mysqlTextDefaultColumn struct {
	base  gorm.ColumnType
	field *schema.Field
}

func (c mysqlTextDefaultColumn) Name() string                      { return c.base.Name() }
func (c mysqlTextDefaultColumn) DatabaseTypeName() string          { return c.base.DatabaseTypeName() }
func (c mysqlTextDefaultColumn) ColumnType() (string, bool)        { return c.base.ColumnType() }
func (c mysqlTextDefaultColumn) PrimaryKey() (bool, bool)          { return c.base.PrimaryKey() }
func (c mysqlTextDefaultColumn) AutoIncrement() (bool, bool)       { return c.base.AutoIncrement() }
func (c mysqlTextDefaultColumn) Length() (int64, bool)             { return c.base.Length() }
func (c mysqlTextDefaultColumn) DecimalSize() (int64, int64, bool) { return c.base.DecimalSize() }
func (c mysqlTextDefaultColumn) Nullable() (bool, bool)            { return c.base.Nullable() }
func (c mysqlTextDefaultColumn) Unique() (bool, bool)              { return c.base.Unique() }
func (c mysqlTextDefaultColumn) ScanType() reflect.Type            { return c.base.ScanType() }
func (c mysqlTextDefaultColumn) Comment() (string, bool)           { return c.base.Comment() }
func (c mysqlTextDefaultColumn) DefaultValue() (string, bool) {
	if c.field.HasDefaultValue && strings.EqualFold(string(c.field.DataType), "text") {
		return c.field.DefaultValue, true
	}
	return c.base.DefaultValue()
}

func (m mysqlMigrator) AlterColumn(value any, name string) error {
	tx := m.db.Session(&gorm.Session{NewDB: true})
	if err := tx.Statement.Parse(value); err != nil {
		return err
	}
	field := tx.Statement.Schema.LookUpField(name)
	if field == nil {
		return fmt.Errorf("failed to look up field with name: %s", name)
	}
	return tx.Exec("ALTER TABLE ? MODIFY COLUMN ? ?",
		clause.Table{Name: m.table}, clause.Column{Name: field.DBName}, m.FullDataTypeOf(field)).Error
}

func autoMigrateModel(db *gorm.DB, value any) error {
	if db.Dialector.Name() != "mysql" {
		return db.AutoMigrate(value)
	}
	model, table, err := newMySQLModel(value)
	if err != nil {
		return fmt.Errorf("prepare MySQL migration schema: %w", err)
	}
	tx := db.Session(&gorm.Session{})
	tx.Dialector = mysqlDialector{Dialector: db.Dialector, db: db, table: table}
	if err := tx.Table(table).AutoMigrate(model); err != nil {
		return fmt.Errorf("MySQL AutoMigrate: %w", err)
	}
	return nil
}

type mysqlDialector struct {
	gorm.Dialector
	db    *gorm.DB
	table string
}

func (d mysqlDialector) Migrator(tx *gorm.DB) gorm.Migrator {
	return mysqlMigrator{Migrator: d.Dialector.Migrator(tx), db: d.db, table: d.table}
}
