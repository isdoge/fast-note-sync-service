package upgrade

import (
	"context"
	"fmt"

	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// NoteTagIndexRebuildMigrate builds the note_tag index for every existing
// note. The table is created empty by AutoMigrate, so without this backfill
// note_list_tags would report nothing until each note happens to be written
// again. Note bodies are read from each note's content.txt (see RebuildForVault).
// The migration version stays "3.6.2" from upstream #509: it only has to be
// unique and greater than the last released version (3.6.1) so it runs once.
//
// NoteTagIndexRebuildMigrate 为既有的每篇笔记构建 note_tag 索引。
// AutoMigrate 只建空表，若不回填，note_list_tags 在每篇笔记被重新写入之前都会返回空。
// 正文从各笔记目录的 content.txt 读取（见 RebuildForVault）。迁移版本沿用上游 #509 的
// "3.6.2"：只需唯一且大于已发布版本 3.6.1，即只执行一次。
type NoteTagIndexRebuildMigrate struct{}

// Version returns the migration version
// Version 返回升级版本号
func (m *NoteTagIndexRebuildMigrate) Version() string {
	return "3.6.2"
}

// Description returns the migration description
// Description 返回升级描述
func (m *NoteTagIndexRebuildMigrate) Description() string {
	return "Backfill the note_tag index from existing note content"
}

// Up runs the migration
// Up 执行升级操作
func (m *NoteTagIndexRebuildMigrate) Up(db *gorm.DB, ctx context.Context, mc *MigrationContext) error {
	if mc.Dao == nil {
		return fmt.Errorf("dao is nil in migration context")
	}

	uids, err := mc.Dao.GetAllUserUIDs()
	if err != nil {
		return fmt.Errorf("failed to get all user UIDs: %w", err)
	}

	repo := dao.NewNoteTagRepository(mc.Dao)
	for _, uid := range uids {
		// note 表在 user_<uid> 库，vault 表在 user_vault_<uid> 库（SQLite 下是两个文件，
		// MySQL/PostgreSQL 下映射到同一 schema），须分别解析。
		// The note table lives in the user_<uid> DB and the vault table in user_vault_<uid>
		// (separate files on SQLite, same schema on MySQL/PostgreSQL); resolve each separately.
		userDB := mc.Dao.ResolveDB(fmt.Sprintf("user_%d", uid))
		vaultDB := mc.Dao.ResolveDB(fmt.Sprintf("user_vault_%d", uid))
		if userDB == nil || vaultDB == nil {
			mc.Logger.Warn("user db connection is nil, skipping", zap.Int64("uid", uid))
			continue
		}
		if !vaultDB.Migrator().HasTable("vault") || !userDB.Migrator().HasTable("note") {
			continue
		}

		var vaultIDs []int64
		if err := vaultDB.WithContext(ctx).Table("vault").
			Where("is_deleted = ?", false).
			Pluck("id", &vaultIDs).Error; err != nil {
			return fmt.Errorf("failed to list vaults for uid %d: %w", uid, err)
		}

		for _, vaultID := range vaultIDs {
			written, err := repo.RebuildForVault(ctx, vaultID, uid)
			if err != nil {
				return fmt.Errorf("failed to rebuild tag index for uid %d vault %d: %w", uid, vaultID, err)
			}
			mc.Logger.Info("note tag index rebuilt",
				zap.Int64("uid", uid),
				zap.Int64("vaultId", vaultID),
				zap.Int("tags", written))
		}
	}

	return nil
}
