package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBackslashPathMigration(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "db.sqlite3")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.DatabaseConfig{Type: "sqlite", Path: dbPath}
	d := dao.New(db, ctx, dao.WithConfig(cfg), dao.WithUserDatabaseConfig(cfg), dao.WithLogger(zap.NewNop()))
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.User{UID: 1, Email: "path@example.com", Username: "path"}).Error; err != nil {
		t.Fatal(err)
	}
	ndb := d.ResolveDB("user_1")
	hdb := d.ResolveDB("user_note_history_1")
	ldb := d.ResolveDB("user_note_link_1")
	for _, entry := range []struct {
		db     *gorm.DB
		models []any
	}{
		{ndb, []any{&model.Note{}, &model.NoteFTS{}, &model.NoteTag{}}},
		{hdb, []any{&model.NoteHistory{}}}, {ldb, []any{&model.NoteLink{}}},
		{d.ResolveDB("user_file_1"), []any{&model.File{}}},
		{d.ResolveDB("user_folder_1"), []any{&model.Folder{}}},
		{d.ResolveDB("user_setting_1"), []any{&model.Setting{}}},
	} {
		if err := entry.db.AutoMigrate(entry.models...); err != nil {
			t.Fatal(err)
		}
	}
	notes := []model.Note{
		{ID: 1, VaultID: 1, Path: `dir\one.md`, PathHash: "legacy", Action: "create"},
		{ID: 2, VaultID: 1, Path: "a/b.md", PathHash: "target", Action: "delete"},
		{ID: 3, VaultID: 1, Path: `a\b.md`, PathHash: "collision", Action: "delete"},
		{ID: 4, VaultID: 1, Path: `bad\..\x.md`, Action: "create"},
		{ID: 5, VaultID: 1, Path: `last\ok.md`, Action: "create"},
		{ID: 6, VaultID: 1, Path: `blocked\note.md`, Action: "create"},
		{ID: 7, VaultID: 1, Path: `later\note.md`, Action: "create"},
		{ID: 8, VaultID: 1, Path: "blocked/note.md", Action: "create"},
	}
	for i := range notes {
		if err := ndb.Create(&notes[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := hdb.Create(&model.NoteHistory{ID: 1, NoteID: 1, VaultID: 1, Path: notes[0].Path, Content: "history"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ldb.Create(&model.NoteLink{ID: 1, SourceNoteID: 99, VaultID: 1, UID: 1, TargetPath: notes[0].Path, TargetPathHash: "legacy"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ndb.Create(&model.NoteFTS{NoteID: 1, Path: notes[0].Path, Content: "body"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		key, table string
		row        any
	}{
		{"user_file_1", "file", &model.File{ID: 1, VaultID: 1, Path: `f\x.png`, Action: "create"}},
		{"user_folder_1", "folder", &model.Folder{ID: 1, VaultID: 1, Path: `d\sub`, Action: "create"}},
		{"user_setting_1", "setting", &model.Setting{ID: 1, VaultID: 1, Path: `.obsidian\app.json`, Action: "create"}},
	} {
		if err := d.ResolveDB(entry.key).Create(entry.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Fail a real SQLite row update after related tables have been written.
	if err := ndb.Exec("CREATE TRIGGER fail_path BEFORE UPDATE OF path ON note WHEN OLD.id = 6 BEGIN SELECT RAISE(FAIL, 'injected row failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := hdb.Create(&model.NoteHistory{ID: 6, NoteID: 6, VaultID: 1, Path: `blocked\note.md`}).Error; err != nil {
		t.Fatal(err)
	}
	// Also cover the extensionless target form used by parsed wikilinks.
	if err := ldb.Create(&model.NoteLink{ID: 2, SourceNoteID: 99, VaultID: 1, UID: 1, TargetPath: `dir\one`, TargetPathHash: "legacy-noext"}).Error; err != nil {
		t.Fatal(err)
	}
	mc := &MigrationContext{Dao: d, Logger: zap.NewNop()}
	m := &BackslashPathMigrate{}
	// Invalid legacy traversal is a failed row; later rows must still succeed.
	if err := m.Up(db, ctx, mc); err == nil {
		t.Fatal("expected retryable failure for traversal row")
	}
	var got model.Note
	if err := ndb.First(&got, 1).Error; err != nil {
		t.Fatal(err)
	}
	if got.Path != "dir/one.md" || got.PathHash != util.EncodeHash32(got.Path) || got.UpdatedTimestamp == 0 {
		t.Fatalf("note not migrated: %+v", got)
	}
	var conflict model.Note
	ndb.First(&conflict, 3)
	if !strings.HasPrefix(conflict.Path, "a/b.conflict.") || !strings.HasSuffix(conflict.Path, ".md") || conflict.Action != "delete" {
		t.Fatalf("bad conflict tombstone: %+v", conflict)
	}
	var last model.Note
	ndb.First(&last, 5)
	if last.Path != "last/ok.md" {
		t.Fatalf("later row skipped: %+v", last)
	}
	var h model.NoteHistory
	hdb.First(&h, 1)
	if h.Path != got.Path || h.Content != "history" {
		t.Fatalf("history: %+v", h)
	}
	var link model.NoteLink
	ldb.First(&link, 1)
	if link.TargetPath != got.Path || link.TargetPathHash != got.PathHash {
		t.Fatalf("link: %+v", link)
	}
	var fts model.NoteFTS
	ndb.First(&fts, 1)
	if fts.Path != got.Path || fts.Content != "body" {
		t.Fatalf("fts: %+v", fts)
	}
	for _, entry := range []struct{ key, table, want string }{
		{"user_file_1", "file", "f/x.png"}, {"user_folder_1", "folder", "d/sub"}, {"user_setting_1", "setting", ".obsidian/app.json"},
	} {
		var row struct {
			Path, PathHash   string
			UpdatedTimestamp int64
		}
		if err := d.ResolveDB(entry.key).Table(entry.table).Where("id = ?", 1).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Path != entry.want || row.PathHash != util.EncodeHash32(row.Path) || row.UpdatedTimestamp == 0 {
			t.Fatalf("%s: %+v", entry.table, row)
		}
	}
	var later model.Note
	ndb.First(&later, 7)
	if later.Path != "later/note.md" {
		t.Fatalf("failed row stopped later row: %+v", later)
	}
	var blocked model.Note
	ndb.First(&blocked, 6)
	if blocked.Path != `blocked\note.md` {
		t.Fatalf("failed row lost retry marker: %+v", blocked)
	}
	var plan backslashPathPlan
	if err := ndb.Where("kind = ? AND resource_id = ?", "note", 6).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var noext model.NoteLink
	ldb.First(&noext, 2)
	if noext.TargetPath != "dir/one" || noext.TargetPathHash != util.EncodeHash32("dir/one") {
		t.Fatalf("extensionless link: %+v", noext)
	}
	if err := ndb.Exec("DROP TRIGGER fail_path").Error; err != nil {
		t.Fatal(err)
	}
	// Remove only the deliberately invalid test fixture, then prove successful retry/idempotence.
	ndb.Delete(&model.Note{}, 4)
	if err := m.Up(db, ctx, mc); err != nil {
		t.Fatal(err)
	}
	var recovered model.Note
	ndb.First(&recovered, 6)
	if recovered.Path != plan.Target {
		t.Fatalf("retry changed reserved target: %q != %q", recovered.Path, plan.Target)
	}
	var recoveredHistory model.NoteHistory
	hdb.First(&recoveredHistory, 6)
	if recoveredHistory.Path != recovered.Path {
		t.Fatalf("retry history mismatch: %+v", recoveredHistory)
	}
	var again model.Note
	ndb.First(&again, 3)
	if again.Path != conflict.Path || again.UpdatedTimestamp != conflict.UpdatedTimestamp {
		t.Fatalf("not idempotent: %+v", again)
	}
	// An unchanged binary/lastVersion must still run this repair and record it only
	// after every row succeeds. Ordinary semver migrations keep their old gating.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.MkdirAll("config", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("config/lastVersion", []byte("3.7.0"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ndb.Create(&model.Note{ID: 99, VaultID: 1, Path: `manager\retry.md`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ndb.Exec("CREATE TRIGGER fail_manager BEFORE UPDATE OF path ON note WHEN OLD.id = 99 BEGIN SELECT RAISE(FAIL, 'retry manager'); END").Error; err != nil {
		t.Fatal(err)
	}
	manager := NewMigrationManager(db, zap.NewNop(), "3.7.0", cfg, cfg)
	if err := manager.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var applied int64
	db.Model(&SchemaVersion{}).Where("version = ?", m.Version()).Count(&applied)
	if applied != 0 {
		t.Fatal("failed repair was marked applied")
	}
	if err := ndb.Exec("DROP TRIGGER fail_manager").Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.Run(ctx); err != nil {
		t.Fatal(err)
	}
	db.Model(&SchemaVersion{}).Where("version = ?", m.Version()).Count(&applied)
	if applied != 1 {
		t.Fatal("completed repair marker missing")
	}
	var managerNote model.Note
	ndb.First(&managerNote, 99)
	if managerNote.Path != "manager/retry.md" {
		t.Fatalf("same-version restart skipped repair: %+v", managerNote)
	}

}
