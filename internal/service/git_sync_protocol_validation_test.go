package service

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"

	appconfig "github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// enableLocalGitRepoPaths enables local remotes only for this test's service, restoring on cleanup.
// enableLocalGitRepoPaths 只对当前测试的服务实例放行本地远端，测试结束后恢复。
func enableLocalGitRepoPaths(t *testing.T, svc *gitSyncService) {
	t.Helper()
	previous := svc.allowLocalRepoPathsForTest.Swap(true)
	t.Cleanup(func() { svc.allowLocalRepoPathsForTest.Store(previous) })
}

func TestGitSyncProtocolValidation_LocalPathsTestSwitch(t *testing.T) {
	t.Parallel()
	svc := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))
	other := newTestGitSyncService(new(domainmocks.MockGitSyncRepository))
	rawURLs := []string{t.TempDir(), "repo.git", "./x", "../x"}

	t.Run("enabled", func(t *testing.T) {
		enableLocalGitRepoPaths(t, svc)
		for _, rawURL := range rawURLs {
			require.NoError(t, svc.repoURLValidator().validateRepoURL(rawURL))
			require.Error(t, other.repoURLValidator().validateRepoURL(rawURL), "switch must be instance scoped")
			require.Error(t, validateRepoURL(rawURL, nil), "default validator must stay closed")
		}
		for _, rawURL := range []string{"file:///x", "git@example.com:repo.git", "ssh://git@example.com/repo.git"} {
			require.Error(t, svc.repoURLValidator().validateRepoURL(rawURL))
		}
	})
	for _, rawURL := range rawURLs {
		require.Error(t, svc.repoURLValidator().validateRepoURL(rawURL), "cleanup must restore default rejection")
	}
}

func TestGitSyncProtocolValidation_RejectsNonHTTPProtocols(t *testing.T) {
	restoreLookup := installGitRepoLookupForTest(gitRepoTestLookup(map[string][]string{
		"example.com": {"93.184.216.34"},
	}))
	defer restoreLookup()

	validator := newGitRepoURLValidator(nil, gitRepoLookupIP)

	for _, tc := range []struct {
		name      string
		rawURL    string
		wantAllow bool
	}{
		{name: "https allowed", rawURL: "https://example.com/repo.git", wantAllow: true},
		{name: "http allowed", rawURL: "http://example.com/repo.git", wantAllow: true},
		{name: "local path rejected by default", rawURL: t.TempDir(), wantAllow: false},
		{name: "git scheme rejected", rawURL: "git://example.com/repo.git", wantAllow: false},
		{name: "git scheme uppercase rejected", rawURL: "GIT://127.0.0.1/repo.git", wantAllow: false},
		{name: "ssh scheme rejected", rawURL: "ssh://git@example.com/repo.git", wantAllow: false},
		{name: "ssh scheme internal rejected", rawURL: "ssh://git@127.0.0.1:2222/repo.git", wantAllow: false},
		{name: "scp style rejected", rawURL: "git@example.com:org/repo.git", wantAllow: false},
		{name: "scp style internal rejected", rawURL: "git@192.168.1.10:repo.git", wantAllow: false},
		{name: "file scheme rejected", rawURL: "file:///etc/passwd", wantAllow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validator.validateRepoURL(tc.rawURL)
			if tc.wantAllow {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, code.ErrorGitSyncValidateFailed.Code(), tokenErrorCode(t, err))
			require.Contains(t, err.Error(), "http")
		})
	}
}

func TestGitSyncProtocolValidation_RejectsLocalPathsByDefault(t *testing.T) {
	for _, rawURL := range []string{t.TempDir(), "repo.git", "./x", "../x", "file:///x", "https:repo.git"} {
		t.Run(rawURL, func(t *testing.T) {
			err := validateRepoURL(rawURL, nil)
			require.Error(t, err)
			require.Equal(t, code.ErrorGitSyncValidateFailed.Code(), tokenErrorCode(t, err))
			if rawURL == "file:///x" {
				require.Contains(t, err.Error(), "unsupported git protocol: file")
			} else {
				require.Contains(t, err.Error(), "repository URL must specify protocol (only http and https are supported)")
			}
		})
	}
}

func TestGitSyncService_RejectsLocalPathsAtEveryEntry(t *testing.T) {
	for _, rawURL := range []string{filepath.Join(t.TempDir(), "missing.git"), "missing.git", "./x", "../x", "file:///x"} {
		for _, entry := range []string{"create", "update", "validate", "execute", "sync"} {
			t.Run(entry+"/"+rawURL, func(t *testing.T) {
				ctx := context.Background()
				conf := &domain.GitSyncConfig{ID: 1, UID: 1, RepoURL: rawURL}
				repo := new(domainmocks.MockGitSyncRepository)
				repo.On("GetByID", mock.Anything, conf.ID, conf.UID).Return(conf, nil)
				repo.On("Save", mock.Anything, mock.Anything, conf.UID).Return(&domain.GitSyncConfig{}, nil)
				repo.On("CreateHistory", mock.Anything, mock.Anything, conf.UID).Return(&domain.GitSyncHistory{}, nil)
				svc := newTestGitSyncService(repo)
				t.Cleanup(func() { require.NoError(t, svc.Shutdown(ctx)) })

				var err error
				switch entry {
				case "create", "update":
					params := &dto.GitSyncConfigRequest{RepoURL: rawURL}
					if entry == "update" {
						params.ID = conf.ID
					}
					_, err = svc.UpdateConfig(ctx, conf.UID, params)
				case "validate":
					err = svc.Validate(ctx, &dto.GitSyncValidateRequest{RepoURL: rawURL})
				case "execute":
					err = svc.ExecuteSync(ctx, conf.UID, conf.ID)
				case "sync":
					err = svc.doSync(ctx, conf)
				}
				require.Error(t, err)
				require.Equal(t, code.ErrorGitSyncValidateFailed.Code(), tokenErrorCode(t, err))
				if rawURL == "file:///x" {
					require.Contains(t, err.Error(), "unsupported git protocol: file")
				} else {
					require.Contains(t, err.Error(), "repository URL must specify protocol (only http and https are supported)")
				}
				repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything, mock.Anything)
				repo.AssertNotCalled(t, "CreateHistory", mock.Anything, mock.Anything, mock.Anything)
			})
		}
	}
}

func TestGitSyncService_RejectsUnsupportedProtocolsOnSaveValidateAndSync(t *testing.T) {
	t.Chdir(t.TempDir())
	// 启动本地黑洞端口，确保 ssh:// / git:// / scp 地址在校验阶段就被拦截，不会发起网络连接。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	var accepted atomic.Int32
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()

	addr := listener.Addr().String()
	unsupportedURLs := []string{
		"git://" + addr + "/repo.git",
		"ssh://git@" + addr + "/repo.git",
		"git@127.0.0.1:repo.git",
		"git@" + addr + ":repo.git",
	}

	for _, rawURL := range unsupportedURLs {
		t.Run(rawURL, func(t *testing.T) {
			repo := new(domainmocks.MockGitSyncRepository)
			repo.On("Save", mock.Anything, mock.Anything, int64(1)).Return(&domain.GitSyncConfig{}, nil)
			repo.On("CreateHistory", mock.Anything, mock.Anything, int64(1)).Return(&domain.GitSyncHistory{}, nil)

			svc := newTestGitSyncService(repo)
			// 即使放开 127.0.0.1 白名单，非 HTTP(S) 协议仍必须被拒绝。
			svc.gitConf = &appconfig.GitConfig{AllowedPrivateHosts: []string{"127.0.0.1"}}
			defer svc.Shutdown(context.Background())

			_, updateErr := svc.UpdateConfig(context.Background(), 1, &dto.GitSyncConfigRequest{
				RepoURL: rawURL,
			})
			require.Error(t, updateErr)
			require.Equal(t, code.ErrorGitSyncValidateFailed.Code(), tokenErrorCode(t, updateErr))
			require.NotContains(t, updateErr.Error(), "invalid auth method")

			validateErr := svc.Validate(context.Background(), &dto.GitSyncValidateRequest{
				RepoURL: rawURL,
			})
			require.Error(t, validateErr)
			require.Equal(t, code.ErrorGitSyncValidateFailed.Code(), tokenErrorCode(t, validateErr))
			require.NotContains(t, validateErr.Error(), "invalid auth method")

			// 模拟数据库中已存在旧的 ssh/git/scp 配置时执行同步：必须返回清晰校验错误而非 invalid auth method 或 panic。
			conf := &domain.GitSyncConfig{
				ID:      99,
				UID:     1,
				VaultID: 1,
				RepoURL: rawURL,
				Branch:  "main",
			}
			syncErr := svc.doSync(context.Background(), conf)
			require.Error(t, syncErr)
			require.Equal(t, code.ErrorGitSyncValidateFailed.Code(), tokenErrorCode(t, syncErr))
			require.NotContains(t, syncErr.Error(), "invalid auth method")

			svc.syncTask(context.Background(), conf)
			require.Equal(t, int64(domain.GitSyncStatusFailed), conf.LastStatus)
			require.NotContains(t, conf.LastMessage, "invalid auth method")
			require.Contains(t, conf.LastMessage, "http")
		})
	}

	require.Zero(t, accepted.Load(), "rejected protocols must not initiate TCP connections")
}
