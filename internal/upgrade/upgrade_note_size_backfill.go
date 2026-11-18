package upgrade

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// NoteSizeBackfillMigrate backfills size=0 rows in the note table. The size column
// (introduced in 7e085b4e, 2025-03) is written on every content change, but rows created
// before the column existed — and rows whose Size was lost by the pre-#511 rename bug —
// keep size=0 forever, because ModifyOrCreate's idempotent short-circuit and mtime-only
// branch never rewrite Size. MCP note_list reports these notes as "Size: 0" while
// note_get reads the full body from content.txt (#360). The actual byte length is read
// from each note's content.txt (the note.content column is empty after lazy migration).
// Version "3.6.3": unique and greater than the last released version, so it runs once;
// "3.6.2" is taken by the tag-index backfill and "3.7.0" by the mtime migration on
// another branch.
//
// NoteSizeBackfillMigrate 回填 note 表中 size=0 的行。size 列（7e085b4e，2025-03 引入）
// 在每次内容变更时写入，但列存在之前创建的行、以及 #511 修复前被重命名 bug 丢掉 Size 的行，
// 会永远停在 size=0——ModifyOrCreate 的幂等短路和 mtime-only 分支都不重写 Size。
// 这些笔记在 MCP note_list 中显示 "Size: 0"，而 note_get 从 content.txt 读到完整正文（#360）。
// 实际字节数从各笔记目录的 content.txt 读取（懒迁移后 note.content 列为空）。
// 版本 "3.6.3"：唯一且大于已发布版本，只执行一次；"3.6.2" 已被标签索引回填占用，
// "3.7.0" 已分配给另一分支的时间戳迁移。
type NoteSizeBackfillMigrate struct{}

// Version returns the migration version
// Version 返回升级版本号
func (m *NoteSizeBackfillMigrate) Version() string {
	return "3.6.3"
}

// Description returns the migration description
// Description 返回升级描述
func (m *NoteSizeBackfillMigrate) Description() string {
	return "Backfill note size from content.txt for rows stuck at size=0"
}

// Up runs the migration
// Up 执行升级操作
func (m *NoteSizeBackfillMigrate) Up(db *gorm.DB, ctx context.Context, mc *MigrationContext) error {
	if mc.Dao == nil {
		return fmt.Errorf("dao is nil in migration context")
	}

	uids, err := mc.Dao.GetAllUserUIDs()
	if err != nil {
		return fmt.Errorf("failed to get all user UIDs: %w", err)
	}

	for _, uid := range uids {
		// note 表在 user_<uid> 库（SQLite 下独立文件，MySQL/PostgreSQL 下同一 schema）
		// The note table lives in the user_<uid> DB (separate file on SQLite, same schema on MySQL/PostgreSQL)
		userDB := mc.Dao.ResolveDB(fmt.Sprintf("user_%d", uid))
		if userDB == nil {
			mc.Logger.Warn("user db connection is nil, skipping", zap.Int64("uid", uid))
			continue
		}
		if !userDB.Migrator().HasTable("note") {
			continue
		}

		var ids []int64
		if err := userDB.WithContext(ctx).Table("note").
			Where("size = ?", 0).
			Pluck("id", &ids).Error; err != nil {
			return fmt.Errorf("failed to list size=0 notes for uid %d: %w", uid, err)
		}
		if len(ids) == 0 {
			continue
		}

		fixed := 0
		for _, id := range ids {
			folder := mc.Dao.GetNoteFolderPath(uid, id)
			content, exists, err := mc.Dao.LoadContentFromFile(folder, "content.txt")
			if err != nil {
				mc.Logger.Warn("NoteSizeBackfill: read content.txt failed, skip note",
					zap.Int64("uid", uid),
					zap.Int64("noteId", id),
					zap.Error(err))
				continue
			}
			if !exists {
				// 无正文文件（如已物理删除但行残留）：跳过，不写无意义的 size
				// No content file (e.g. physically deleted leftover row): skip
				continue
			}
			if err := userDB.WithContext(ctx).Table("note").
				Where("id = ?", id).
				Update("size", int64(len(content))).Error; err != nil {
				return fmt.Errorf("failed to update size for note %d (uid %d): %w", id, uid, err)
			}
			fixed++
		}

		if fixed > 0 {
			mc.Logger.Info("note size backfilled",
				zap.Int64("uid", uid),
				zap.Int("notes", fixed))
		}
	}

	return nil
}
