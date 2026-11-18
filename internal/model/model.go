package model

import (
	"gorm.io/gorm"
)

func AutoMigrate(db *gorm.DB, key string) error {
	if db == nil {
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
	switch key {

	case "AuthToken":
		return db.AutoMigrate(AuthToken{})

	case "AuthTokenLog":
		return db.AutoMigrate(AuthTokenLog{})

	case "BackupConfig":
		return db.AutoMigrate(BackupConfig{})

	case "BackupHistory":
		return db.AutoMigrate(BackupHistory{})

	case "File":
		return db.AutoMigrate(File{})

	case "Folder":
		return db.AutoMigrate(Folder{})

	case "GitSyncConfig":
		return db.AutoMigrate(GitSyncConfig{})

	case "GitSyncHistory":
		return db.AutoMigrate(GitSyncHistory{})

	case "Note":
		return db.AutoMigrate(Note{})

	case "NoteHistory":
		return db.AutoMigrate(NoteHistory{})

	case "NoteLink":
		return db.AutoMigrate(NoteLink{})

	case "NoteTag":
		return db.AutoMigrate(NoteTag{})

	case "Setting":
		return db.AutoMigrate(Setting{})

	case "Storage":
		return db.AutoMigrate(Storage{})

	case "SyncLog":
		return db.AutoMigrate(SyncLog{})

	case "User":
		return db.AutoMigrate(User{})

	case "UserOIDCIdentity":
		return db.AutoMigrate(UserOIDCIdentity{})

	case "UserShare":
		return db.AutoMigrate(UserShare{})

	case "Vault":
		return db.AutoMigrate(Vault{})
	}
	return nil
}
