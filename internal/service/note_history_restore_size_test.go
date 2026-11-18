// Package service_test contains black-box tests for the service layer.
// Package service_test 服务层的黑盒测试
package service_test

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	servicemocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestNoteHistoryService_RestoreFromHistory_UpdatesSize（#360）
// 从历史版本恢复时正文长度已变，note.Size 必须随内容重算后再落库；
// 修复前 RestoreFromHistory 只改 Content/ContentHash，Size 停留在旧值，
// 列表与 MCP note_list 报告的 Size 与实际正文脱节。
func TestNoteHistoryService_RestoreFromHistory_UpdatesSize(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	historyRepo := new(domainmocks.MockNoteHistoryRepository)
	folderSvc := new(servicemocks.MockFolderService)
	noteSvc := new(servicemocks.MockNoteService)

	// 当前笔记：Size=100（旧正文长度），恢复后的正文是 5 字节
	note := &domain.Note{
		ID: 7, VaultID: 3, Path: "a.md", PathHash: "ha",
		Content: "current long content", ContentHash: "hcur",
		ContentLastSnapshot: "snap", ContentLastSnapshotHash: "hsnap",
		Version: 2, Size: 100,
	}
	// 历史快照：5 字节正文 + 空补丁 → 恢复结果为 "short"（len=5）
	// （恢复结果为空字符串会被服务端视为恢复失败早退，故用非空正文）
	// (an empty restored string is treated as a failed restore, so use a non-empty body)
	history := &domain.NoteHistory{
		ID: 55, NoteID: 7, VaultID: 3, Content: "short", DiffPatch: "", Version: 1,
	}

	historyRepo.On("GetByID", mock.Anything, int64(55), int64(9)).Return(history, nil)
	noteRepo.On("GetByID", mock.Anything, int64(7), int64(9)).Return(note, nil)
	// RestoreFromHistory 尾部 ProcessDelay 也会走 GetByID/GetLatestVersion/Create
	historyRepo.On("GetLatestVersion", mock.Anything, int64(7), int64(9)).Return(int64(2), nil).Maybe()
	historyRepo.On("Create", mock.Anything, mock.AnythingOfType("*domain.NoteHistory"), int64(9)).Return(&domain.NoteHistory{ID: 1}, nil).Maybe()
	noteRepo.On("UpdateSnapshot", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	var updatedNote *domain.Note
	noteRepo.On("Update", mock.Anything, mock.AnythingOfType("*domain.Note"), int64(9)).
		Run(func(args mock.Arguments) {
			updatedNote = args.Get(1).(*domain.Note)
		}).
		Return(note, nil)

	// RestoreFromHistory 尾部的异步调用（goroutine/nil 安全 stub）
	folderSvc.On("SyncResourceFID", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	noteSvc.On("CountSizeSum", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	noteSvc.On("UpdateNoteLinks", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
	noteSvc.On("UpdateNoteTags", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()

	keepUnlimited := 0
	svc := service.NewNoteHistoryService(historyRepo, noteRepo, nil, nil, folderSvc, noteSvc, nil, nil, zap.NewNop(), &service.AppServiceConfig{HistoryKeepVersions: &keepUnlimited})

	_, err := svc.RestoreFromHistory(context.Background(), 9, 55)
	require.NoError(t, err)
	require.NotNil(t, updatedNote, "noteRepo.Update 必须被调用")
	require.Equal(t, int64(len("short")), updatedNote.Size,
		"Bug: 恢复历史版本后 Size 必须随新正文重算（此处恢复为 5 字节正文），修复前停留在旧值 100（#360）")
}
