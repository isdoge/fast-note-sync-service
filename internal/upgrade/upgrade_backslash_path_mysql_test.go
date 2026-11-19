package upgrade

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	gormMysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestBackslashPathMigrationMySQL covers all shared repository tables and the
// note history/link/FTS side effects on a real MySQL or MariaDB database.
// TestBackslashPathMigrationMySQL 在真实 MySQL/MariaDB 上覆盖所有共享仓储表，
// 以及笔记历史、链接和 FTS 联动。设置 FNS_TEST_MYSQL_DSN 时执行。
func TestBackslashPathMigrationMySQL(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_MYSQL_DSN not set")
	}
	ctx := context.Background()
	db, err := gorm.Open(gormMysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	cfg := &config.DatabaseConfig{Type: "mysql", EnableWriteQueue: util.Ptr(false)}
	d := dao.New(db, ctx, dao.WithConfig(cfg), dao.WithUserDatabaseConfig(&config.DatabaseConfig{}), dao.WithLogger(zap.NewNop()))
	for _, table := range []string{"user", "note", "note_history", "note_link", "note_fts", "file", "folder", "setting", "backslash_path_plan"} {
		require.NoError(t, db.Exec("DROP TABLE IF EXISTS `"+table+"`").Error)
	}
	t.Cleanup(func() {
		for _, table := range []string{"user", "note", "note_history", "note_link", "note_fts", "file", "folder", "setting", "backslash_path_plan"} {
			_ = db.Exec("DROP TABLE IF EXISTS `" + table + "`").Error
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, db.Create(&model.User{UID: 1, Email: "mysql-path@example.invalid", Username: "mysql-path"}).Error)
	for _, key := range []string{"Note", "NoteHistory", "NoteLink", "File", "Folder", "Setting"} {
		require.NoError(t, model.AutoMigrate(db, key))
	}
	require.NoError(t, db.AutoMigrate(&model.NoteFTS{}))
	ndb := d.ResolveDB("user_1")
	require.Same(t, db, ndb)
	notes := []model.Note{
		{ID: 1, VaultID: 1, Path: `dir\one.md`, PathHash: "legacy", Action: "create", FID: 7},
		{ID: 2, VaultID: 1, Path: "a/b.md", PathHash: "target", Action: "delete"},
		{ID: 3, VaultID: 1, Path: `a\b.md`, PathHash: "collision", Action: "delete"},
		{ID: 4, VaultID: 1, Path: `last\ok.md`, Action: "create"},
		{ID: 5, VaultID: 1, Path: "dir", Action: "create"},
	}
	require.NoError(t, ndb.Create(&notes).Error)
	require.NoError(t, ndb.Create(&model.File{ID: 1, VaultID: 1, Path: `f\x.png`, Action: "create"}).Error)
	require.NoError(t, ndb.Create(&model.Folder{ID: 1, VaultID: 1, Path: `d\sub`, Action: "create"}).Error)
	require.NoError(t, ndb.Create(&model.Folder{ID: 2, VaultID: 1, Path: "dir", Action: "create"}).Error)
	require.NoError(t, ndb.Create(&model.Setting{ID: 1, VaultID: 1, Path: `.obsidian\app.json`, Action: "create"}).Error)
	require.NoError(t, ndb.Create(&model.NoteHistory{ID: 1, NoteID: 1, VaultID: 1, Path: notes[0].Path, Content: "history"}).Error)
	require.NoError(t, ndb.Create(&model.NoteLink{ID: 1, SourceNoteID: 99, VaultID: 1, UID: 1, TargetPath: notes[0].Path, TargetPathHash: "legacy"}).Error)
	require.NoError(t, ndb.Create(&model.NoteLink{ID: 2, SourceNoteID: 99, VaultID: 1, UID: 1, TargetPath: `dir\one`, TargetPathHash: "legacy-noext"}).Error)
	require.NoError(t, ndb.Create(&model.NoteFTS{NoteID: 1, Path: notes[0].Path, Content: "body"}).Error)

	m := &BackslashPathMigrate{}
	require.NoError(t, m.Up(db, ctx, &MigrationContext{Dao: d, Logger: zap.NewNop()}))
	var got, conflict model.Note
	require.NoError(t, ndb.First(&got, 1).Error)
	require.Equal(t, "dir/one.md", got.Path)
	require.Equal(t, util.EncodeHash32(got.Path), got.PathHash)
	require.Equal(t, int64(2), got.FID)
	require.NotZero(t, got.UpdatedTimestamp)
	require.NoError(t, ndb.First(&conflict, 3).Error)
	require.True(t, strings.HasPrefix(conflict.Path, "a/b.conflict.") && strings.HasSuffix(conflict.Path, ".md"), conflict.Path)
	require.Equal(t, "delete", conflict.Action)
	var history model.NoteHistory
	require.NoError(t, ndb.First(&history, 1).Error)
	require.Equal(t, got.Path, history.Path)
	require.Equal(t, "history", history.Content)
	var link, noext model.NoteLink
	require.NoError(t, ndb.First(&link, 1).Error)
	require.Equal(t, got.Path, link.TargetPath)
	require.Equal(t, got.PathHash, link.TargetPathHash)
	require.NoError(t, ndb.First(&noext, 2).Error)
	require.Equal(t, "dir/one", noext.TargetPath)
	require.Equal(t, util.EncodeHash32("dir/one"), noext.TargetPathHash)
	var fts model.NoteFTS
	require.NoError(t, ndb.First(&fts, 1).Error)
	require.Equal(t, got.Path, fts.Path)
	require.Equal(t, "body", fts.Content)
	for _, entry := range []struct{ table, want string }{{"file", "f/x.png"}, {"folder", "d/sub"}, {"setting", ".obsidian/app.json"}} {
		var row struct{ Path, PathHash string }
		require.NoError(t, ndb.Table(entry.table).Where("id = ?", 1).Take(&row).Error)
		require.Equal(t, entry.want, row.Path)
		require.Equal(t, util.EncodeHash32(row.Path), row.PathHash)
	}
	require.NoError(t, m.Up(db, ctx, &MigrationContext{Dao: d, Logger: zap.NewNop()}))
	var again model.Note
	require.NoError(t, ndb.First(&again, 3).Error)
	require.Equal(t, conflict.Path, again.Path)
	require.Equal(t, conflict.UpdatedTimestamp, again.UpdatedTimestamp)
}
