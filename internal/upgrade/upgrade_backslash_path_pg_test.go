package upgrade

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"go.uber.org/zap"
	gormPostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestBackslashPathMigrationPostgres verifies the one-shot backslash repair on a
// real PostgreSQL instance (dialect-specific path: tenant schemas instead of one
// SQLite file per repository key). It runs only when FNS_TEST_PG_DSN is provided
// by the real PostgreSQL validation step; otherwise it skips.
// TestBackslashPathMigrationPostgres 在真实 PostgreSQL 上验证一次性反斜杠修复
// （方言差异点：租户走 schema 而不是每个仓储 key 一个 SQLite 文件）。只有真实
// PostgreSQL 验证步骤提供 FNS_TEST_PG_DSN 时才执行，否则跳过。
func TestBackslashPathMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("FNS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("FNS_TEST_PG_DSN not set")
	}

	ctx := context.Background()
	db, err := gorm.Open(gormPostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}

	// 用户库与主库同实例同库：extractUserSchema 把所有 user_*_<uid> key 折到
	// user_<uid> schema（ensurePostgresSchema 会自动建 schema）。测试隔离靠前后
	// 清理 user_<uid> 与主库表，不改 DSN 指向的真实库。
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	port, _ := strconv.Atoi(u.Port())
	cfg := &config.DatabaseConfig{Type: "postgres", Host: u.Hostname(), Port: port, UserName: u.User.Username(), Password: pw, Name: "postgres", SSLMode: "disable", EnableWriteQueue: util.Ptr(false)}
	d := dao.New(db, ctx, dao.WithConfig(cfg), dao.WithUserDatabaseConfig(cfg), dao.WithLogger(zap.NewNop()))
	if err := db.Exec(`DROP SCHEMA IF EXISTS user_1 CASCADE`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Exec(`DROP SCHEMA IF EXISTS user_1 CASCADE`).Error })
	if err := db.Exec(`DROP TABLE IF EXISTS "user", schema_version CASCADE`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO "user" (uid, email, username) VALUES (1, 'pg-path@example.com', 'pg')`).Error; err != nil {
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

	// PG 没有 SQLite 的 CREATE TRIGGER RAISE 便利注入法：跳过"单行失败不影响其它行"，
	// 该行为已在 SQLite 集成测试里用真实触发器覆盖；这里验证方言相关的部分。
	mc := &MigrationContext{Dao: d, Logger: zap.NewNop()}
	m := &BackslashPathMigrate{}
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
	if !strings.HasPrefix(conflict.Path, "a/b.conflict.") || conflict.Action != "delete" {
		t.Fatalf("bad conflict tombstone: %+v", conflict)
	}
	var h model.NoteHistory
	hdb.First(&h, 1)
	if h.Path != got.Path {
		t.Fatalf("history: %+v", h)
	}
	var link model.NoteLink
	ldb.First(&link, 1)
	if link.TargetPath != got.Path || link.TargetPathHash != util.EncodeHash32(got.Path) {
		t.Fatalf("link: %+v", link)
	}
	var fts model.NoteFTS
	ndb.First(&fts, 1)
	if fts.Path != got.Path {
		t.Fatalf("fts: %+v", fts)
	}
	// 幂等：清掉故意失败的行后重跑，冲突行不再变化。
	ndb.Delete(&model.Note{}, 4)
	if err := m.Up(db, ctx, mc); err != nil {
		t.Fatal(err)
	}
	var again model.Note
	ndb.First(&again, 3)
	if again.Path != conflict.Path || again.UpdatedTimestamp != conflict.UpdatedTimestamp {
		t.Fatalf("not idempotent: %+v", again)
	}
}
