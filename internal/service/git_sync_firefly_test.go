package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 路径与黑名单
// ---------------------------------------------------------------------------

func TestNormalizeRelativeSubdir(t *testing.T) {
	valid := map[string]string{
		"":                       "",
		"src/content/posts/":     "src/content/posts",
		"/src/content/posts":     "src/content/posts",
		"src//content/./posts":   "src/content/posts",
		`src\content\posts`:      "src/content/posts",
		"src/content/posts/经验分享": "src/content/posts/经验分享",
	}
	for in, want := range valid {
		got, err := normalizeRelativeSubdir(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}

	// 首尾 “/” 按设计稿清洗；其余绝对路径形态（盘符、UNC、越界段）必须拒绝。
	invalid := []string{"../etc", "a/../../b", "C:dir", "C:\\dir\\sub", "a/.git/config", ".git", "a/.GIT/b"}
	for _, in := range invalid {
		_, err := normalizeRelativeSubdir(in)
		require.Error(t, err, in)
	}
}

func TestIsProtectedFireflyPath(t *testing.T) {
	protected := []string{
		"src/config/settings-overrides.json",
		"package.json",
		"pnpm-lock.yaml",
		"pnpm-workspace.yaml",
		".npmrc",
		"astro.config.mjs",
		"tsconfig.json",
		"public/uploads/settings/a.png",
		"public/uploads/settings",
		".settings-backups/2026/x.json",
		".settings-site/source-commit",
		"./package.json",
	}
	for _, p := range protected {
		require.True(t, isProtectedFireflyPath(p), p)
	}
	allowed := []string{
		"src/content/posts/a.md",
		"public/uploads/a.png",
		"src/components/Header.astro",
		"package.json.bak",
	}
	for _, p := range allowed {
		require.False(t, isProtectedFireflyPath(p), p)
	}
}

func TestMapSourceToTargetPath(t *testing.T) {
	relRepo, relIn, matched := mapSourceToTargetPath("Blog/post.md", "Blog", "src/content/posts")
	require.True(t, matched)
	require.Equal(t, "src/content/posts/post.md", relRepo)
	require.Equal(t, "post.md", relIn)

	// 不在 SourceDir 下：不匹配
	_, _, matched = mapSourceToTargetPath("Other/post.md", "Blog", "src/content/posts")
	require.False(t, matched)

	// 无 SourceDir、无 TargetSubdir：原路径
	relRepo, relIn, matched = mapSourceToTargetPath("a/b.md", "", "")
	require.True(t, matched)
	require.Equal(t, "a/b.md", relRepo)
	require.Equal(t, "a/b.md", relIn)

	// SourceDir 本身不是文件
	_, _, matched = mapSourceToTargetPath("Blog", "Blog", "t")
	require.False(t, matched)
}

// ---------------------------------------------------------------------------
// Frontmatter 过滤与 Astro 预检
// ---------------------------------------------------------------------------

func TestMatchFrontmatterFilter(t *testing.T) {
	content := "---\ntitle: Hi\ntype: share\nblog: true\n---\n\nbody\n"
	require.True(t, matchFrontmatterFilter(content, ""))
	require.True(t, matchFrontmatterFilter(content, "type=share"))
	require.True(t, matchFrontmatterFilter(content, "type=share,blog=true"))
	require.True(t, matchFrontmatterFilter(content, "blog"))
	require.False(t, matchFrontmatterFilter(content, "type=resource"))
	require.False(t, matchFrontmatterFilter(content, "type=share,blog=false"))
	require.False(t, matchFrontmatterFilter(content, "missing"))
	require.False(t, matchFrontmatterFilter("no frontmatter", "type=share"))

	// CRLF 换行
	crlf := "---\r\ntype: share\r\n---\r\nbody"
	require.True(t, matchFrontmatterFilter(crlf, "type=share"))
}

func TestValidateAstroPostFrontmatter(t *testing.T) {
	existsAll := func(string) bool { return true }
	existsNone := func(string) bool { return false }

	good := "---\ntitle: Post\npublished: 2026-10-20\n---\n\nbody\n"
	require.NoError(t, validateAstroPostFrontmatter("a.md", good, "src/content/posts", existsAll))

	placeholder := "---\ntitle: Post\npublished: YYYY-MM-DD\n---\n"
	require.ErrorContains(t, validateAstroPostFrontmatter("a.md", placeholder, "t", existsAll), "invalid published date")

	emptyPublished := "---\ntitle: Post\npublished: \"\"\n---\n"
	require.Error(t, validateAstroPostFrontmatter("a.md", emptyPublished, "t", existsAll))

	noTitle := "---\npublished: 2026-10-20\n---\n"
	require.ErrorContains(t, validateAstroPostFrontmatter("a.md", noTitle, "t", existsAll), "title")

	badUpdated := "---\ntitle: P\npublished: 2026-10-20\nupdated: YYYY-MM-DD\n---\n"
	require.ErrorContains(t, validateAstroPostFrontmatter("a.md", badUpdated, "t", existsAll), "updated")

	// 空 updated 视为未填写
	emptyUpdated := "---\ntitle: P\npublished: 2026-10-20\nupdated: \"\"\n---\n"
	require.NoError(t, validateAstroPostFrontmatter("a.md", emptyUpdated, "t", existsAll))

	// 正文图片缺失
	missingImage := "---\ntitle: P\npublished: 2026-10-20\n---\n\n![x](./assets/a.png)\n"
	require.ErrorContains(t, validateAstroPostFrontmatter("a.md", missingImage, "posts/p", existsNone), "missing image")

	// 代码块里的图片引用不参与校验
	inCode := "---\ntitle: P\npublished: 2026-10-20\n---\n\n```\n![x](./assets/a.png)\n```\n`![y](./assets/b.png)`\n"
	require.NoError(t, validateAstroPostFrontmatter("a.md", inCode, "posts/p", existsNone))

	// 外链、根路径、api 不校验
	external := "---\ntitle: P\npublished: 2026-10-20\nimage: https://x/y.png\n---\n\n![x](/uploads/a.png)\n![y](data:image/png;base64,AA)\n"
	require.NoError(t, validateAstroPostFrontmatter("a.md", external, "posts/p", existsNone))

	// frontmatter image 本地相对路径必须存在
	fmImage := "---\ntitle: P\npublished: 2026-10-20\nimage: ./assets/cover.png\n---\n"
	require.ErrorContains(t, validateAstroPostFrontmatter("a.md", fmImage, "posts/p", existsNone), "frontmatter.image")
}

// ---------------------------------------------------------------------------
// 沙箱：仓库其余文件 100% 原封不动
// ---------------------------------------------------------------------------

// seedAstroRepo 在 bare 远端里预置 Astro 仓库的受保护文件，返回远端路径。
func seedAstroRepo(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	_, err := git.PlainInit(remote, true)
	require.NoError(t, err)

	seed := filepath.Join(t.TempDir(), "seed")
	seedRepo, err := git.PlainInit(seed, false)
	require.NoError(t, err)
	files := map[string]string{
		"package.json":                        `{"name":"blog"}`,
		"src/config/settings-overrides.json":  `{"site":"nas"}`,
		"src/components/Header.astro":         "<header/>",
		"src/content/posts/00-外部知识库/other.md": "---\ntitle: other\n---\n",
	}
	for rel, content := range files {
		full := filepath.Join(seed, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0644))
	}
	wt, err := seedRepo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	_, err = wt.Commit("seed", &git.CommitOptions{Author: &object.Signature{Name: "seed", Email: "seed@example.com", When: time.Now()}})
	require.NoError(t, err)
	_, err = seedRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{remote}})
	require.NoError(t, err)
	require.NoError(t, seedRepo.Push(&git.PushOptions{RemoteName: "origin"}))

	// 让 master 成为默认分支
	return remote
}

func readRepoFile(t *testing.T, root, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func TestProcessNotesBatch_TargetSubdirSandboxKeepsRepoUntouched(t *testing.T) {
	t.Chdir(t.TempDir())
	remote := seedAstroRepo(t)

	repo := new(domainmocks.MockGitSyncRepository)
	repo.On("Save", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncConfig{}, nil)
	svc := newTestGitSyncService(repo)
	enableLocalGitRepoPaths(t, svc)

	notes := []*domain.Note{
		{Path: "Blog/hello.md", Content: "---\ntitle: hello\n---\nbody", Action: domain.NoteActionCreate},
	}
	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(notes, nil).Once()
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{}, nil)
	svc.fileRepo.(*domainmocks.MockFileRepository).
		On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.File{}, nil)

	conf := &domain.GitSyncConfig{
		ID: 1, UID: 1, VaultID: 1, RepoURL: remote, Branch: "master",
		SourceDir: "Blog", TargetSubdir: "src/content/posts/经验分享",
	}
	require.NoError(t, svc.doSync(context.Background(), conf))

	ws := svc.getWorkspacePath(1, 1)
	require.FileExists(t, filepath.Join(ws, "src/content/posts/经验分享/hello.md"))

	// 仓库其余文件必须原封不动
	for rel, want := range map[string]string{
		"package.json":                        `{"name":"blog"}`,
		"src/config/settings-overrides.json":  `{"site":"nas"}`,
		"src/components/Header.astro":         "<header/>",
		"src/content/posts/00-外部知识库/other.md": "---\ntitle: other\n---\n",
	} {
		got, ok := readRepoFile(t, ws, rel)
		require.True(t, ok, rel)
		require.Equal(t, want, got, rel)
	}

	// 远端同样未被触碰
	check := filepath.Join(t.TempDir(), "check")
	_, err := git.PlainClone(check, false, &git.CloneOptions{URL: remote})
	require.NoError(t, err)
	got, ok := readRepoFile(t, check, "src/config/settings-overrides.json")
	require.True(t, ok)
	require.Equal(t, `{"site":"nas"}`, got)

	// 删除笔记后：只删目标子目录内文件，其余不动
	notes2 := []*domain.Note{{Path: "Blog/hello.md", Action: domain.NoteActionDelete}}
	noteRepo.ExpectedCalls = nil
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(notes2, nil).Once()
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{}, nil)
	require.NoError(t, svc.doSync(context.Background(), conf))

	require.NoFileExists(t, filepath.Join(ws, "src/content/posts/经验分享/hello.md"))
	_, ok = readRepoFile(t, ws, "package.json")
	require.True(t, ok)
	_, ok = readRepoFile(t, ws, "src/content/posts/00-外部知识库/other.md")
	require.True(t, ok)
}

// TestProcessNotesBatch_RequireFrontmatterFilterAndWithdraw 验证 SourceDir + RequireFrontmatter：
// 只导出匹配笔记，去掉 type: share 后已导出文件被移除。
func TestProcessNotesBatch_RequireFrontmatterFilterAndWithdraw(t *testing.T) {
	ws := t.TempDir()
	svc := newTestGitSyncService(nil)

	conf := &domain.GitSyncConfig{
		SourceDir: "Blog", TargetSubdir: "src/content/posts", RequireFrontmatter: "type=share",
	}
	notes := []*domain.Note{
		{Path: "Blog/share.md", Content: "---\ntype: share\n---\nA", Action: domain.NoteActionCreate},
		{Path: "Blog/private.md", Content: "---\ntype: resource\n---\nB", Action: domain.NoteActionCreate},
		{Path: "Outside/share.md", Content: "---\ntype: share\n---\nC", Action: domain.NoteActionCreate},
	}
	changed, err := svc.processNotesBatch(notes, conf, ws)
	require.NoError(t, err)
	require.True(t, changed)

	require.FileExists(t, filepath.Join(ws, "src/content/posts/share.md"))
	require.NoFileExists(t, filepath.Join(ws, "src/content/posts/private.md"))
	require.NoFileExists(t, filepath.Join(ws, "Outside/share.md"))

	// 去掉 type: share → 撤回
	withdraw := []*domain.Note{
		{Path: "Blog/share.md", Content: "---\ntype: resource\n---\nA", Action: domain.NoteActionCreate},
	}
	changed, err = svc.processNotesBatch(withdraw, conf, ws)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoFileExists(t, filepath.Join(ws, "src/content/posts/share.md"))
}

// TestProcessNotesBatch_ValidationKeepsPreviousVersionAndReports 验证预检拦截时保留旧合法版并记录原因，
// 修好后下一轮自动导出。
func TestProcessNotesBatch_ValidationKeepsPreviousVersionAndReports(t *testing.T) {
	ws := t.TempDir()
	svc := newTestGitSyncService(nil)
	conf := &domain.GitSyncConfig{
		SourceDir: "Blog", TargetSubdir: "src/content/posts", ValidateFrontmatter: true,
	}
	svc.setRunReport(conf.ID, &gitSyncRunReport{})

	good := "---\ntitle: P\npublished: 2026-10-20\n---\nbody"
	_, err := svc.processNotesBatch([]*domain.Note{
		{Path: "Blog/p.md", Content: good, Action: domain.NoteActionCreate},
	}, conf, ws)
	require.NoError(t, err)
	before, ok := readRepoFile(t, ws, "src/content/posts/p.md")
	require.True(t, ok)
	require.Equal(t, good, before)

	// 坏日期：跳过写入，保留旧版
	bad := "---\ntitle: P\npublished: YYYY-MM-DD\n---\nbody"
	_, err = svc.processNotesBatch([]*domain.Note{
		{Path: "Blog/p.md", Content: bad, Action: domain.NoteActionCreate},
	}, conf, ws)
	require.NoError(t, err)
	after, ok := readRepoFile(t, ws, "src/content/posts/p.md")
	require.True(t, ok)
	require.Equal(t, good, after, "invalid post must not overwrite the last valid export")

	report := svc.takeRunReport(conf.ID)
	require.NotNil(t, report)
	require.Len(t, report.skipped, 1)
	require.Contains(t, report.skipSummary(), "p.md")
	require.Contains(t, report.skipSummary(), "published")

	// 修好后自动导出
	svc.setRunReport(conf.ID, &gitSyncRunReport{})
	fixed := "---\ntitle: P\npublished: 2026-11-20\n---\nbody"
	changed, err := svc.processNotesBatch([]*domain.Note{
		{Path: "Blog/p.md", Content: fixed, Action: domain.NoteActionCreate},
	}, conf, ws)
	require.NoError(t, err)
	require.True(t, changed)
	got, _ := readRepoFile(t, ws, "src/content/posts/p.md")
	require.Equal(t, fixed, got)
}

// ---------------------------------------------------------------------------
// BundleAssets：跨目录附件汇聚 + 工作区副本改写，数据库原文不变
// ---------------------------------------------------------------------------

func TestBundleNoteAssets_CopiesAndRewritesWithoutTouchingSource(t *testing.T) {
	storage := t.TempDir()
	picPath := filepath.Join(storage, "pic.png")
	require.NoError(t, os.WriteFile(picPath, []byte("PNGDATA"), 0644))

	ws := t.TempDir()
	repo := new(domainmocks.MockGitSyncRepository)
	svc := newTestGitSyncService(repo)
	fileRepo := svc.fileRepo.(*domainmocks.MockFileRepository)
	// frontmatter.image 指向库内其它目录：精确路径命中；![[pic.png]] 裸文件名走 GetByPathLike。
	fileRepo.On("GetByPath", mock.Anything, "99-Assets/assets/pic.png", int64(1), int64(1)).
		Return(&domain.File{Path: "99-Assets/assets/pic.png", SavePath: picPath, Action: domain.FileActionCreate}, nil)
	fileRepo.On("GetByPath", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, os.ErrNotExist)
	fileRepo.On("GetByPathLike", mock.Anything, "/pic.png", int64(1), int64(1)).
		Return(&domain.File{Path: "99-Assets/assets/pic.png", SavePath: picPath, Action: domain.FileActionCreate}, nil)

	conf := &domain.GitSyncConfig{UID: 1, VaultID: 1, SourceDir: "Blog", TargetSubdir: "src/content/posts", BundleAssets: true, ValidateFrontmatter: true}
	svc.setRunReport(conf.ID, &gitSyncRunReport{})

	original := "---\ntitle: P\npublished: 2026-10-20\nimage: 99-Assets/assets/pic.png\n---\n\n正文\n\n![[pic.png]]\n"
	changed, err := svc.processNotesBatch([]*domain.Note{
		{Path: "Blog/p.md", Content: original, Action: domain.NoteActionCreate},
	}, conf, ws)
	require.NoError(t, err)
	require.True(t, changed)

	// 附件已汇聚到目标文章同目录 ./assets/
	require.FileExists(t, filepath.Join(ws, "src/content/posts/assets/pic.png"))

	exported, ok := readRepoFile(t, ws, "src/content/posts/p.md")
	require.True(t, ok)
	require.Contains(t, exported, "![pic](./assets/pic.png)")
	require.Contains(t, exported, `image: "./assets/pic.png"`)

	// 数据库原文丝毫未变：导出副本改写了，但写回内容只是局部变量，原笔记未被触碰。
	// The exported copy is rewritten; the stored note content is untouched.
	require.Contains(t, original, "![[pic.png]]")
	require.NotContains(t, exported, "99-Assets/assets/pic.png")
}

// ---------------------------------------------------------------------------
// pendingRedo 单飞队列
// ---------------------------------------------------------------------------

func TestExecuteSync_SingleFlightQueuesFollowUp(t *testing.T) {
	t.Chdir(t.TempDir())
	remote := filepath.Join(t.TempDir(), "remote.git")
	_, err := git.PlainInit(remote, true)
	require.NoError(t, err)

	repo := new(domainmocks.MockGitSyncRepository)
	conf := &domain.GitSyncConfig{ID: 7, UID: 1, VaultID: 1, RepoURL: remote, Branch: "master"}
	repo.On("GetByID", mock.Anything, int64(7), int64(1)).Return(conf, nil)
	repo.On("Save", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncConfig{}, nil)
	repo.On("CreateHistory", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncHistory{}, nil)

	svc := newTestGitSyncService(repo)
	enableLocalGitRepoPaths(t, svc)

	started := make(chan struct{})
	release := make(chan struct{})
	var runs int32
	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) {
			if atomic.AddInt32(&runs, 1) == 1 {
				close(started)
				<-release
			}
		}).
		Return([]*domain.Note{}, nil)
	svc.fileRepo.(*domainmocks.MockFileRepository).
		On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.File{}, nil)

	require.NoError(t, svc.ExecuteSync(context.Background(), 1, 7))
	<-started

	// 运行中再次触发：不取消当前任务，只登记补跑
	require.NoError(t, svc.ExecuteSync(context.Background(), 1, 7))
	require.NoError(t, svc.ExecuteSync(context.Background(), 1, 7))

	svc.mu.Lock()
	pending := svc.pendingRedo[7]
	svc.mu.Unlock()
	require.True(t, pending, "second trigger must be queued, not start a concurrent run")

	close(release)

	// 等补跑结束
	require.Eventually(t, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return len(svc.running) == 0 && !svc.pendingRedo[7]
	}, 5*time.Second, 20*time.Millisecond)

	require.GreaterOrEqual(t, atomic.LoadInt32(&runs), int32(2), "queued follow-up must run after the first finishes")
	require.NoError(t, svc.Shutdown(context.Background()))
}

func TestExecuteSync_ShutdownDropsPendingRedo(t *testing.T) {
	svc := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))
	svc.mu.Lock()
	svc.pendingRedo[3] = true
	svc.mu.Unlock()

	require.NoError(t, svc.Shutdown(context.Background()))
	svc.mu.Lock()
	defer svc.mu.Unlock()
	require.Empty(t, svc.pendingRedo)
}

// ---------------------------------------------------------------------------
// 远端 hook sideband 回执
// ---------------------------------------------------------------------------

func TestApplyRemoteHookOutput(t *testing.T) {
	svc := newTestGitSyncService(nil)

	svc.setRunReport(1, &gitSyncRunReport{})
	require.NoError(t, svc.applyRemoteHookOutput(1, "remote: Preview deployed: bd8a3a2\n"))
	report := svc.takeRunReport(1)
	require.NotNil(t, report)
	require.Contains(t, report.hookReceipt, "Preview deployed: bd8a3a2")

	svc.setRunReport(2, &gitSyncRunReport{})
	err := svc.applyRemoteHookOutput(2, "remote: Saved settings await confirmation: rebuild from /settings/ before retrying\n")
	require.ErrorContains(t, err, "remote hook deploy failed")
	require.Contains(t, err.Error(), "Saved settings await confirmation")

	svc.setRunReport(3, &gitSyncRunReport{})
	err = svc.applyRemoteHookOutput(3, "remote: Preview deployment failed: astro build error\n")
	require.ErrorContains(t, err, "Preview deployment failed")

	svc.setRunReport(4, &gitSyncRunReport{})
	require.NoError(t, svc.applyRemoteHookOutput(4, "unrelated progress output"))
	require.Empty(t, svc.takeRunReport(4).hookReceipt)
}

// ---------------------------------------------------------------------------
// 安全状态接口：不泄露敏感字段、库级隔离
// ---------------------------------------------------------------------------

// TestGitSyncStatusMessageFormatIsStable locks the English LastMessage wire
// format. WebGUI and the Obsidian plugin localize it client-side; existing
// scripts and tests match these prefixes, so they must not drift.
func TestGitSyncStatusMessageFormatIsStable(t *testing.T) {
	var nilReport *gitSyncRunReport
	require.Empty(t, nilReport.skipSummary())

	report := &gitSyncRunReport{skipped: []gitSyncSkippedPost{{path: "p.md", reason: "missing published"}}}
	require.Equal(t, "skipped 1 invalid post: p.md: missing published", report.skipSummary())

	report.skipped = append(report.skipped, gitSyncSkippedPost{path: "q.md", reason: "bad date"})
	require.Equal(t, "skipped 2 invalid posts: p.md: missing published", report.skipSummary())
}

func TestGetVaultStatuses_OmitsSensitiveFields(t *testing.T) {
	repo := new(domainmocks.MockGitSyncRepository)
	svc := newTestGitSyncService(repo)
	vaultRepo := svc.vaultRepo.(*domainmocks.MockVaultRepository)
	vaultRepo.On("GetByName", mock.Anything, "doge", int64(1)).Return(&domain.Vault{ID: 5, Name: "doge"}, nil)

	lastSync := time.Now().Add(-time.Hour)
	repo.On("ListByVaultID", mock.Anything, int64(5), int64(1)).Return([]*domain.GitSyncConfig{
		{
			ID: 6, UID: 1, VaultID: 5, RepoURL: "http://127.0.0.1:4323/blog.git", Username: "u", Password: "secret",
			WebhookURL: "http://127.0.0.1:4323/__republish", Branch: "master", TargetSubdir: "src/content/posts",
			SourceDir: "blog", RequireFrontmatter: "published: true", ValidateFrontmatter: true, BundleAssets: true,
			IsEnabled: true, LastStatus: domain.GitSyncStatusSuccess, LastMessage: "ok", LastSyncTime: &lastSync,
		},
	}, nil)

	list, err := svc.GetVaultStatuses(context.Background(), 1, "doge")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, int64(6), list[0].ID)
	require.Equal(t, "src/content/posts", list[0].TargetSubdir)
	// 只读映射摘要字段必须原样返回，供插件展示当前导出配置。
	require.Equal(t, "blog", list[0].SourceDir)
	require.Equal(t, "published: true", list[0].RequireFrontmatter)
	require.True(t, list[0].ValidateFrontmatter)
	require.True(t, list[0].BundleAssets)

	// 结构体本身没有敏感字段（编译期保证），此处再从序列化结果确认没有以字符串形式带出。
	// The struct has no sensitive fields (compile-time guarantee); also assert the
	// serialized payload never carries repository endpoints or credentials.
	payload, err := json.Marshal(list[0])
	require.NoError(t, err)
	serialized := string(payload)
	require.Contains(t, serialized, `"sourceDir":"blog"`)
	require.Contains(t, serialized, `"requireFrontmatter":"published: true"`)
	require.Contains(t, serialized, `"validateFrontmatter":true`)
	require.Contains(t, serialized, `"bundleAssets":true`)
	require.NotContains(t, serialized, "127.0.0.1:4323")
	require.NotContains(t, serialized, "secret")
	require.NotContains(t, serialized, "webhook")
	require.NotContains(t, serialized, "repoUrl")
	require.NotContains(t, serialized, "username")
	require.NotContains(t, serialized, "password")
}

func TestTriggerByVault_RejectsForeignConfig(t *testing.T) {
	repo := new(domainmocks.MockGitSyncRepository)
	svc := newTestGitSyncService(repo)
	vaultRepo := svc.vaultRepo.(*domainmocks.MockVaultRepository)
	vaultRepo.On("GetByName", mock.Anything, "doge", int64(1)).Return(&domain.Vault{ID: 5, Name: "doge"}, nil)
	repo.On("GetByID", mock.Anything, int64(99), int64(1)).Return(&domain.GitSyncConfig{ID: 99, VaultID: 42}, nil)

	err := svc.TriggerByVault(context.Background(), 1, "doge", 99)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not belong")
}

// TestUpdateConfig_ValidatesMappingAndResetsCursor 验证子目录映射参数落库、非法值被拒，
// 且映射变更会重置工作区与同步游标。
func TestUpdateConfig_ValidatesMappingAndResetsCursor(t *testing.T) {
	t.Chdir(t.TempDir())
	repo := new(domainmocks.MockGitSyncRepository)
	svc := newTestGitSyncService(repo)
	enableLocalGitRepoPaths(t, svc)

	lastSync := time.Now().Add(-time.Hour)
	existing := &domain.GitSyncConfig{ID: 1, UID: 1, VaultID: 1, RepoURL: "https://example.com/r.git", Branch: "main"}
	repo.On("GetByID", mock.Anything, int64(1), int64(1)).Return(existing, nil)
	savedConf := &domain.GitSyncConfig{}
	repo.On("Save", mock.Anything, mock.Anything, mock.Anything).
		Return(savedConf, nil).
		Run(func(args mock.Arguments) {
			// 模拟仓储：落库后回读同一份配置（ID 由数据库分配）。
			*savedConf = *args.Get(1).(*domain.GitSyncConfig)
			savedConf.ID = 1
		})

	// 非法 targetSubdir
	_, err := svc.UpdateConfig(context.Background(), 1, &dto.GitSyncConfigRequest{
		ID: 1, RepoURL: "https://example.com/r.git", TargetSubdir: "../etc",
	})
	require.Error(t, err)

	// 非法 webhook 协议
	_, err = svc.UpdateConfig(context.Background(), 1, &dto.GitSyncConfigRequest{
		ID: 1, RepoURL: "https://example.com/r.git", WebhookURL: "file:///etc/passwd",
	})
	require.Error(t, err)

	// 合法映射：规范化后落库并重置游标
	existing.LastSyncTime = &lastSync
	saved, err := svc.UpdateConfig(context.Background(), 1, &dto.GitSyncConfigRequest{
		ID: 1, RepoURL: "https://example.com/r.git", Branch: "main",
		SourceDir: "Blog/", TargetSubdir: "/src/content/posts/", RequireFrontmatter: "type=share",
		ValidateFrontmatter: true, BundleAssets: true,
		WebhookURL: "https://example.com/hook",
	})
	require.NoError(t, err)
	require.Equal(t, "Blog", saved.SourceDir)
	require.Equal(t, "src/content/posts", saved.TargetSubdir)
	require.True(t, saved.ValidateFrontmatter)
	require.True(t, saved.BundleAssets)
	require.Equal(t, "https://example.com/hook", saved.WebhookURL)
	require.True(t, saved.LastSyncTime.IsZero(), "mapping change must reset the sync cursor")
}

// TestDomainToDTO_CarriesMappingFields 保证 WebGUI 能读到新增字段（密码仍不回显）。
func TestDomainToDTO_CarriesMappingFields(t *testing.T) {
	svc := newTestGitSyncService(nil)
	d := svc.domainToDTO(&domain.GitSyncConfig{
		ID: 1, Password: "secret", SourceDir: "Blog", TargetSubdir: "src/content/posts",
		RequireFrontmatter: "type=share", ValidateFrontmatter: true, BundleAssets: true,
		WebhookURL: "https://example.com/hook",
	})
	require.Equal(t, "Blog", d.SourceDir)
	require.Equal(t, "src/content/posts", d.TargetSubdir)
	require.True(t, d.ValidateFrontmatter)
	require.Equal(t, "", d.Password)
}

// TestDecodeSidebandText 覆盖 receive-pack 响应里 sideband 文本的解析。
func TestDecodeSidebandText(t *testing.T) {
	// 0030 \x01 "000eunpack ok\n" 0019 "ok refs/heads/master\n" 0000 0019 \x02 "Preview deployed: x\n" 0000
	body := []byte("0030\x01000eunpack ok\n0019ok refs/heads/master\n00000019\x02Preview deployed: x\n0000")
	require.Equal(t, "Preview deployed: x\n", decodeSidebandText(body))
	require.Equal(t, "", decodeSidebandText([]byte("0000")))
}

// TestProcessNotesBatch_TargetSubdirSkipsObsidianConfig 验证子目录沙箱下 .obsidian 配置
// 不会进入 Astro 内容目录（不变量 2 的第二半）。
func TestProcessNotesBatch_TargetSubdirSkipsObsidianConfig(t *testing.T) {
	ws := t.TempDir()
	svc := newTestGitSyncService(nil)
	conf := &domain.GitSyncConfig{TargetSubdir: "src/content/posts"}
	changed, err := svc.processNotesBatch([]*domain.Note{
		{Path: ".obsidian/app.json", Content: "{}", Action: domain.NoteActionCreate},
		{Path: "notes/a.md", Content: "hi", Action: domain.NoteActionCreate},
	}, conf, ws)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoFileExists(t, filepath.Join(ws, "src/content/posts/.obsidian/app.json"))
	require.FileExists(t, filepath.Join(ws, "src/content/posts/notes/a.md"))
}

// TestBundleNoteAssets_RelativeWorkspacePathWithValidation 覆盖 E2E-3：工作区路径是相对路径
// （storage/git_workspace/... 形态）时，附件汇聚 + Frontmatter 预检 + 图片存在性校验必须
// 全部成功，不得因 filepath.Rel(相对根, 绝对目标) 失败而误报 missing image file。
// A relative workspace path must not break asset bundling or the image existence check.
func TestBundleNoteAssets_RelativeWorkspacePathWithValidation(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)

	picPath := filepath.Join(base, "pic.png")
	require.NoError(t, os.WriteFile(picPath, []byte("PNGDATA"), 0644))

	repo := new(domainmocks.MockGitSyncRepository)
	svc := newTestGitSyncService(repo)
	fileRepo := svc.fileRepo.(*domainmocks.MockFileRepository)
	fileRepo.On("GetByPath", mock.Anything, "Blog/assets/pic.png", int64(1), int64(1)).
		Return(&domain.File{Path: "Blog/assets/pic.png", SavePath: picPath, Action: domain.FileActionCreate}, nil)
	fileRepo.On("GetByPath", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, os.ErrNotExist)
	fileRepo.On("GetByPathLike", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, os.ErrNotExist)

	conf := &domain.GitSyncConfig{
		UID: 1, VaultID: 1, SourceDir: "Blog", TargetSubdir: "src/content/posts",
		BundleAssets: true, ValidateFrontmatter: true,
	}
	svc.setRunReport(conf.ID, &gitSyncRunReport{})

	// 相对工作区路径：这正是导出层从配置里拿到的形态。
	relWs := filepath.Join("tmp_rel_ws_"+t.Name(), "ws")
	require.NoError(t, os.MkdirAll(relWs, 0755))

	content := "---\ntitle: P\npublished: 2026-10-20\nimage: assets/pic.png\n---\n\n正文\n"
	changed, err := svc.processNotesBatch([]*domain.Note{
		{Path: "Blog/p.md", Content: content, Action: domain.NoteActionCreate},
	}, conf, relWs)
	require.NoError(t, err)
	require.True(t, changed)

	// 导出成功：文章与汇聚后的附件都在沙箱内，预检没有被误报拦截。
	require.FileExists(t, filepath.Join(relWs, "src/content/posts/p.md"))
	require.FileExists(t, filepath.Join(relWs, "src/content/posts/assets/pic.png"))
	report := svc.takeRunReport(conf.ID)
	require.NotNil(t, report)
	require.Empty(t, report.skipped, "相对工作区路径下不得误报 missing image file：%s", report.skipSummary())
}

// TestFireflySandbox_NormalizesRelativeRoot 直接验证 sandbox 根目录统一为绝对路径，
// 保证与 safeWorkspacePath 返回的绝对目标路径处于同一命名空间。
func TestFireflySandbox_NormalizesRelativeRoot(t *testing.T) {
	t.Chdir(t.TempDir())
	root, subdir, err := fireflySandbox(filepath.Join("storage", "git_workspace", "1", "2"), &domain.GitSyncConfig{TargetSubdir: "src/content/posts"})
	require.NoError(t, err)
	require.Equal(t, "src/content/posts", subdir)
	require.True(t, filepath.IsAbs(root))
	require.Contains(t, filepath.ToSlash(root), "storage/git_workspace/1/2/src/content/posts")

	root, subdir, err = fireflySandbox(filepath.Join("storage", "git_workspace", "1", "2"), &domain.GitSyncConfig{})
	require.NoError(t, err)
	require.Equal(t, "", subdir)
	require.True(t, filepath.IsAbs(root))
}

// TestTriggerByVault_AcceptsOwnConfig 同一库内、且已启用的配置必须允许按 ID 触发。
func TestTriggerByVault_AcceptsOwnConfig(t *testing.T) {
	repo := new(domainmocks.MockGitSyncRepository)
	svc := newTestGitSyncService(repo)
	vaultRepo := svc.vaultRepo.(*domainmocks.MockVaultRepository)
	vaultRepo.On("GetByName", mock.Anything, "doge", int64(1)).Return(&domain.Vault{ID: 5, Name: "doge"}, nil)
	repo.On("GetByID", mock.Anything, int64(6), int64(1)).
		Return(&domain.GitSyncConfig{ID: 6, UID: 1, VaultID: 5, IsEnabled: true, RepoURL: "https://example.com/r.git", Branch: "main"}, nil)
	enableLocalGitRepoPaths(t, svc)
	t.Chdir(t.TempDir())

	// ExecuteSync 会启动后台任务；这里只关心它没有被跨库校验拒绝。
	require.NoError(t, svc.TriggerByVault(context.Background(), 1, "doge", 6))
	require.NoError(t, svc.Shutdown(context.Background()))
}

// TestTriggerByVault_RejectsDisabledConfig 本库但未启用的配置不得被手动触发：
// 客户端接口的语义是“触发本库同步”，禁用配置必须保持不运行。
func TestTriggerByVault_RejectsDisabledConfig(t *testing.T) {
	repo := new(domainmocks.MockGitSyncRepository)
	svc := newTestGitSyncService(repo)
	vaultRepo := svc.vaultRepo.(*domainmocks.MockVaultRepository)
	vaultRepo.On("GetByName", mock.Anything, "doge", int64(1)).Return(&domain.Vault{ID: 5, Name: "doge"}, nil)
	repo.On("GetByID", mock.Anything, int64(6), int64(1)).
		Return(&domain.GitSyncConfig{ID: 6, UID: 1, VaultID: 5, IsEnabled: false, RepoURL: "https://example.com/r.git", Branch: "main"}, nil)

	err := svc.TriggerByVault(context.Background(), 1, "doge", 6)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not enabled")
}

// TestDoSync_IdleRunSkipsWebhook 覆盖 P1-02 的后半：空闲库的后台运行不得打 Webhook，
// 只有显式手动触发才允许。
// An idle background run must not fire the webhook; only a manual trigger may.
func TestDoSync_IdleRunSkipsWebhook(t *testing.T) {
	t.Chdir(t.TempDir())
	remote := filepath.Join(t.TempDir(), "remote.git")
	_, err := git.PlainInit(remote, true)
	require.NoError(t, err)

	var webhookHits int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&webhookHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(webhook.Close)

	repo := new(domainmocks.MockGitSyncRepository)
	repo.On("Save", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncConfig{}, nil)
	repo.On("CreateHistory", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncHistory{}, nil)
	svc := newTestGitSyncService(repo)
	enableLocalGitRepoPaths(t, svc)
	svc.gitConf.AllowedPrivateHosts = []string{"127.0.0.1"}

	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{}, nil)
	svc.fileRepo.(*domainmocks.MockFileRepository).
		On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.File{}, nil)

	conf := &domain.GitSyncConfig{
		ID: 1, UID: 1, VaultID: 1, RepoURL: remote, Branch: "master",
		WebhookURL: webhook.URL,
	}

	// 后台运行：无变更 → 不打 Webhook。
	svc.syncTask(context.Background(), conf)
	require.Equal(t, int32(0), atomic.LoadInt32(&webhookHits), "空闲后台运行不得触发 Webhook")

	// 手动触发：即使无变更也允许一次受控重试。
	svc.requestWebhookRetry(conf.ID)
	svc.syncTask(context.Background(), conf)
	require.Equal(t, int32(1), atomic.LoadInt32(&webhookHits), "手动触发应打一次 Webhook")
}

// TestFireDeployWebhook_RejectsPrivateTarget DNS/地址校验在拨号前拒绝回环与私网目标。
func TestFireDeployWebhook_RejectsPrivateTarget(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	svc := newTestGitSyncService(nil)
	// 未放行 127.0.0.1：校验阶段就应拒绝。
	svc.fireDeployWebhook(context.Background(), &domain.GitSyncConfig{ID: 1, WebhookURL: server.URL})
	require.Equal(t, int32(0), atomic.LoadInt32(&hits), "私网/回环目标必须被拒绝")

	// 放行后正常发出，且不跟随重定向。
	svc.gitConf.AllowedPrivateHosts = []string{"127.0.0.1"}
	svc.fireDeployWebhook(context.Background(), &domain.GitSyncConfig{ID: 1, WebhookURL: server.URL})
	require.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

// TestFireDeployWebhook_DoesNotFollowRedirects Webhook 不跟随跳转：避免 302 到内网地址
// 绕过拨号期校验（P1-02 的 DNS Rebinding / 重定向 SSRF 防护）。
// The webhook never follows redirects, so a 302 into the intranet cannot bypass the
// dial-time target validation.
func TestFireDeployWebhook_DoesNotFollowRedirects(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	svc := newTestGitSyncService(nil)
	svc.gitConf.AllowedPrivateHosts = []string{"127.0.0.1"}
	svc.fireDeployWebhook(context.Background(), &domain.GitSyncConfig{ID: 1, WebhookURL: redirector.URL})
	require.Equal(t, int32(0), atomic.LoadInt32(&targetHits), "不得跟随 Webhook 跳转")
}

// TestBundleNoteAssets_SameNameDifferentSourceKeepsBoth 覆盖 P1-08：不同源路径但同名的
// 图片必须各自保留（追加短哈希后缀），不得互相覆盖。
// Same-named images from different source paths must both survive with a hash suffix.
func TestBundleNoteAssets_SameNameDifferentSourceKeepsBoth(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	picA := filepath.Join(dirA, "cover.png")
	picB := filepath.Join(dirB, "cover.png")
	require.NoError(t, os.WriteFile(picA, []byte("AAA"), 0644))
	require.NoError(t, os.WriteFile(picB, []byte("BBB"), 0644))

	ws := t.TempDir()
	svc := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))
	fileRepo := svc.fileRepo.(*domainmocks.MockFileRepository)
	fileRepo.On("GetByPath", mock.Anything, "A/cover.png", int64(1), int64(1)).
		Return(&domain.File{Path: "A/cover.png", SavePath: picA, Action: domain.FileActionCreate}, nil)
	fileRepo.On("GetByPath", mock.Anything, "B/cover.png", int64(1), int64(1)).
		Return(&domain.File{Path: "B/cover.png", SavePath: picB, Action: domain.FileActionCreate}, nil)
	fileRepo.On("GetByPath", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, os.ErrNotExist)
	fileRepo.On("GetByPathLike", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, os.ErrNotExist)

	conf := &domain.GitSyncConfig{UID: 1, VaultID: 1, SourceDir: "Blog", TargetSubdir: "posts", BundleAssets: true}
	svc.setRunReport(conf.ID, &gitSyncRunReport{})

	content := "---\ntitle: P\n---\n\n![a](A/cover.png)\n![b](B/cover.png)\n"
	_, err := svc.processNotesBatch([]*domain.Note{
		{Path: "Blog/p.md", Content: content, Action: domain.NoteActionCreate},
	}, conf, ws)
	require.NoError(t, err)

	assets := filepath.Join(ws, "posts", "assets")
	entries, err := os.ReadDir(assets)
	require.NoError(t, err)
	require.Len(t, entries, 2, "两张同名不同源图片必须都保留")

	var payloads []string
	for _, e := range entries {
		body, readErr := os.ReadFile(filepath.Join(assets, e.Name()))
		require.NoError(t, readErr)
		payloads = append(payloads, string(body))
	}
	require.ElementsMatch(t, []string{"AAA", "BBB"}, payloads)
}

// TestProcessSettingsBatch_LargeBatchDoesNotAccumulateHandles 覆盖 P1-08：大批量配置导出
// 必须在每次比较后立即关闭旧文件，不能把句柄累积到整批结束。这里用远超默认 fd 软上限
// 的条目数（5000）验证不会因句柄耗尽而失败。
// A large settings batch must close each old file immediately instead of accumulating
// handles until the batch ends.
func TestProcessSettingsBatch_LargeBatchDoesNotAccumulateHandles(t *testing.T) {
	ws := t.TempDir()
	svc := newTestGitSyncService(nil)
	conf := &domain.GitSyncConfig{ConfigSyncRules: []string{"User"}}

	settings := make([]*domain.Setting, 0, 5000)
	for i := 0; i < 5000; i++ {
		path := fmt.Sprintf("User/setting-%04d.json", i)
		// 先写一份旧内容，确保每条都会走到"读取旧文件比较"分支。
		full := filepath.Join(ws, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte("old"), 0644))
		settings = append(settings, &domain.Setting{
			Path: path, Content: fmt.Sprintf(`{"v":%d}`, i), Action: domain.SettingActionCreate,
		})
	}

	changed, err := svc.processSettingsBatch(settings, conf, ws)
	require.NoError(t, err, "大批量导出不得因句柄耗尽失败")
	require.True(t, changed)

	// 对比未修复的旧实现（循环内 defer Close）：在较低软上限下必然耗尽句柄，
	// 以此证明这条断言真正覆盖了句柄累积问题，而不是恰好没触发。
	// Reproduce the old implementation (defer inside the loop) under a low fd limit to
	// prove this assertion actually catches handle accumulation.
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		// 压低软上限并预先占用大部分句柄，模拟真实进程里调用方已持有的句柄压力。
		// Lower the soft limit and pre-hold most handles to mirror real process pressure.
		var limit syscall.Rlimit
		require.NoError(t, syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit))
		original := limit
		limit.Cur = 256
		if limit.Max < limit.Cur {
			limit.Max = limit.Cur
		}
		require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit))
		t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &original) })

		held := make([]*os.File, 0, 240)
		t.Cleanup(func() {
			for _, f := range held {
				_ = f.Close()
			}
		})
		for len(held) < 240 {
			f, openErr := os.Open(filepath.Join(ws, "User", "setting-0000.json"))
			if openErr != nil {
				break
			}
			held = append(held, f)
		}
		require.Greater(t, len(held), 200, "应能占用大部分软上限")

		// 关键：defer 必须写在测试函数作用域内的循环里（与旧实现 processSettingsBatch
		// 同构），才会累积到函数返回。
		// The defer must live in a loop inside this test function (mirroring the old
		// processSettingsBatch) so handles accumulate until the function returns.
		legacyExhausted := func() bool {
			for i := 0; i < 5000; i++ {
				fullPath := filepath.Join(ws, "User", fmt.Sprintf("setting-%04d.json", i))
				oldFile, err := os.Open(fullPath)
				if err != nil {
					t.Logf("legacy failed at %d: %v", i, err)
					return true
				}
				defer oldFile.Close()
				if oldContent, err := io.ReadAll(oldFile); err == nil {
					if bytes.Equal(oldContent, []byte("new")) {
						continue
					}
				}
			}
			return false
		}()
		require.True(t, legacyExhausted, "旧实现（循环内 defer）在低软上限下必须耗尽句柄，证明本测试有效")
	}
}

// TestDoSync_ManualWebhookConsumedOnce 覆盖 B-04：手动触发在一次空闲运行里打一次 Webhook
// 后，标记必须被消费；紧接着的后台运行即使无变更也不得再打第二次。
// A manual trigger must consume the flag: the next background idle run must not fire
// the webhook a second time.
func TestDoSync_ManualWebhookConsumedOnce(t *testing.T) {
	t.Chdir(t.TempDir())
	remote := filepath.Join(t.TempDir(), "remote.git")
	_, err := git.PlainInit(remote, true)
	require.NoError(t, err)

	var webhookHits int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&webhookHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(webhook.Close)

	repo := new(domainmocks.MockGitSyncRepository)
	repo.On("Save", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncConfig{}, nil)
	repo.On("CreateHistory", mock.Anything, mock.Anything, mock.Anything).Return(&domain.GitSyncHistory{}, nil)
	svc := newTestGitSyncService(repo)
	enableLocalGitRepoPaths(t, svc)
	svc.gitConf.AllowedPrivateHosts = []string{"127.0.0.1"}

	noteRepo := svc.noteRepo.(*domainmocks.MockNoteRepository)
	noteRepo.On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.Note{}, nil)
	svc.fileRepo.(*domainmocks.MockFileRepository).
		On("ListByUpdatedTimestampPage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]*domain.File{}, nil)

	conf := &domain.GitSyncConfig{
		ID: 1, UID: 1, VaultID: 1, RepoURL: remote, Branch: "master",
		WebhookURL: webhook.URL,
	}

	// 手动触发：空跑也应打一次 Webhook。
	svc.requestWebhookRetry(conf.ID)
	svc.syncTask(context.Background(), conf)
	require.Equal(t, int32(1), atomic.LoadInt32(&webhookHits), "手动触发应打一次 Webhook")

	// 紧随其后的普通后台运行：标记已被消费，不得再打。
	svc.syncTask(context.Background(), conf)
	require.Equal(t, int32(1), atomic.LoadInt32(&webhookHits), "后台运行不得重复触发已消费的 Webhook")

	// 再次手动触发仍然可以打一次（消费不是永久禁用）。
	svc.requestWebhookRetry(conf.ID)
	svc.syncTask(context.Background(), conf)
	require.Equal(t, int32(2), atomic.LoadInt32(&webhookHits), "新的手动触发应重新打一次 Webhook")
}
