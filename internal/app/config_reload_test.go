package app

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// loadConfigForReload writes body to a temp config file and loads it through the
// real LoadConfig path, so defaults and validation match production behavior.
func loadConfigForReload(t *testing.T, body string) *AppConfig {
	t.Helper()
	cfg, _, err := LoadConfig(writeTestConfig(t, body))
	require.NoError(t, err)
	return cfg
}

// TestConfigReloadRequiresRestart locks the hot-reload whitelist: changing only
// user.admin-uid does not require a server rebuild, anything else does.
func TestConfigReloadRequiresRestart(t *testing.T) {
	base := loadConfigForReload(t, "{}")
	adminOnly := loadConfigForReload(t, "user:\n  admin-uid: 7\n")
	staticChange := loadConfigForReload(t, "server:\n  http-port: \":19999\"\n")
	combined := loadConfigForReload(t, "user:\n  admin-uid: 7\nserver:\n  http-port: \":19999\"\n")

	require.False(t, ConfigReloadRequiresRestart(base, adminOnly),
		"an admin-uid-only change must be treated as hot-reloadable")
	require.True(t, ConfigReloadRequiresRestart(base, staticChange),
		"a listener/timeout change must still restart the server")
	require.True(t, ConfigReloadRequiresRestart(base, combined),
		"a mixed change must restart the server")
	require.False(t, ConfigReloadRequiresRestart(base, base),
		"an unchanged config must not restart the server")
}

// TestApplyConfigFileReload_AdminUIDOnlyIsHot is the regression lock for the
// WebGUI "make current user admin" flow: writing user.admin-uid must update the
// live config in place instead of closing the HTTP listeners.
func TestApplyConfigFileReload_AdminUIDOnlyIsHot(t *testing.T) {
	base := loadConfigForReload(t, "{}")
	adminOnly := loadConfigForReload(t, "user:\n  admin-uid: 7\n")

	a := &App{Infra: &Infra{config: base, logger: zap.NewNop()}}
	require.NoError(t, a.captureConfigFingerprint(base))

	require.False(t, a.ApplyConfigFileReload(adminOnly))
	require.Equal(t, 7, a.config.User.AdminUID)

	// A repeated watcher event for the same content must stay a no-op.
	require.False(t, a.ApplyConfigFileReload(adminOnly))
	require.Equal(t, 7, a.config.User.AdminUID)
}

// TestApplyConfigFileReload_StaticChangeRequiresRestart ensures the hot path
// only applies to admin-uid; static settings still return true and leave the
// running config untouched for the caller to rebuild the server.
func TestApplyConfigFileReload_StaticChangeRequiresRestart(t *testing.T) {
	base := loadConfigForReload(t, "{}")
	staticChange := loadConfigForReload(t, "server:\n  http-port: \":19999\"\n")
	combined := loadConfigForReload(t, "user:\n  admin-uid: 7\nserver:\n  http-port: \":19999\"\n")

	a := &App{Infra: &Infra{config: base, logger: zap.NewNop()}}
	require.NoError(t, a.captureConfigFingerprint(base))
	originalPort := a.config.Server.HttpPort

	require.True(t, a.ApplyConfigFileReload(staticChange))
	require.Equal(t, originalPort, a.config.Server.HttpPort)

	require.True(t, a.ApplyConfigFileReload(combined))
	require.Equal(t, originalPort, a.config.Server.HttpPort)
	require.Equal(t, 0, a.config.User.AdminUID)
}
