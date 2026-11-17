package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// gitSyncNonFastForwardFixture wires a git sync service whose note/file repositories serve
// scripted pages, so a test can replay the note set exported by each sync run.
type gitSyncNonFastForwardFixture struct {
	svc       *gitSyncService
	notePages [][]*domain.Note
	noteCalls int
}

func newGitSyncNonFastForwardFixture(t *testing.T, notePages [][]*domain.Note) *gitSyncNonFastForwardFixture {
	t.Helper()
	f := &gitSyncNonFastForwardFixture{notePages: notePages}

	repo := new(domainmocks.MockGitSyncRepository)
	f.svc = newTestGitSyncService(repo)

	noteRepo := f.svc.noteRepo.(*domainmocks.MockNoteRepository)
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{}, nil)
	call := noteRepo.ExpectedCalls[len(noteRepo.ExpectedCalls)-1]
	call.Run(func(args mock.Arguments) {
		var notes []*domain.Note
		if f.noteCalls < len(f.notePages) {
			notes = f.notePages[f.noteCalls]
		}
		f.noteCalls++
		call.ReturnArguments = mock.Arguments{notes, nil}
	})

	f.svc.fileRepo.(*domainmocks.MockFileRepository).
		On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.File{}, nil)

	return f
}

// advanceRemote clones the bare remote, commits relPath on top of its current head and pushes,
// simulating another writer moving the remote branch forward.
func advanceRemote(t *testing.T, remotePath, relPath, content string) plumbing.Hash {
	t.Helper()
	otherPath := filepath.Join(t.TempDir(), "other")
	other, err := git.PlainClone(otherPath, false, &git.CloneOptions{URL: remotePath})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(otherPath, relPath), []byte(content), 0644))
	wt, err := other.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(relPath)
	require.NoError(t, err)
	hash, err := wt.Commit("remote change", &git.CommitOptions{Author: &object.Signature{Name: "other", Email: "other@example.com"}})
	require.NoError(t, err)
	require.NoError(t, other.Push(&git.PushOptions{}))
	return hash
}

// commitInWorkspace creates a local commit in the service workspace without pushing it,
// simulating leftover unpushed commits from a previously failed sync.
func commitInWorkspace(t *testing.T, wsPath, relPath, content string) {
	t.Helper()
	repo, err := git.PlainOpen(wsPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(wsPath, relPath), []byte(content), 0644))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(relPath)
	require.NoError(t, err)
	_, err = wt.Commit("local unpushed change", &git.CommitOptions{Author: &object.Signature{Name: "fns", Email: "fns@example.com"}})
	require.NoError(t, err)
}

// requireAncestor asserts that ancestor is reachable from head, i.e. the remote history was
// extended without a force push.
func requireAncestor(t *testing.T, repo *git.Repository, head, ancestor plumbing.Hash) {
	t.Helper()
	headCommit, err := object.GetCommit(repo.Storer, head)
	require.NoError(t, err)
	iter := object.NewCommitPreorderIter(headCommit, nil, nil)
	found := false
	require.NoError(t, iter.ForEach(func(c *object.Commit) error {
		if c.Hash == ancestor {
			found = true
			return storer.ErrStop
		}
		return nil
	}))
	require.True(t, found, "expected %s to be an ancestor of %s (no force push)", ancestor, head)
}

// TestGitSyncService_PushRecoversWhenRemoteMovesBetweenPullAndPush reproduces issue #435:
// the remote branch advances after the pull but before the push (another writer, or a push
// session that saw a stale expected SHA). The old code returned the non-fast-forward error
// and froze in failed state; the sync must instead realign to the remote head, re-export the
// notes on top of it and push again without discarding remote commits.
func TestGitSyncService_PushRecoversWhenRemoteMovesBetweenPullAndPush(t *testing.T) {
	t.Chdir(t.TempDir())
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	remote, err := git.PlainInit(remotePath, true)
	require.NoError(t, err)

	var injectedHash plumbing.Hash
	injected := false

	fixture := newGitSyncNonFastForwardFixture(t, nil)
	svc := fixture.svc
	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	call := noteRepo.ExpectedCalls[len(noteRepo.ExpectedCalls)-1]
	call.Run(func(args mock.Arguments) {
		var notes []*domain.Note
		switch fixture.noteCalls {
		case 0:
			notes = []*domain.Note{{Path: "a.md", Content: "first", Action: domain.NoteActionCreate}}
		case 1:
			// Remote moves forward after this run's pull but before its push.
			if !injected {
				injected = true
				injectedHash = advanceRemote(t, remotePath, "other.md", "remote change")
			}
			notes = []*domain.Note{{Path: "b.md", Content: "second", Action: domain.NoteActionCreate}}
		case 2:
			// Recovery re-export after realigning to the remote head.
			notes = []*domain.Note{{Path: "b.md", Content: "second", Action: domain.NoteActionCreate}}
		}
		fixture.noteCalls++
		call.ReturnArguments = mock.Arguments{notes, nil}
	})

	conf := &domain.GitSyncConfig{ID: 1, UID: 1, VaultID: 1, RepoURL: remotePath, Branch: "master"}
	require.NoError(t, svc.doSync(context.Background(), conf))

	now := time.Now()
	conf.LastSyncTime = &now
	require.NoError(t, svc.doSync(context.Background(), conf))
	require.True(t, injected, "test setup must move the remote between pull and push")

	remoteHead, err := remote.Reference(plumbing.NewBranchReferenceName("master"), false)
	require.NoError(t, err)
	require.NotEqual(t, injectedHash, remoteHead.Hash(), "recovery must add a commit on top of the remote head")

	local, err := git.PlainOpen(svc.getWorkspacePath(conf.UID, conf.ID))
	require.NoError(t, err)
	localHead, err := local.Head()
	require.NoError(t, err)
	require.Equal(t, localHead.Hash(), remoteHead.Hash(), "local and remote heads must match after recovery")

	// No force push: the commit injected by the other writer is an ancestor of the new head.
	requireAncestor(t, remote, remoteHead.Hash(), injectedHash)
	require.FileExists(t, filepath.Join(svc.getWorkspacePath(conf.UID, conf.ID), "other.md"))
	require.FileExists(t, filepath.Join(svc.getWorkspacePath(conf.UID, conf.ID), "b.md"))
}

// TestGitSyncService_PullRecoversWhenWorkspaceDiverged covers the other #435 failure mode:
// leftover unpushed local commits plus a remote that moved forward make the forced pull fail
// with non-fast-forward (go-git never force-aligns a diverged branch). The sync must reset
// the workspace to the remote head and re-export on top of it.
func TestGitSyncService_PullRecoversWhenWorkspaceDiverged(t *testing.T) {
	t.Chdir(t.TempDir())
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	remote, err := git.PlainInit(remotePath, true)
	require.NoError(t, err)

	fixture := newGitSyncNonFastForwardFixture(t, [][]*domain.Note{
		{{Path: "a.md", Content: "first", Action: domain.NoteActionCreate}},
		{{Path: "b.md", Content: "second", Action: domain.NoteActionCreate}},
	})
	svc := fixture.svc

	conf := &domain.GitSyncConfig{ID: 1, UID: 1, VaultID: 1, RepoURL: remotePath, Branch: "master"}
	require.NoError(t, svc.doSync(context.Background(), conf))

	wsPath := svc.getWorkspacePath(conf.UID, conf.ID)
	commitInWorkspace(t, wsPath, "pending.md", "leftover unpushed commit")
	otherHash := advanceRemote(t, remotePath, "other.md", "remote change")

	now := time.Now()
	conf.LastSyncTime = &now
	require.NoError(t, svc.doSync(context.Background(), conf))

	remoteHead, err := remote.Reference(plumbing.NewBranchReferenceName("master"), false)
	require.NoError(t, err)
	local, err := git.PlainOpen(wsPath)
	require.NoError(t, err)
	localHead, err := local.Head()
	require.NoError(t, err)
	require.Equal(t, localHead.Hash(), remoteHead.Hash())
	requireAncestor(t, remote, remoteHead.Hash(), otherHash)
	require.FileExists(t, filepath.Join(wsPath, "other.md"))
	require.FileExists(t, filepath.Join(wsPath, "b.md"))
}

// TestGitSyncService_SyncPushesLeftoverCommits covers the self-inflicted half of #435: a
// previous run pushed nothing (e.g. its push failed), leaving local commits the remote lacks.
// The next run exports no new notes, and the old code returned "no changes" without pushing,
// so the remote stayed behind forever. The sync must notice the leftover commits and push.
func TestGitSyncService_SyncPushesLeftoverCommits(t *testing.T) {
	t.Chdir(t.TempDir())
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	remote, err := git.PlainInit(remotePath, true)
	require.NoError(t, err)

	fixture := newGitSyncNonFastForwardFixture(t, [][]*domain.Note{
		{{Path: "a.md", Content: "first", Action: domain.NoteActionCreate}},
	})
	svc := fixture.svc

	conf := &domain.GitSyncConfig{ID: 1, UID: 1, VaultID: 1, RepoURL: remotePath, Branch: "master"}
	require.NoError(t, svc.doSync(context.Background(), conf))

	wsPath := svc.getWorkspacePath(conf.UID, conf.ID)
	commitInWorkspace(t, wsPath, "pending.md", "leftover unpushed commit")
	local, err := git.PlainOpen(wsPath)
	require.NoError(t, err)
	leftover, err := local.Head()
	require.NoError(t, err)

	now := time.Now()
	conf.LastSyncTime = &now
	require.NoError(t, svc.doSync(context.Background(), conf))

	remoteHead, err := remote.Reference(plumbing.NewBranchReferenceName("master"), false)
	require.NoError(t, err)
	require.Equal(t, leftover.Hash(), remoteHead.Hash(), "leftover local commits must be pushed even when the mirror exports nothing new")
}
