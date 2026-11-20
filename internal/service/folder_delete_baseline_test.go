package service_test

// B-01 服务端侧：目录删除在基线模式下必须与笔记/附件/配置的删除语义对齐——
// 不存在与已墓碑都幂等成功，活跃行令牌不匹配统一按 local_deleted 报告。
// Baseline folder deletion must match note/file/setting semantics: missing and
// tombstone rows are idempotent successes, and a live-row token mismatch reports
// local_deleted instead of revision_changed.

import (
	"context"
	"errors"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	servicemocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newFolderDeleteService(folderRepo domain.FolderRepository) service.FolderService {
	vaultSvc := new(servicemocks.MockVaultService)
	vaultSvc.On("MustGetID", mock.Anything, int64(1), "vault").Return(int64(1), nil)
	return service.NewFolderService(folderRepo, nil, nil, vaultSvc, nil, nil, nil, nil)
}

func folderDeleteRequest() *dto.FolderDeleteRequest {
	return &dto.FolderDeleteRequest{
		Vault:    "vault",
		Path:     "Old",
		PathHash: "fh-old",
		Baseline: &dto.BaselineCommit{BaseRevision: "rev-1"},
	}
}

// TestFolderService_Delete_BaselineMissingIsIdempotent 目录不存在时基线删除必须成功，
// 不能报 446（客户端会据此无限重试）。
func TestFolderService_Delete_BaselineMissingIsIdempotent(t *testing.T) {
	folderRepo := new(domainmocks.MockFolderRepository)
	folderRepo.On("GetByPathHashAndPath", mock.Anything, "fh-old", "Old", int64(1), int64(1)).
		Return(nil, gorm.ErrRecordNotFound)

	svc := newFolderDeleteService(folderRepo)
	got, err := svc.Delete(context.Background(), 1, folderDeleteRequest())

	require.NoError(t, err, "不存在的目录在基线模式下必须幂等成功")
	require.NotNil(t, got)
	assert.Equal(t, "Old", got.Path)
	assert.Equal(t, "fh-old", got.PathHash)
	folderRepo.AssertNotCalled(t, "UpdateIfRevision", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestFolderService_Delete_BaselineTombstoneIsIdempotent 已是墓碑的目录基线删除必须成功，
// 不得落入 revision 比对误报冲突。
func TestFolderService_Delete_BaselineTombstoneIsIdempotent(t *testing.T) {
	folderRepo := new(domainmocks.MockFolderRepository)
	folderRepo.On("GetByPathHashAndPath", mock.Anything, "fh-old", "Old", int64(1), int64(1)).
		Return(&domain.Folder{
			ID: 7, VaultID: 1, Path: "Old", PathHash: "fh-old",
			Action: domain.FolderActionDelete, Revision: "rev-tombstone",
		}, nil)

	svc := newFolderDeleteService(folderRepo)
	got, err := svc.Delete(context.Background(), 1, folderDeleteRequest())

	require.NoError(t, err, "已是墓碑的目录重复删除必须幂等成功")
	require.NotNil(t, got)
	assert.Equal(t, "fh-old", got.PathHash)
	folderRepo.AssertNotCalled(t, "UpdateIfRevision", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestFolderService_Delete_BaselineStaleTokenReportsLocalDeleted 活跃行令牌不匹配时必须
// 报 local_deleted（而不是 revision_changed），与笔记/附件/配置一致。
func TestFolderService_Delete_BaselineStaleTokenReportsLocalDeleted(t *testing.T) {
	folderRepo := new(domainmocks.MockFolderRepository)
	folderRepo.On("GetByPathHashAndPath", mock.Anything, "fh-old", "Old", int64(1), int64(1)).
		Return(&domain.Folder{
			ID: 8, VaultID: 1, Path: "Old", PathHash: "fh-old",
			Action: domain.FolderActionCreate, Revision: "rev-live-newer",
		}, nil)

	svc := newFolderDeleteService(folderRepo)
	_, err := svc.Delete(context.Background(), 1, folderDeleteRequest())

	require.Error(t, err)
	var conflict *dto.BaselineConflictError
	require.True(t, errors.As(err, &conflict), "应返回基线冲突错误")
	assert.Equal(t, dto.SyncConflictReasonLocalDeleted, conflict.Reason)
	assert.True(t, conflict.ServerActive)
	assert.Equal(t, "rev-live-newer", conflict.ServerRevision)
}

// TestFolderService_Delete_BaselineCASRaceReportsLocalDeleted CAS 竞争兜底分支同样必须
// 报 local_deleted。
func TestFolderService_Delete_BaselineCASRaceReportsLocalDeleted(t *testing.T) {
	live := &domain.Folder{
		ID: 9, VaultID: 1, Path: "Old", PathHash: "fh-old",
		Action: domain.FolderActionCreate, Revision: "rev-1",
	}
	folderRepo := new(domainmocks.MockFolderRepository)
	folderRepo.On("GetByPathHashAndPath", mock.Anything, "fh-old", "Old", int64(1), int64(1)).
		Return(live, nil)
	folderRepo.On("UpdateIfRevision", mock.Anything, mock.Anything, "rev-1", int64(1)).
		Return(false, nil)

	svc := newFolderDeleteService(folderRepo)
	_, err := svc.Delete(context.Background(), 1, folderDeleteRequest())

	require.Error(t, err)
	var conflict *dto.BaselineConflictError
	require.True(t, errors.As(err, &conflict), "应返回基线冲突错误")
	assert.Equal(t, dto.SyncConflictReasonLocalDeleted, conflict.Reason)
}

// TestFolderService_Delete_BaselineMatchSucceeds 令牌匹配时正常软删除并换新令牌。
func TestFolderService_Delete_BaselineMatchSucceeds(t *testing.T) {
	live := &domain.Folder{
		ID: 10, VaultID: 1, Path: "Old", PathHash: "fh-old",
		Action: domain.FolderActionCreate, Revision: "rev-1",
	}
	folderRepo := new(domainmocks.MockFolderRepository)
	folderRepo.On("GetByPathHashAndPath", mock.Anything, "fh-old", "Old", int64(1), int64(1)).
		Return(live, nil)
	folderRepo.On("UpdateIfRevision", mock.Anything, mock.Anything, "rev-1", int64(1)).
		Return(true, nil)

	svc := newFolderDeleteService(folderRepo)
	got, err := svc.Delete(context.Background(), 1, folderDeleteRequest())

	require.NoError(t, err)
	require.NotNil(t, got)
	folderRepo.AssertCalled(t, "UpdateIfRevision", mock.Anything, mock.Anything, "rev-1", int64(1))
}
