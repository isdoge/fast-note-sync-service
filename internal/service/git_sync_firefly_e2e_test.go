package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// gitHTTPBackendPath 在常见位置寻找 git-http-backend；找不到时跳过端到端用例。
func gitHTTPBackendPath(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"/usr/lib/git-core/git-http-backend",
		"/usr/libexec/git-core/git-http-backend",
		"/Library/Developer/CommandLineTools/usr/libexec/git-core/git-http-backend",
		"/opt/homebrew/libexec/git-core/git-http-backend",
		"/usr/local/libexec/git-core/git-http-backend",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return c
		}
	}
	if p, err := exec.LookPath("git-http-backend"); err == nil {
		return p
	}
	t.Skip("git-http-backend not available on this host")
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestFireflyGateway_EndToEnd_PostReceiveSideband 跑完整链路：
// FNS 通过本机回环 git-http-backend 网关推送到 bare 仓库，bare 仓库的 post-receive
// 钩子输出经由 sideband 回到 PushOptions.Progress，并在同步状态里如实体现。
// 同时验证受保护路径在子目录沙箱下不被触碰、失败标记会让本轮同步报错。
func TestFireflyGateway_EndToEnd_PostReceiveSideband(t *testing.T) {
	backend := gitHTTPBackendPath(t)
	// 脚本路径必须在 Chdir 之前解析为绝对路径。
	// Resolve the script path before changing the working directory.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	gatewayScript := filepath.Join(filepath.Dir(thisFile), "..", "..", "scripts", "firefly-git-http.py")
	gatewayScript, err := filepath.Abs(gatewayScript)
	require.NoError(t, err)
	t.Chdir(t.TempDir())

	root := t.TempDir()
	repoName := "blog.git"
	bare := filepath.Join(root, repoName)
	_, err = exec.Command("git", "init", "--bare", "--initial-branch=master", bare).CombinedOutput()
	require.NoError(t, err)

	// post-receive：往 stdout 写 sideband 回执，内容由 marker 文件控制。
	markerFile := filepath.Join(t.TempDir(), "marker")
	require.NoError(t, os.WriteFile(markerFile, []byte("ok"), 0644))
	hook := fmt.Sprintf("#!/bin/sh\necho \"Preview deployed: $(git rev-parse --short HEAD)\"\nif [ -f %q ]; then cat %q; fi\n", markerFile, markerFile)
	require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "post-receive"), []byte(hook), 0755))

	// 预置受保护文件
	seed := filepath.Join(t.TempDir(), "seed")
	seedRepo, err := git.PlainInit(seed, false)
	require.NoError(t, err)
	for rel, content := range map[string]string{
		"package.json":                       `{"name":"blog"}`,
		"src/config/settings-overrides.json": `{"site":"nas"}`,
		"src/components/Header.astro":        "<header/>",
	} {
		full := filepath.Join(seed, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0644))
	}
	seedWT, err := seedRepo.Worktree()
	require.NoError(t, err)
	require.NoError(t, seedWT.AddWithOptions(&git.AddOptions{All: true}))
	_, err = seedWT.Commit("seed", &git.CommitOptions{Author: &object.Signature{Name: "seed", Email: "seed@example.com", When: time.Now()}})
	require.NoError(t, err)
	_, err = seedRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{bare}})
	require.NoError(t, err)
	require.NoError(t, seedRepo.Push(&git.PushOptions{RemoteName: "origin"}))

	// 网关脚本（纯标准库）以子进程方式启动
	tokenFile := filepath.Join(t.TempDir(), "token")
	const testUser, testPass = "fns", "local-e2e-token"
	require.NoError(t, os.WriteFile(tokenFile, []byte(testUser+":"+testPass+"\n"), 0600))
	port := freePort(t)
	gateway := exec.Command("python3",
		gatewayScript,
		"--root", root,
		"--repo", repoName,
		"--token-file", tokenFile,
		"--git-http-backend", backend,
		"--port", fmt.Sprint(port),
		"--no-republish",
		"--quiet",
	)
	gateway.Stderr = os.Stderr
	require.NoError(t, gateway.Start())
	t.Cleanup(func() {
		_ = gateway.Process.Kill()
		_, _ = gateway.Process.Wait()
	})

	// 等端口就绪
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 10*time.Second, 50*time.Millisecond)

	remoteURL := fmt.Sprintf("http://127.0.0.1:%d/%s", port, repoName)

	repo := new(domainmocks.MockGitSyncRepository)
	repo.On("Save", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncConfig{}, nil)
	repo.On("CreateHistory", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncHistory{}, nil)
	svc := newTestGitSyncService(repo)
	enableLocalGitRepoPaths(t, svc)
	svc.gitConf.AllowedPrivateHosts = []string{"127.0.0.1"}

	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{{Path: "Blog/post.md", Content: "---\ntitle: post\npublished: 2026-11-20\n---\nbody", Action: domain.NoteActionCreate}}, nil).Once()
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{}, nil)
	svc.fileRepo.(*domainmocks.MockFileRepository).
		On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.File{}, nil)

	conf := &domain.GitSyncConfig{
		ID: 1, UID: 1, VaultID: 1, RepoURL: remoteURL, Branch: "master", Username: testUser, Password: testPass,
		SourceDir: "Blog", TargetSubdir: "src/content/posts", ValidateFrontmatter: true,
	}
	svc.syncTask(context.Background(), conf)
	require.Equal(t, int64(domain.GitSyncStatusSuccess), conf.LastStatus, conf.LastMessage)
	require.Contains(t, conf.LastMessage, "Preview deployed:", "post-receive sideband receipt must surface in the status message")

	// 远端仓库：受保护文件原样保留，文章已进入目标子目录
	check := filepath.Join(t.TempDir(), "check")
	_, err = git.PlainClone(check, false, &git.CloneOptions{URL: bare})
	require.NoError(t, err)
	for rel, want := range map[string]string{
		"package.json":                       `{"name":"blog"}`,
		"src/config/settings-overrides.json": `{"site":"nas"}`,
		"src/components/Header.astro":        "<header/>",
	} {
		got, err := os.ReadFile(filepath.Join(check, rel))
		require.NoError(t, err, rel)
		require.Equal(t, want, string(got), rel)
	}
	require.FileExists(t, filepath.Join(check, "src/content/posts/post.md"))

	// 第二轮：远端 hook 报“等待确认设置”→ 同步状态必须为失败并带原因
	require.NoError(t, os.WriteFile(markerFile, []byte("Saved settings await confirmation: rebuild from /settings/\n"), 0644))
	noteRepo.ExpectedCalls = nil
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{{Path: "Blog/post.md", Content: "---\ntitle: post\npublished: 2026-11-20\n---\nbody v2", Action: domain.NoteActionCreate}}, nil).Once()
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{}, nil)

	conf.LastSyncTime = nil
	svc.syncTask(context.Background(), conf)
	require.Equal(t, int64(domain.GitSyncStatusFailed), conf.LastStatus, conf.LastMessage)
	require.Contains(t, conf.LastMessage, "Saved settings await confirmation")
	require.True(t, strings.Contains(conf.LastMessage, "remote hook deploy failed"))
}

// startFireflyGatewayForTest 启动网关子进程并返回远端 URL 与清理函数。
func startFireflyGatewayForTest(t *testing.T, backend, root, repoName string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	gatewayScript := filepath.Join(filepath.Dir(thisFile), "..", "..", "scripts", "firefly-git-http.py")
	gatewayScript, err := filepath.Abs(gatewayScript)
	require.NoError(t, err)

	tokenFile := filepath.Join(t.TempDir(), "token")
	const testUser, testPass = "fns", "local-e2e-token"
	require.NoError(t, os.WriteFile(tokenFile, []byte(testUser+":"+testPass+"\n"), 0600))
	port := freePort(t)
	gateway := exec.Command("python3",
		gatewayScript,
		"--root", root,
		"--repo", repoName,
		"--token-file", tokenFile,
		"--git-http-backend", backend,
		"--port", fmt.Sprint(port),
		"--no-republish",
		"--quiet",
	)
	gateway.Stderr = os.Stderr
	require.NoError(t, gateway.Start())
	t.Cleanup(func() {
		_ = gateway.Process.Kill()
		_, _ = gateway.Process.Wait()
	})
	require.Eventually(t, func() bool {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 10*time.Second, 50*time.Millisecond)
	return fmt.Sprintf("http://127.0.0.1:%d/%s", port, repoName)
}

// newEmptyBareRepo 创建一个空的 bare 仓库并返回根目录与仓库名。
func newEmptyBareRepo(t *testing.T, repoName string) (root string) {
	t.Helper()
	root = t.TempDir()
	_, err := exec.Command("git", "init", "--bare", "--initial-branch=master", filepath.Join(root, repoName)).CombinedOutput()
	require.NoError(t, err)
	return root
}

// TestFireflyGateway_ChunkedRequestBody 覆盖 P0-07：go-git 用 chunked 编码
// （ContentLength == -1）发送推送请求时，网关必须完整解码请求体，否则 receive-pack
// 会拿到空包并拒绝推送。
// A chunked push (ContentLength == -1) must be decoded and forwarded intact.
func TestFireflyGateway_ChunkedRequestBody(t *testing.T) {
	backend := gitHTTPBackendPath(t)
	repoName := "blog.git"
	root := newEmptyBareRepo(t, repoName)
	remoteURL := startFireflyGatewayForTest(t, backend, root, repoName)
	t.Chdir(t.TempDir())

	repo := new(domainmocks.MockGitSyncRepository)
	repo.On("Save", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncConfig{}, nil)
	repo.On("CreateHistory", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncHistory{}, nil)
	svc := newTestGitSyncService(repo)
	enableLocalGitRepoPaths(t, svc)
	svc.gitConf.AllowedPrivateHosts = []string{"127.0.0.1"}

	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{{Path: "a.md", Content: "---\ntitle: t\n---\nbody", Action: domain.NoteActionCreate}}, nil).Once()
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{}, nil)
	svc.fileRepo.(*domainmocks.MockFileRepository).
		On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.File{}, nil)

	conf := &domain.GitSyncConfig{
		ID: 1, UID: 1, VaultID: 1, RepoURL: remoteURL, Branch: "master",
		Username: "fns", Password: "local-e2e-token",
	}
	svc.syncTask(context.Background(), conf)
	require.Equal(t, int64(domain.GitSyncStatusSuccess), conf.LastStatus, conf.LastMessage)

	check := filepath.Join(t.TempDir(), "check")
	_, err := git.PlainClone(check, false, &git.CloneOptions{URL: filepath.Join(root, repoName)})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(check, "a.md"))
}

// TestFireflyGateway_RejectsMaliciousSubPath 覆盖 P2-01：解码后的子路径含 NUL、反斜杠
// 或 '..' 段时必须回 400，绝不能把越界路径交给 git-http-backend。
// Decoded sub-paths with NUL, backslashes or '..' must be rejected with 400.
func TestFireflyGateway_RejectsMaliciousSubPath(t *testing.T) {
	backend := gitHTTPBackendPath(t)
	repoName := "blog.git"
	root := newEmptyBareRepo(t, repoName)
	remoteURL := startFireflyGatewayForTest(t, backend, root, repoName)

	do := func(rawPath string) int {
		req, err := http.NewRequest(http.MethodGet, remoteURL+rawPath, nil)
		require.NoError(t, err)
		req.SetBasicAuth("fns", "local-e2e-token")
		// 直接用 RawPath 语义发送，避免 net/http 替我们规范化 '..'。
		req.URL.Opaque = "//" + req.URL.Host + rawPath
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	// 白名单仓库内的恶意子路径必须回 400（解码后再判定，%2e%2e 同样被拦）。
	for _, raw := range []string{"/blog.git/../etc/passwd", "/blog.git/a%5Cb", "/blog.git/a%00b", "/blog.git/%2e%2e/x"} {
		require.Equal(t, http.StatusBadRequest, do(raw), "raw path %q must be rejected", raw)
	}
	// 仓库名本身不在白名单：仍是 404，不会把请求交给 git-http-backend。
	for _, raw := range []string{"/../etc/passwd", "/other.git/info/refs"} {
		require.Equal(t, http.StatusNotFound, do(raw), "raw path %q must not resolve", raw)
	}
}
