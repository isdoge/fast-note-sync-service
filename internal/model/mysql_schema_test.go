package model

import (
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	gormMysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
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

// TestMySQLMigratorOverridesDispatch proves the mysqlMigrator overrides are
// reachable through GORM's own dispatch: AutoMigrate calls
// execTx.Migrator().MigrateColumn, and the base MigrateColumn calls
// m.DB.Migrator().FullDataTypeOf / AlterColumn, all of which resolve to the
// wrapper because the session's Dialector is mysqlDialector.
// TestMySQLMigratorOverridesDispatch 证明 mysqlMigrator 的覆盖会被 GORM 的
// 派发链调用：AutoMigrate 经过 execTx.Migrator()，基类 MigrateColumn 又经过
// m.DB.Migrator() 调用 FullDataTypeOf / AlterColumn；会话的 Dialector 是
// mysqlDialector，因此都会落到包装器上。
func TestMySQLMigratorOverridesDispatch(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_MYSQL_DSN not set")
	}
	db, err := gorm.Open(gormMysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS note").Error)
	t.Cleanup(func() { _ = db.Exec("DROP TABLE IF EXISTS note").Error })
	require.NoError(t, db.Exec("CREATE TABLE note (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, path varchar(1024) DEFAULT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4").Error)

	modelValue, table, err := newMySQLModel(&Note{})
	require.NoError(t, err)
	require.Equal(t, "note", table)
	parsed, err := schema.ParseWithSpecialTableName(modelValue, &sync.Map{}, schema.NamingStrategy{SingularTable: true}, table)
	require.NoError(t, err)
	field := parsed.LookUpField("Path")
	require.NotNil(t, field)

	tx := db.Session(&gorm.Session{})
	tx.Dialector = mysqlDialector{Dialector: db.Dialector, db: db, table: table}

	// FullDataTypeOf override: dynamic varbinary tag wins over stock typing.
	expr := tx.Migrator().FullDataTypeOf(field)
	require.Contains(t, expr.SQL, "varbinary(1024)")
	require.True(t, expr.WithoutParentheses)

	// AlterColumn override: rewrites the legacy varchar column in place.
	require.NoError(t, tx.Migrator().AlterColumn(modelValue, "Path"))
	var dataType string
	require.NoError(t, db.Raw("SELECT data_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'note' AND column_name = 'path'").Scan(&dataType).Error)
	require.Equal(t, "varbinary", dataType)

	// MigrateColumn override: receives the same dynamic field and leaves the
	// already-correct binary column untouched.
	columnTypes, err := tx.Migrator().ColumnTypes(modelValue)
	require.NoError(t, err)
	var found gorm.ColumnType
	for _, ct := range columnTypes {
		if ct.Name() == "path" {
			found = ct
			break
		}
	}
	require.NotNil(t, found)
	require.NoError(t, tx.Migrator().MigrateColumn(modelValue, field, found))
	require.NoError(t, db.Raw("SELECT data_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'note' AND column_name = 'path'").Scan(&dataType).Error)
	require.Equal(t, "varbinary", dataType)
}
