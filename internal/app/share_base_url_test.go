package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShareBaseURLStore_LoadMissingFileIsNoop(t *testing.T) {
	store := NewShareBaseURLStore(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, store.Load())
	assert.Empty(t, store.Override())
}

func TestShareBaseURLStore_AtomicWriteAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "share_base_url")
	store := NewShareBaseURLStore(path)
	require.NoError(t, store.Set("https://a.example.com"))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "https://a.example.com\n", string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// No temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestShareBaseURLStore_ConcurrentAccess(t *testing.T) {
	store := NewShareBaseURLStore(filepath.Join(t.TempDir(), "share_base_url"))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = store.Set("https://concurrent.example.com")
			}
			_ = store.Override()
		}(i)
	}
	wg.Wait()
	require.NoError(t, store.Load())
	assert.Equal(t, "https://concurrent.example.com", store.Override())
}

func TestShareBaseURLValue_Precedence(t *testing.T) {
	a := NewTestApp(nil)
	// The configured value is only effective when both ports are configured,
	// matching how share links are generated.
	// 只有两个端口都配置时配置值才实际生效，与分享链接生成逻辑保持一致。
	a.Config().Server.WebGuiPort = "8080"
	a.Config().Server.SharePort = "9100"
	a.Config().Server.ShareBaseUrl = "https://config.example.com"

	value, source := a.ShareBaseURLValue()
	assert.Equal(t, "https://config.example.com", value)
	assert.Equal(t, ShareBaseURLSourceConfig, source)

	require.NoError(t, a.ShareBaseURLStore().Set("https://override.example.com"))
	value, source = a.ShareBaseURLValue()
	assert.Equal(t, "https://override.example.com", value)
	assert.Equal(t, ShareBaseURLSourceOverride, source)

	require.NoError(t, a.ShareBaseURLStore().Set(""))
	value, source = a.ShareBaseURLValue()
	assert.Equal(t, "https://config.example.com", value, "清除覆盖后回落配置")
	assert.Equal(t, ShareBaseURLSourceConfig, source)
}

func TestShareBaseURLValue_RequiresBothPorts(t *testing.T) {
	a := NewTestApp(nil)
	a.Config().Server.ShareBaseUrl = "https://config.example.com"

	// One port missing: the config value is not used by share links, so the
	// effective value is empty while the source is still the config.
	// 缺少任一端口：分享链接不会采用配置值，生效值为空，来源仍是配置。
	a.Config().Server.WebGuiPort = "8080"
	value, source := a.ShareBaseURLValue()
	assert.Empty(t, value)
	assert.Equal(t, ShareBaseURLSourceConfig, source)
	assert.Empty(t, a.ConfiguredShareBaseURL())

	a.Config().Server.WebGuiPort = ""
	a.Config().Server.SharePort = "9100"
	value, source = a.ShareBaseURLValue()
	assert.Empty(t, value)
	assert.Equal(t, ShareBaseURLSourceConfig, source)

	a.Config().Server.WebGuiPort = "8080"
	value, source = a.ShareBaseURLValue()
	assert.Equal(t, "https://config.example.com", value)
	assert.Equal(t, ShareBaseURLSourceConfig, source)

	// A runtime override wins regardless of the ports.
	// 运行时覆盖值不受端口门槛影响。
	require.NoError(t, a.ShareBaseURLStore().Set("https://override.example.com"))
	a.Config().Server.WebGuiPort = ""
	a.Config().Server.SharePort = ""
	value, source = a.ShareBaseURLValue()
	assert.Equal(t, "https://override.example.com", value)
	assert.Equal(t, ShareBaseURLSourceOverride, source)
}

// 模拟重启：同一路径的新 store 在 Load 后恢复覆盖值。
func TestShareBaseURLStore_SurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "share_base_url")
	first := NewShareBaseURLStore(path)
	require.NoError(t, first.Set("https://persisted.example.com:9000"))

	second := NewShareBaseURLStore(path)
	require.NoError(t, second.Load())
	assert.Equal(t, "https://persisted.example.com:9000", second.Override())
}
