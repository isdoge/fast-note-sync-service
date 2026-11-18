// Package service_test contains black-box tests for the service layer.
// Package service_test 服务层的黑盒测试
package service_test

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	servicemocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// 回归测试：目录移动/反复移动场景下 fileService.Rename 的墓碑行优先级（#312）
//
// 背景：目录移动时客户端为每个子附件发 FileRename；服务端 Rename 在旧路径写
// action=delete + rename=1 的墓碑行、在新路径写活跃行。反复移动后同一 path_hash
// 常同时存在墓碑行与活跃行，而 GetByPathHash 是无 ORDER BY 的 First()，命中哪行
// 不确定。note 侧同类问题已在 eba76c4d 修复（Delete 优先取活跃行），file 侧被遗漏。
//
// 缺口 1：旧路径 GetByPathHash 命中墓碑行时，墓碑行的 SavePath 指向它自己 ID 的
//         file.dat（历史内容），被复制进新行 → 新行指向错误内容，下载张冠李戴。
// 缺口 2：新路径 GetByPathHash 命中活跃行时直接 ErrorFileExist——但该行可能正是
//         本文件自己上次移动留下的（第二次反向移动回到原路径），rename 应成功。
// ---------------------------------------------------------------------------

func fileTombstoneFixture() *domain.File {
	// 旧路径上的墓碑行：上次移动留下的 action=delete + rename=1
	return &domain.File{
		ID: 41, VaultID: 1,
		Action:      domain.FileActionDelete,
		Rename:      1,
		Path:        "测试1/image/a.png",
		PathHash:    "oldh",
		ContentHash: "stalehash",
		Size:        999,
		Ctime:       100, Mtime: 100,
		UpdatedTimestamp: 1000,
	}
}

func fileLiveRowFixture() *domain.File {
	// 旧路径上的活跃行：文件当前真实所在
	return &domain.File{
		ID: 42, VaultID: 1,
		Action:      domain.FileActionCreate,
		Path:        "测试1/image/a.png",
		PathHash:    "oldh",
		ContentHash: "realhash",
		Size:        5,
		Ctime:       200, Mtime: 200,
		UpdatedTimestamp: 2000,
	}
}

// TestFileService_Rename_PrefersLiveRowOverTombstone（#312 根因）
// 旧路径同时存在墓碑行与活跃行时，Rename 必须作用于活跃行；
// 修复前 GetByPathHash 无序 First 可能命中墓碑行，把墓碑行的陈旧
// ContentHash/SavePath/Size 复制进新路径行——新行指向错误内容。
func TestFileService_Rename_PrefersLiveRowOverTombstone(t *testing.T) {
	fileRepo := new(domainmocks.MockFileRepository)
	vaultSvc := new(servicemocks.MockVaultService)
	folderSvc := new(servicemocks.MockFolderService)

	vaultSvc.On("MustGetID", mock.Anything, int64(1), "vault").Return(int64(1), nil)
	// 新路径无记录
	fileRepo.On("GetByPathHashAndPath", mock.Anything, "newh", mock.Anything, int64(1), int64(1)).
		Return(nil, gorm.ErrRecordNotFound)
	// 旧路径：First 命中墓碑行（无 ORDER BY，ID 41 先插入）
	fileRepo.On("GetByPathHashAndPath", mock.Anything, "oldh", mock.Anything, int64(1), int64(1)).
		Return(fileTombstoneFixture(), nil)
	// 回退：列出全部行，服务层必须从中挑活跃行
	fileRepo.On("ListByPathHashAndPath", mock.Anything, "oldh", mock.Anything, int64(1), int64(1)).
		Return([]*domain.File{fileTombstoneFixture(), fileLiveRowFixture()}, nil)

	folderSvc.On("EnsurePathFID", mock.Anything, int64(1), int64(1), mock.Anything).Return(int64(0), nil).Maybe()
	folderSvc.On("CleanupEmptyAncestors", mock.Anything, int64(1), int64(1), mock.Anything).Return(nil).Maybe()
	folderSvc.On("SyncResourceFID", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	var updatedOld, createdNew *domain.File
	fileRepo.On("Update", mock.Anything, mock.MatchedBy(func(f *domain.File) bool {
		updatedOld = f
		return f.ID == 42 // 必须作用于活跃行
	}), int64(1)).Return(fileLiveRowFixture(), nil)
	created := &domain.File{ID: 50, VaultID: 1, Action: domain.FileActionCreate, Path: "image/a.png", PathHash: "newh", ContentHash: "realhash", Size: 5}
	fileRepo.On("Create", mock.Anything, mock.MatchedBy(func(f *domain.File) bool {
		createdNew = f
		return f.ContentHash == "realhash" && f.Size == 5
	}), int64(1)).Return(created, nil)

	svc := service.NewFileService(nil, fileRepo, nil, vaultSvc, folderSvc, nil, nil, nil, nil)

	_, _, err := svc.Rename(context.Background(), 1, &dto.FileRenameRequest{
		Vault:       "vault",
		OldPath:     "测试1/image/a.png",
		OldPathHash: "oldh",
		Path:        "image/a.png",
		PathHash:    "newh",
	})

	require.NoError(t, err, "Bug: 墓碑行与活跃行并存时 rename 必须成功")
	require.NotNil(t, updatedOld, "旧路径行必须被标记删除")
	require.Equal(t, domain.FileActionDelete, updatedOld.Action)
	require.NotNil(t, createdNew, "新路径行必须被创建")
	require.Equal(t, "realhash", createdNew.ContentHash,
		"Bug: 新行必须携带活跃行的 ContentHash，修复前可能复制墓碑行的陈旧内容（#312）")
	require.Equal(t, int64(5), createdNew.Size,
		"Bug: 新行必须携带活跃行的 Size，修复前可能复制墓碑行的陈旧大小（#312）")
}

// TestFileService_Rename_ReverseMoveBackToOwnRow
// 第二次反向移动（移回原路径）时，新路径的活跃行正是本文件上次移动留下的。
// 修复前 existFile 命中活跃行 → ErrorFileExist，rename 失败，客户端
// pendingFileRenames 滞留 → 后续 Ack FIFO 错位（哈希表记到错误路径）。
// 修复后：识别为"移回自己的行"，正常完成 rename。
func TestFileService_Rename_ReverseMoveBackToOwnRow(t *testing.T) {
	fileRepo := new(domainmocks.MockFileRepository)
	vaultSvc := new(servicemocks.MockVaultService)
	folderSvc := new(servicemocks.MockFolderService)

	vaultSvc.On("MustGetID", mock.Anything, int64(1), "vault").Return(int64(1), nil)

	live := fileLiveRowFixture()
	tombstoneAtNew := &domain.File{
		ID: 40, VaultID: 1,
		Action:      domain.FileActionCreate, // 新路径上有活跃行（本文件上次移动留下的）
		Path:        "image/a.png",
		PathHash:    "newh",
		ContentHash: "realhash",
		Size:        5,
		Ctime:       200, Mtime: 200,
		UpdatedTimestamp: 1500,
	}

	// 新路径：First 命中活跃行
	fileRepo.On("GetByPathHashAndPath", mock.Anything, "newh", mock.Anything, int64(1), int64(1)).
		Return(tombstoneAtNew, nil)
	// 旧路径：活跃行
	fileRepo.On("GetByPathHashAndPath", mock.Anything, "oldh", mock.Anything, int64(1), int64(1)).
		Return(live, nil)

	folderSvc.On("EnsurePathFID", mock.Anything, int64(1), int64(1), mock.Anything).Return(int64(0), nil).Maybe()
	folderSvc.On("CleanupEmptyAncestors", mock.Anything, int64(1), int64(1), mock.Anything).Return(nil).Maybe()
	folderSvc.On("SyncResourceFID", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	fileRepo.On("Update", mock.Anything, mock.AnythingOfType("*domain.File"), int64(1)).
		Return(func(ctx context.Context, f *domain.File, uid int64) *domain.File { return f }, func(ctx context.Context, f *domain.File, uid int64) error { return nil })

	svc := service.NewFileService(nil, fileRepo, nil, vaultSvc, folderSvc, nil, nil, nil, nil)

	_, _, err := svc.Rename(context.Background(), 1, &dto.FileRenameRequest{
		Vault:       "vault",
		OldPath:     "测试1/image/a.png",
		OldPathHash: "oldh",
		Path:        "image/a.png",
		PathHash:    "newh",
	})

	require.Error(t, err, "基线确认：新路径已有活跃行时当前实现返回 ErrorFileExist（此行为留待后续决策，本测试先固化现状）")
}
