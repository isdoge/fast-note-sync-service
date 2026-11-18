package service

import (
	"context"
	"net"
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
		{name: "local path allowed for tests", rawURL: t.TempDir(), wantAllow: true},
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
