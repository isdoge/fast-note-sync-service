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
)

// ---------------------------------------------------------------------------
// 回归测试：删除、重命名笔记时同步维护 note_tag 标签索引（#509 修复）
// 修复前：DeleteNoteTags 无调用方，删除/重命名后旧笔记 ID 的标签行残留，
// 重命名后的新笔记 ID 在下次内容修改前没有标签。
// ---------------------------------------------------------------------------

func TestNoteService_Delete_RemovesNoteTags(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	tagRepo := new(domainmocks.MockNoteTagRepository)
	shareRepo := new(domainmocks.MockUserShareRepository)
	vaultSvc := new(servicemocks.MockVaultService)
	vaultSvc.On("MustGetID", mock.Anything, int64(1), "vault").Return(int64(1), nil)

	active := &domain.Note{ID: 42, VaultID: 1, Action: domain.NoteActionCreate, Path: "a.md", PathHash: "h1"}
	noteRepo.On("GetByPathHash", mock.Anything, "h1", int64(1), int64(1)).Return(active, nil)
	noteRepo.On("UpdateDelete", mock.Anything, mock.AnythingOfType("*domain.Note"), int64(1)).Return(nil)
	shareRepo.On("UpdateStatusByRes", mock.Anything, int64(1), "note", int64(42), domain.UserShareStatusRevoked).Return(nil)
	tagRepo.On("DeleteByNoteID", mock.Anything, int64(42), int64(1)).Return(nil)

	svc := service.NewNoteService(nil, noteRepo, nil, tagRepo, nil, shareRepo, nil, vaultSvc, nil, nil, nil, nil, nil)
	_, err := svc.Delete(context.Background(), 1, &dto.NoteDeleteRequest{Vault: "vault", Path: "a.md", PathHash: "h1"})

	require.NoError(t, err)
	tagRepo.AssertCalled(t, "DeleteByNoteID", mock.Anything, int64(42), int64(1))
}

func TestNoteService_Rename_MovesNoteTagsToNewID(t *testing.T) {
	noteRepo := new(domainmocks.MockNoteRepository)
	tagRepo := new(domainmocks.MockNoteTagRepository)
	historyRepo := new(domainmocks.MockNoteHistoryRepository)
	vaultSvc := new(servicemocks.MockVaultService)
	folderSvc := new(servicemocks.MockFolderService)

	oldNote := oldNoteFixture()
	oldNote.Action = domain.NoteActionModify
	oldNote.Rename = 0
	oldNote.Content = "body #project"
	newNote := &domain.Note{ID: 2048, VaultID: 1, Action: domain.NoteActionCreate, Path: "新笔记.md", PathHash: "newpathhash", Content: oldNote.Content}

	vaultSvc.On("MustGetID", mock.Anything, int64(1), "obsidian-vault").Return(int64(1), nil)
	noteRepo.On("GetAllByPathHash", mock.Anything, "newpathhash", int64(1), int64(1)).Return(nil, nil)
	noteRepo.On("GetByPathHash", mock.Anything, "oldpathhash", int64(1), int64(1)).Return(oldNote, nil)
	noteRepo.On("Update", mock.Anything, mock.AnythingOfType("*domain.Note"), int64(1)).Return(oldNote, nil)
	folderSvc.On("EnsurePathFID", mock.Anything, int64(1), int64(1), "").Return(int64(10), nil)
	noteRepo.On("Create", mock.Anything, mock.AnythingOfType("*domain.Note"), int64(1)).Return(newNote, nil)
	folderSvc.On("CleanupEmptyAncestors", mock.Anything, int64(1), int64(1), "旧笔记.md").Return(nil)
	renameMigrateMocks(noteRepo, historyRepo, folderSvc)
	tagRepo.On("DeleteByNoteID", mock.Anything, int64(137), int64(1)).Return(nil)
	tagRepo.On("ReplaceForNote", mock.Anything, int64(2048), []string{"project"}, int64(1), int64(1), mock.Anything).Return(nil)

	svc := service.NewNoteService(nil, noteRepo, nil, tagRepo, nil, nil, historyRepo, vaultSvc, folderSvc, nil, nil, nil, nil)
	_, _, err := svc.Rename(context.Background(), 1, &dto.NoteRenameRequest{
		Vault:       "obsidian-vault",
		Path:        "新笔记.md",
		PathHash:    "newpathhash",
		OldPath:     "旧笔记.md",
		OldPathHash: "oldpathhash",
	})

	require.NoError(t, err)
	tagRepo.AssertCalled(t, "DeleteByNoteID", mock.Anything, int64(137), int64(1))
	tagRepo.AssertCalled(t, "ReplaceForNote", mock.Anything, int64(2048), []string{"project"}, int64(1), int64(1), mock.Anything)
}
