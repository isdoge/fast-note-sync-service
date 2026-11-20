package model

import (
	"fmt"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"gorm.io/gorm"
)

// revisionTables are the four sync resources that persist a 128-bit revision token.
// revisionTables 是持久化 128 位版本令牌的四类同步资源表。
var revisionTables = []string{TableNameNote, TableNameFile, TableNameSetting, TableNameFolder}

// revisionBackfillBatch bounds how many rows are rewritten in one statement.
// revisionBackfillBatch 限制单条语句回写的行数。
const revisionBackfillBatch = 200

// BackfillRevisionTokens fills revision=” rows of one resource table with fresh
// 128-bit random tokens. It runs after AutoMigrate so an upgraded deployment always
// exposes a non-empty revision for every live row and every tombstone, including
// rows written by older versions.
//
// The implementation only uses portable SQL (SELECT id ... LIMIT, then one
// UPDATE ... SET revision = CASE id ... END WHERE id IN (...)), so it behaves the
// same on SQLite, PostgreSQL, MySQL and MariaDB; no dialect-private functions are
// used and no DDL is issued.
// BackfillRevisionTokens 为某资源表中 revision 为空的行回填新的 128 位随机令牌。
// 它在 AutoMigrate 之后执行，保证升级后的部署里每一行活跃记录与墓碑都具备非空
// 令牌（包括旧版本写入的行）。
// 实现只使用可移植 SQL（先按 LIMIT 选 id，再用一条 UPDATE ... CASE 回写），
// 在 SQLite / PostgreSQL / MySQL / MariaDB 上行为一致，不使用方言私有函数、不发 DDL。
func BackfillRevisionTokens(db *gorm.DB, table string) error {
	if db == nil || !isRevisionTable(table) {
		return nil
	}

	for {
		var ids []int64
		if err := db.Table(table).
			Where("revision IS NULL OR revision = ?", "").
			Order("id ASC").
			Limit(revisionBackfillBatch).
			Pluck("id", &ids).Error; err != nil {
			return fmt.Errorf("select rows without revision from %s: %w", table, err)
		}
		if len(ids) == 0 {
			return nil
		}
		if err := writeRevisionTokens(db, table, ids); err != nil {
			return err
		}
		if len(ids) < revisionBackfillBatch {
			return nil
		}
	}
}

// writeRevisionTokens rewrites one batch of ids with per-row random tokens.
// writeRevisionTokens 为一批 id 逐行写入新的随机令牌。
func writeRevisionTokens(db *gorm.DB, table string, ids []int64) error {
	var sb strings.Builder
	sb.WriteString("UPDATE ")
	sb.WriteString(table)
	sb.WriteString(" SET revision = CASE id")
	args := make([]any, 0, len(ids)*2+len(ids))
	for _, id := range ids {
		sb.WriteString(" WHEN ? THEN ?")
		args = append(args, id, util.NewRevisionToken())
	}
	sb.WriteString(" ELSE revision END WHERE id IN (")
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("?")
		args = append(args, id)
	}
	sb.WriteString(")")
	if err := db.Exec(sb.String(), args...).Error; err != nil {
		return fmt.Errorf("backfill revision tokens of %s: %w", table, err)
	}
	return nil
}

// isRevisionTable reports whether table is one of the four revision-bearing tables.
// isRevisionTable 判断 table 是否属于四类带版本令牌的资源表。
func isRevisionTable(table string) bool {
	for _, name := range revisionTables {
		if name == table {
			return true
		}
	}
	return false
}

// BackfillAllRevisionTokens backfills every revision-bearing table of a database.
// BackfillAllRevisionTokens 回填一个数据库中所有带版本令牌的资源表。
func BackfillAllRevisionTokens(db *gorm.DB) error {
	for _, table := range revisionTables {
		if err := BackfillRevisionTokens(db, table); err != nil {
			return err
		}
	}
	return nil
}
