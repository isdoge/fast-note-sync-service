package service

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// 回归测试：服务端「墓碑闸门」——陈旧重报不得复活已删资源
// Capture tests: stale uploads must not revive server-deleted resources
//
// 背景：客户端本地同步状态（folderSnapshot / hashManager）失效后会周期性
// 全量重报整个 vault；服务端此前对 action=delete 的记录无条件复活（笔记、
// 附件、目录三处），形成"删了又被顶回来"的复活拉锯。
// 修复：以墓碑记录的 updated_timestamp 为删除时刻，上传的 mtime 与 ctime
// 均早于「删除时刻 - 容差」时判定为陈旧重报 → 拒绝复活。
// 修复前，本文件中期望"拒绝"的用例必然 FAIL（复活发生）；修复后 PASS。
// ---------------------------------------------------------------------------

const testDeletedAt = int64(1_700_000_000_000)

// stubVaultService 仅覆盖测试所需的 VaultService 子集（其余方法不会被调用）。
type stubVaultService struct {
	VaultService
}

func (stubVaultService) MustGetID(ctx context.Context, uid int64, name string) (int64, error) {
	return 1, nil
}

func (stubVaultService) UpdateNoteStats(ctx context.Context, noteSize, noteCount, vaultID, uid int64) error {
	return nil
}

// stubFolderService 仅覆盖 SyncResourceFID（成功路径会异步调用它）。
type stubFolderService struct {
	FolderService
}

func (stubFolderService) SyncResourceFID(ctx context.Context, uid int64, vaultID int64, noteIDs []int64, fileIDs []int64) error {
	return nil
}

// ---------------------------------------------------------------------------
// Section A：isStaleRevive 判定表
// ---------------------------------------------------------------------------

func TestIsStaleRevive_Table(t *testing.T) {
	tests := []struct {
		name      string
		mtime     int64
		ctime     int64
		deletedAt int64
		want      bool
	}{
		{"both timestamps long before deletion -> stale", testDeletedAt - 10_000_000, testDeletedAt - 10_000_000, testDeletedAt, true},
		{"mtime after deletion (genuine edit) -> fresh", testDeletedAt + 5_000, testDeletedAt - 10_000_000, testDeletedAt, false},
		{"ctime after deletion (recycle restore) -> fresh", testDeletedAt - 10_000_000, testDeletedAt + 5_000, testDeletedAt, false},
		{"mtime at tolerance edge counts as fresh", testDeletedAt - staleReviveToleranceMs, testDeletedAt - 10_000_000, testDeletedAt, false},
		{"mtime one ms past tolerance -> stale", testDeletedAt - staleReviveToleranceMs - 1, testDeletedAt - 10_000_000, testDeletedAt, true},
		{"both unknown (zero) -> stale", 0, 0, testDeletedAt, true},
		{"ctime unknown, mtime old -> stale", testDeletedAt - 10_000_000, 0, testDeletedAt, true},
		{"ctime unknown, mtime fresh -> fresh", testDeletedAt + 1, 0, testDeletedAt, false},
		{"no tombstone timestamp -> never stale", 0, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isStaleRevive(tc.mtime, tc.ctime, tc.deletedAt),
				"Bug: isStaleRevive(mtime=%d, ctime=%d, deletedAt=%d) 判定错误", tc.mtime, tc.ctime, tc.deletedAt)
		})
	}
}

// ---------------------------------------------------------------------------
// Section B：笔记侧墓碑闸门
// ---------------------------------------------------------------------------

func deletedNoteFixture() *domain.Note {
	return &domain.Note{
		ID:               7,
		VaultID:          1,
		Action:           domain.NoteActionDelete,
		Path:             "skills/old.md",
		PathHash:         "h1",
		Content:          "old content",
		ContentHash:      "oldhash",
		Size:             11,
		Ctime:            100,
		Mtime:            100,
		UpdatedTimestamp: testDeletedAt,
	}
}

// TestNoteService_ModifyOrCreate_StaleReviveRejected：双时间戳均早于删除时刻的
// 上传必须被拒绝且不写库（修复前该上传会把 delete 记录无条件翻回 create）。
func TestNoteService_ModifyOrCreate_StaleReviveRejected(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	noteRepo.On("GetAllByPathHash", mock.Anything, "h1", int64(1), int64(1)).Return(deletedNoteFixture(), nil)

	svc := NewNoteService(nil, noteRepo, nil, nil, nil, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	_, got, err := svc.ModifyOrCreate(context.Background(), 1, &dto.NoteModifyOrCreateRequest{
		Vault:       "vault",
		Path:        "skills/old.md",
		PathHash:    "h1",
		Content:     "old content",
		ContentHash: "oldhash", // 与墓碑一致：原样重报
		Mtime:       100,
		Ctime:       100,
	}, true)

	require.ErrorIs(t, err, code.ErrorStaleReviveRejected,
		"Bug: 内容与墓碑一致、mtime/ctime 均早于删除时刻的上传必须被墓碑闸门拒绝（修复前会被无条件复活）")
	require.Nil(t, got)
	noteRepo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything)
	noteRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything, mock.Anything)
}

// TestNoteService_ModifyOrCreate_FreshMtimeRevivesDeletedNote：mtime 晚于删除时刻
// （用户在删除后真实编辑/重建）= 新意图，必须照常复活。
func TestNoteService_ModifyOrCreate_FreshMtimeRevivesDeletedNote(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	noteRepo.On("GetAllByPathHash", mock.Anything, "h1", int64(1), int64(1)).Return(deletedNoteFixture(), nil)

	updated := deletedNoteFixture()
	updated.Action = domain.NoteActionCreate
	updated.Mtime = testDeletedAt + 60_000
	updated.UpdatedTimestamp = testDeletedAt + 60_000
	noteRepo.On("Update", mock.Anything, mock.AnythingOfType("*domain.Note"), int64(1)).Return(updated, nil)
	noteRepo.On("CountSizeSum", mock.Anything, int64(1), int64(1)).Return(&domain.CountSizeResult{Count: 1, Size: 11}, nil).Maybe()

	svc := NewNoteService(nil, noteRepo, nil, nil, nil, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	isNew, got, err := svc.ModifyOrCreate(context.Background(), 1, &dto.NoteModifyOrCreateRequest{
		Vault:       "vault",
		Path:        "skills/old.md",
		PathHash:    "h1",
		Content:     "genuinely new content",
		ContentHash: "newhash",
		Mtime:       testDeletedAt + 60_000,
		Ctime:       100,
	}, true)

	require.NoError(t, err)
	require.False(t, isNew)
	require.NotNil(t, got)
	noteRepo.AssertCalled(t, "Update", mock.Anything, mock.AnythingOfType("*domain.Note"), int64(1))
}

// TestNoteService_ModifyOrCreate_FreshCtimeRevivesDeletedNote：回收站恢复场景，
// mtime 保留旧值但 ctime（rename 会刷新）晚于删除时刻 → 属于真实恢复，放行。
func TestNoteService_ModifyOrCreate_FreshCtimeRevivesDeletedNote(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	noteRepo.On("GetAllByPathHash", mock.Anything, "h1", int64(1), int64(1)).Return(deletedNoteFixture(), nil)

	updated := deletedNoteFixture()
	updated.Action = domain.NoteActionCreate
	updated.UpdatedTimestamp = testDeletedAt + 60_000
	noteRepo.On("Update", mock.Anything, mock.AnythingOfType("*domain.Note"), int64(1)).Return(updated, nil)
	noteRepo.On("CountSizeSum", mock.Anything, int64(1), int64(1)).Return(&domain.CountSizeResult{Count: 1, Size: 11}, nil).Maybe()

	svc := NewNoteService(nil, noteRepo, nil, nil, nil, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	_, got, err := svc.ModifyOrCreate(context.Background(), 1, &dto.NoteModifyOrCreateRequest{
		Vault:       "vault",
		Path:        "skills/old.md",
		PathHash:    "h1",
		Content:     "restored content",
		ContentHash: "restoredhash",
		Mtime:       100, // 保留旧值
		Ctime:       testDeletedAt + 60_000,
	}, true)

	require.NoError(t, err)
	require.NotNil(t, got)
	noteRepo.AssertCalled(t, "Update", mock.Anything, mock.AnythingOfType("*domain.Note"), int64(1))
}

// ---------------------------------------------------------------------------
// Section C：附件侧墓碑闸门
// ---------------------------------------------------------------------------

func deletedFileFixture() *domain.File {
	return &domain.File{
		ID:               9,
		VaultID:          1,
		Action:           domain.FileActionDelete,
		Path:             "skills/img.png",
		PathHash:         "fh1",
		ContentHash:      "oldfh",
		Size:             5,
		Ctime:            100,
		Mtime:            100,
		UpdatedTimestamp: testDeletedAt,
	}
}

func TestFileService_UpdateOrCreate_StaleReviveRejected(t *testing.T) {
	fileRepo := new(domainmocks.MockFileRepository)
	fileRepo.On("GetByPathHash", mock.Anything, "fh1", int64(1), int64(1)).Return(deletedFileFixture(), nil)

	svc := NewFileService(nil, fileRepo, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	_, got, err := svc.UpdateOrCreate(context.Background(), 1, &dto.FileUpdateRequest{
		Vault:       "vault",
		Path:        "skills/img.png",
		PathHash:    "fh1",
		ContentHash: "oldfh", // 与墓碑一致：原样重报
		SavePath:    "/tmp/test-not-written",
		Size:        5,
		Ctime:       100,
		Mtime:       100,
	}, true)

	require.ErrorIs(t, err, code.ErrorStaleReviveRejected,
		"Bug: 附件陈旧重报同样必须被墓碑闸门拒绝")
	require.Nil(t, got)
	fileRepo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything)
	fileRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything, mock.Anything)
}

func TestFileService_UpdateOrCreate_FreshEditRevivesDeletedFile(t *testing.T) {
	fileRepo := new(domainmocks.MockFileRepository)
	fileRepo.On("GetByPathHash", mock.Anything, "fh1", int64(1), int64(1)).Return(deletedFileFixture(), nil)

	updated := deletedFileFixture()
	updated.Action = domain.FileActionCreate
	updated.Mtime = testDeletedAt + 60_000
	updated.UpdatedTimestamp = testDeletedAt + 60_000
	fileRepo.On("Update", mock.Anything, mock.AnythingOfType("*domain.File"), int64(1)).Return(updated, nil)
	fileRepo.On("CountSizeSum", mock.Anything, int64(1), int64(1)).Return(&domain.CountSizeResult{Count: 1, Size: 5}, nil).Maybe()

	svc := NewFileService(nil, fileRepo, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	_, got, err := svc.UpdateOrCreate(context.Background(), 1, &dto.FileUpdateRequest{
		Vault:       "vault",
		Path:        "skills/img.png",
		PathHash:    "fh1",
		ContentHash: "newhash",
		SavePath:    "/tmp/test-not-written",
		Size:        5,
		Ctime:       100,
		Mtime:       testDeletedAt + 60_000,
	}, true)

	require.NoError(t, err)
	require.NotNil(t, got)
	fileRepo.AssertCalled(t, "Update", mock.Anything, mock.AnythingOfType("*domain.File"), int64(1))
}

// ---------------------------------------------------------------------------
// Section D：目录侧墓碑判定（doFolderSync 用它拦截旧上报复活）
// ---------------------------------------------------------------------------

func TestFolderService_IsStaleRevive(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name   string
		folder *domain.Folder
		err    error
		mtime  int64
		want   bool
	}{
		{"tombstone, stale report -> stale", &domain.Folder{ID: 3, Action: domain.FolderActionDelete, UpdatedTimestamp: testDeletedAt}, nil, testDeletedAt - 10_000_000, true},
		{"tombstone, no mtime reported -> stale", &domain.Folder{ID: 3, Action: domain.FolderActionDelete, UpdatedTimestamp: testDeletedAt}, nil, 0, true},
		{"tombstone, re-created after deletion -> fresh", &domain.Folder{ID: 3, Action: domain.FolderActionDelete, UpdatedTimestamp: testDeletedAt}, nil, testDeletedAt + 60_000, false},
		{"tombstone, mtime within tolerance -> fresh", &domain.Folder{ID: 3, Action: domain.FolderActionDelete, UpdatedTimestamp: testDeletedAt}, nil, testDeletedAt - staleReviveToleranceMs, false},
		{"active record -> not stale", &domain.Folder{ID: 3, Action: domain.FolderActionCreate, UpdatedTimestamp: testDeletedAt}, nil, 0, false},
		{"missing record -> not stale", nil, gorm.ErrRecordNotFound, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := new(domainmocks.MockFolderRepository)
			repo.On("GetByPathHash", mock.Anything, "fh1", int64(1), int64(1)).Return(tc.folder, tc.err)

			svc := NewFolderService(repo, nil, nil, stubVaultService{}, nil, nil, nil, nil)

			stale, err := svc.IsStaleRevive(ctx, 1, "vault", "fh1", tc.mtime)
			require.NoError(t, err)
			require.Equal(t, tc.want, stale, "Bug: 目录墓碑闸门须与笔记/文件同一 mtime 规则判定")
		})
	}
}

// ---------------------------------------------------------------------------
// Section E：mtime-only 分支不得绕过墓碑闸门
// 墓碑与上传 contentHash 相同、上传 mtime 大于墓碑 mtime 但早于删除时刻时，
// 修复前会走"只更新 mtime"分支把 action 改回 modify，绕过墓碑闸门复活记录。
// ---------------------------------------------------------------------------

func TestNoteService_ModifyOrCreate_MtimeOnlyBranchSkipsTombstone(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	noteRepo.On("GetAllByPathHash", mock.Anything, "h1", int64(1), int64(1)).Return(deletedNoteFixture(), nil)

	svc := NewNoteService(nil, noteRepo, nil, nil, nil, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	_, got, err := svc.ModifyOrCreate(context.Background(), 1, &dto.NoteModifyOrCreateRequest{
		Vault:       "vault",
		Path:        "skills/old.md",
		PathHash:    "h1",
		Content:     "old content",
		ContentHash: "oldhash",
		Mtime:       200,
		Ctime:       200,
	}, true)

	require.ErrorIs(t, err, code.ErrorStaleReviveRejected,
		"Bug: 墓碑记录不得走 mtime-only 分支，陈旧重报必须被墓碑闸门拒绝")
	require.Nil(t, got)
	noteRepo.AssertNotCalled(t, "UpdateActionMtime", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	noteRepo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything)
}

func TestFileService_UpdateOrCreate_MtimeOnlyBranchSkipsTombstone(t *testing.T) {
	fileRepo := new(domainmocks.MockFileRepository)
	fileRepo.On("GetByPathHash", mock.Anything, "fh1", int64(1), int64(1)).Return(deletedFileFixture(), nil)

	svc := NewFileService(nil, fileRepo, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	_, got, err := svc.UpdateOrCreate(context.Background(), 1, &dto.FileUpdateRequest{
		Vault:       "vault",
		Path:        "skills/img.png",
		PathHash:    "fh1",
		ContentHash: "oldfh",
		SavePath:    "/tmp/test-not-written",
		Size:        5,
		Ctime:       200,
		Mtime:       200,
	}, true)

	require.ErrorIs(t, err, code.ErrorStaleReviveRejected,
		"Bug: 墓碑文件不得走 mtime-only 分支，陈旧重报必须被墓碑闸门拒绝")
	require.Nil(t, got)
	fileRepo.AssertNotCalled(t, "UpdateActionMtime", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	fileRepo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything)
}

// 新鲜 mtime 且内容相同：走完整复活路径（action=create），而不是 mtime-only 的 modify。
func TestNoteService_ModifyOrCreate_FreshSameContentRevivesAsCreate(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	noteRepo.On("GetAllByPathHash", mock.Anything, "h1", int64(1), int64(1)).Return(deletedNoteFixture(), nil)

	updated := deletedNoteFixture()
	updated.Action = domain.NoteActionCreate
	noteRepo.On("Update", mock.Anything, mock.MatchedBy(func(n *domain.Note) bool {
		return n.Action == domain.NoteActionCreate
	}), int64(1)).Return(updated, nil)
	noteRepo.On("CountSizeSum", mock.Anything, int64(1), int64(1)).Return(&domain.CountSizeResult{Count: 1, Size: 11}, nil).Maybe()

	svc := NewNoteService(nil, noteRepo, nil, nil, nil, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	_, got, err := svc.ModifyOrCreate(context.Background(), 1, &dto.NoteModifyOrCreateRequest{
		Vault:       "vault",
		Path:        "skills/old.md",
		PathHash:    "h1",
		Content:     "old content",
		ContentHash: "oldhash",
		Mtime:       testDeletedAt + 60_000,
		Ctime:       100,
	}, true)

	require.NoError(t, err)
	require.NotNil(t, got)
	noteRepo.AssertNotCalled(t, "UpdateActionMtime", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// ---------------------------------------------------------------------------
// Section F：时间戳陈旧但内容与墓碑不同 → 放行复活（#480 复核 F-1）
// 内容不同说明客户端有服务端未见过的修改；若拒绝并补发删除，客户端会删掉
// 这份修改。闸门只拦原样重报。
// ---------------------------------------------------------------------------

func TestNoteService_ModifyOrCreate_StaleTimestampsDifferentContentRevives(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	noteRepo.On("GetAllByPathHash", mock.Anything, "h1", int64(1), int64(1)).Return(deletedNoteFixture(), nil)

	updated := deletedNoteFixture()
	updated.Action = domain.NoteActionCreate
	updated.ContentHash = "offlineedit"
	noteRepo.On("Update", mock.Anything, mock.MatchedBy(func(n *domain.Note) bool {
		return n.Action == domain.NoteActionCreate && n.ContentHash == "offlineedit"
	}), int64(1)).Return(updated, nil)
	noteRepo.On("CountSizeSum", mock.Anything, int64(1), int64(1)).Return(&domain.CountSizeResult{Count: 1, Size: 11}, nil).Maybe()

	svc := NewNoteService(nil, noteRepo, nil, nil, nil, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	_, got, err := svc.ModifyOrCreate(context.Background(), 1, &dto.NoteModifyOrCreateRequest{
		Vault:       "vault",
		Path:        "skills/old.md",
		PathHash:    "h1",
		Content:     "offline edit",
		ContentHash: "offlineedit",
		Mtime:       200,
		Ctime:       200,
	}, true)

	require.NoError(t, err, "Bug: 内容与墓碑不同的上传必须按正常上传复活，不得拒绝后补发删除")
	require.NotNil(t, got)
	noteRepo.AssertCalled(t, "Update", mock.Anything, mock.AnythingOfType("*domain.Note"), int64(1))
}

func TestFileService_UpdateOrCreate_StaleTimestampsDifferentContentRevives(t *testing.T) {
	fileRepo := new(domainmocks.MockFileRepository)
	fileRepo.On("GetByPathHash", mock.Anything, "fh1", int64(1), int64(1)).Return(deletedFileFixture(), nil)

	updated := deletedFileFixture()
	updated.Action = domain.FileActionCreate
	updated.ContentHash = "offlineedit"
	fileRepo.On("Update", mock.Anything, mock.MatchedBy(func(f *domain.File) bool {
		return f.Action == domain.FileActionCreate && f.ContentHash == "offlineedit"
	}), int64(1)).Return(updated, nil)
	fileRepo.On("CountSizeSum", mock.Anything, int64(1), int64(1)).Return(&domain.CountSizeResult{Count: 1, Size: 5}, nil).Maybe()

	svc := NewFileService(nil, fileRepo, nil, stubVaultService{}, stubFolderService{}, nil, nil, nil, nil)

	_, got, err := svc.UpdateOrCreate(context.Background(), 1, &dto.FileUpdateRequest{
		Vault:       "vault",
		Path:        "skills/img.png",
		PathHash:    "fh1",
		ContentHash: "offlineedit",
		SavePath:    "/tmp/test-not-written",
		Size:        5,
		Ctime:       200,
		Mtime:       200,
	}, true)

	require.NoError(t, err, "Bug: 附件内容与墓碑不同的上传必须按正常上传复活")
	require.NotNil(t, got)
	fileRepo.AssertCalled(t, "Update", mock.Anything, mock.AnythingOfType("*domain.File"), int64(1))
}
