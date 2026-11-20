package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 本文件覆盖阶段 A「墓碑正文与元数据分离」：
//   - 过期墓碑只清理正文，数据库行保留且 updated_timestamp 不变；
//   - 重复清理幂等；
//   - 正文已清理的墓碑 Restore 明确失败，不会恢复出空正文/空附件；
//   - 离线旧副本（同 contentHash、旧 mtime）在正文清理后仍被墓碑闸门拦截；
//   - 同路径重新创建新内容后 content_purged / rename 正确归零；
//   - setting ClearByVault 保留删除墓碑而不是物理删行。
//
// 测试使用真实 SQLite 仓储（与 file_delete_tombstone_test.go 相同的方式），
// 避免只 mock 仓储而漏掉真正的数据库/磁盘副作用。

// retentionVaultService 在 stubVaultService（定义于 stale_revive_guard_test.go）基础上
// 补齐附件统计接口，供 fileService.CountSizeSum 的异步回调使用。
type retentionVaultService struct {
	stubVaultService
}

func (retentionVaultService) UpdateFileStats(ctx context.Context, fileSize, fileCount, vaultID, uid int64) error {
	return nil
}

const retentionTestUID int64 = 1

type retentionFixture struct {
	daoInst     *dao.Dao
	noteRepo    domain.NoteRepository
	fileRepo    domain.FileRepository
	settingRepo domain.SettingRepository
	historyRepo domain.NoteHistoryRepository
	noteSvc     NoteService
	fileSvc     FileService
	settingSvc  SettingService
}

func newRetentionFixture(t *testing.T) *retentionFixture {
	t.Helper()
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join("storage", "database"), 0755))
	dbPath := filepath.Join("storage", "database", "db.sqlite3")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// 附件/配置的 CleanupByTime 通过主库用户表枚举 UID，测试里需要真实用户行。
	// File/setting CleanupByTime enumerate UIDs from the main user table, so the test
	// needs a real user row.
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, db.Create(&model.User{UID: retentionTestUID}).Error)

	cfg := &config.DatabaseConfig{Type: "sqlite", Path: dbPath, EnableWriteQueue: util.Ptr(false)}
	// 全文检索在测试里关闭：笔记写路径会投递 FTS 操作，nil 管理器会 panic。
	// Full-text search is disabled here: the note write path enqueues FTS ops, and a
	// nil manager would panic.
	bleveMgr := dao.NewBleveManager(util.Ptr(false), nil, zap.NewNop())
	daoInst := dao.New(db, context.Background(),
		dao.WithConfig(cfg), dao.WithUserDatabaseConfig(cfg), dao.WithLogger(zap.NewNop()),
		dao.WithBleveManager(bleveMgr))
	t.Cleanup(func() { daoInst.CleanupConnections(-time.Second) })

	noteRepo := dao.NewNoteRepository(daoInst)
	fileRepo := dao.NewFileRepository(daoInst)
	settingRepo := dao.NewSettingRepository(daoInst)
	historyRepo := dao.NewNoteHistoryRepository(daoInst)

	userRepo := new(domainmocks.MockUserRepository)
	userRepo.On("GetAllUIDs", mock.Anything).Return([]int64{retentionTestUID}, nil).Maybe()

	vaultSvc := retentionVaultService{}
	folderSvc := stubFolderService{}

	noteSvc := NewNoteService(userRepo, noteRepo, nil, nil, fileRepo, nil, historyRepo, vaultSvc, folderSvc, nil, nil, nil, nil)
	fileSvc := NewFileService(nil, fileRepo, noteRepo, vaultSvc, folderSvc, nil, nil, nil, nil)
	settingSvc := NewSettingService(settingRepo, vaultSvc, nil, nil)

	f := &retentionFixture{
		daoInst:     daoInst,
		noteRepo:    noteRepo,
		fileRepo:    fileRepo,
		settingRepo: settingRepo,
		historyRepo: historyRepo,
		noteSvc:     noteSvc,
		fileSvc:     fileSvc,
		settingSvc:  settingSvc,
	}
	f.stopDebouncedCounters(t)
	return f
}

// stopDebouncedCounters 阻止 CountSizeSum 注册的 10 秒防抖定时器在测试结束后访问已关闭
// 的临时数据库。
func (f *retentionFixture) stopDebouncedCounters(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		maps := []*sync.Map{f.noteSvc.(*noteService).countTimers, f.fileSvc.(*fileService).countTimers}
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			for _, m := range maps {
				m.Range(func(key, value any) bool {
					if timer, ok := value.(*time.Timer); ok {
						timer.Stop()
					}
					m.Delete(key)
					return true
				})
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

func (f *retentionFixture) noteFolder(noteID int64) string {
	return filepath.Join("storage", "vault", fmt.Sprintf("u_%d", retentionTestUID), "note", fmt.Sprintf("n_%d", noteID))
}

func (f *retentionFixture) noteContentFile(noteID int64) string {
	return filepath.Join(f.noteFolder(noteID), "content.txt")
}

func (f *retentionFixture) fileFolder(fileID int64) string {
	return filepath.Join("storage", "vault", fmt.Sprintf("u_%d", retentionTestUID), "file", fmt.Sprintf("f_%d", fileID))
}

func (f *retentionFixture) settingFolder(settingID int64) string {
	return filepath.Join("storage", "vault", fmt.Sprintf("u_%d", retentionTestUID), "setting", fmt.Sprintf("s_%d", settingID))
}

func requireNotExist(t *testing.T, path, msg string) {
	t.Helper()
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "%s: path %s 仍存在 (err=%v)", msg, path, err)
}

func writeUploadFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "upload.bin")
	require.NoError(t, os.WriteFile(p, []byte(content), 0644))
	return p
}

// createSoftDeletedNote 创建笔记并写入删除墓碑（等价于 noteService.Delete 的写库部分），
// 返回墓碑行与删除时刻（updated_timestamp）。
func (f *retentionFixture) createSoftDeletedNote(t *testing.T, path, pathHash, content, contentHash string) (*domain.Note, int64) {
	t.Helper()
	ctx := context.Background()
	note, err := f.noteRepo.Create(ctx, &domain.Note{
		VaultID: 1, Path: path, PathHash: pathHash,
		Content: content, ContentHash: contentHash, Size: int64(len(content)),
		Ctime: 1000, Mtime: 2000, Action: domain.NoteActionCreate,
	}, retentionTestUID)
	require.NoError(t, err)

	note.Action = domain.NoteActionDelete
	note.Rename = 0
	require.NoError(t, f.noteRepo.UpdateDelete(ctx, note, retentionTestUID))
	require.NotZero(t, note.UpdatedTimestamp)
	return note, note.UpdatedTimestamp
}

func (f *retentionFixture) createSoftDeletedFile(t *testing.T, path, pathHash, payload, contentHash string) (*domain.File, int64) {
	t.Helper()
	ctx := context.Background()
	file, err := f.fileRepo.Create(ctx, &domain.File{
		VaultID: 1, Path: path, PathHash: pathHash,
		ContentHash: contentHash, SavePath: writeUploadFile(t, payload),
		Size: int64(len(payload)), Ctime: 1000, Mtime: 2000, Action: domain.FileActionCreate,
	}, retentionTestUID)
	require.NoError(t, err)

	updated, err := f.fileRepo.Update(ctx, &domain.File{
		ID: file.ID, VaultID: 1, Path: path, PathHash: pathHash,
		ContentHash: contentHash, Size: file.Size, Ctime: file.Ctime, Mtime: file.Mtime,
		Action: domain.FileActionDelete, Rename: 0,
	}, retentionTestUID)
	require.NoError(t, err)
	return updated, updated.UpdatedTimestamp
}

func (f *retentionFixture) createSoftDeletedSetting(t *testing.T, path, pathHash, content, contentHash string) (*domain.Setting, int64) {
	t.Helper()
	ctx := context.Background()
	setting, err := f.settingRepo.Create(ctx, &domain.Setting{
		VaultID: 1, Path: path, PathHash: pathHash,
		Content: content, ContentHash: contentHash, Size: int64(len(content)),
		Ctime: 1000, Mtime: 2000, Action: domain.SettingActionCreate,
	}, retentionTestUID)
	require.NoError(t, err)

	require.NoError(t, f.settingRepo.UpdateActionMtime(ctx, domain.SettingActionDelete, setting.Mtime, setting.ID, retentionTestUID))
	rows, err := f.settingRepo.ListByPathHash(ctx, pathHash, 1, retentionTestUID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, domain.SettingActionDelete, rows[0].Action)
	return rows[0], rows[0].UpdatedTimestamp
}

// TestTombstoneRetention_PurgesContentKeepsMetadata 到期清理只释放正文，墓碑行保留且
// updated_timestamp 不变，二次执行幂等。
func TestTombstoneRetention_PurgesContentKeepsMetadata(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	cutoff := time.Now().UnixMilli() + 1000

	note, noteDeletedAt := f.createSoftDeletedNote(t, "notes/old.md", "ph-note", "old body", "ch-old")
	fileRow, fileDeletedAt := f.createSoftDeletedFile(t, "assets/old.png", "ph-file", "old payload", "fh-old")
	setting, settingDeletedAt := f.createSoftDeletedSetting(t, "config/sync.json", "ph-set", "{\"old\":true}", "sh-old")

	require.FileExists(t, f.noteContentFile(note.ID))
	require.FileExists(t, filepath.Join(f.fileFolder(fileRow.ID), "file.dat"))
	require.FileExists(t, filepath.Join(f.settingFolder(setting.ID), "content.txt"))

	require.NoError(t, f.noteSvc.CleanupByTime(ctx, cutoff))
	require.NoError(t, f.fileSvc.CleanupByTime(ctx, cutoff))
	require.NoError(t, f.settingSvc.CleanupByTime(ctx, cutoff))

	// 磁盘正文已释放
	requireNotExist(t, f.noteFolder(note.ID), "笔记正文目录应被删除")
	requireNotExist(t, f.fileFolder(fileRow.ID), "附件文件应被删除")
	requireNotExist(t, f.settingFolder(setting.ID), "配置正文目录应被删除")

	// 数据库墓碑行仍在，且关键字段保留
	gotNote, err := f.noteRepo.GetByID(ctx, note.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.NoteActionDelete, gotNote.Action)
	require.Equal(t, int64(1), gotNote.ContentPurged)
	require.Equal(t, int64(2), gotNote.Rename)
	require.Equal(t, "ch-old", gotNote.ContentHash, "删除前最后内容哈希必须保留，供墓碑闸门比对")
	require.Equal(t, noteDeletedAt, gotNote.UpdatedTimestamp, "定时清理不得刷新 updated_timestamp")
	require.Equal(t, "", gotNote.Content)

	gotFile, err := f.fileRepo.GetByID(ctx, fileRow.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.FileActionDelete, gotFile.Action)
	require.Equal(t, int64(1), gotFile.ContentPurged)
	require.Equal(t, int64(2), gotFile.Rename)
	require.Equal(t, "fh-old", gotFile.ContentHash)
	require.Equal(t, fileDeletedAt, gotFile.UpdatedTimestamp)

	settingRows, err := f.settingRepo.ListByPathHash(ctx, "ph-set", 1, retentionTestUID)
	require.NoError(t, err)
	require.Len(t, settingRows, 1)
	require.Equal(t, domain.SettingActionDelete, settingRows[0].Action)
	require.Equal(t, int64(1), settingRows[0].ContentPurged)
	require.Equal(t, int64(2), settingRows[0].Rename)
	require.Equal(t, "sh-old", settingRows[0].ContentHash)
	require.Equal(t, settingDeletedAt, settingRows[0].UpdatedTimestamp)

	// 回收站不可恢复列表不再展示已清理墓碑
	noteList, noteCount, err := f.noteSvc.List(ctx, retentionTestUID, &dto.NoteListRequest{Vault: "vault", IsRecycle: true}, &app.Pager{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Zero(t, noteCount)
	require.Empty(t, noteList)
	fileList, fileCount, err := f.fileSvc.List(ctx, retentionTestUID, &dto.FileListRequest{Vault: "vault", IsRecycle: true}, &app.Pager{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Zero(t, fileCount)
	require.Empty(t, fileList)

	// 幂等：再次执行不报错、状态不变
	require.NoError(t, f.noteSvc.CleanupByTime(ctx, cutoff))
	require.NoError(t, f.fileSvc.CleanupByTime(ctx, cutoff))
	require.NoError(t, f.settingSvc.CleanupByTime(ctx, cutoff))

	againNote, err := f.noteRepo.GetByID(ctx, note.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, gotNote.UpdatedTimestamp, againNote.UpdatedTimestamp)
	require.Equal(t, int64(1), againNote.ContentPurged)

	againFile, err := f.fileRepo.GetByID(ctx, fileRow.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, gotFile.UpdatedTimestamp, againFile.UpdatedTimestamp)
	require.Equal(t, int64(1), againFile.ContentPurged)

	requireNotExist(t, f.noteFolder(note.ID), "重复清理不得重建正文")
}

// TestTombstoneRetention_RestoreRejected 正文已清理的墓碑必须明确拒绝恢复，
// 且不会产生空正文/空附件。
func TestTombstoneRetention_RestoreRejected(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	cutoff := time.Now().UnixMilli() + 1000

	note, _ := f.createSoftDeletedNote(t, "notes/old.md", "ph-note", "old body", "ch-old")
	fileRow, _ := f.createSoftDeletedFile(t, "assets/old.png", "ph-file", "old payload", "fh-old")

	require.NoError(t, f.noteSvc.CleanupByTime(ctx, cutoff))
	require.NoError(t, f.fileSvc.CleanupByTime(ctx, cutoff))

	_, err := f.noteSvc.Restore(ctx, retentionTestUID, &dto.NoteRestoreRequest{Vault: "vault", Path: note.Path, PathHash: note.PathHash})
	require.ErrorIs(t, err, code.ErrorContentPurged, "正文已清理的笔记必须明确拒绝恢复")
	requireNotExist(t, f.noteContentFile(note.ID), "拒绝恢复时不得创建空正文文件")

	gotNote, err := f.noteRepo.GetByID(ctx, note.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.NoteActionDelete, gotNote.Action)
	require.Equal(t, int64(1), gotNote.ContentPurged)

	_, err = f.fileSvc.Restore(ctx, retentionTestUID, &dto.FileRestoreRequest{Vault: "vault", Path: fileRow.Path, PathHash: fileRow.PathHash})
	require.ErrorIs(t, err, code.ErrorContentPurged, "正文已清理的附件必须明确拒绝恢复")
	requireNotExist(t, f.fileFolder(fileRow.ID), "拒绝恢复时不得创建空附件")

	gotFile, err := f.fileRepo.GetByID(ctx, fileRow.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.FileActionDelete, gotFile.Action)
	require.Equal(t, int64(1), gotFile.ContentPurged)
}

// TestTombstoneRetention_StaleUploadStillBlocked 正文清理后，离线旧副本（同 contentHash、
// 旧 mtime/ctime）上传仍被墓碑闸门拦截，不复活。
func TestTombstoneRetention_StaleUploadStillBlocked(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	cutoff := time.Now().UnixMilli() + 1000

	note, _ := f.createSoftDeletedNote(t, "notes/old.md", "ph-note", "old body", "ch-old")
	fileRow, _ := f.createSoftDeletedFile(t, "assets/old.png", "ph-file", "old payload", "fh-old")

	require.NoError(t, f.noteSvc.CleanupByTime(ctx, cutoff))
	require.NoError(t, f.fileSvc.CleanupByTime(ctx, cutoff))

	_, _, err := f.noteSvc.ModifyOrCreate(ctx, retentionTestUID, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: note.Path, PathHash: note.PathHash,
		Content: "old body", ContentHash: "ch-old", Mtime: 2000, Ctime: 1000,
	}, true)
	require.ErrorIs(t, err, code.ErrorStaleReviveRejected, "正文清理后旧副本上传仍必须被墓碑闸门拦截")

	_, _, err = f.fileSvc.UpdateOrCreate(ctx, retentionTestUID, &dto.FileUpdateRequest{
		Vault: "vault", Path: fileRow.Path, PathHash: fileRow.PathHash,
		ContentHash: "fh-old", Size: int64(len("old payload")), Mtime: 2000, Ctime: 1000,
	}, true)
	require.ErrorIs(t, err, code.ErrorStaleReviveRejected, "正文清理后旧附件上传仍必须被墓碑闸门拦截")

	gotNote, err := f.noteRepo.GetByID(ctx, note.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.NoteActionDelete, gotNote.Action)
	require.Equal(t, int64(1), gotNote.ContentPurged)

	gotFile, err := f.fileRepo.GetByID(ctx, fileRow.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.FileActionDelete, gotFile.Action)
	require.Equal(t, int64(1), gotFile.ContentPurged)
}

// TestTombstoneRetention_RecreateResetsFlags 同一路径重新创建新内容后，
// content_purged 与 rename 必须归零，读取/同步/再次删除与恢复全部正常。
func TestTombstoneRetention_RecreateResetsFlags(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	cutoff := time.Now().UnixMilli() + 1000

	note, noteDeletedAt := f.createSoftDeletedNote(t, "notes/old.md", "ph-note", "old body", "ch-old")
	fileRow, fileDeletedAt := f.createSoftDeletedFile(t, "assets/old.png", "ph-file", "old payload", "fh-old")
	setting, _ := f.createSoftDeletedSetting(t, "config/sync.json", "ph-set", "{\"old\":true}", "sh-old")

	require.NoError(t, f.noteSvc.CleanupByTime(ctx, cutoff))
	require.NoError(t, f.fileSvc.CleanupByTime(ctx, cutoff))
	require.NoError(t, f.settingSvc.CleanupByTime(ctx, cutoff))

	freshMtime := time.Now().UnixMilli() + 60_000

	// 笔记：新内容 -> 复用墓碑行复活
	_, noteDTO, err := f.noteSvc.ModifyOrCreate(ctx, retentionTestUID, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: note.Path, PathHash: note.PathHash,
		Content: "fresh body", ContentHash: "ch-fresh", Mtime: freshMtime, Ctime: noteDeletedAt + 60_000,
	}, true)
	require.NoError(t, err)
	require.Equal(t, "ch-fresh", noteDTO.ContentHash)

	gotNote, err := f.noteRepo.GetByID(ctx, note.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.NoteActionCreate, gotNote.Action, "复活行应为存活状态")
	require.Zero(t, gotNote.ContentPurged, "复活后 content_purged 必须归零")
	require.Zero(t, gotNote.Rename, "复活后 rename 必须归零")
	require.Equal(t, "fresh body", gotNote.Content)
	require.FileExists(t, f.noteContentFile(note.ID))
	stored, err := os.ReadFile(f.noteContentFile(note.ID))
	require.NoError(t, err)
	require.Equal(t, "fresh body", string(stored))

	// 附件：新内容 -> 复用墓碑行复活
	_, fileDTO, err := f.fileSvc.UpdateOrCreate(ctx, retentionTestUID, &dto.FileUpdateRequest{
		Vault: "vault", Path: fileRow.Path, PathHash: fileRow.PathHash,
		ContentHash: "fh-fresh", SavePath: writeUploadFile(t, "fresh payload"),
		Size: int64(len("fresh payload")), Mtime: freshMtime, Ctime: fileDeletedAt + 60_000,
	}, true)
	require.NoError(t, err)
	require.Equal(t, "fh-fresh", fileDTO.ContentHash)

	gotFile, err := f.fileRepo.GetByID(ctx, fileRow.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.FileActionCreate, gotFile.Action)
	require.Zero(t, gotFile.ContentPurged)
	require.Zero(t, gotFile.Rename)
	require.FileExists(t, filepath.Join(f.fileFolder(fileRow.ID), "file.dat"))

	// 配置：新内容 -> 复用墓碑行复活
	_, settingDTO, err := f.settingSvc.ModifyOrCreate(ctx, retentionTestUID, &dto.SettingModifyOrCreateRequest{
		Vault: "vault", Path: setting.Path, PathHash: setting.PathHash,
		Content: "{\"fresh\":true}", ContentHash: "sh-fresh", Mtime: freshMtime, Ctime: setting.Mtime,
	}, true)
	require.NoError(t, err)
	require.Equal(t, "sh-fresh", settingDTO.ContentHash)

	settingRows, err := f.settingRepo.ListByPathHash(ctx, setting.PathHash, 1, retentionTestUID)
	require.NoError(t, err)
	require.Len(t, settingRows, 1)
	require.Equal(t, domain.SettingActionCreate, settingRows[0].Action)
	require.Zero(t, settingRows[0].ContentPurged)
	require.Zero(t, settingRows[0].Rename)
	require.Equal(t, "{\"fresh\":true}", settingRows[0].Content)
}

// TestTombstoneRetention_RecycleClearPurgesContent 手动清空回收站应立即释放正文并标
// content_purged=1（不可恢复），同时保留墓碑行与 content_hash。
func TestTombstoneRetention_RecycleClearPurgesContent(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()

	note, _ := f.createSoftDeletedNote(t, "notes/cleared.md", "ph-note-clear", "cleared body", "ch-clear")
	fileRow, _ := f.createSoftDeletedFile(t, "assets/cleared.png", "ph-file-clear", "cleared payload", "fh-clear")

	require.NoError(t, f.noteSvc.RecycleClear(ctx, retentionTestUID, &dto.NoteRecycleClearRequest{Vault: "vault", Path: note.Path, PathHash: note.PathHash}))
	require.NoError(t, f.fileSvc.RecycleClear(ctx, retentionTestUID, &dto.FileRecycleClearRequest{Vault: "vault", Path: fileRow.Path, PathHash: fileRow.PathHash}))

	requireNotExist(t, f.noteFolder(note.ID), "手动清空回收站应删除笔记正文目录")
	requireNotExist(t, f.fileFolder(fileRow.ID), "手动清空回收站应删除附件文件")

	gotNote, err := f.noteRepo.GetByID(ctx, note.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.NoteActionDelete, gotNote.Action)
	require.Equal(t, int64(1), gotNote.ContentPurged)
	require.Equal(t, int64(2), gotNote.Rename)
	require.Equal(t, "ch-clear", gotNote.ContentHash)

	gotFile, err := f.fileRepo.GetByID(ctx, fileRow.ID, retentionTestUID)
	require.NoError(t, err)
	require.Equal(t, domain.FileActionDelete, gotFile.Action)
	require.Equal(t, int64(1), gotFile.ContentPurged)
	require.Equal(t, int64(2), gotFile.Rename)
	require.Equal(t, "fh-clear", gotFile.ContentHash)

	// 清空后带旧路径恢复必须明确失败
	_, err = f.noteSvc.Restore(ctx, retentionTestUID, &dto.NoteRestoreRequest{Vault: "vault", Path: note.Path, PathHash: note.PathHash})
	require.ErrorIs(t, err, code.ErrorContentPurged)
	_, err = f.fileSvc.Restore(ctx, retentionTestUID, &dto.FileRestoreRequest{Vault: "vault", Path: fileRow.Path, PathHash: fileRow.PathHash})
	require.ErrorIs(t, err, code.ErrorContentPurged)
}

// TestSettingClearByVault_KeepsTombstones 清空全部配置必须转为删除墓碑而不是物理删行。
func TestSettingClearByVault_KeepsTombstones(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()

	ctx1, err := f.settingRepo.Create(ctx, &domain.Setting{
		VaultID: 1, Path: "config/a.json", PathHash: "ph-set-a",
		Content: "{\"a\":1}", ContentHash: "sh-a", Size: 7, Mtime: 1000, Action: domain.SettingActionCreate,
	}, retentionTestUID)
	require.NoError(t, err)
	ctx2, err := f.settingRepo.Create(ctx, &domain.Setting{
		VaultID: 1, Path: "config/b.json", PathHash: "ph-set-b",
		Content: "{\"b\":2}", ContentHash: "sh-b", Size: 7, Mtime: 1000, Action: domain.SettingActionCreate,
	}, retentionTestUID)
	require.NoError(t, err)
	otherVault, err := f.settingRepo.Create(ctx, &domain.Setting{
		VaultID: 2, Path: "config/c.json", PathHash: "ph-set-c",
		Content: "{\"c\":3}", ContentHash: "sh-c", Size: 7, Mtime: 1000, Action: domain.SettingActionCreate,
	}, retentionTestUID)
	require.NoError(t, err)

	before := time.Now().UnixMilli()
	require.NoError(t, f.settingSvc.ClearByVault(ctx, retentionTestUID, "vault"))

	// 墓碑行仍在，且带有删除时间与最后内容哈希
	for _, row := range []*domain.Setting{ctx1, ctx2} {
		rows, err := f.settingRepo.ListByPathHash(ctx, row.PathHash, 1, retentionTestUID)
		require.NoError(t, err)
		require.Len(t, rows, 1, "清空全部配置不能物理删行")
		require.Equal(t, domain.SettingActionDelete, rows[0].Action)
		require.Equal(t, int64(2), rows[0].Rename)
		require.Equal(t, int64(1), rows[0].ContentPurged)
		require.Equal(t, row.ContentHash, rows[0].ContentHash, "必须保留删除前最后内容哈希")
		require.GreaterOrEqual(t, rows[0].UpdatedTimestamp, before, "清空操作必须刷新 updated_timestamp，供其它客户端增量同步")
		require.Equal(t, "", rows[0].Content)
		requireNotExist(t, f.settingFolder(row.ID), "清空全部配置应释放正文文件")
	}

	// 其它仓库不受影响
	otherRows, err := f.settingRepo.ListByPathHash(ctx, otherVault.PathHash, 2, retentionTestUID)
	require.NoError(t, err)
	require.Len(t, otherRows, 1)
	require.Equal(t, domain.SettingActionCreate, otherRows[0].Action)
	require.Zero(t, otherRows[0].ContentPurged)
	require.FileExists(t, filepath.Join(f.settingFolder(otherVault.ID), "content.txt"))

	// 增量同步（lastTime=0）仍能拿到这两条删除记录
	synced, err := f.settingRepo.ListByUpdatedTimestamp(ctx, 0, 1, retentionTestUID)
	require.NoError(t, err)
	deleted := 0
	for _, s := range synced {
		if s.Action == domain.SettingActionDelete {
			deleted++
		}
	}
	require.Equal(t, 2, deleted, "删除墓碑必须仍可被增量同步读取")
}
