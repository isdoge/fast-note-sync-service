package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// BackslashPathMigrate is registered independently of release semver: partial
// failures must be retried even when config/lastVersion already equals the binary.
// Successful rows lose their backslash and are naturally idempotent.
type BackslashPathMigrate struct{}

func (*BackslashPathMigrate) Version() string { return "backslash-path-v1" }
func (*BackslashPathMigrate) Description() string {
	return "Normalize legacy vault backslash paths (#451)"
}

var backslashTables = []struct{ prefix, table string }{
	{"user_folder_", "folder"}, {"user_", "note"}, {"user_file_", "file"}, {"user_setting_", "setting"},
}

// A durable target reservation keeps cross-file SQLite repairs resumable if a
// process stops after updating history/links but before updating the resource.
type backslashPathPlan struct {
	Kind       string `gorm:"primaryKey"`
	ResourceID int64  `gorm:"primaryKey;autoIncrement:false"`
	VaultID    int64
	Target     string
	Conflict   bool
}

func (*backslashPathPlan) TableName() string { return "backslash_path_plan" }

type legacyPathRow struct {
	ID, VaultID, UpdatedTimestamp int64
	Path, PathHash, Action        string
}

func (m *BackslashPathMigrate) Up(_ *gorm.DB, ctx context.Context, mc *MigrationContext) error {
	if mc.Dao == nil {
		return fmt.Errorf("dao is nil in migration context")
	}
	logger := mc.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	uids, err := mc.Dao.GetAllUserUIDs()
	if err != nil {
		return err
	}
	migrated := map[string]int{"note": 0, "file": 0, "folder": 0, "setting": 0}
	conflicts := map[string]int{"note": 0, "file": 0, "folder": 0, "setting": 0}
	failed := 0
	for _, uid := range uids {
		for _, entry := range backslashTables {
			db := mc.Dao.ResolveDB(entry.prefix + strconv.FormatInt(uid, 10)).WithContext(ctx)
			if !db.Migrator().HasTable(entry.table) {
				continue
			}
			if err := db.AutoMigrate(&backslashPathPlan{}); err != nil {
				failed++
				logger.Error("backslash migration plan table failed", zap.Error(err))
				continue
			}
			// Go filtering avoids the incompatible SQLite/PostgreSQL LIKE escape rules.
			var rows []legacyPathRow
			if err := db.Table(entry.table).Select("id", "vault_id", "path", "path_hash", "action", "updated_timestamp").Order("id").Find(&rows).Error; err != nil {
				failed++
				logger.Error("backslash migration list failed", zap.Int64("uid", uid), zap.String("table", entry.table), zap.Error(err))
				continue
			}
			for _, row := range rows {
				if !strings.Contains(row.Path, `\`) {
					continue
				}
				target, conflict, err := m.moveRow(ctx, mc.Dao, db, entry.table, uid, row)
				if err != nil {
					failed++
					logger.Error("backslash migration row failed; retry on next startup", zap.Int64("uid", uid), zap.String("table", entry.table), zap.Int64("id", row.ID), zap.String("path", row.Path), zap.Error(err))
					continue
				}
				migrated[entry.table]++
				if conflict {
					conflicts[entry.table]++
					logger.Warn("backslash migration conflict", zap.Int64("uid", uid), zap.String("table", entry.table), zap.String("oldPath", row.Path), zap.String("newPath", target))
				}
			}
		}
	}
	logger.Info("backslash path migration summary", zap.Any("migrated", migrated), zap.Any("conflicts", conflicts), zap.Int("failed", failed))
	if failed > 0 {
		return fmt.Errorf("backslash path migration: %d failures pending retry", failed)
	}
	return nil
}

func (m *BackslashPathMigrate) moveRow(ctx context.Context, d *dao.Dao, db *gorm.DB, table string, uid int64, row legacyPathRow) (string, bool, error) {
	target, ok := util.NormalizeBackslashPath(row.Path)
	if !ok {
		return "", false, fmt.Errorf("invalid legacy vault path")
	}
	base := target
	plan := backslashPathPlan{}
	err := db.Where("kind = ? AND resource_id = ?", table, row.ID).Take(&plan).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, err
	}
	previousTarget := ""
	conflict := false
	if err == nil {
		target = plan.Target
		previousTarget = plan.Target
		conflict = plan.Conflict
	}
	at := time.Now()
	for {
		occupied, err := migrationPathOccupied(ctx, d, uid, row.VaultID, table, row.ID, target)
		if err != nil {
			return "", false, err
		}
		if !occupied {
			break
		}
		conflict = true
		target = util.ConflictPath(base, at)
		at = at.Add(time.Second)
	}
	plan = backslashPathPlan{Kind: table, ResourceID: row.ID, VaultID: row.VaultID, Target: target, Conflict: conflict}
	if err := db.Save(&plan).Error; err != nil {
		return "", false, err
	}
	oldPaths := []string{row.Path}
	if previousTarget != "" {
		oldPaths = append(oldPaths, previousTarget)
	}
	hash := util.EncodeHash32(target)
	now := time.Now()
	stamp := now.UnixMilli()
	if stamp <= row.UpdatedTimestamp {
		stamp = row.UpdatedTimestamp + 1
	}
	updates := map[string]any{"path": target, "path_hash": hash, "updated_timestamp": stamp, "updated_at": timex.Time(now)}
	if table == "folder" || table == "note" || table == "file" {
		parent := ""
		if i := strings.LastIndex(target, "/"); i >= 0 {
			parent = target[:i]
		}
		fid, err := migrationParentFID(ctx, d, uid, row.VaultID, parent)
		if err != nil {
			return "", false, err
		}
		updates["fid"] = fid
		if table == "folder" {
			updates["level"] = int64(strings.Count(target, "/") + 1)
		}
	}
	// Related databases may be separate SQLite files. Write the resource row last:
	// failures leave its backslash in place, making every dependent write retryable.
	if table == "note" {
		hdb := d.ResolveDB("user_note_history_" + strconv.FormatInt(uid, 10)).WithContext(ctx)
		if hdb.Migrator().HasTable("note_history") {
			if err := hdb.Table("note_history").Where("note_id = ? AND path IN ?", row.ID, oldPaths).Update("path", target).Error; err != nil {
				return "", false, err
			}
		}
		ldb := d.ResolveDB("user_note_link_" + strconv.FormatInt(uid, 10)).WithContext(ctx)
		if ldb.Migrator().HasTable("note_link") {
			linkMoves := map[string]string{row.Path: target}
			if previousTarget != "" {
				linkMoves[previousTarget] = target
				linkMoves[strings.TrimSuffix(previousTarget, ".md")] = strings.TrimSuffix(target, ".md")
			}
			newVariations := util.GeneratePathVariations(target)
			for _, old := range util.GeneratePathVariations(row.Path) {
				if !strings.Contains(old, `\`) {
					continue
				}
				normalized, ok := util.NormalizeBackslashPath(old)
				if !ok {
					continue
				}
				depth := strings.Count(normalized, "/")
				if depth < len(newVariations) {
					linkMoves[old] = newVariations[depth]
				}
			}
			for old, newPath := range linkMoves {
				if err := ldb.Table("note_link").Where("vault_id = ? AND target_path = ?", row.VaultID, old).Updates(map[string]any{"target_path": newPath, "target_path_hash": util.EncodeHash32(newPath)}).Error; err != nil {
					return "", false, err
				}
			}
		}
		if err := migrateBlevePath(d, db, uid, row, target); err != nil {
			return "", false, err
		}
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if table == "note" && tx.Migrator().HasTable("note_fts") {
			if err := tx.Table("note_fts").Where("note_id = ?", row.ID).Update("path", target).Error; err != nil {
				return err
			}
		}
		result := tx.Table(table).Where("id = ? AND path = ?", row.ID, row.Path).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("legacy row changed during migration")
		}
		return tx.Delete(&plan).Error
	})
	return target, conflict, err
}

// Notes, attachments and directories share the vault filesystem namespace;
// settings have their own namespace. Include tombstones and pending plans.
func migrationPathOccupied(ctx context.Context, d *dao.Dao, uid, vaultID int64, kind string, id int64, target string) (bool, error) {
	for _, entry := range backslashTables {
		if (kind == "setting") != (entry.table == "setting") {
			continue
		}
		db := d.ResolveDB(entry.prefix + strconv.FormatInt(uid, 10)).WithContext(ctx)
		if !db.Migrator().HasTable(entry.table) {
			continue
		}
		q := db.Table(entry.table).Where("vault_id = ? AND path = ?", vaultID, target)
		if kind == entry.table {
			q = q.Where("id <> ?", id)
		}
		var count int64
		if err := q.Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
		if db.Migrator().HasTable(&backslashPathPlan{}) {
			q = db.Model(&backslashPathPlan{}).Where("kind = ? AND vault_id = ? AND target = ?", entry.table, vaultID, target)
			if kind == entry.table {
				q = q.Where("resource_id <> ?", id)
			}
			if err := q.Count(&count).Error; err != nil {
				return false, err
			}
			if count > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

// Preserve existing ancestors including tombstones; create only missing folders.
func migrationParentFID(ctx context.Context, d *dao.Dao, uid, vaultID int64, parent string) (int64, error) {
	if parent == "" {
		return 0, nil
	}
	db := d.ResolveDB("user_folder_" + strconv.FormatInt(uid, 10)).WithContext(ctx)
	if !db.Migrator().HasTable("folder") {
		if err := db.AutoMigrate(&model.Folder{}); err != nil {
			return 0, err
		}
	}
	var fid int64
	parts := strings.Split(parent, "/")
	for i := range parts {
		path := strings.Join(parts[:i+1], "/")
		var folder model.Folder
		err := db.Where("vault_id = ? AND path = ?", vaultID, path).Order("id").Take(&folder).Error
		if err == nil {
			fid = folder.ID
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, err
		}
		now := timex.Now()
		folder = model.Folder{VaultID: vaultID, Path: path, PathHash: util.EncodeHash32(path), Action: "create", FID: fid, Level: int64(i + 1), Ctime: now.UnixMilli(), Mtime: now.UnixMilli(), UpdatedTimestamp: now.UnixMilli(), CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&folder).Error; err != nil {
			return 0, err
		}
		fid = folder.ID
	}
	return fid, nil
}

// Existing Bleve indexes are keyed by note ID; update the indexed document in
// place without rebuilding or changing its store-raw configuration.
func migrateBlevePath(d *dao.Dao, db *gorm.DB, uid int64, row legacyPathRow, target string) error {
	mgr := d.BleveMgr
	if mgr == nil {
		disabled := false
		mgr = dao.NewBleveManager(&disabled, nil, zap.NewNop())
	}
	indexPath := mgr.GetIndexPath(uid, row.VaultID)
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var index bleve.Index
	var err error
	if d.BleveMgr != nil && d.BleveMgr.IsEnabled() {
		index, err = d.BleveMgr.GetIndex(uid, row.VaultID)
	} else {
		index, err = bleve.Open(indexPath)
		if err == nil {
			defer index.Close()
		}
	}
	if err != nil {
		return err
	}
	var note model.Note
	if err := db.First(&note, row.ID).Error; err != nil {
		return err
	}
	content, exists, err := d.LoadContentFromFile(d.GetNoteFolderPath(uid, row.ID), "content.txt")
	if err != nil {
		return err
	}
	if !exists {
		content = note.Content
	}
	id := strconv.FormatInt(row.ID, 10)
	return index.Index(id, dao.BleveNoteDoc{ID: id, Path: target, PathRaw: target, Content: content, Action: note.Action, Rename: float64(note.Rename), Ctime: float64(note.Ctime), Mtime: float64(note.Mtime)})
}
