package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigPreservesExplicitFalseAndEmptyDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
app:
  ws-parallel-enabled: false
  ws-check-utf8-enabled: false
  ws-compression-enabled: false
security:
  webgui-login-token-expiry:
  webgui-login-token-bind-ip: false
tracer:
  enabled: false
storage:
  local-fs:
    httpfs-is-enable: false
  aliyun-oss:
    is-enable: false
  aws-s3:
    is-enable:
database:
  auto-migrate: false
user-database:
  auto-migrate: false
`), 0644)
	require.NoError(t, err)

	cfg, _, err := LoadConfig(configPath)
	require.NoError(t, err)

	require.False(t, *cfg.App.WebSocketParallelEnabled)
	require.False(t, *cfg.App.WebSocketCheckUtf8Enabled)
	require.False(t, *cfg.App.WebSocketCompressionEnabled)
	require.False(t, *cfg.Security.WebGUILoginTokenBindIP)
	require.False(t, *cfg.Tracer.Enabled)
	require.False(t, *cfg.Storage.LocalFS.HttpfsIsEnable)
	require.False(t, *cfg.Storage.AliyunOSS.IsEnabled)
	require.False(t, *cfg.Database.AutoMigrate)
	require.False(t, *cfg.UserDatabase.AutoMigrate)

	require.Equal(t, "7d", cfg.Security.WebGUILoginTokenExpiry)
	require.True(t, *cfg.Storage.AwsS3.IsEnabled)
	require.True(t, *cfg.Storage.MinIO.IsEnabled)
}

func TestLoadConfigWebGUILoginTokenBindUADefaultAndOverride(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`security:
  webgui-login-token-bind-ua: false
`), 0644)
	require.NoError(t, err)

	cfg, _, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg.Security.WebGUILoginTokenBindUA)
	require.False(t, *cfg.Security.WebGUILoginTokenBindUA)

	require.NoError(t, os.WriteFile(configPath, []byte("security: {}\n"), 0644))
	cfg, _, err = LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg.Security.WebGUILoginTokenBindUA)
	require.True(t, *cfg.Security.WebGUILoginTokenBindUA)
}

func TestLoadConfigSyncExcludeSystemJunkDefaultAndOverride(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`app:
  sync-exclude-system-junk: false
`), 0644)
	require.NoError(t, err)

	cfg, _, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg.App.SyncExcludeSystemJunk)
	require.False(t, *cfg.App.SyncExcludeSystemJunk)
	require.False(t, cfg.App.SyncExcludeSystemJunkEnabled())

	require.NoError(t, os.WriteFile(configPath, []byte("app: {}\n"), 0644))
	cfg, _, err = LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg.App.SyncExcludeSystemJunk)
	require.True(t, *cfg.App.SyncExcludeSystemJunk)
	require.True(t, cfg.App.SyncExcludeSystemJunkEnabled())

	// 未经 LoadConfig 构造的零值配置防御性回退为开启
	require.True(t, config.AppSettings{}.SyncExcludeSystemJunkEnabled())
}

func TestLoadConfigGitAllowedPrivateHosts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`git:
  allowed-private-hosts:
    - gitea.internal
    - 10.20.0.0/16
    - 192.168.1.20
`), 0644)
	require.NoError(t, err)

	cfg, _, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.Equal(t, []string{"gitea.internal", "10.20.0.0/16", "192.168.1.20"}, cfg.Git.AllowedPrivateHosts)

	require.NoError(t, os.WriteFile(configPath, []byte("git: {}\n"), 0644))
	cfg, _, err = LoadConfig(configPath)
	require.NoError(t, err)
	require.Empty(t, cfg.Git.AllowedPrivateHosts)
}

func TestLoadConfigMCPDisableLocalhostProtection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(configPath, []byte(`
server:
  mcp-disable-localhost-protection: true
`), 0644)
	require.NoError(t, err)

	cfg, _, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.True(t, cfg.Server.MCPDisableLocalhostProtection)
}
