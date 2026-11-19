package model

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	gormMysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestMySQLPortableSchema verifies all shared models and the legacy FTS helper
// on a real MySQL/MariaDB server. It also checks that a second migration leaves
// path columns binary and that two case-distinct notes remain independent.
// TestMySQLPortableSchema 在真实 MySQL/MariaDB 上验证全部共享模型和旧 FTS
// 辅助表，并验证二次迁移后路径列仍为二进制、大小写不同的笔记保持独立。
// 设置 FNS_TEST_MYSQL_DSN 时执行。
func TestMySQLPortableSchema(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_MYSQL_DSN not set")
	}
	db, err := gorm.Open(gormMysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	keys := []string{"AuthToken", "AuthTokenLog", "BackupConfig", "BackupHistory", "File", "Folder", "GitSyncConfig", "GitSyncHistory", "Note", "NoteHistory", "NoteLink", "NoteTag", "Setting", "Storage", "SyncLog", "User", "UserOIDCIdentity", "UserShare", "Vault"}
	for _, key := range keys {
		require.NoError(t, AutoMigrate(db, key), key)
	}
	require.NoError(t, CreateNoteFTSTable(db))
	// A legacy case-insensitive table must become binary without being recreated
	// back into longtext by GORM's stock AlterColumn.
	require.NoError(t, db.Exec("ALTER TABLE note MODIFY COLUMN path varchar(1024) NOT NULL DEFAULT ''").Error)
	require.NoError(t, db.Exec("ALTER TABLE note MODIFY COLUMN path_hash varchar(1024) NOT NULL DEFAULT ''").Error)
	require.NoError(t, AutoMigrate(db, "Note"))

	for _, table := range []string{"note", "file", "folder", "setting"} {
		for _, column := range []string{"path", "path_hash"} {
			var dataType string
			require.NoError(t, db.Raw("SELECT data_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", table, column).Scan(&dataType).Error)
			require.Equal(t, "varbinary", dataType, table+"."+column)
		}
	}
	var tokenType string
	require.NoError(t, db.Raw("SELECT data_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'note_fts_token' AND column_name = 'token'").Scan(&tokenType).Error)
	require.Equal(t, "varchar", tokenType)
	var vaultType string
	require.NoError(t, db.Raw("SELECT data_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'vault' AND column_name = 'vault'").Scan(&vaultType).Error)
	require.Equal(t, "varbinary", vaultType)

	require.NoError(t, db.Create(&Note{VaultID: 1, Path: "case/Note.md", PathHash: "upper", Content: "upper"}).Error)
	require.NoError(t, db.Create(&Note{VaultID: 1, Path: "case/note.md", PathHash: "lower", Content: "lower"}).Error)
	require.NoError(t, db.Create(&File{VaultID: 1, Path: "case/File.bin", PathHash: "upper"}).Error)
	require.NoError(t, db.Create(&File{VaultID: 1, Path: "case/file.bin", PathHash: "lower"}).Error)
	require.NoError(t, db.Create(&Folder{VaultID: 1, Path: "case/Dir", PathHash: "upper"}).Error)
	require.NoError(t, db.Create(&Folder{VaultID: 1, Path: "case/dir", PathHash: "lower"}).Error)
	require.NoError(t, db.Create(&Setting{VaultID: 1, Path: "case/App.json", PathHash: "upper"}).Error)
	require.NoError(t, db.Create(&Setting{VaultID: 1, Path: "case/app.json", PathHash: "lower"}).Error)
	require.NoError(t, db.Create(&Vault{Vault: "Case"}).Error)
	require.NoError(t, db.Create(&Vault{Vault: "case"}).Error)
	require.NoError(t, db.Create(&NoteHistory{NoteID: 1, VaultID: 1, Path: "case/Note.md"}).Error)
	require.NoError(t, db.Create(&NoteHistory{NoteID: 2, VaultID: 1, Path: "case/note.md"}).Error)
	require.NoError(t, db.Create(&NoteLink{SourceNoteID: 1, VaultID: 1, UID: 1, TargetPath: "case/Note.md", TargetPathHash: "upper"}).Error)
	require.NoError(t, db.Create(&NoteLink{SourceNoteID: 2, VaultID: 1, UID: 1, TargetPath: "case/note.md", TargetPathHash: "lower"}).Error)
	var upper, lower Note
	require.NoError(t, db.Where("path = ?", "case/Note.md").First(&upper).Error)
	require.NoError(t, db.Where("path = ?", "case/note.md").First(&lower).Error)
	require.NotEqual(t, upper.ID, lower.ID)
	require.Equal(t, "upper", upper.Content)
	require.Equal(t, "lower", lower.Content)

	for _, table := range []string{"auth_token", "auth_token_log", "backup_config", "backup_history", "file", "folder", "git_sync_config", "git_sync_history", "note", "note_history", "note_link", "note_tag", "note_fts", "note_fts_token", "note_fts_meta", "setting", "storage", "sync_log", "user", "user_oidc_identity", "user_share", "vault"} {
		require.NoError(t, db.Exec("DROP TABLE IF EXISTS `"+table+"`").Error)
	}
}
