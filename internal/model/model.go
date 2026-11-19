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
	return autoMigrateModel(db, value)
}
