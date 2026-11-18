package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// repoKey follows the real repository's DB resolution path, independently of the migration.
// repoKey 沿用真实仓储的库解析路径，避免测试与迁移重复同一个错误键。
func repoKey(repo any) string {
	return repo.(interface{ GetKey(int64) string }).GetKey(1)
}

// TestTimestampSecondsToMillisMigrate 验证 #461 存量迁移：note / file / folder 表中
// (0, 1e11) 区间的秒级 ctime/mtime 被乘 1000 归一化为毫秒，0、负数和已是毫秒的值保持不动。
func TestTimestampSecondsToMillisMigrate(t *testing.T) {
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	if err := os.MkdirAll(filepath.Join("storage", "database"), 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join("storage", "database", "db.sqlite3")
	mainDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbCfg := &config.DatabaseConfig{Type: "sqlite", Path: dbPath}
	d := dao.New(mainDB, context.Background(), dao.WithConfig(dbCfg), dao.WithUserDatabaseConfig(dbCfg), dao.WithLogger(zap.NewNop()))
	t.Cleanup(func() {
		for _, key := range []string{"", repoKey(dao.NewNoteRepository(d)), repoKey(dao.NewFileRepository(d)), repoKey(dao.NewFolderRepository(d))} {
			if db := d.ResolveDB(key); db != nil {
				if sqlDB, err := db.DB(); err == nil {
					_ = sqlDB.Close()
				}
			}
		}
	})

	if err := mainDB.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if err := mainDB.Create(&model.User{UID: 1, Email: "a@example.com", Username: "a"}).Error; err != nil {
		t.Fatal(err)
	}

	noteDB := d.ResolveDB(repoKey(dao.NewNoteRepository(d)))
	if err := noteDB.AutoMigrate(&model.Note{}); err != nil {
		t.Fatal(err)
	}
	notes := []model.Note{
		{ID: 1, VaultID: 5, Path: "sec.md", PathHash: "h1", Action: "create", Ctime: 1651719914, Mtime: 1651719914},
		{ID: 2, VaultID: 5, Path: "ms.md", PathHash: "h2", Action: "create", Ctime: 1651719914000, Mtime: 1651719914000},
		{ID: 4, VaultID: 5, Path: "negative.md", PathHash: "h4", Action: "create", Ctime: -1, Mtime: -1},
		{ID: 3, VaultID: 5, Path: "zero.md", PathHash: "h3", Action: "create", Ctime: 0, Mtime: 0},
	}
	for i := range notes {
		if err := noteDB.Create(&notes[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	fileDB := d.ResolveDB(repoKey(dao.NewFileRepository(d)))
	if err := fileDB.AutoMigrate(&model.File{}); err != nil {
		t.Fatal(err)
	}
	if err := fileDB.Create(&model.File{ID: 1, VaultID: 5, Path: "sec.png", PathHash: "f1", Action: "create", Ctime: 1651719914, Mtime: 1651719914}).Error; err != nil {
		t.Fatal(err)
	}

	folderDB := d.ResolveDB(repoKey(dao.NewFolderRepository(d)))
	if err := folderDB.AutoMigrate(&model.Folder{}); err != nil {
		t.Fatal(err)
	}
	if err := folderDB.Create(&model.Folder{ID: 1, VaultID: 5, Path: "sec", PathHash: "d1", Action: "create", Ctime: 1651719914, Mtime: 1651719914}).Error; err != nil {
		t.Fatal(err)
	}

	m := &TimestampSecondsToMillisMigrate{}
	if err := m.Up(mainDB, context.Background(), &MigrationContext{Logger: zap.NewNop(), Dao: d}); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	assertTimes := func(db *gorm.DB, table string, id int64, wantCtime, wantMtime int64) {
		t.Helper()
		var row struct {
			Ctime int64
			Mtime int64
		}
		if err := db.Table(table).Select("ctime", "mtime").Where("id = ?", id).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Ctime != wantCtime || row.Mtime != wantMtime {
			t.Fatalf("%s id=%d: got (ctime=%d, mtime=%d), want (%d, %d)", table, id, row.Ctime, row.Mtime, wantCtime, wantMtime)
		}
	}

	assertTimes(noteDB, "note", 1, 1651719914000, 1651719914000)     // 秒级 → 毫秒
	assertTimes(noteDB, "note", 2, 1651719914000, 1651719914000)     // 已是毫秒，保持不变
	assertTimes(noteDB, "note", 4, -1, -1)                           // 负值保持不变
	assertTimes(noteDB, "note", 3, 0, 0)                             // 0 值保持不变
	assertTimes(fileDB, "file", 1, 1651719914000, 1651719914000)     // file 表同样迁移
	assertTimes(folderDB, "folder", 1, 1651719914000, 1651719914000) // folder 表同样迁移
}

// Bind the migration keys to the repositories without sharing fixture keys with the migration.
// 将迁移键与真实仓储绑定，测试造数不依赖迁移自己的键。
func TestTimestampMigrationRepositoryKeys(t *testing.T) {
	repos := map[string]any{
		"note":   dao.NewNoteRepository(nil),
		"file":   dao.NewFileRepository(nil),
		"folder": dao.NewFolderRepository(nil),
	}
	for _, target := range timestampMigrationTables {
		if got, want := target.keyPrefix+"1", repoKey(repos[target.table]); got != want {
			t.Errorf("%s migration key = %q, repository key = %q", target.table, got, want)
		}
	}
}
