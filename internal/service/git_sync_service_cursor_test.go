package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// #474：运行期间写入的笔记，时间戳必须晚于本轮成功后记录的 last_sync_time，下一轮（updated_timestamp > 游标）才会导出它。
// 用本地裸仓库跑一次完整 syncTask，在导出笔记时模拟“运行中又写入一篇笔记”。
func TestGitSyncService_SyncTask_CursorCoversWritesDuringRun(t *testing.T) {
	t.Chdir(t.TempDir())
	remote := filepath.Join(t.TempDir(), "remote.git")
	_, err := git.PlainInit(remote, true)
	require.NoError(t, err)

	repo := new(domainmocks.MockGitSyncRepository)
	repo.On("Save", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncConfig{}, nil)
	repo.On("CreateHistory", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncHistory{}, nil)
	svc := newTestGitSyncService(repo)

	var writeDuringRun int64
	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, int64(0), mock.Anything, int64(1), 0, mock.Anything).
		Run(func(mock.Arguments) {
			time.Sleep(5 * time.Millisecond)
			// 导出进行中，另一篇笔记被写入，其 updated_timestamp 为此刻
			writeDuringRun = time.Now().UnixMilli()
			time.Sleep(5 * time.Millisecond)
		}).
		Return([]*domain.Note{{Path: "a.md", Content: "hello", Action: domain.NoteActionCreate}}, nil)
	svc.fileRepo.(*domainmocks.MockFileRepository).
		On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.File{}, nil)

	conf := &domain.GitSyncConfig{ID: 1, UID: 1, VaultID: 1, RepoURL: remote, Branch: "master"}
	svc.syncTask(context.Background(), conf)

	require.Equal(t, int64(domain.GitSyncStatusSuccess), conf.LastStatus, conf.LastMessage)
	require.NotNil(t, conf.LastSyncTime)
	require.Less(t, conf.LastSyncTime.UnixMilli(), writeDuringRun,
		"Bug: last_sync_time 晚于运行期间的写入，下一轮按 updated_timestamp > last_sync_time 增量导出时会永久漏掉它")
}
