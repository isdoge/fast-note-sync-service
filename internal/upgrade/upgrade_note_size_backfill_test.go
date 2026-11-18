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

// TestNoteSizeBackfillMigrate_ReadsContentFile（#360）
// size=0 的存量行必须从 content.txt 回填实际字节数（note.content 列懒迁移后为空）；
// 已有正确 size 的行不动；无 content.txt 的行跳过；版本标识必须唯一（"3.6.3"）。
func TestNoteSizeBackfillMigrate_ReadsContentFile(t *testing.T) {
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
		for _, db := range []*gorm.DB{mainDB, d.ResolveDB("user_1")} {
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

	userDB := d.ResolveDB("user_1")
	if err := userDB.AutoMigrate(&model.Note{}); err != nil {
		t.Fatal(err)
	}
	notes := []model.Note{
		{ID: 1, VaultID: 5, Path: "zero.md", PathHash: "h1", Action: "create", Size: 0},        // 待回填
		{ID: 2, VaultID: 5, Path: "correct.md", PathHash: "h2", Action: "create", Size: 42},     // 已有正确值，不动
		{ID: 3, VaultID: 5, Path: "nofile.md", PathHash: "h3", Action: "create", Size: 0},       // 无 content.txt，跳过
		{ID: 4, VaultID: 5, Path: "deleted.md", PathHash: "h4", Action: "delete", Size: 0},     // 已删除，跳过
		{ID: 5, VaultID: 5, Path: "empty.md", PathHash: "h5", Action: "create", Size: 0},        // 空正文：0 是真实值，写 0 无害
	}
	for i := range notes {
		if err := userDB.Create(&notes[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 只有 note 1 和 5 有 content.txt；note 5 的正文为空
	for id, body := range map[int64]string{1: "hello world, 13b", 5: ""} {
		folder := d.GetNoteFolderPath(1, id)
		if err := os.MkdirAll(folder, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "content.txt"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	m := &NoteSizeBackfillMigrate{}
	if err := m.Up(mainDB, context.Background(), &MigrationContext{Logger: zap.NewNop(), Dao: d}); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// gorm First 到已带主键的目标变量会追加条件，每个断言用独立变量（见调试记录）
	// gorm First into a dest that already carries a primary key appends conditions;
	// each assertion uses its own variable.
	assertSize := func(id int64, want int64) {
		t.Helper()
		var got model.Note
		if err := userDB.First(&got, id).Error; err != nil {
			t.Fatalf("First(%d): %v", id, err)
		}
		if got.Size != want {
			t.Fatalf("note %d size = %d, want %d", id, got.Size, want)
		}
	}
	assertSize(1, int64(len("hello world, 13b"))) // 从 content.txt 回填
	assertSize(2, 42)                             // 已有正确值，不动
	assertSize(3, 0)                              // 无 content.txt，跳过
	assertSize(4, 0)                              // 已删除，跳过

	if m.Version() != "3.6.3" {
		t.Fatalf("Version() = %q, want 3.6.3 (unique id, greater than released 3.6.x)", m.Version())
	}
}
