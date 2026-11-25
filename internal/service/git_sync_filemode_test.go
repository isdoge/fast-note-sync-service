package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/stretchr/testify/require"
)

// seedFileModeWorkspace creates a workspace with the same shape as the failing blog export:
// repository files outside the export scope, notes and scripts inside it, and the two
// executable scripts a repository is expected to keep.
func seedFileModeWorkspace(t *testing.T) (*git.Repository, *git.Worktree, string) {
	t.Helper()
	wsPath := t.TempDir()
	repo, err := git.PlainInit(wsPath, false)
	require.NoError(t, err)

	files := map[string]struct {
		content string
		mode    os.FileMode
	}{
		"README.md":                          {"readme\n", 0644},
		".npmrc":                             {"registry=https://example.com\n", 0644},
		"deploy/app.sh":                      {"#!/bin/sh\necho app\n", 0755},
		"src/content/posts/old.md":           {"old\n", 0644},
		"src/content/posts/scripts/build.sh": {"#!/bin/sh\necho build\n", 0755},
		"src/content/posts/assets/pic.bin":   {"pic\n", 0644},
	}
	for rel, f := range files {
		full := filepath.Join(wsPath, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte(f.content), f.mode))
		require.NoError(t, os.Chmod(full, f.mode))
	}

	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	_, err = wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "seed", Email: "seed@example.com", When: time.Now()},
	})
	require.NoError(t, err)
	return repo, wt, wsPath
}

func mustTreeEntryMode(t *testing.T, tree *object.Tree, path string) filemode.FileMode {
	t.Helper()
	entry, err := tree.FindEntry(path)
	require.NoError(t, err, "expected %s in commit tree", path)
	return entry.Mode
}

func mustTreeFileContents(t *testing.T, tree *object.Tree, path string) string {
	t.Helper()
	f, err := tree.File(path)
	require.NoError(t, err, "expected %s in commit tree", path)
	content, err := f.Contents()
	require.NoError(t, err)
	return content
}

// TestGitSyncService_CommitIgnoresModeChangesAndStaysInScope reproduces the 2026-11-21
// incident: the workspace filesystem reported the owner execute bit for ordinary files, so
// go-git staged every tracked file as 100755 and committed the whole repository, including
// files outside TargetSubdir. The commit must contain only real content changes inside the
// export scope, keep tracked scripts executable, commit new files as 100644, and never
// include a mode change.
func TestGitSyncService_CommitIgnoresModeChangesAndStaysInScope(t *testing.T) {
	repo, wt, wsPath := seedFileModeWorkspace(t)
	seedHead, err := repo.Head()
	require.NoError(t, err)

	// 模拟事件里的磁盘权限：普通文件带上所有者执行位。
	for _, rel := range []string{"README.md", ".npmrc", "src/content/posts/old.md", "src/content/posts/assets/pic.bin"} {
		require.NoError(t, os.Chmod(filepath.Join(wsPath, filepath.FromSlash(rel)), 0755))
	}
	// 范围外的内容改动不得进入提交。
	require.NoError(t, os.WriteFile(filepath.Join(wsPath, "README.md"), []byte("readme changed\n"), 0644))
	// 范围内的真实改动：普通笔记改内容、脚本改内容（保持 0755）、新增普通文件。
	require.NoError(t, os.WriteFile(filepath.Join(wsPath, "src/content/posts/old.md"), []byte("old changed\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(wsPath, "src/content/posts/scripts/build.sh"), []byte("#!/bin/sh\necho build changed\n"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(wsPath, "src/content/posts/new.md"), []byte("new\n"), 0644))

	svc := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))
	conf := &domain.GitSyncConfig{ID: 1, TargetSubdir: "src/content/posts"}
	committed, err := svc.commitWorkspace(conf, repo, wt, wsPath)
	require.NoError(t, err)
	require.True(t, committed)

	head, err := repo.Head()
	require.NoError(t, err)
	require.NotEqual(t, seedHead.Hash(), head.Hash())
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)

	// 只有范围内的真实改动进入提交；纯模式差异和范围外内容都没有带上。
	stats, err := commit.Stats()
	require.NoError(t, err)
	changed := make(map[string]struct{}, len(stats))
	for _, st := range stats {
		changed[st.Name] = struct{}{}
	}
	require.Equal(t, map[string]struct{}{
		"src/content/posts/old.md":           {},
		"src/content/posts/scripts/build.sh": {},
		"src/content/posts/new.md":           {},
	}, changed)

	// 脚本保持 100755，普通文件与新文件都是 100644。
	require.Equal(t, filemode.Executable, mustTreeEntryMode(t, tree, "src/content/posts/scripts/build.sh"))
	require.Equal(t, filemode.Regular, mustTreeEntryMode(t, tree, "src/content/posts/old.md"))
	require.Equal(t, filemode.Regular, mustTreeEntryMode(t, tree, "src/content/posts/new.md"))
	require.Equal(t, filemode.Regular, mustTreeEntryMode(t, tree, "src/content/posts/assets/pic.bin"))
	require.Equal(t, filemode.Regular, mustTreeEntryMode(t, tree, "README.md"))
	require.Equal(t, filemode.Executable, mustTreeEntryMode(t, tree, "deploy/app.sh"))

	// 范围外的内容保持 HEAD 里的旧值。
	require.Equal(t, "readme\n", mustTreeFileContents(t, tree, "README.md"))
	require.Equal(t, "old changed\n", mustTreeFileContents(t, tree, "src/content/posts/old.md"))
	require.Equal(t, "#!/bin/sh\necho build changed\n", mustTreeFileContents(t, tree, "src/content/posts/scripts/build.sh"))
}

// TestGitSyncService_CommitSkipsModeOnlyChanges verifies that a workspace whose files only
// differ in permissions creates no commit, in both scoped and full-mirror modes, and that a
// new file written with the execute bit is still committed as 100644.
func TestGitSyncService_CommitSkipsModeOnlyChanges(t *testing.T) {
	repo, wt, wsPath := seedFileModeWorkspace(t)
	seedHead, err := repo.Head()
	require.NoError(t, err)

	for _, rel := range []string{"README.md", ".npmrc", "src/content/posts/old.md", "src/content/posts/assets/pic.bin"} {
		require.NoError(t, os.Chmod(filepath.Join(wsPath, filepath.FromSlash(rel)), 0755))
	}

	svc := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))

	committed, err := svc.commitWorkspace(&domain.GitSyncConfig{ID: 1, TargetSubdir: "src/content/posts"}, repo, wt, wsPath)
	require.NoError(t, err)
	require.False(t, committed, "mode-only differences must not produce a commit")

	committed, err = svc.commitWorkspace(&domain.GitSyncConfig{ID: 1}, repo, wt, wsPath)
	require.NoError(t, err)
	require.False(t, committed, "mode-only differences must not produce a commit in full-mirror mode")

	head, err := repo.Head()
	require.NoError(t, err)
	require.Equal(t, seedHead.Hash(), head.Hash())

	// 整库镜像模式下，新文件即便在磁盘上带执行位，也按 100644 提交。
	newPath := filepath.Join(wsPath, "notes", "new.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0755))
	require.NoError(t, os.WriteFile(newPath, []byte("new\n"), 0755))
	require.NoError(t, os.Chmod(newPath, 0755))

	committed, err = svc.commitWorkspace(&domain.GitSyncConfig{ID: 1}, repo, wt, wsPath)
	require.NoError(t, err)
	require.True(t, committed)

	head, err = repo.Head()
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	require.Equal(t, filemode.Regular, mustTreeEntryMode(t, tree, "notes/new.md"))
}

// TestEnsureWorkspaceFileModeConfig verifies the workspace gets core.fileMode=false without
// losing its existing repository settings (remotes must survive the config rewrite).
func TestEnsureWorkspaceFileModeConfig(t *testing.T) {
	repo, _, _ := seedFileModeWorkspace(t)
	_, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{"https://example.com/repo.git"}})
	require.NoError(t, err)

	require.NoError(t, ensureWorkspaceFileModeConfig(repo))
	cfg, err := repo.Config()
	require.NoError(t, err)
	require.Equal(t, "false", cfg.Raw.Section("core").Option("filemode"))
	require.Equal(t, "https://example.com/repo.git", cfg.Remotes["origin"].URLs[0])

	// 已经是 false 时再调用一次也不报错。
	require.NoError(t, ensureWorkspaceFileModeConfig(repo))
}

// TestGitSyncService_CommitStagesScopeDeletionAndRename verifies that removing a note and
// renaming another inside the export scope both reach the commit, while a content change
// outside the scope stays uncommitted.
// TestGitSyncService_CommitStagesScopeDeletionAndRename 验证导出范围内删除笔记、改名笔记都会
// 进入提交，而范围外的内容改动不会。
func TestGitSyncService_CommitStagesScopeDeletionAndRename(t *testing.T) {
	repo, wt, wsPath := seedFileModeWorkspace(t)

	// 范围内的删除与改名：old.md 删除，pic.bin 改名并保留内容。
	require.NoError(t, os.Remove(filepath.Join(wsPath, "src/content/posts/old.md")))
	require.NoError(t, os.Rename(
		filepath.Join(wsPath, "src/content/posts/assets/pic.bin"),
		filepath.Join(wsPath, "src/content/posts/assets/renamed.bin"),
	))
	// 范围外的改动不得进入提交。
	require.NoError(t, os.WriteFile(filepath.Join(wsPath, "README.md"), []byte("outside changed\n"), 0644))

	svc := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))
	committed, err := svc.commitWorkspace(&domain.GitSyncConfig{ID: 1, TargetSubdir: "src/content/posts"}, repo, wt, wsPath)
	require.NoError(t, err)
	require.True(t, committed, "a deletion and a rename inside the scope must produce a commit")

	head, err := repo.Head()
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)

	stats, err := commit.Stats()
	require.NoError(t, err)
	changed := make(map[string]struct{}, len(stats))
	for _, st := range stats {
		changed[st.Name] = struct{}{}
	}
	require.Equal(t, map[string]struct{}{
		"src/content/posts/old.md": {},
		// go-git 把改名统计为「旧路径 => 新路径」一条。
		// go-git reports a rename as a single "old => new" entry.
		"src/content/posts/assets/pic.bin => src/content/posts/assets/renamed.bin": {},
	}, changed)

	// 删除与改名都落到 HEAD 的树里。
	_, err = tree.File("src/content/posts/old.md")
	require.Error(t, err, "deleted note must be gone from the commit tree")
	_, err = tree.File("src/content/posts/assets/pic.bin")
	require.Error(t, err, "renamed source must be gone from the commit tree")
	require.Equal(t, "pic\n", mustTreeFileContents(t, tree, "src/content/posts/assets/renamed.bin"))
	// 范围外的文件保持 HEAD 内容。
	require.Equal(t, "readme\n", mustTreeFileContents(t, tree, "README.md"))
}

// TestGitSyncService_CommitStagesScopeDeletionWhenDirectoryMissing verifies the review fix:
// when the export directory itself no longer exists, its tracked files are staged as
// deletions instead of being silently skipped, and nothing outside the scope is removed.
// TestGitSyncService_CommitStagesScopeDeletionWhenDirectoryMissing 验证复核修复：导出目录整个
// 不存在时，其下已跟踪文件按删除提交，而不是被静默跳过；范围外的文件不受影响。
func TestGitSyncService_CommitStagesScopeDeletionWhenDirectoryMissing(t *testing.T) {
	repo, wt, wsPath := seedFileModeWorkspace(t)

	// 整个导出目录消失，同时改动一个范围外的文件。
	require.NoError(t, os.RemoveAll(filepath.Join(wsPath, "src/content/posts")))
	require.NoError(t, os.WriteFile(filepath.Join(wsPath, "README.md"), []byte("outside changed\n"), 0644))

	svc := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))
	committed, err := svc.commitWorkspace(&domain.GitSyncConfig{ID: 1, TargetSubdir: "src/content/posts"}, repo, wt, wsPath)
	require.NoError(t, err)
	require.True(t, committed, "deletions under a missing scope directory must produce a commit")

	head, err := repo.Head()
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)

	stats, err := commit.Stats()
	require.NoError(t, err)
	changed := make(map[string]struct{}, len(stats))
	for _, st := range stats {
		changed[st.Name] = struct{}{}
	}
	require.Equal(t, map[string]struct{}{
		"src/content/posts/old.md":           {},
		"src/content/posts/scripts/build.sh": {},
		"src/content/posts/assets/pic.bin":   {},
	}, changed)

	// 范围内文件从树里消失，范围外文件原样保留。
	_, err = tree.File("src/content/posts/old.md")
	require.Error(t, err)
	require.Equal(t, "readme\n", mustTreeFileContents(t, tree, "README.md"))
	require.Equal(t, filemode.Executable, mustTreeEntryMode(t, tree, "deploy/app.sh"))
}
