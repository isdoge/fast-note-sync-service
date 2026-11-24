package api_router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// 缓存过期瞬间的并发请求应合并成一次上游请求，所有调用方共用结果。
func TestCLIUpgrade_ConcurrentRequestsShareOneFetch(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cliUpgradeFixture))
	}))
	defer srv.Close()

	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	handler.setFetchURL(srv.URL)

	const n = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*dto.CLIUpgradeResponse, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = handler.latest(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&hits), "并发请求只应打上游一次")
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "第 %d 个并发调用应成功", i)
		require.NotNil(t, results[i])
		assert.Equal(t, "v1.4.2", results[i].Version, "所有调用方共用同一结果")
	}
}

// cliUpgradePayload 生成指定 tag 的上游发布载荷。
func cliUpgradePayload(tag string) string {
	return fmt.Sprintf(`{
  "tag_name": %q,
  "name": "FastNodeSync CLI %s",
  "body": "release notes",
  "html_url": "https://example.com/%s",
  "published_at": "2026-11-21T10:00:00Z",
  "prerelease": false,
  "assets": []
}`, tag, tag, tag)
}

// seedExpiredCLICache 在锁内写入一条明确早于 TTL 的旧缓存。
func seedExpiredCLICache(h *CLIUpgradeHandler, version string) time.Time {
	cachedAt := time.Now().Add(-cliUpgradeCacheTTL - time.Minute)
	h.mu.Lock()
	h.cached = &dto.CLIUpgradeResponse{Repository: CLIUpgradeRepository, Version: version}
	h.cachedAt = cachedAt
	h.mu.Unlock()
	return cachedAt
}

// 过期缓存被并发刷新时，所有调用方共用一次上游请求，返回同一份新结果且
// Cached/CachedAt 一致；刷新完成后再次调用命中缓存。
func TestCLIUpgrade_ExpiredCacheConcurrentRefreshFlags(t *testing.T) {
	const newTag = "v9.9.9"

	upstreamArrived := make(chan struct{})
	release := make(chan struct{})
	var arrivedOnce, releaseOnce sync.Once
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		arrivedOnce.Do(func() { close(upstreamArrived) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cliUpgradePayload(newTag)))
	}))
	defer srv.Close()
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	handler.setFetchURL(srv.URL)
	seedExpiredCLICache(handler, "v0.0.1-old")

	const n = 16
	results := make([]*dto.CLIUpgradeResponse, n)
	errs := make([]error, n)
	var started, done sync.WaitGroup
	started.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			started.Done()
			results[i], errs[i] = handler.latest(context.Background())
		}(i)
	}

	// 先确认所有 goroutine 已启动、上游桩已收到本轮请求，再在上游被闸住期间
	// 让出调度，使其余 goroutine 进入同一轮 singleflight 等待。
	started.Wait()
	<-upstreamArrived
	for i := 0; i < 200; i++ {
		runtime.Gosched()
	}
	releaseAll()
	done.Wait()

	require.Equal(t, int32(1), atomic.LoadInt32(&hits), "并发刷新只应打上游一次")
	var cachedAt string
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "第 %d 个并发调用应成功", i)
		require.NotNil(t, results[i])
		assert.Equal(t, newTag, results[i].Version, "所有调用方应拿到新版本")
		assert.False(t, results[i].Cached, "参与本轮刷新的调用方 Cached 应为 false")
		assert.False(t, results[i].Stale, "刷新成功不应标记 stale")
		assert.NotEmpty(t, results[i].CachedAt)
		if i == 0 {
			cachedAt = results[i].CachedAt
			continue
		}
		assert.Equal(t, cachedAt, results[i].CachedAt, "同轮调用方应共享缓存时间")
	}

	// 刷新已落地，再单独调用应命中新缓存且不再打上游。
	again, err := handler.latest(context.Background())
	require.NoError(t, err)
	require.NotNil(t, again)
	assert.Equal(t, newTag, again.Version)
	assert.True(t, again.Cached, "TTL 内应命中缓存")
	assert.False(t, again.Stale)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits), "命缓存不应再打上游")
}

// 过期缓存 + 上游失败时回退到旧缓存并标记 Cached/Stale；失败不落缓存，
// 上游恢复后下一次调用能拿到新版本且 Cached=false。
func TestCLIUpgrade_ExpiredCacheUpstreamFailureThenRecover(t *testing.T) {
	const (
		oldTag = "v0.0.1-old"
		newTag = "v9.9.9"
	)
	var fail int32 = 1
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if atomic.LoadInt32(&fail) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cliUpgradePayload(newTag)))
	}))
	defer srv.Close()

	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	handler.setFetchURL(srv.URL)
	seedExpiredCLICache(handler, oldTag)

	fallback, err := handler.latest(context.Background())
	require.NoError(t, err, "有旧缓存时上游失败应回退而不是报错")
	require.NotNil(t, fallback)
	assert.Equal(t, oldTag, fallback.Version, "应返回过期缓存里的旧版本")
	assert.True(t, fallback.Cached, "回退结果来自缓存")
	assert.True(t, fallback.Stale, "过期缓存回退应标记 stale")
	assert.NotEmpty(t, fallback.CachedAt)

	// 上游恢复：证明失败没有被缓存，会重新请求上游。
	atomic.StoreInt32(&fail, 0)
	recovered, err := handler.latest(context.Background())
	require.NoError(t, err)
	require.NotNil(t, recovered)
	assert.Equal(t, newTag, recovered.Version, "上游恢复后应拿到新版本")
	assert.False(t, recovered.Cached, "新结果来自上游而非缓存")
	assert.False(t, recovered.Stale)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits), "回退不缓存，恢复后应再打一次上游")
}

// 发起者取消上下文不会连累同一轮 singleflight 的等待者，也不会中断上游请求：
// latest 用 context.WithoutCancel 把上游请求与调用方上下文解绑，因此发起者
// 本身同样拿到结果（以实际实现为准）。
func TestCLIUpgrade_InitiatorCancelDoesNotFailWaiters(t *testing.T) {
	const newTag = "v9.9.9"

	upstreamArrived := make(chan struct{})
	release := make(chan struct{})
	var arrivedOnce, releaseOnce sync.Once
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		arrivedOnce.Do(func() { close(upstreamArrived) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cliUpgradePayload(newTag)))
	}))
	defer srv.Close()
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	handler.setFetchURL(srv.URL)

	type cliCallResult struct {
		resp *dto.CLIUpgradeResponse
		err  error
	}

	ctxInitiator, cancelInitiator := context.WithCancel(context.Background())
	defer cancelInitiator()

	resInitiator := make(chan cliCallResult, 1)
	go func() {
		resp, err := handler.latest(ctxInitiator)
		resInitiator <- cliCallResult{resp: resp, err: err}
	}()

	<-upstreamArrived // 发起者的请求已到达上游
	cancelInitiator() // 此时取消发起者上下文

	// 等待者在发起者的上游请求尚未放行时加入同一轮刷新。
	waiterStarted := make(chan struct{})
	resWaiter := make(chan cliCallResult, 1)
	go func() {
		close(waiterStarted)
		resp, err := handler.latest(context.Background())
		resWaiter <- cliCallResult{resp: resp, err: err}
	}()
	<-waiterStarted
	for i := 0; i < 200; i++ {
		runtime.Gosched()
	}
	releaseAll()

	waiter := <-resWaiter
	require.NoError(t, waiter.err, "等待者不应被发起者取消连累")
	require.NotNil(t, waiter.resp)
	assert.Equal(t, newTag, waiter.resp.Version)

	initiator := <-resInitiator
	require.NoError(t, initiator.err, "上游请求已与发起者上下文解绑，发起者仍应拿到结果")
	require.NotNil(t, initiator.resp)
	assert.Equal(t, newTag, initiator.resp.Version)

	assert.Equal(t, int32(1), atomic.LoadInt32(&hits), "两个调用应共用一次上游请求")
}

// 上游失败不缓存：修复后下一次调用可重新请求上游并成功。
func TestCLIUpgrade_RetryAfterUpstreamFailure(t *testing.T) {
	var hits int32
	var failFirst int32 = 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if atomic.CompareAndSwapInt32(&failFirst, 1, 0) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cliUpgradeFixture))
	}))
	defer srv.Close()

	handler := NewCLIUpgradeHandler(app.NewTestApp(nil))
	handler.setFetchURL(srv.URL)

	_, err := handler.latest(context.Background())
	require.Error(t, err, "首次上游失败应报错")

	data, err := handler.latest(context.Background())
	require.NoError(t, err, "失败不缓存，下一次应重新请求上游")
	assert.Equal(t, "v1.4.2", data.Version)
	assert.False(t, data.Cached, "来自重新发起的上游请求")
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits), "两次调用应各打一次上游")
}
