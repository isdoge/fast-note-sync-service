package upgrade

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// secondsToMillisThreshold 与写入入口 timex.NormalizeMilli 使用同一阈值：1e11 毫秒对应
// 1973-03-03，早于本项目的存在时间，因此 (0, 1e11) 区间内的存量值只可能是秒级脏数据。
const secondsToMillisThreshold = int64(100_000_000_000)

// TimestampSecondsToMillisMigrate 把 note / file / folder 表里的秒级 ctime / mtime
// 存量脏数据乘以 1000 归一化为毫秒（#461）。
//
// 安全性论证：
//   - (0, 1e11) 区间对应 1973-03-03 之前，本项目不可能有合法毫秒值落在其中，
//     该区间内的值一定是第三方客户端误传的秒级时间戳。
//   - 归一化规则与写入入口 timex.NormalizeMilli 完全一致：同一客户端再次以秒写入时，
//     入口归一化后的值与迁移后的存量值相等，新旧比较结果相等，不会触发无谓的回写。
//   - 迁移前这类记录在新旧比较中永远偏旧（客户端永远"更新"），只造成多余上行流量；
//     迁移后比较恢复准确，不存在把新数据改旧的方向性风险。
//   - updated_timestamp 等服务端自产毫秒列不在处理范围内；迁移通过 schema_version
//     只执行一次。
//
// 迁移版本取引入它的发布版本 "3.7.0"。升级管理器按“标识大于上次运行的版本”决定是否执行，
// 并靠 schema_version 的唯一记录保证只跑一次：从 3.6.x 升级到 3.7.0 时执行，之后不再执行。
// 标识必须在所有迁移里唯一（3.6.2 为标签回填），也不能占用尚未发布的后续版本号。
type TimestampSecondsToMillisMigrate struct{}

// Version returns the migration version
// Version 返回升级版本号
func (m *TimestampSecondsToMillisMigrate) Version() string {
	return "3.7.0"
}

// Description returns the migration description
// Description 返回升级描述
func (m *TimestampSecondsToMillisMigrate) Description() string {
	return "Normalize legacy second-scale ctime/mtime in note/file/folder tables to milliseconds (#461)"
}

// timestampMigrationTables maps each user-DB repo prefix key to the table migrated in that
// database. SQLite keeps one file per key; MySQL/PostgreSQL route all keys to the same
// user_<uid> database/schema, where the per-table HasTable guard keeps updates targeted.
// timestampMigrationTables 把各用户库 repo 前缀键映射到该库中要迁移的表。SQLite 每个键一个
// 文件；MySQL/PostgreSQL 下所有键路由到同一个 user_<uid> 库/schema，由逐表 HasTable 判断
// 保证更新只落到目标表。
var timestampMigrationTables = []struct {
	keyPrefix string
	table     string
}{
	{"user_", "note"},
	{"user_file_", "file"},
	{"user_folder_", "folder"},
}

// Up runs the migration
// Up 执行升级操作
func (m *TimestampSecondsToMillisMigrate) Up(db *gorm.DB, ctx context.Context, mc *MigrationContext) error {
	if mc.Dao == nil {
		return fmt.Errorf("dao is nil in migration context")
	}

	uids, err := mc.Dao.GetAllUserUIDs()
	if err != nil {
		return fmt.Errorf("failed to get all user UIDs: %w", err)
	}

	for _, uid := range uids {
		for _, target := range timestampMigrationTables {
			userDB := mc.Dao.ResolveDB(target.keyPrefix + fmt.Sprintf("%d", uid))
			if userDB == nil {
				mc.Logger.Warn("user db connection is nil, skipping",
					zap.Int64("uid", uid), zap.String("table", target.table))
				continue
			}

			if err := userDB.Transaction(func(tx *gorm.DB) error {
				if !tx.Migrator().HasTable(target.table) {
					return nil
				}
				for _, column := range []string{"ctime", "mtime"} {
					err := tx.WithContext(ctx).Table(target.table).
						Where(column+" > 0 AND "+column+" < ?", secondsToMillisThreshold).
						Update(column, gorm.Expr(column+" * 1000")).Error
					if err != nil {
						return fmt.Errorf("failed to normalize %s.%s to milliseconds for uid %d: %w", target.table, column, uid, err)
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}

	return nil
}
