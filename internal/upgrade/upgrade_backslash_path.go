package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
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

const backslashDeletedAction = "delete"

// errLegacyPathUnmappable marks a legacy path that can never be cleaned
// automatically (empty result or a ".." segment). Such rows are skipped
// permanently and do not prevent the migration from being marked as applied.
// errLegacyPathUnmappable 标记永远无法自动清洗的旧路径（归一后为空，或含
// ".." 段）。这类行永久跳过，但不阻止迁移记录为已完成。
var errLegacyPathUnmappable = errors.New("legacy vault path cannot be normalized")

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

// folderConflict remembers that a directory was renamed to a conflict path so
// its descendants move with it instead of merging into the normalized directory.
// folderConflict 记录目录被改名为冲突路径，使子孙项跟随它迁移，而不是并入
// 规范化后的同名目录。
type folderConflict struct {
	from string // original backslash folder path
	to   string // conflict target path
}

type moveOutcome struct {
	Target         string
	Conflict       bool
	FolderConflict bool
	Removed        bool
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
	cleaned := map[string]int{"note": 0, "file": 0, "folder": 0, "setting": 0}
	skipped := map[string]int{"note": 0, "file": 0, "folder": 0, "setting": 0}
	failed := 0
	// Folder conflicts are collected while processing the folder table (which
	// runs first) so notes/files nested under a conflict-renamed directory move
	// with it. Keyed by uid and vault because vaults namespace paths separately.
	folderConflicts := map[string][]folderConflict{}
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
			rows, err := scanBackslashRows(db, entry.table)
			if err != nil {
				failed++
				logger.Error("backslash migration list failed", zap.Int64("uid", uid), zap.String("table", entry.table), zap.Error(err))
				continue
			}
			if entry.table == "folder" {
				// Process ancestors first so descendant rows can see their conflict
				// mapping regardless of row ID order.
				sort.SliceStable(rows, func(i, j int) bool {
					di, dj := backslashDepth(rows[i].Path), backslashDepth(rows[j].Path)
					if di != dj {
						return di < dj
					}
					return rows[i].ID < rows[j].ID
				})
			}
			for _, row := range rows {
				if !strings.Contains(row.Path, `\`) {
					continue
				}
				var mappings []folderConflict
				if entry.table != "setting" {
					mappings = folderConflicts[conflictScopeKey(uid, row.VaultID)]
				}
				outcome, err := m.moveRow(ctx, mc.Dao, db, entry.table, uid, row, mappings)
				if err != nil {
					if errors.Is(err, errLegacyPathUnmappable) {
						skipped[entry.table]++
						logger.Error("backslash migration skipped unnormalizable path",
							zap.Int64("uid", uid), zap.String("table", entry.table), zap.Int64("id", row.ID), zap.String("path", row.Path))
						continue
					}
					failed++
					logger.Error("backslash migration row failed; retry on next startup", zap.Int64("uid", uid), zap.String("table", entry.table), zap.Int64("id", row.ID), zap.String("path", row.Path), zap.Error(err))
					continue
				}
				if outcome.Removed {
					cleaned[entry.table]++
					logger.Info("backslash migration removed obsolete tombstone",
						zap.Int64("uid", uid), zap.String("table", entry.table), zap.Int64("id", row.ID), zap.String("path", row.Path))
					continue
				}
				migrated[entry.table]++
				if outcome.Conflict {
					conflicts[entry.table]++
					logger.Warn("backslash migration conflict", zap.Int64("uid", uid), zap.String("table", entry.table), zap.String("oldPath", row.Path), zap.String("newPath", outcome.Target))
				}
				if outcome.FolderConflict {
					key := conflictScopeKey(uid, row.VaultID)
					folderConflicts[key] = append(folderConflicts[key], folderConflict{from: row.Path, to: outcome.Target})
				}
			}
		}
	}
	logger.Info("backslash path migration summary",
		zap.Any("migrated", migrated), zap.Any("conflicts", conflicts), zap.Any("cleaned", cleaned), zap.Any("skipped", skipped), zap.Int("failed", failed))
	if failed > 0 {
		return fmt.Errorf("backslash path migration: %d failures pending retry", failed)
	}
	return nil
}

// scanBackslashRows loads only the metadata columns of rows that still contain
// a backslash. The filter is pushed into SQL so large note tables (with their
// content column) are not read into memory. INSTR/STRPOS avoid the different
// LIKE escape rules of SQLite, MySQL and PostgreSQL.
// scanBackslashRows 只读取仍含反斜杠的行的元数据列。过滤下推到 SQL，避免把
// 大笔记表（含正文列）整体读入内存；用 INSTR/STRPOS 规避三种数据库不同的
// LIKE 转义规则。
func scanBackslashRows(db *gorm.DB, table string) ([]legacyPathRow, error) {
	var rows []legacyPathRow
	err := db.Table(table).
		Select("id", "vault_id", "path", "path_hash", "action", "updated_timestamp").
		Where(backslashPathFilter(db)).
		Order("id").
		Find(&rows).Error
	return rows, err
}

func backslashPathFilter(db *gorm.DB) string {
	if db.Dialector.Name() == "postgres" {
		return `strpos(path, chr(92)) > 0`
	}
	// SQLite and MySQL both provide INSTR and CHAR.
	return `instr(path, char(92)) > 0`
}

func backslashDepth(path string) int {
	return strings.Count(path, "/") + strings.Count(path, `\`)
}

func conflictScopeKey(uid, vaultID int64) string {
	return strconv.FormatInt(uid, 10) + ":" + strconv.FormatInt(vaultID, 10)
}

func backslashTableModel(table string) any {
	switch table {
	case "note":
		return &model.Note{}
	case "file":
		return &model.File{}
	case "folder":
		return &model.Folder{}
	case "setting":
		return &model.Setting{}
	}
	return nil
}

func (m *BackslashPathMigrate) moveRow(ctx context.Context, d *dao.Dao, db *gorm.DB, table string, uid int64, row legacyPathRow, mappings []folderConflict) (moveOutcome, error) {
	base, mapped, valid := conflictMappedTarget(row.Path, mappings)
	if !valid {
		return moveOutcome{}, fmt.Errorf("%w: %q", errLegacyPathUnmappable, row.Path)
	}
	if !mapped {
		var ok bool
		base, ok = util.NormalizeBackslashPath(row.Path)
		if !ok {
			return moveOutcome{}, fmt.Errorf("%w: %q", errLegacyPathUnmappable, row.Path)
		}
	}

	plan, hasPlan, err := loadPathPlan(db, table, row.ID)
	if err != nil {
		return moveOutcome{}, err
	}
	if row.Action == backslashDeletedAction {
		return m.moveTombstoneRow(ctx, db, table, uid, row, base, plan, hasPlan)
	}
	return m.moveLiveRow(ctx, d, db, table, uid, row, base, plan, hasPlan)
}

// conflictMappedTarget applies the closest conflict-renamed ancestor. The second
// result reports whether any ancestor mapping matched; the third reports whether
// the remainder after the mapped prefix is a valid path.
// conflictMappedTarget 套用最深的冲突改名祖先。第二个返回值表示是否命中
// 祖先映射，第三个返回值表示映射前缀之后的部分是否是合法路径。
func conflictMappedTarget(path string, mappings []folderConflict) (string, bool, bool) {
	var chosen folderConflict
	found := false
	for _, mapping := range mappings {
		if path != mapping.from && !strings.HasPrefix(path, mapping.from+`\`) {
			continue
		}
		if !found || len(mapping.from) > len(chosen.from) {
			chosen = mapping
			found = true
		}
	}
	if !found {
		return "", false, true
	}
	if path == chosen.from {
		return chosen.to, true, true
	}
	rest := path[len(chosen.from)+1:]
	normalized, ok := util.NormalizeBackslashPath(rest)
	if !ok || normalized == "" {
		return "", true, false
	}
	return chosen.to + "/" + normalized, true, true
}

func loadPathPlan(db *gorm.DB, table string, id int64) (backslashPathPlan, bool, error) {
	var plan backslashPathPlan
	err := db.Where("kind = ? AND resource_id = ?", table, id).Take(&plan).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return plan, false, nil
	}
	if err != nil {
		return plan, false, err
	}
	return plan, true, nil
}

// moveTombstoneRow never creates a conflict copy and never rewrites links.
// A tombstone whose canonical path is already taken is obsolete bookkeeping and
// is removed; otherwise its own path is normalized in place.
// moveTombstoneRow 不创建冲突副本、不改写链接。目标路径已有行时，该墓碑是
// 过期账本，直接清理；否则就地归一它的路径。
func (m *BackslashPathMigrate) moveTombstoneRow(ctx context.Context, db *gorm.DB, table string, uid int64, row legacyPathRow, base string, plan backslashPathPlan, hasPlan bool) (moveOutcome, error) {
	target := base
	if hasPlan {
		target = plan.Target
	}
	occupied, err := migrationPathSameTableAny(db, table, row.VaultID, row.ID, target)
	if err != nil {
		return moveOutcome{}, err
	}
	if occupied {
		if err := removeTombstoneRow(db, table, row, hasPlan); err != nil {
			return moveOutcome{}, err
		}
		return moveOutcome{Removed: true}, nil
	}

	hash := util.EncodeHash32(target)
	now := time.Now()
	stamp := now.UnixMilli()
	if stamp <= row.UpdatedTimestamp {
		stamp = row.UpdatedTimestamp + 1
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Table(table).Where("id = ? AND path = ?", row.ID, row.Path).Updates(map[string]any{
			"path": target, "path_hash": hash, "updated_timestamp": stamp, "updated_at": timex.Time(now),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("legacy row changed during migration")
		}
		if hasPlan {
			return tx.Where("kind = ? AND resource_id = ?", table, row.ID).Delete(&backslashPathPlan{}).Error
		}
		return nil
	})
	if err != nil {
		return moveOutcome{}, err
	}
	return moveOutcome{Target: target}, nil
}

func removeTombstoneRow(db *gorm.DB, table string, row legacyPathRow, hasPlan bool) error {
	prototype := backslashTableModel(table)
	if prototype == nil {
		return fmt.Errorf("unknown backslash table %s", table)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if table == "note" && tx.Migrator().HasTable("note_fts") {
			if err := tx.Where("note_id = ?", row.ID).Delete(&model.NoteFTS{}).Error; err != nil {
				return err
			}
		}
		result := tx.Where("id = ? AND path = ?", row.ID, row.Path).Delete(prototype)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("legacy row changed during migration")
		}
		if hasPlan {
			return tx.Where("kind = ? AND resource_id = ?", table, row.ID).Delete(&backslashPathPlan{}).Error
		}
		return nil
	})
}

func (m *BackslashPathMigrate) moveLiveRow(ctx context.Context, d *dao.Dao, db *gorm.DB, table string, uid int64, row legacyPathRow, base string, plan backslashPathPlan, hasPlan bool) (moveOutcome, error) {
	target := base
	conflict := false
	previousTarget := ""
	if hasPlan {
		target = plan.Target
		previousTarget = plan.Target
		conflict = plan.Conflict
	}
	at := time.Now()
	for {
		// Only live rows occupy a path. Tombstones at the target are obsolete:
		// the live row takes the canonical path and the tombstones are removed
		// in the same transaction, so sync never emits a delete for a live path.
		// 只有存活行占用路径。目标上的墓碑已过期：存活行直接占用规范路径，
		// 并在同一事务里清理这些墓碑，避免同步对存活路径下发删除。
		occupied, err := migrationPathActiveOccupied(ctx, d, uid, row.VaultID, table, row.ID, target)
		if err != nil {
			return moveOutcome{}, err
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
		return moveOutcome{}, err
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
			return moveOutcome{}, err
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
				return moveOutcome{}, err
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
					return moveOutcome{}, err
				}
			}
		}
		if err := migrateBlevePath(d, db, uid, row, target); err != nil {
			return moveOutcome{}, err
		}
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if table == "note" && tx.Migrator().HasTable("note_fts") {
			if err := tx.Table("note_fts").Where("note_id = ?", row.ID).Update("path", target).Error; err != nil {
				return err
			}
		}
		if err := deleteTombstonesAt(tx, table, row.VaultID, target, row.ID); err != nil {
			return err
		}
		result := tx.Table(table).Where("id = ? AND path = ?", row.ID, row.Path).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("legacy row changed during migration")
		}
		return tx.Where("kind = ? AND resource_id = ?", table, row.ID).Delete(&backslashPathPlan{}).Error
	})
	if err != nil {
		return moveOutcome{}, err
	}
	return moveOutcome{Target: target, Conflict: conflict, FolderConflict: table == "folder" && conflict}, nil
}

// deleteTombstonesAt removes deleted rows of the same table sitting on the
// canonical path that a live row is about to take over. Leaving them would make
// ListByLastTime emit a delete for a live path (newest row wins, but a tombstone
// with a newer timestamp would otherwise win).
// deleteTombstonesAt 清理规范路径上同表的删除行，避免存活行接管后被同步流
// 当作删除下发。
func deleteTombstonesAt(tx *gorm.DB, table string, vaultID int64, target string, excludeID int64) error {
	prototype := backslashTableModel(table)
	if prototype == nil {
		return fmt.Errorf("unknown backslash table %s", table)
	}
	var ids []int64
	if err := tx.Table(table).
		Where("vault_id = ? AND path = ? AND action = ? AND id <> ?", vaultID, target, backslashDeletedAction, excludeID).
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if table == "note" && tx.Migrator().HasTable("note_fts") {
		if err := tx.Where("note_id IN ?", ids).Delete(&model.NoteFTS{}).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("kind = ? AND resource_id IN ?", table, ids).Delete(&backslashPathPlan{}).Error; err != nil {
		return err
	}
	return tx.Where("id IN ?", ids).Delete(prototype).Error
}

// Notes, attachments and directories share the vault filesystem namespace;
// settings have their own namespace. Only live rows occupy a canonical path.
// 笔记、附件和目录共享库内文件系统命名空间；配置有独立命名空间。只有存活行
// 才占用规范路径。
func migrationPathActiveOccupied(ctx context.Context, d *dao.Dao, uid, vaultID int64, kind string, id int64, target string) (bool, error) {
	for _, entry := range backslashTables {
		if (kind == "setting") != (entry.table == "setting") {
			continue
		}
		db := d.ResolveDB(entry.prefix + strconv.FormatInt(uid, 10)).WithContext(ctx)
		if !db.Migrator().HasTable(entry.table) {
			continue
		}
		q := db.Table(entry.table).Where("vault_id = ? AND path = ? AND action <> ?", vaultID, target, backslashDeletedAction)
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
		occupied, err := pathPlanOccupied(db, vaultID, entry.table, kind, id, target)
		if err != nil || occupied {
			return occupied, err
		}
	}
	return false, nil
}

// migrationPathSameTableAny reports whether the canonical path already holds
// any row of the same table (regardless of action). Used for tombstone rows,
// which are normalized in place only when the path is free.
// migrationPathSameTableAny 报告规范路径上是否已有同表任意状态的行。用于墓碑行：
// 只有路径空闲时才就地归一。
func migrationPathSameTableAny(db *gorm.DB, table string, vaultID, id int64, target string) (bool, error) {
	if db.Migrator().HasTable(table) {
		var count int64
		if err := db.Table(table).Where("vault_id = ? AND path = ? AND id <> ?", vaultID, target, id).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	if db.Migrator().HasTable(&backslashPathPlan{}) {
		var count int64
		if err := db.Model(&backslashPathPlan{}).Where("kind = ? AND vault_id = ? AND target = ? AND resource_id <> ?", table, vaultID, target, id).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func pathPlanOccupied(db *gorm.DB, vaultID int64, planKind, kind string, id int64, target string) (bool, error) {
	if !db.Migrator().HasTable(&backslashPathPlan{}) {
		return false, nil
	}
	q := db.Model(&backslashPathPlan{}).Where("kind = ? AND vault_id = ? AND target = ?", planKind, vaultID, target)
	if kind == planKind {
		q = q.Where("resource_id <> ?", id)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// Preserve existing ancestors including tombstones; create only missing folders.
func migrationParentFID(ctx context.Context, d *dao.Dao, uid, vaultID int64, parent string) (int64, error) {
	if parent == "" {
		return 0, nil
	}
	db := d.ResolveDB("user_folder_" + strconv.FormatInt(uid, 10)).WithContext(ctx)
	if !db.Migrator().HasTable("folder") {
		// MySQL needs the custom migrator (binary path columns and length-bounded
		// path indexes); plain db.AutoMigrate would create varchar columns and can
		// fail with "index too long" on older servers.
		// MySQL 必须走自定义迁移器（二进制路径列、带长度的路径索引）；原生
		// db.AutoMigrate 会建成 varchar，且在旧版本上可能报索引超长。
		if err := model.AutoMigrate(db, "Folder"); err != nil {
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
