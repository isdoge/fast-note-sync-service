package service

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	appconfig "github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/stretchr/testify/require"
)

// TestGitSyncService_ValidateHonorsContextDeadline 复现 haierkeys/obsidian-fast-note-sync#231：
// 配置“验证”一直显示检测中。旧实现调用 rem.List 时完全不带 context，远端无响应时
// 只能等 TCP 层默认超时（分钟级），前端表现为永远“检测中”。
// 修复后 Validate 必须带超时并在截止时返回明确的超时错误。
func TestGitSyncService_ValidateHonorsContextDeadline(t *testing.T) {
	// 起一个 TCP 监听但从不响应任何数据的“黑洞”服务器，模拟远端挂起。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			// 不读不写不关闭，让客户端请求一直挂着直到超时。
			_ = conn
		}
	}()

	// 允许访问 127.0.0.1（#410 私网白名单），让请求真正走到 TCP 层。
	svc := newTestGitSyncService(nil)
	svc.gitConf = &appconfig.GitConfig{AllowedPrivateHosts: []string{"127.0.0.1"}}

	start := time.Now()
	validateErr := svc.Validate(context.Background(), &dto.GitSyncValidateRequest{
		RepoURL:  "http://" + listener.Addr().String() + "/repo.git",
		Username: "u",
		Password: "p",
	})
	elapsed := time.Since(start)

	require.Error(t, validateErr, "hung remote must fail validation")
	require.Less(t, elapsed, gitSyncValidateTimeout+15*time.Second,
		"Validate must give up around its own timeout instead of hanging on TCP defaults")
	require.True(t,
		errors.Is(validateErr, context.DeadlineExceeded) ||
			strings.Contains(validateErr.Error(), "deadline exceeded") ||
			strings.Contains(validateErr.Error(), "timeout") ||
			strings.Contains(validateErr.Error(), "timed out"),
		"timeout error must be explicit, got: %v", validateErr)
}
