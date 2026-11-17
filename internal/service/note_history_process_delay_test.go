package service_test

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newHistorySvcForProcessDelay(noteRepo *domainmocks.MockNoteRepository, historyRepo *domainmocks.MockNoteHistoryRepository) service.NoteHistoryService {
	keepUnlimited := 0
	return service.NewNoteHistoryService(historyRepo, noteRepo, nil, nil, nil, nil, nil, nil, zap.NewNop(), &service.AppServiceConfig{HistoryKeepVersions: &keepUnlimited})
}

// ProcessDelay 生成的补丁应用到上一快照后必须得到当前正文（#499 改用 PatchText 后的接线回归）。
func TestNoteHistoryService_ProcessDelay_PatchRestoresContent(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	historyRepo := new(domainmocks.MockNoteHistoryRepository)
	note := &domain.Note{
		ID: 5, VaultID: 1, Path: "a.md",
		Content: "第一行\n第二行 已修改\n第三行\n", ContentHash: "h2",
		ContentLastSnapshot: "第一行\n第二行\n第三行\n", ContentLastSnapshotHash: "h1",
		Version: 3,
	}
	noteRepo.On("GetByID", mock.Anything, int64(5), int64(9)).Return(note, nil)
	historyRepo.On("GetLatestVersion", mock.Anything, int64(5), int64(9)).Return(int64(3), nil)
	var created *domain.NoteHistory
	historyRepo.On("Create", mock.Anything, mock.AnythingOfType("*domain.NoteHistory"), int64(9)).
		Run(func(args mock.Arguments) { created = args.Get(1).(*domain.NoteHistory) }).
		Return(&domain.NoteHistory{ID: 1}, nil)
	noteRepo.On("UpdateSnapshot", mock.Anything, note.Content, "h2", int64(4), int64(5), int64(9)).Return(nil)

	require.NoError(t, newHistorySvcForProcessDelay(noteRepo, historyRepo).ProcessDelay(context.Background(), 5, 9))
	require.NotNil(t, created)
	dmp := diffmatchpatch.New()
	patches, err := dmp.PatchFromText(created.DiffPatch)
	require.NoError(t, err)
	restored, _ := dmp.PatchApply(patches, note.ContentLastSnapshot)
	require.Equal(t, note.Content, restored)
	noteRepo.AssertExpectations(t)
}

