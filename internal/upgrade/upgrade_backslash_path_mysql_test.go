package upgrade

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"

	mysqlDriver "github.com/go-sql-driver/mysql"
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
// note history/link/FTS side effects on a real MySQL or MariaDB database using
// the production-reachable independent user database mode
// (user-database.type=mysql, database user_<uid> created by the service).
// TestBackslashPathMigrationMySQL 在真实 MySQL/MariaDB 上覆盖所有共享仓储表，
// 以及笔记历史、链接和 FTS 联动。使用生产可达的独立用户库模式
// （user-database.type=mysql，服务端自动创建 user_<uid> 库）。
// 设置 FNS_TEST_MYSQL_DSN 时执行；账号需要 CREATE DATABASE 权限。
func TestBackslashPathMigrationMySQL(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_MYSQL_DSN not set")
	}
	ctx := context.Background()
	db, err := gorm.Open(gormMysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	mysqlCfg, err := mysqlDriver.ParseDSN(dsn)
	require.NoError(t, err)
	host, portStr, err := net.SplitHostPort(mysqlCfg.Addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	mainCfg := &config.DatabaseConfig{
		Type: "mysql", Host: host, Port: port, UserName: mysqlCfg.User, Password: mysqlCfg.Passwd,
		Name: mysqlCfg.DBName, Charset: "utf8mb4", ParseTime: true, EnableWriteQueue: util.Ptr(false),
	}
	userCfg := *mainCfg
	d := dao.New(db, ctx, dao.WithConfig(mainCfg), dao.WithUserDatabaseConfig(&userCfg), dao.WithLogger(zap.NewNop()))

	dropUserDB := func() { _ = db.Exec("DROP DATABASE IF EXISTS `user_1`").Error }
	dropUserDB()
	t.Cleanup(func() {
		dropUserDB()
		_ = db.Exec("DROP TABLE IF EXISTS `user`").Error
	})
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS `user`").Error)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, db.Create(&model.User{UID: 1, Email: "mysql-path@example.invalid", Username: "mysql-path"}).Error)

	// ResolveDB creates the user_<uid> database on first use, exactly like the
	// running service. All repository keys of uid 1 share it.
	ndb := d.ResolveDB("user_1")
	require.NotSame(t, db, ndb)
	var userDBName string
	require.NoError(t, ndb.Raw("SELECT DATABASE()").Scan(&userDBName).Error)
	require.Equal(t, "user_1", userDBName)
	require.Same(t, ndb, d.ResolveDB("user_note_1"))
	for _, key := range []string{"Note", "NoteHistory", "NoteLink", "File", "Folder", "Setting"} {
		require.NoError(t, model.AutoMigrate(ndb, key))
	}
	require.NoError(t, ndb.AutoMigrate(&model.NoteFTS{}))
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
	var got model.Note
	require.NoError(t, ndb.First(&got, 1).Error)
	require.Equal(t, "dir/one.md", got.Path)
	require.Equal(t, util.EncodeHash32(got.Path), got.PathHash)
	require.Equal(t, int64(2), got.FID)
	require.NotZero(t, got.UpdatedTimestamp)
	// 目标路径上的行也是墓碑：清理源墓碑，不产生冲突副本。
	var removed model.Note
	require.ErrorIs(t, ndb.First(&removed, 3).Error, gorm.ErrRecordNotFound)
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
	require.NoError(t, ndb.First(&again, 1).Error)
	require.Equal(t, got.Path, again.Path)
	require.Equal(t, got.UpdatedTimestamp, again.UpdatedTimestamp)
}

// TestBackslashPathMigrationMySQLCreatesFolderTable covers the missing-table
// branch of migrationParentFID: on MySQL the folder table must be created by
// the custom migrator (varbinary path + length-bounded prefix index), not by
// native db.AutoMigrate.
// TestBackslashPathMigrationMySQLCreatesFolderTable 覆盖 migrationParentFID 的
// 建表分支：MySQL 上 folder 表必须由自定义迁移器创建（varbinary 路径列 + 带
// 长度的前缀索引），而不是原生 db.AutoMigrate。
func TestBackslashPathMigrationMySQLCreatesFolderTable(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_MYSQL_DSN not set")
	}
	ctx := context.Background()
	db, err := gorm.Open(gormMysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	mysqlCfg, err := mysqlDriver.ParseDSN(dsn)
	require.NoError(t, err)
	host, portStr, err := net.SplitHostPort(mysqlCfg.Addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	mainCfg := &config.DatabaseConfig{
		Type: "mysql", Host: host, Port: port, UserName: mysqlCfg.User, Password: mysqlCfg.Passwd,
		Name: mysqlCfg.DBName, Charset: "utf8mb4", ParseTime: true, EnableWriteQueue: util.Ptr(false),
	}
	userCfg := *mainCfg
	d := dao.New(db, ctx, dao.WithConfig(mainCfg), dao.WithUserDatabaseConfig(&userCfg), dao.WithLogger(zap.NewNop()))

	dropUserDB := func() { _ = db.Exec("DROP DATABASE IF EXISTS `user_1`").Error }
	dropUserDB()
	t.Cleanup(func() {
		dropUserDB()
		_ = db.Exec("DROP TABLE IF EXISTS `user`").Error
	})
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS `user`").Error)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, db.Create(&model.User{UID: 1, Email: "mysql-folder@example.invalid", Username: "mysql-folder"}).Error)

	ndb := d.ResolveDB("user_1")
	require.NoError(t, model.AutoMigrate(ndb, "Note"))
	require.False(t, ndb.Migrator().HasTable("folder"), "precondition: folder table must be absent")
	require.NoError(t, ndb.Create(&model.Note{ID: 1, VaultID: 1, Path: `parent\child.md`, Action: "create"}).Error)

	require.NoError(t, (&BackslashPathMigrate{}).Up(db, ctx, &MigrationContext{Dao: d, Logger: zap.NewNop()}))

	require.True(t, ndb.Migrator().HasTable("folder"))
	var dataType string
	require.NoError(t, ndb.Raw(`SELECT DATA_TYPE FROM information_schema.columns
		WHERE table_schema = 'user_1' AND table_name = 'folder' AND column_name = 'path'`).Scan(&dataType).Error)
	require.Equal(t, "varbinary", dataType)
	var prefixLen int64
	require.NoError(t, ndb.Raw(`SELECT COALESCE(sub_part, 0) FROM information_schema.statistics
		WHERE table_schema = 'user_1' AND table_name = 'folder'
		AND index_name = 'idx_folder_vault_id_path' AND column_name = 'path'`).Scan(&prefixLen).Error)
	require.Equal(t, int64(255), prefixLen, "path index must be a 255-byte prefix index")

	var note model.Note
	require.NoError(t, ndb.First(&note, 1).Error)
	require.Equal(t, "parent/child.md", note.Path)
	var parent model.Folder
	require.NoError(t, ndb.Where("vault_id = ? AND path = ?", 1, "parent").Take(&parent).Error)
	require.Equal(t, parent.ID, note.FID)
}
