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

// TestNoteTagIndexRebuildMigrate_ReadsContentFile 回填迁移必须从 content.txt 取正文
// （note.content 列在懒迁移后为空），并能在 SQLite 多文件布局下找到 user_vault_<uid> 库里的 vault 表。
// 修复前：迁移在 user_<uid> 库里找 vault 表（找不到即跳过全部用户），查询还引用了不存在的 uid 列，
// 且从空的 note.content 列提取标签，回填结果为空。
func TestNoteTagIndexRebuildMigrate_ReadsContentFile(t *testing.T) {
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
		for _, db := range []*gorm.DB{mainDB, d.ResolveDB("user_1"), d.ResolveDB("user_vault_1")} {
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
	})

	if err := mainDB.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if err := mainDB.Create(&model.User{UID: 1, Email: "a@example.com", Username: "a"}).Error; err != nil {
		t.Fatal(err)
	}

	vaultDB := d.ResolveDB("user_vault_1")
	if err := vaultDB.AutoMigrate(&model.Vault{}); err != nil {
		t.Fatal(err)
	}
	if err := vaultDB.Create(&model.Vault{ID: 5, Vault: "v"}).Error; err != nil {
		t.Fatal(err)
	}

	userDB := d.ResolveDB("user_1")
	if err := userDB.AutoMigrate(&model.Note{}); err != nil {
		t.Fatal(err)
	}
	notes := []model.Note{
		{ID: 1, VaultID: 5, Path: "a.md", PathHash: "ha", Action: "create"},
		{ID: 2, VaultID: 5, Path: "b.md", PathHash: "hb", Action: "delete"},
	}
	for i := range notes {
		if err := userDB.Create(&notes[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for id, body := range map[int64]string{1: "---\ntags: [project]\n---\nbody #inline", 2: "#gone"} {
		folder := d.GetNoteFolderPath(1, id)
		if err := os.MkdirAll(folder, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "content.txt"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	m := &NoteTagIndexRebuildMigrate{}
	if err := m.Up(mainDB, context.Background(), &MigrationContext{Logger: zap.NewNop(), Dao: d}); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	tags, err := dao.NewNoteTagRepository(d).GetTagsByNoteID(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "inline" || tags[1] != "project" {
		t.Fatalf("expected tags [inline project] extracted from content.txt, got %v", tags)
	}
	gone, err := dao.NewNoteTagRepository(d).GetTagsByNoteID(context.Background(), 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Fatalf("deleted note must not be indexed, got %v", gone)
	}
}
