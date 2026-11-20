package service_test

// 基线条件提交（CAS）与旧客户端零回归测试（真实 SQLite 仓储 + 真实服务层）。
//
// 覆盖：
//   - 预检通过后行被抢先改动：条件写必须拒绝（并发抢写 CAS）；
//   - 用户标记 isConflictResolved 后版本再次变化：仍然拦截（解决标记不跳过版本校验）；
//   - 同内容重建（内容相同但令牌已换）不得被旧基线覆盖；
//   - 墓碑复活必须携带墓碑当前令牌；
//   - 旧客户端（params.Baseline == nil）写入照常成功且刷新 Revision；
//   - 版本令牌在创建/修改/删除/恢复之间严格变化，纯 mtime 校准与幂等重试不变。
//
// Baseline CAS tests against a real SQLite repository and the real service layer.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	servicemocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// baselineEnv 是真实 SQLite 下的笔记服务与仓储组合。
// baselineEnv bundles the real note service and repository on a SQLite database.
type baselineEnv struct {
	svc      service.NoteService
	noteRepo domain.NoteRepository
}

// newBaselineEnv 在临时目录建库并返回真实笔记服务。
// newBaselineEnv creates a temporary database and returns the real note service.
func newBaselineEnv(t *testing.T) *baselineEnv {
	t.Helper()
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join("storage", "database"), 0755))
	dbPath := filepath.Join("storage", "database", "db.sqlite3")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	cfg := &config.DatabaseConfig{Type: "sqlite", Path: dbPath, EnableWriteQueue: util.Ptr(false)}
	daoInst := dao.New(db, context.Background(),
		dao.WithConfig(cfg), dao.WithUserDatabaseConfig(cfg), dao.WithLogger(zap.NewNop()),
		dao.WithBleveManager(dao.NewBleveManager(util.Ptr(false), util.Ptr(false), zap.NewNop())))
	t.Cleanup(func() { daoInst.CleanupConnections(-time.Second) })

	noteRepo := dao.NewNoteRepository(daoInst)
	vaultSvc := new(servicemocks.MockVaultService)
	vaultSvc.On("MustGetID", mock.Anything, int64(1), "vault").Return(int64(1), nil).Maybe()
	vaultSvc.On("UpdateNoteStats", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	folderSvc := new(servicemocks.MockFolderService)
	folderSvc.On("SyncResourceFID", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	shareRepo := new(domainmocks.MockUserShareRepository)
	shareRepo.On("UpdateStatusByRes", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	svc := service.NewNoteService(nil, noteRepo, nil, nil, nil, shareRepo, nil, vaultSvc, folderSvc, nil, nil, nil, nil)
	t.Cleanup(func() {
		// 停掉异步统计防抖定时器，避免测试结束后访问已关闭的临时库。
		// Stop the async stats debounce timer so nothing touches the closed temp DB.
		time.Sleep(10 * time.Millisecond)
	})
	return &baselineEnv{svc: svc, noteRepo: noteRepo}
}

// seed 建一条活跃笔记并返回其当前令牌。
// seed creates a live note and returns its revision token.
func (e *baselineEnv) seed(t *testing.T, path, contentHash string) *dto.NoteDTO {
	t.Helper()
	_, created, err := e.svc.ModifyOrCreate(context.Background(), 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: util.EncodeHash32(path),
		Content: "body-" + contentHash, ContentHash: contentHash,
		Ctime: 1000, Mtime: 1000,
	}, false)
	require.NoError(t, err)
	require.NotEmpty(t, created.Revision, "每次创建都必须生成版本令牌")
	return created
}

// TestBaselineCAS_ConcurrentChangeRejected 预检通过后行被另一设备改动：条件写必须拒绝。
func TestBaselineCAS_ConcurrentChangeRejected(t *testing.T) {
	env := newBaselineEnv(t)
	ctx := context.Background()
	created := env.seed(t, "a.md", "H1")
	oldRevision := created.Revision

	// 另一设备先写入新内容，令牌随之变化。
	// Another device writes first, moving the revision.
	_, other, err := env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: "a.md", PathHash: util.EncodeHash32("a.md"),
		Content: "other", ContentHash: "H2", Ctime: 1000, Mtime: 2000,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: oldRevision},
	}, false)
	require.NoError(t, err)
	require.NotEqual(t, oldRevision, other.Revision)

	// 第一个设备拿着旧令牌继续写：必须被拒绝。
	// The first device still holds the old token: its write must be rejected.
	_, _, err = env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: "a.md", PathHash: util.EncodeHash32("a.md"),
		Content: "stale", ContentHash: "H3", Ctime: 1000, Mtime: 3000,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: oldRevision},
	}, false)
	require.Error(t, err, "旧令牌的写入必须被条件提交拒绝")
	var conflict *dto.BaselineConflictError
	require.True(t, errors.As(err, &conflict), "必须返回可映射为 SyncConflict 的冲突错误，实际 %v", err)
	// 服务端内容也已改变：按设计报 both_modified（令牌变化是前提，内容变化决定原因）。
	// The server content changed too, so both_modified is the accurate reason.
	require.Equal(t, dto.BaselineRejectBothModified, conflict.Reason)
	require.Equal(t, other.Revision, conflict.ServerRevision)
	require.False(t, conflict.ServerDeleted)

	// 服务端内容未被覆盖。
	// The server content was not overwritten.
	got, err := env.noteRepo.GetByPathHashAndPath(ctx, util.EncodeHash32("a.md"), "a.md", 1, 1)
	require.NoError(t, err)
	require.Equal(t, "H2", got.ContentHash)
}

// TestBaselineCAS_ResolvedStillBlockedBySecondChange isConflictResolved 只表示用户做过
// 选择，不能跳过版本校验：提交前版本再次变化必须重新报冲突（设计 §2.6 规则 5）。
func TestBaselineCAS_ResolvedStillBlockedBySecondChange(t *testing.T) {
	env := newBaselineEnv(t)
	ctx := context.Background()
	created := env.seed(t, "a.md", "H1")
	resolvedRevision := created.Revision

	// 用户解决冲突后又有一台设备改动了服务端。
	// After the user resolved, another device changed the server again.
	_, later, err := env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: "a.md", PathHash: util.EncodeHash32("a.md"),
		Content: "third", ContentHash: "H3", Ctime: 1000, Mtime: 2000,
	}, false)
	require.NoError(t, err)
	require.NotEqual(t, resolvedRevision, later.Revision)

	_, _, err = env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: "a.md", PathHash: util.EncodeHash32("a.md"),
		Content: "resolved", ContentHash: "H2", Ctime: 1000, Mtime: 3000,
		IsConflictResolved: true,
		Baseline: &dto.BaselineCommit{
			HasBase: true, BaseHash: "H1", BaseRevision: resolvedRevision, IsConflictResolved: true,
		},
	}, false)
	require.Error(t, err, "isConflictResolved 不得跳过版本校验")
	var conflict *dto.BaselineConflictError
	require.True(t, errors.As(err, &conflict), "实际 %v", err)
	require.Equal(t, dto.BaselineRejectBothModified, conflict.Reason)
	require.Equal(t, later.Revision, conflict.ServerRevision)
}

// TestBaselineCAS_SameContentRebuildNotOverwritten 同内容重建：内容哈希与基线相同但令牌
// 已换，旧基线写入必须被拒绝（#482 的提交侧）。
func TestBaselineCAS_SameContentRebuildNotOverwritten(t *testing.T) {
	env := newBaselineEnv(t)
	ctx := context.Background()
	path, pathHash := "a.md", util.EncodeHash32("a.md")
	created := env.seed(t, path, "H1")
	staleRevision := created.Revision

	// 删除后以相同内容重建：内容哈希回到 H1，令牌是新的。
	// Delete then recreate with the same content: the hash returns to H1, the token is new.
	_, err := env.svc.Delete(ctx, 1, &dto.NoteDeleteRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: staleRevision},
	})
	require.NoError(t, err)
	// 用真实毫秒时间戳重建：mtime/ctime 晚于删除时刻，不会被"陈旧重报"闸门拦下。
	// Rebuild with realistic millisecond timestamps so the stale-revive guard lets it through.
	rebuildAt := timex.Now().UnixMilli() + 1000
	_, rebuiltDTO, err := env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Content: "body-H1", ContentHash: "H1", Ctime: rebuildAt, Mtime: rebuildAt,
	}, false)
	require.NoError(t, err)
	require.Equal(t, "H1", rebuiltDTO.ContentHash, "重建必须真正写入")
	require.Equal(t, "create", rebuiltDTO.Action, "墓碑复活后的状态是活跃")
	rebuilt, err := env.noteRepo.GetByPathHashAndPath(ctx, pathHash, path, 1, 1)
	require.NoError(t, err)
	require.NotEqual(t, staleRevision, rebuilt.Revision, "重建必须产生新令牌")

	_, _, err = env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Content: "stale edit", ContentHash: "H9", Ctime: 1000, Mtime: rebuildAt + 1000,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: staleRevision},
	}, false)
	require.Error(t, err, "同内容重建后旧基线的写入必须被拒绝")
	var conflict *dto.BaselineConflictError
	require.True(t, errors.As(err, &conflict), "实际 %v", err)
	require.Equal(t, rebuilt.Revision, conflict.ServerRevision)
}

// TestBaselineCAS_TombstoneReviveNeedsCurrentRevision 墓碑复活必须携带墓碑当前令牌。
func TestBaselineCAS_TombstoneReviveNeedsCurrentRevision(t *testing.T) {
	env := newBaselineEnv(t)
	ctx := context.Background()
	path, pathHash := "a.md", util.EncodeHash32("a.md")
	created := env.seed(t, path, "H1")

	deleted, err := env.svc.Delete(ctx, 1, &dto.NoteDeleteRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: created.Revision},
	})
	require.NoError(t, err)
	require.NotEqual(t, created.Revision, deleted.Revision, "删除必须产生新令牌")

	// 未标记解决：不得静默复活。
	// Not resolved: never revive silently.
	_, _, err = env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Content: "revive", ContentHash: "H2", Ctime: 2000, Mtime: 2000,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: deleted.Revision},
	}, false)
	require.Error(t, err)
	var conflict *dto.BaselineConflictError
	require.True(t, errors.As(err, &conflict), "实际 %v", err)
	require.Equal(t, dto.BaselineRejectRemoteDeleted, conflict.Reason)
	require.True(t, conflict.ServerDeleted)

	// 标记解决但令牌不是墓碑当前令牌：仍然拒绝。
	// Resolved with a stale tombstone token: still rejected.
	_, _, err = env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Content: "revive", ContentHash: "H2", Ctime: 2000, Mtime: 2000,
		IsConflictResolved: true,
		Baseline: &dto.BaselineCommit{
			HasBase: true, BaseHash: "H1", BaseRevision: created.Revision, IsConflictResolved: true,
		},
	}, false)
	require.Error(t, err, "令牌与墓碑不一致时不得复活")
	require.True(t, errors.As(err, &conflict))
	require.Equal(t, dto.BaselineRejectRevisionMoved, conflict.Reason)

	// 标记解决且令牌等于墓碑当前令牌：允许复活。
	// Resolved with the tombstone's current token: revive allowed.
	_, revived, err := env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Content: "revive", ContentHash: "H2", Ctime: 9000, Mtime: 9000,
		IsConflictResolved: true,
		Baseline: &dto.BaselineCommit{
			HasBase: true, BaseHash: "H1", BaseRevision: deleted.Revision, IsConflictResolved: true,
		},
	}, false)
	require.NoError(t, err)
	require.Equal(t, "H2", revived.ContentHash)
	require.NotEqual(t, deleted.Revision, revived.Revision)
}

// TestBaselineCAS_DeleteRequiresMatchingRevision 实时删除：活跃行只有在令牌与内容哈希
// 都匹配时才允许软删除。
func TestBaselineCAS_DeleteRequiresMatchingRevision(t *testing.T) {
	env := newBaselineEnv(t)
	ctx := context.Background()
	path, pathHash := "a.md", util.EncodeHash32("a.md")
	created := env.seed(t, path, "H1")

	// 令牌正确但内容哈希不匹配：说明客户端看到的不是当前内容，拒绝。
	// Correct token but a mismatching content hash: reject.
	_, err := env.svc.Delete(ctx, 1, &dto.NoteDeleteRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "H0", BaseRevision: created.Revision},
	})
	require.Error(t, err)
	var conflict *dto.BaselineConflictError
	require.True(t, errors.As(err, &conflict), "实际 %v", err)
	require.Equal(t, dto.BaselineRejectBothModified, conflict.Reason)

	// 没有令牌：不得授权破坏性删除。
	// No token: never authorizes a destructive delete.
	_, err = env.svc.Delete(ctx, 1, &dto.NoteDeleteRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "H1"},
	})
	require.Error(t, err)
	require.True(t, errors.As(err, &conflict))
	require.Equal(t, dto.BaselineRejectRevisionMoved, conflict.Reason)

	// 全部匹配：条件删除成功并换新令牌。
	// Everything matches: the conditional delete succeeds with a new token.
	deleted, err := env.svc.Delete(ctx, 1, &dto.NoteDeleteRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "H1", BaseRevision: created.Revision},
	})
	require.NoError(t, err)
	require.Equal(t, "delete", deleted.Action)
	require.NotEqual(t, created.Revision, deleted.Revision)
	require.False(t, deleted.Unchanged)
}

// TestLegacyClientWriteRefreshesRevision 旧客户端（Baseline == nil）写入必须照常成功，
// 且同样刷新令牌——否则新客户端无法识别旧客户端造成的变化。
func TestLegacyClientWriteRefreshesRevision(t *testing.T) {
	env := newBaselineEnv(t)
	ctx := context.Background()
	path, pathHash := "legacy.md", util.EncodeHash32("legacy.md")

	_, created, err := env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Content: "v1", ContentHash: "H1", Ctime: 1000, Mtime: 1000,
	}, false)
	require.NoError(t, err)
	require.True(t, util.IsValidRevisionToken(created.Revision), "令牌必须是 32 位十六进制")

	// 旧客户端覆盖写入：无条件成功。
	// A legacy unconditional write succeeds.
	_, updated, err := env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Content: "v2", ContentHash: "H2", Ctime: 1000, Mtime: 2000,
	}, false)
	require.NoError(t, err, "旧客户端写入不得被基线逻辑影响")
	require.Equal(t, "H2", updated.ContentHash)
	require.NotEqual(t, created.Revision, updated.Revision, "旧客户端写入也必须刷新令牌")

	// 旧客户端删除：无条件成功并再换令牌。
	// A legacy delete succeeds and mints another token.
	deleted, err := env.svc.Delete(ctx, 1, &dto.NoteDeleteRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
	})
	require.NoError(t, err)
	require.Equal(t, "delete", deleted.Action)
	require.NotEqual(t, updated.Revision, deleted.Revision)
}

// TestRevisionStableOnMtimeOnlyCalibration 纯 mtime 校准不换令牌（设计 §2.3）。
func TestRevisionStableOnMtimeOnlyCalibration(t *testing.T) {
	env := newBaselineEnv(t)
	ctx := context.Background()
	path, pathHash := "cal.md", util.EncodeHash32("cal.md")
	created := env.seed(t, path, "H1")

	_, calibrated, err := env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Content: "body-H1", ContentHash: "H1", Ctime: 1000, Mtime: 5000,
	}, true)
	require.NoError(t, err)
	require.Equal(t, created.Revision, calibrated.Revision, "纯 mtime 校准不得改变令牌")

	// 幂等重报（内容与 mtime 都一致）同样不换令牌。
	// An idempotent re-report does not change the token either.
	_, again, err := env.svc.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{
		Vault: "vault", Path: path, PathHash: pathHash,
		Content: "body-H1", ContentHash: "H1", Ctime: 1000, Mtime: 5000,
	}, true)
	require.NoError(t, err)
	require.Equal(t, created.Revision, again.Revision)
}

// TestFileServiceBaselineCAS 附件条件提交：活跃行需令牌匹配，墓碑需显式解决。
func TestFileServiceBaselineCAS(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join("storage", "database"), 0755))
	dbPath := filepath.Join("storage", "database", "db.sqlite3")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	cfg := &config.DatabaseConfig{Type: "sqlite", Path: dbPath, EnableWriteQueue: util.Ptr(false)}
	daoInst := dao.New(db, context.Background(),
		dao.WithConfig(cfg), dao.WithUserDatabaseConfig(cfg), dao.WithLogger(zap.NewNop()),
		dao.WithBleveManager(dao.NewBleveManager(util.Ptr(false), util.Ptr(false), zap.NewNop())))
	t.Cleanup(func() { daoInst.CleanupConnections(-time.Second) })

	fileRepo := dao.NewFileRepository(daoInst)
	vaultSvc := new(servicemocks.MockVaultService)
	vaultSvc.On("MustGetID", mock.Anything, int64(1), "vault").Return(int64(1), nil).Maybe()
	folderSvc := new(servicemocks.MockFolderService)
	folderSvc.On("SyncResourceFID", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	svc := service.NewFileService(nil, fileRepo, nil, vaultSvc, folderSvc, nil, nil, nil, nil)
	ctx := context.Background()

	_, created, err := svc.UpdateOrCreate(ctx, 1, &dto.FileUpdateRequest{
		Vault: "vault", Path: "img.png", PathHash: "fh",
		ContentHash: "F1", Size: 10, Ctime: 1000, Mtime: 1000,
	}, false)
	require.NoError(t, err)
	require.True(t, util.IsValidRevisionToken(created.Revision))

	// 令牌不一致：拒绝并保持活跃附件不变。
	// A stale token is rejected and the active attachment stays untouched.
	_, _, err = svc.UpdateOrCreate(ctx, 1, &dto.FileUpdateRequest{
		Vault: "vault", Path: "img.png", PathHash: "fh",
		ContentHash: "F2", Size: 20, Ctime: 1000, Mtime: 2000,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "F1", BaseRevision: "deadbeef"},
	}, false)
	require.Error(t, err)
	var conflict *dto.BaselineConflictError
	require.True(t, errors.As(err, &conflict), "实际 %v", err)
	require.Equal(t, dto.BaselineRejectRevisionMoved, conflict.Reason)

	got, err := fileRepo.GetByPathHashAndPath(ctx, "fh", "img.png", 1, 1)
	require.NoError(t, err)
	require.Equal(t, "F1", got.ContentHash, "被拒绝的上传不得替换活跃附件")

	// 令牌一致：允许提交并换新令牌。
	// A matching token commits and mints a new one.
	_, committed, err := svc.UpdateOrCreate(ctx, 1, &dto.FileUpdateRequest{
		Vault: "vault", Path: "img.png", PathHash: "fh",
		ContentHash: "F2", Size: 20, Ctime: 1000, Mtime: 2000,
		Baseline: &dto.BaselineCommit{HasBase: true, BaseHash: "F1", BaseRevision: created.Revision},
	}, false)
	require.NoError(t, err)
	require.Equal(t, "F2", committed.ContentHash)
	require.NotEqual(t, created.Revision, committed.Revision)

	// 旧客户端（无基线）继续无条件写入。
	// Legacy clients (no baseline) keep writing unconditionally.
	_, legacy, err := svc.UpdateOrCreate(ctx, 1, &dto.FileUpdateRequest{
		Vault: "vault", Path: "img.png", PathHash: "fh",
		ContentHash: "F3", Size: 30, Ctime: 1000, Mtime: 3000,
	}, false)
	require.NoError(t, err)
	require.Equal(t, "F3", legacy.ContentHash)
	require.NotEqual(t, committed.Revision, legacy.Revision)
}
