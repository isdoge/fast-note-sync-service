package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// updateConfigViaService 通过 UpdateConfig 更新已有配置并返回保存后的配置，
// 模拟用户在 WebGUI 里修改已有 Git 配置的仓库地址或分支。
func updateConfigViaService(t *testing.T, svc *gitSyncService, conf *domain.GitSyncConfig, repoURL, branch string) *domain.GitSyncConfig {
	t.Helper()
	repo := svc.repo.(*domainmocks.MockGitSyncRepository)
	repo.ExpectedCalls = nil
	repo.On("GetByID", mock.Anything, conf.ID, conf.UID).Return(conf, nil)
	repo.On("Save", mock.Anything, mock.Anything, conf.UID).Run(func(args mock.Arguments) {
		saved := args.Get(1).(*domain.GitSyncConfig)
		*conf = *saved
	}).Return(conf, nil)

	_, err := svc.UpdateConfig(context.Background(), conf.UID, &dto.GitSyncConfigRequest{
		ID:      conf.ID,
		RepoURL: repoURL,
		Branch:  branch,
	})
	require.NoError(t, err)
	return conf
}

// commitFileToRemote 在裸仓库的指定分支上提交一个文件，模拟远端已有内容。
func commitFileToRemote(t *testing.T, remotePath, branch, relPath, content string) {
	t.Helper()
	seedPath := filepath.Join(t.TempDir(), "seed")
	seed, err := git.PlainInit(seedPath, false)
	require.NoError(t, err)
	_, err = seed.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{remotePath}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(seedPath, relPath), []byte(content), 0644))
	wt, err := seed.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(relPath)
	require.NoError(t, err)
	_, err = wt.Commit("seed", &git.CommitOptions{Author: &object.Signature{Name: "seed", Email: "seed@example.com"}})
	require.NoError(t, err)
	require.NoError(t, seed.Push(&git.PushOptions{
		RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec("+refs/heads/master:refs/heads/" + branch)},
	}))
}

// TestGitSyncService_BranchChangeSyncsNewBranch 复现服务端 issue #428：修改已有 Git 配置的
// 分支后，已有工作区 HEAD 仍指向旧分支，Pull 拉的是新分支却被合进旧分支，Push 没有显式
// refspec 时只推当前分支，导致新分支永远等不到内容、旧分支反而被改写。
// 期望：分支变更后下一轮同步把内容完整导出并推送到新分支，旧分支不被改动。
func TestGitSyncService_BranchChangeSyncsNewBranch(t *testing.T) {
	t.Chdir(t.TempDir())
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	remote, err := git.PlainInit(remotePath, true)
	require.NoError(t, err)
	commitFileToRemote(t, remotePath, "master", "seed.md", "seed")

	fixture := newGitSyncNonFastForwardFixture(t, nil)
	svc := fixture.svc
	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	call := noteRepo.ExpectedCalls[len(noteRepo.ExpectedCalls)-1]
	call.Run(func(args mock.Arguments) {
		call.ReturnArguments = mock.Arguments{[]*domain.Note{
			{Path: "note.md", Content: "content", Action: domain.NoteActionCreate},
		}, nil}
	})

	conf := &domain.GitSyncConfig{ID: 1, UID: 1, VaultID: 1, RepoURL: remotePath, Branch: "master"}
	require.NoError(t, svc.doSync(context.Background(), conf))

	// 记录首次同步后 master 的 head，作为后续比较基线。
	masterBefore, err := remote.Reference(plumbing.NewBranchReferenceName("master"), true)
	require.NoError(t, err)

	// 用户在配置里把分支从 master 改成 release（工作区已存在，HEAD 仍是 master）。
	conf = updateConfigViaService(t, svc, conf, remotePath, "release")
	require.NoError(t, svc.doSync(context.Background(), conf))

	// 新分支必须存在且包含导出的笔记。
	relRef, err := remote.Reference(plumbing.NewBranchReferenceName("release"), true)
	require.NoError(t, err, "branch change must push the mirror to the new branch")
	relCommit, err := object.GetCommit(remote.Storer, relRef.Hash())
	require.NoError(t, err)
	tree, err := relCommit.Tree()
	require.NoError(t, err)
	_, err = tree.File("note.md")
	require.NoError(t, err, "new branch must contain the exported note")

	// 旧分支 head 在分支变更后不能再被推进。
	masterAfter, err := remote.Reference(plumbing.NewBranchReferenceName("master"), true)
	require.NoError(t, err)
	require.Equal(t, masterBefore.Hash(), masterAfter.Hash(),
		"old branch must not receive new commits after the config switched branches")
}

// TestGitSyncService_RepoURLChangeReplacesWorkspace 复现 issue #428 的另一半：只改仓库地址时，
// 旧代码对已存在的工作区直接 PlainOpen 后 pull，旧 remote 的提交会污染新仓库；
// 期望：地址变更后重建工作区，旧仓库的提交不出现在新仓库历史里。
func TestGitSyncService_RepoURLChangeReplacesWorkspace(t *testing.T) {
	t.Chdir(t.TempDir())
	oldRemotePath := filepath.Join(t.TempDir(), "old.git")
	_, err := git.PlainInit(oldRemotePath, true)
	require.NoError(t, err)
	newRemotePath := filepath.Join(t.TempDir(), "new.git")
	newRemote, err := git.PlainInit(newRemotePath, true)
	require.NoError(t, err)
	commitFileToRemote(t, newRemotePath, "master", "seed.md", "seed")

	fixture := newGitSyncNonFastForwardFixture(t, nil)
	svc := fixture.svc
	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	call := noteRepo.ExpectedCalls[len(noteRepo.ExpectedCalls)-1]
	call.Run(func(args mock.Arguments) {
		call.ReturnArguments = mock.Arguments{[]*domain.Note{
			{Path: "note.md", Content: "content", Action: domain.NoteActionCreate},
		}, nil}
	})

	conf := &domain.GitSyncConfig{ID: 1, UID: 1, VaultID: 1, RepoURL: oldRemotePath, Branch: "master"}
	require.NoError(t, svc.doSync(context.Background(), conf))
	// 旧远端此刻只有服务端的导出提交，取它作为“不应泄漏”的标记。
	localBefore, err := git.PlainOpen(svc.getWorkspacePath(conf.UID, conf.ID))
	require.NoError(t, err)
	oldHead, err := localBefore.Head()
	require.NoError(t, err)

	conf = updateConfigViaService(t, svc, conf, newRemotePath, "master")
	require.NoError(t, svc.doSync(context.Background(), conf))

	newHeadRef, err := newRemote.Reference(plumbing.NewBranchReferenceName("master"), true)
	require.NoError(t, err)
	newCommit, err := object.GetCommit(newRemote.Storer, newHeadRef.Hash())
	require.NoError(t, err)
	tree, err := newCommit.Tree()
	require.NoError(t, err)
	_, err = tree.File("note.md")
	require.NoError(t, err)

	// 新仓库的历史里不允许出现旧仓库工作区的提交。
	iter := object.NewCommitPreorderIter(newCommit, nil, nil)
	require.NoError(t, iter.ForEach(func(c *object.Commit) error {
		require.NotEqual(t, oldHead.Hash(), c.Hash, "commits from the old remote must not leak into the new remote")
		return nil
	}))
}
