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
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type backslashTestEnv struct {
	db                                *gorm.DB
	d                                 *dao.Dao
	ndb, hdb, ldb, fdb, folderDB, sdb *gorm.DB
}

func newBackslashTestEnv(t *testing.T) *backslashTestEnv {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "db.sqlite3")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	cfg := &config.DatabaseConfig{Type: "sqlite", Path: dbPath}
	d := dao.New(db, ctx, dao.WithConfig(cfg), dao.WithUserDatabaseConfig(cfg), dao.WithLogger(zap.NewNop()))
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, db.Create(&model.User{UID: 1, Email: "path@example.com", Username: "path"}).Error)

	env := &backslashTestEnv{
		db:       db,
		d:        d,
		ndb:      d.ResolveDB("user_1"),
		hdb:      d.ResolveDB("user_note_history_1"),
		ldb:      d.ResolveDB("user_note_link_1"),
		fdb:      d.ResolveDB("user_file_1"),
		folderDB: d.ResolveDB("user_folder_1"),
		sdb:      d.ResolveDB("user_setting_1"),
	}
	for _, entry := range []struct {
		db     *gorm.DB
		models []any
	}{
		{env.ndb, []any{&model.Note{}, &model.NoteFTS{}, &model.NoteTag{}}},
		{env.hdb, []any{&model.NoteHistory{}}},
		{env.ldb, []any{&model.NoteLink{}}},
		{env.fdb, []any{&model.File{}}},
		{env.folderDB, []any{&model.Folder{}}},
		{env.sdb, []any{&model.Setting{}}},
	} {
		require.NoError(t, entry.db.AutoMigrate(entry.models...))
	}
	return env
}

func (e *backslashTestEnv) migrate(t *testing.T) error {
	t.Helper()
	return (&BackslashPathMigrate{}).Up(e.db, context.Background(), &MigrationContext{Dao: e.d, Logger: zap.NewNop()})
}

func TestBackslashPathMigration(t *testing.T) {
	env := newBackslashTestEnv(t)
	ndb, hdb, ldb := env.ndb, env.hdb, env.ldb

	notes := []model.Note{
		{ID: 1, VaultID: 1, Path: `dir\one.md`, PathHash: "legacy", Action: "create"},
		{ID: 2, VaultID: 1, Path: "a/b.md", PathHash: "target", Action: "delete"},
		{ID: 3, VaultID: 1, Path: `a\b.md`, PathHash: "collision", Action: "delete"},
		{ID: 4, VaultID: 1, Path: `bad\..\x.md`, Action: "create"},
		{ID: 5, VaultID: 1, Path: `last\ok.md`, Action: "create"},
		{ID: 6, VaultID: 1, Path: `blocked\note.md`, Action: "create"},
		{ID: 7, VaultID: 1, Path: `later\note.md`, Action: "create"},
		{ID: 8, VaultID: 1, Path: "blocked/note.md", Action: "create"},
		{ID: 9, VaultID: 1, Path: `free\slot.md`, Action: "create"},
		{ID: 10, VaultID: 1, Path: `gone\away.md`, Action: "delete"},
		{ID: 11, VaultID: 1, Path: "free/slot.md", PathHash: "free-target", Action: "delete"},
	}
	for i := range notes {
		require.NoError(t, ndb.Create(&notes[i]).Error)
	}
	require.NoError(t, hdb.Create(&model.NoteHistory{ID: 1, NoteID: 1, VaultID: 1, Path: notes[0].Path, Content: "history"}).Error)
	require.NoError(t, ldb.Create(&model.NoteLink{ID: 1, SourceNoteID: 99, VaultID: 1, UID: 1, TargetPath: notes[0].Path, TargetPathHash: "legacy"}).Error)
	require.NoError(t, ndb.Create(&model.NoteFTS{NoteID: 1, Path: notes[0].Path, Content: "body"}).Error)
	// Extensionless wikilink form.
	require.NoError(t, ldb.Create(&model.NoteLink{ID: 2, SourceNoteID: 99, VaultID: 1, UID: 1, TargetPath: `dir\one`, TargetPathHash: "legacy-noext"}).Error)
	// Tombstone source whose target is also a tombstone: no link rewrite expected.
	require.NoError(t, ldb.Create(&model.NoteLink{ID: 3, SourceNoteID: 99, VaultID: 1, UID: 1, TargetPath: `a\b.md`, TargetPathHash: "tombstone-link"}).Error)

	require.NoError(t, env.fdb.Create(&model.File{ID: 1, VaultID: 1, Path: `f\x.png`, Action: "create"}).Error)
	require.NoError(t, env.folderDB.Create(&model.Folder{ID: 1, VaultID: 1, Path: `d\sub`, Action: "create"}).Error)
	require.NoError(t, env.sdb.Create(&model.Setting{ID: 1, VaultID: 1, Path: `.obsidian\app.json`, Action: "create"}).Error)

	// Fail one real SQLite row update after related tables have been written.
	require.NoError(t, ndb.Exec("CREATE TRIGGER fail_path BEFORE UPDATE OF path ON note WHEN OLD.id = 6 BEGIN SELECT RAISE(FAIL, 'injected row failure'); END").Error)
	require.NoError(t, hdb.Create(&model.NoteHistory{ID: 6, NoteID: 6, VaultID: 1, Path: `blocked\note.md`}).Error)

	// Invalid traversal rows are skipped permanently, but the injected row
	// failure still makes this run retryable.
	require.Error(t, env.migrate(t))

	var got model.Note
	require.NoError(t, ndb.First(&got, 1).Error)
	require.Equal(t, "dir/one.md", got.Path)
	require.Equal(t, util.EncodeHash32(got.Path), got.PathHash)
	require.NotZero(t, got.UpdatedTimestamp)

	var h model.NoteHistory
	require.NoError(t, hdb.First(&h, 1).Error)
	require.Equal(t, got.Path, h.Path)
	require.Equal(t, "history", h.Content)

	var link, noext model.NoteLink
	require.NoError(t, ldb.First(&link, 1).Error)
	require.Equal(t, got.Path, link.TargetPath)
	require.Equal(t, got.PathHash, link.TargetPathHash)
	require.NoError(t, ldb.First(&noext, 2).Error)
	require.Equal(t, "dir/one", noext.TargetPath)
	require.Equal(t, util.EncodeHash32("dir/one"), noext.TargetPathHash)

	var fts model.NoteFTS
	require.NoError(t, ndb.First(&fts, 1).Error)
	require.Equal(t, got.Path, fts.Path)
	require.Equal(t, "body", fts.Content)

	// Tombstone whose target is another tombstone is removed, not renamed and
	// not linked to a conflict copy. Its link is left untouched.
	var removed model.Note
	require.ErrorIs(t, ndb.First(&removed, 3).Error, gorm.ErrRecordNotFound)
	var tombstoneLink model.NoteLink
	require.NoError(t, ldb.First(&tombstoneLink, 3).Error)
	require.Equal(t, `a\b.md`, tombstoneLink.TargetPath)

	// Free target: tombstone is normalized in place.
	var gone model.Note
	require.NoError(t, ndb.First(&gone, 10).Error)
	require.Equal(t, "gone/away.md", gone.Path)
	require.Equal(t, util.EncodeHash32(gone.Path), gone.PathHash)

	// Live row takes the canonical path over a tombstone; the tombstone is
	// removed so sync never emits a delete for the live path.
	var freeSlot model.Note
	require.NoError(t, ndb.First(&freeSlot, 9).Error)
	require.Equal(t, "free/slot.md", freeSlot.Path)
	var staleTarget model.Note
	require.ErrorIs(t, ndb.First(&staleTarget, 11).Error, gorm.ErrRecordNotFound)
	require.Equal(t, int64(1), countRows(t, ndb, "note", "vault_id = ? AND path = ?", 1, "free/slot.md"))

	// Invalid path is skipped and left for a later manual fix.
	var bad model.Note
	require.NoError(t, ndb.First(&bad, 4).Error)
	require.Equal(t, `bad\..\x.md`, bad.Path)

	var last, later, blocked, target model.Note
	require.NoError(t, ndb.First(&last, 5).Error)
	require.Equal(t, "last/ok.md", last.Path)
	require.NoError(t, ndb.First(&later, 7).Error)
	require.Equal(t, "later/note.md", later.Path)
	require.NoError(t, ndb.First(&blocked, 6).Error)
	require.Equal(t, `blocked\note.md`, blocked.Path)
	require.NoError(t, ndb.First(&target, 8).Error)
	require.Equal(t, "blocked/note.md", target.Path)

	// Conflict target was durably reserved before the failing update.
	var plan backslashPathPlan
	require.NoError(t, ndb.Where("kind = ? AND resource_id = ?", "note", 6).Take(&plan).Error)
	require.True(t, strings.HasPrefix(plan.Target, "blocked/note.conflict."))

	for _, entry := range []struct{ key, table, want string }{
		{"user_file_1", "file", "f/x.png"}, {"user_folder_1", "folder", "d/sub"}, {"user_setting_1", "setting", ".obsidian/app.json"},
	} {
		var row struct {
			Path, PathHash   string
			UpdatedTimestamp int64
		}
		require.NoError(t, env.d.ResolveDB(entry.key).Table(entry.table).Where("id = ?", 1).Take(&row).Error)
		require.Equal(t, entry.want, row.Path)
		require.Equal(t, util.EncodeHash32(row.Path), row.PathHash)
		require.NotZero(t, row.UpdatedTimestamp)
	}

	require.NoError(t, ndb.Exec("DROP TRIGGER fail_path").Error)
	require.NoError(t, env.migrate(t))

	var recovered model.Note
	require.NoError(t, ndb.First(&recovered, 6).Error)
	require.Equal(t, plan.Target, recovered.Path)
	var recoveredHistory model.NoteHistory
	require.NoError(t, hdb.First(&recoveredHistory, 6).Error)
	require.Equal(t, recovered.Path, recoveredHistory.Path)

	// Invalid row remains skipped and the re-run is idempotent for migrated rows.
	var again model.Note
	require.NoError(t, ndb.First(&again, 1).Error)
	require.Equal(t, got.UpdatedTimestamp, again.UpdatedTimestamp)
	require.NoError(t, ndb.First(&bad, 4).Error)
	require.Equal(t, `bad\..\x.md`, bad.Path)
	require.NoError(t, env.migrate(t))
}

func TestBackslashPathMigrationTargetStates(t *testing.T) {
	type row struct {
		path, action string
	}
	for _, tc := range []struct {
		name         string
		table        string
		source       row
		target       *row
		wantPath     string // empty means the source row must be removed
		wantTarget   string // expected path of the pre-existing target row
		wantConflict bool
		targetGone   bool // live source takes over: obsolete target tombstone is removed
	}{
		{"note/active/free", "note", row{`a\b.md`, "create"}, nil, "a/b.md", "", false, false},
		{"note/active/tombstone", "note", row{`a\b.md`, "create"}, &row{"a/b.md", "delete"}, "a/b.md", "", false, true},
		{"note/active/active", "note", row{`a\b.md`, "create"}, &row{"a/b.md", "create"}, "a/b.conflict.", "a/b.md", true, false},
		{"note/tombstone/free", "note", row{`a\b.md`, "delete"}, nil, "a/b.md", "", false, false},
		{"note/tombstone/tombstone", "note", row{`a\b.md`, "delete"}, &row{"a/b.md", "delete"}, "", "a/b.md", false, false},
		{"note/tombstone/active", "note", row{`a\b.md`, "delete"}, &row{"a/b.md", "create"}, "", "a/b.md", false, false},
		{"file/active/tombstone", "file", row{`a\b.md`, "create"}, &row{"a/b.md", "delete"}, "a/b.md", "", false, true},
		{"file/active/active", "file", row{`a\b.md`, "create"}, &row{"a/b.md", "create"}, "a/b.conflict.", "a/b.md", true, false},
		{"file/tombstone/free", "file", row{`a\b.md`, "delete"}, nil, "a/b.md", "", false, false},
		{"file/tombstone/tombstone", "file", row{`a\b.md`, "delete"}, &row{"a/b.md", "delete"}, "", "a/b.md", false, false},
		{"file/tombstone/active", "file", row{`a\b.md`, "delete"}, &row{"a/b.md", "create"}, "", "a/b.md", false, false},
		{"folder/active/tombstone", "folder", row{`a\b`, "create"}, &row{"a/b", "delete"}, "a/b", "", false, true},
		{"folder/active/active", "folder", row{`a\b`, "create"}, &row{"a/b", "create"}, "a/b.conflict.", "a/b", true, false},
		{"folder/tombstone/free", "folder", row{`a\b`, "delete"}, nil, "a/b", "", false, false},
		{"folder/tombstone/tombstone", "folder", row{`a\b`, "delete"}, &row{"a/b", "delete"}, "", "a/b", false, false},
		{"folder/tombstone/active", "folder", row{`a\b`, "delete"}, &row{"a/b", "create"}, "", "a/b", false, false},
		{"setting/active/tombstone", "setting", row{`a\b.json`, "create"}, &row{"a/b.json", "delete"}, "a/b.json", "", false, true},
		{"setting/active/active", "setting", row{`a\b.json`, "create"}, &row{"a/b.json", "create"}, "a/b.conflict.", "a/b.json", true, false},
		{"setting/tombstone/tombstone", "setting", row{`a\b.json`, "delete"}, &row{"a/b.json", "delete"}, "", "a/b.json", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newBackslashTestEnv(t)
			tableDB := map[string]*gorm.DB{"note": env.ndb, "file": env.fdb, "folder": env.folderDB, "setting": env.sdb}[tc.table]
			require.NoError(t, tableDB.Table(tc.table).Create(map[string]any{
				"id": 1, "vault_id": 1, "path": tc.source.path, "action": tc.source.action, "path_hash": "source",
			}).Error)
			if tc.target != nil {
				require.NoError(t, tableDB.Table(tc.table).Create(map[string]any{
					"id": 2, "vault_id": 1, "path": tc.target.path, "action": tc.target.action, "path_hash": "target",
				}).Error)
			}
			require.NoError(t, env.migrate(t))

			var source struct {
				Path, PathHash string
			}
			err := tableDB.Table(tc.table).Where("id = ?", 1).Take(&source).Error
			if tc.wantPath == "" {
				require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			} else {
				require.NoError(t, err)
				if tc.wantConflict {
					require.True(t, strings.HasPrefix(source.Path, tc.wantPath), source.Path)
				} else {
					require.Equal(t, tc.wantPath, source.Path)
				}
				require.Equal(t, util.EncodeHash32(source.Path), source.PathHash)
			}
			if tc.target != nil {
				var target struct{ Path string }
				err := tableDB.Table(tc.table).Where("id = ?", 2).Take(&target).Error
				if tc.targetGone {
					require.ErrorIs(t, err, gorm.ErrRecordNotFound)
				} else {
					require.NoError(t, err)
					require.Equal(t, tc.wantTarget, target.Path)
				}
			}
		})
	}
}

// TestBackslashPathMigrationFolderConflictKeepsTree verifies that when two live
// directories collapse onto one path, the renamed directory keeps its children
// under the conflict name instead of scattering them into the other directory.
// TestBackslashPathMigrationFolderConflictKeepsTree 验证两个存活目录归一到同一
// 路径时，被改名的目录会带着子项一起进入冲突目录，而不是散落到另一个目录里。
func TestBackslashPathMigrationFolderConflictKeepsTree(t *testing.T) {
	env := newBackslashTestEnv(t)
	require.NoError(t, env.folderDB.Create(&model.Folder{ID: 1, VaultID: 1, Path: `d\sub`, Action: "create"}).Error)
	require.NoError(t, env.folderDB.Create(&model.Folder{ID: 2, VaultID: 1, Path: "d/sub", Action: "create"}).Error)
	require.NoError(t, env.folderDB.Create(&model.Folder{ID: 3, VaultID: 1, Path: `d\sub\nested`, Action: "create", FID: 1}).Error)
	require.NoError(t, env.ndb.Create(&model.Note{ID: 1, VaultID: 1, Path: `d\sub\x.md`, Action: "create", FID: 1}).Error)
	require.NoError(t, env.ndb.Create(&model.Note{ID: 2, VaultID: 1, Path: "d/sub/y.md", Action: "create", FID: 2}).Error)
	require.NoError(t, env.fdb.Create(&model.File{ID: 1, VaultID: 1, Path: `d\sub\f.png`, Action: "create", FID: 1}).Error)

	require.NoError(t, env.migrate(t))

	var renamed model.Folder
	require.NoError(t, env.folderDB.First(&renamed, 1).Error)
	require.True(t, strings.HasPrefix(renamed.Path, "d/sub.conflict."), renamed.Path)
	var kept model.Folder
	require.NoError(t, env.folderDB.First(&kept, 2).Error)
	require.Equal(t, "d/sub", kept.Path)

	var nested model.Folder
	require.NoError(t, env.folderDB.First(&nested, 3).Error)
	require.Equal(t, renamed.Path+"/nested", nested.Path)
	require.Equal(t, renamed.ID, nested.FID)

	var followed model.Note
	require.NoError(t, env.ndb.First(&followed, 1).Error)
	require.Equal(t, renamed.Path+"/x.md", followed.Path)
	require.Equal(t, renamed.ID, followed.FID)
	var untouched model.Note
	require.NoError(t, env.ndb.First(&untouched, 2).Error)
	require.Equal(t, "d/sub/y.md", untouched.Path)

	var file model.File
	require.NoError(t, env.fdb.First(&file, 1).Error)
	require.Equal(t, renamed.Path+"/f.png", file.Path)
}

func TestBackslashPathMigrationSameVersionRetry(t *testing.T) {
	env := newBackslashTestEnv(t)
	ndb, cfg := env.ndb, &config.DatabaseConfig{Type: "sqlite", Path: env.d.Config().Path}
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(t.TempDir()))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.MkdirAll("config", 0755))
	require.NoError(t, os.WriteFile("config/lastVersion", []byte("3.7.0"), 0644))

	require.NoError(t, ndb.Create(&model.Note{ID: 99, VaultID: 1, Path: `manager\retry.md`}).Error)
	require.NoError(t, ndb.Exec("CREATE TRIGGER fail_manager BEFORE UPDATE OF path ON note WHEN OLD.id = 99 BEGIN SELECT RAISE(FAIL, 'retry manager'); END").Error)
	manager := NewMigrationManager(env.db, zap.NewNop(), "3.7.0", cfg, cfg)
	require.NoError(t, manager.Run(context.Background()))
	var applied int64
	env.db.Model(&SchemaVersion{}).Where("version = ?", (&BackslashPathMigrate{}).Version()).Count(&applied)
	require.Zero(t, applied, "failed repair must not be marked applied")

	require.NoError(t, ndb.Exec("DROP TRIGGER fail_manager").Error)
	require.NoError(t, manager.Run(context.Background()))
	env.db.Model(&SchemaVersion{}).Where("version = ?", (&BackslashPathMigrate{}).Version()).Count(&applied)
	require.Equal(t, int64(1), applied, "completed repair marker missing")
	var managerNote model.Note
	require.NoError(t, ndb.First(&managerNote, 99).Error)
	require.Equal(t, "manager/retry.md", managerNote.Path)
}

func countRows(t *testing.T, db *gorm.DB, table, where string, args ...any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table(table).Where(where, args...).Count(&count).Error)
	return count
}
