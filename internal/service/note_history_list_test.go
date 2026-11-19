package service_test

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	servicemocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestNoteHistoryService_ListUsesRequestedPath verifies the history list resolves
// the note by path when the request provides one, so hash-colliding notes do not
// turn the list into an ambiguous-hash error.
// TestNoteHistoryService_ListUsesRequestedPath 验证历史列表在请求带路径时按路径
// 定位笔记，哈希碰撞的笔记不会让列表报歧义错误。
func TestNoteHistoryService_ListUsesRequestedPath(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	historyRepo := new(domainmocks.MockNoteHistoryRepository)
	vaultSvc := new(servicemocks.MockVaultService)
	vaultSvc.On("MustGetID", mock.Anything, int64(9), "v").Return(int64(1), nil)

	path := "Aa.md"
	hash := util.EncodeHash32(path)
	noteRepo.On("GetByPathHashAndPathIncludeRecycle", mock.Anything, hash, path, int64(1), int64(9), false).
		Return(&domain.Note{ID: 5, VaultID: 1, Path: path, PathHash: hash}, nil)
	historyRepo.On("ListByNoteID", mock.Anything, int64(5), 1, 20, int64(9)).
		Return([]*domain.NoteHistory{}, int64(0), nil)

	svc := service.NewNoteHistoryService(historyRepo, noteRepo, nil, vaultSvc, nil, nil, nil, nil, zap.NewNop(), nil)
	_, _, err := svc.List(context.Background(), 9, &dto.NoteHistoryListRequest{Vault: "v", Path: path, PathHash: hash}, &app.Pager{Page: 1, PageSize: 20})
	require.NoError(t, err)
	noteRepo.AssertNotCalled(t, "GetByPathHashIncludeRecycle", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	noteRepo.AssertExpectations(t)
}
