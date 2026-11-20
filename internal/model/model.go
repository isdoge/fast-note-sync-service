package model

import (
	"gorm.io/gorm"
)

func AutoMigrate(db *gorm.DB, key string) error {
	if db == nil {
		return nil
	}
	var value any
	switch key {
	case "AuthToken":
		value = AuthToken{}
	case "AuthTokenLog":
		value = AuthTokenLog{}
	case "BackupConfig":
		value = BackupConfig{}
	case "BackupHistory":
		value = BackupHistory{}
	case "File":
		value = File{}
	case "Folder":
		value = Folder{}
	case "GitSyncConfig":
		value = GitSyncConfig{}
	case "GitSyncHistory":
		value = GitSyncHistory{}
	case "Note":
		value = Note{}
	case "NoteHistory":
		value = NoteHistory{}
	case "NoteLink":
		value = NoteLink{}
	case "NoteTag":
		value = NoteTag{}
	case "Setting":
		value = Setting{}
	case "Storage":
		value = Storage{}
	case "SyncLog":
		value = SyncLog{}
	case "User":
		value = User{}
	case "UserOIDCIdentity":
		value = UserOIDCIdentity{}
	case "UserShare":
		value = UserShare{}
	case "Vault":
		value = Vault{}
	default:
		return nil
	}
	// v3.8.0 narrows path and vault columns to varbinary. Refuse the upgrade
	// before any ALTER if legacy rows are too long for the new type.
	// v3.8.0 把 path 与 vault 列收窄为 varbinary。若存量行超出新类型长度，
	// 在任何 ALTER 之前拒绝升级。
	if err := PrecheckMySQLVarbinaryColumns(db, key); err != nil {
		return err
	}
	// #275: legacy MySQL deployments created full-column path indexes at
	// varchar(255). Rebuild them as prefix indexes before widening the columns,
	// otherwise utf8mb4 varchar(1024) exceeds InnoDB's 3072-byte key limit.
	// #275：既有 MySQL 部署按 varchar(255) 创建了全列 path 索引。扩列前先
	// 重建为前缀索引，否则 utf8mb4 varchar(1024) 会超过 InnoDB 3072 字节
	// 索引键上限。
	if key == "Note" || key == "File" || key == "Folder" || key == "Setting" || key == "NoteLink" {
		if err := PrepareMySQLPathIndexes(db); err != nil {
			return err
		}
	}
	if err := autoMigrateModel(db, value); err != nil {
		return err
	}
	// 阶段 D：升级后为存量行补 128 位 revision 令牌（跨方言可移植的 Go 分批回填），
	// 保证新模式连接读取到的每一行活跃记录与墓碑都有非空令牌。
	// Stage D: backfill 128-bit revision tokens for pre-existing rows after the
	// migration (portable, batched Go loop) so every live row and tombstone read by a
	// baseline-mode connection carries a non-empty token.
	if table := revisionTableOf(key); table != "" {
		return BackfillRevisionTokens(db, table)
	}
	return nil
}

// revisionTableOf maps an AutoMigrate model key to its revision-bearing table.
// revisionTableOf 把 AutoMigrate 的模型 key 映射到带版本令牌的表名。
func revisionTableOf(key string) string {
	switch key {
	case "Note":
		return TableNameNote
	case "File":
		return TableNameFile
	case "Setting":
		return TableNameSetting
	case "Folder":
		return TableNameFolder
	default:
		return ""
	}
}
