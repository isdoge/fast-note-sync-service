package api_router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cliUpgradeFixture = `{
  "tag_name": "v1.4.2",
  "name": "FastNodeSync CLI v1.4.2",
  "body": "release notes",
  "html_url": "https://github.com/Go1c/FastNodeSync-CLI/releases/tag/v1.4.2",
  "published_at": "2026-11-20T10:00:00Z",
  "prerelease": false,
  "assets": [
    {"name": "fns-cli-linux-amd64", "browser_download_url": "https://example.com/fns-cli-linux-amd64", "size": 12345}
  ]
}`

func decodeCLI(t *testing.T, w *httptest.ResponseRecorder) (dto.CLIUpgradeResponse, int64) {
	t.Helper()
	var resp struct {
		Code int64                  `json:"code"`
		Data dto.CLIUpgradeResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "body: %s", w.Body.String())
	return resp.Data, int64(resp.Code)
}

func newCLIContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/upgrade/cli", nil)
	return c, w
}

func TestCLIUpgrade_FetchAndCache(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		assert.Equal(t, "fast-note-sync-service-cli-upgrade-relay", r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cliUpgradeFixture))
	}))
	defer srv.Close()

	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	handler.setFetchURL(srv.URL)

	c, w := newCLIContext()
	handler.CLI(c)
	data, rc := decodeCLI(t, w)
	require.Equal(t, int64(1), rc, w.Body.String())
	assert.Equal(t, "v1.4.2", data.Version)
	assert.Equal(t, CLIUpgradeRepository, data.Repository)
	assert.Equal(t, "https://github.com/Go1c/FastNodeSync-CLI/releases/tag/v1.4.2", data.HTMLURL)
	require.Len(t, data.Assets, 1)
	assert.Equal(t, "fns-cli-linux-amd64", data.Assets[0].Name)
	assert.False(t, data.Cached, "首次请求来自上游")

	// Second call within TTL must be served from cache (no new upstream hit).
	c, w = newCLIContext()
	handler.CLI(c)
	data, rc = decodeCLI(t, w)
	require.Equal(t, int64(1), rc)
	assert.True(t, data.Cached, "TTL 内应命中缓存")
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits), "缓存命中不应再打上游")
}

func TestCLIUpgrade_UpstreamFailureNoCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	handler.setFetchURL(srv.URL)

	c, w := newCLIContext()
	handler.CLI(c)
	_, rc := decodeCLI(t, w)
	assert.NotEqual(t, int64(1), rc, "无缓存时上游失败应报错而不是成功")
	assert.Equal(t, int64(300), rc, "应返回服务器内部错误码")
}

func TestCLIUpgrade_StaleCacheFallback(t *testing.T) {
	var fail int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&fail) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(cliUpgradeFixture))
	}))
	defer srv.Close()

	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	handler.setFetchURL(srv.URL)

	c, w := newCLIContext()
	handler.CLI(c)
	_, rc := decodeCLI(t, w)
	require.Equal(t, int64(1), rc)

	// Expire the cache, then make the upstream fail: stale cache must be served.
	handler.mu.Lock()
	handler.cachedAt = handler.cachedAt.Add(-cliUpgradeCacheTTL - 1)
	handler.mu.Unlock()
	atomic.StoreInt32(&fail, 1)

	c, w = newCLIContext()
	handler.CLI(c)
	data, rc := decodeCLI(t, w)
	require.Equal(t, int64(1), rc, "上游失败应回退到过期缓存")
	assert.True(t, data.Stale, "标记为过期缓存")
	assert.Equal(t, "v1.4.2", data.Version)
}

func TestCLIUpgrade_BadPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"no tag"}`))
	}))
	defer srv.Close()

	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	handler.setFetchURL(srv.URL)

	c, w := newCLIContext()
	handler.CLI(c)
	_, rc := decodeCLI(t, w)
	assert.NotEqual(t, int64(1), rc, "缺少 tag_name 的载荷应报错")
}

// 上游地址固定在服务端：客户端无法通过参数影响请求目标。
func TestCLIUpgrade_FixedUpstreamURL(t *testing.T) {
	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	assert.Equal(t, "https://api.github.com/repos/Go1c/FastNodeSync-CLI/releases/latest", handler.upstreamURL())
}
