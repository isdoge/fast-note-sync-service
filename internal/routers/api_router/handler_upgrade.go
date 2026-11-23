package api_router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const (
	// CLIUpgradeRepository is the only upstream the CLI upgrade relay ever
	// contacts. The URL is fixed server-side; clients cannot influence it.
	// CLIUpgradeRepository 是中转接口唯一访问的上游；地址固定在服务端，
	// 客户端无法影响它。
	CLIUpgradeRepository = "Go1c/FastNodeSync-CLI"

	// cliUpgradeAPIURL is the fixed, whitelisted GitHub API endpoint.
	// cliUpgradeAPIURL 固定的、白名单内的 GitHub API 地址。
	cliUpgradeAPIURL = "https://api.github.com/repos/" + CLIUpgradeRepository + "/releases/latest"

	// cliUpgradeCacheTTL is the short in-memory cache lifetime that protects the
	// GitHub API from per-client request amplification.
	// cliUpgradeCacheTTL 是内存缓存的短有效期，避免每个客户端请求都打到 GitHub。
	cliUpgradeCacheTTL = 10 * time.Minute

	// cliUpgradeFetchTimeout bounds one upstream request.
	// cliUpgradeFetchTimeout 限制单次上游请求耗时。
	cliUpgradeFetchTimeout = 8 * time.Second
)

// CLIUpgradeHandler relays the latest FastNodeSync-CLI release so restricted
// intranet clients can upgrade without direct GitHub access.
// CLIUpgradeHandler 中转最新 FastNodeSync-CLI 发布信息，供受限内网客户端在
// 无法直连 GitHub 时升级。
type CLIUpgradeHandler struct {
	*Handler

	mu        sync.Mutex
	cached    *dto.CLIUpgradeResponse
	cachedAt  time.Time
	fetchURL  string // overridable in tests; empty uses cliUpgradeAPIURL // 测试可覆盖；空则用固定地址
	httpClint *http.Client

	// fetchGroup collapses concurrent cache-miss refreshes into a single upstream
	// request, so an expired cache cannot fan out into N GitHub calls.
	// fetchGroup 合并缓存过期瞬间的并发刷新，只向上游发一次请求，避免并发
	// 请求把 GitHub 未认证限额打满。
	fetchGroup singleflight.Group
}

// cliUpgradeFetchFlight is the value shared by all callers of one collapsed fetch.
// cliUpgradeFetchFlight 是一次合并刷新中由所有调用方共享的结果。
type cliUpgradeFetchFlight struct {
	resp      *dto.CLIUpgradeResponse
	cachedAt  time.Time
	fromCache bool // the flight found a fresh cache instead of hitting upstream // 本轮未打上游而是命中缓存
}

// NewCLIUpgradeHandler creates CLIUpgradeHandler instance.
// NewCLIUpgradeHandler 创建 CLIUpgradeHandler 实例。
func NewCLIUpgradeHandler(a *app.App) *CLIUpgradeHandler {
	return &CLIUpgradeHandler{
		Handler:   NewHandler(a),
		httpClint: &http.Client{Timeout: cliUpgradeFetchTimeout},
	}
}

// setFetchURL overrides the upstream URL (tests only).
// setFetchURL 覆盖上游地址（仅供测试）。
func (h *CLIUpgradeHandler) setFetchURL(u string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fetchURL = u
}

func (h *CLIUpgradeHandler) upstreamURL() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fetchURL != "" {
		return h.fetchURL
	}
	return cliUpgradeAPIURL
}

// CLI returns the latest FastNodeSync-CLI release metadata.
// @Summary Get latest FastNodeSync-CLI release
// @Description Proxy the latest FastNodeSync-CLI release (fixed upstream, 10-minute in-memory cache) so restricted intranet clients can upgrade. Requires an authenticated user token.
// @Tags System
// @Security UserAuthToken
// @Produce json
// @Success 200 {object} pkgapp.Res{data=dto.CLIUpgradeResponse} "Success"
// @Router /api/upgrade/cli [get]
func (h *CLIUpgradeHandler) CLI(c *gin.Context) {
	response := pkgapp.NewResponse(c)
	ctx := c.Request.Context()

	result, err := h.latest(ctx)
	if err != nil {
		// No cache and upstream failed: report gracefully instead of panicking.
		// 无缓存且上游失败：优雅报错而不是崩溃。
		h.App.Logger().Warn("CLIUpgradeHandler.CLI: upstream fetch failed", zap.Error(err))
		response.ToResponse(code.ErrorServerInternal.WithDetails("failed to fetch CLI release metadata: " + err.Error()))
		return
	}

	response.ToResponse(code.Success.WithData(result))
}

// latest returns the cached release, refreshing it when stale. A stale cached
// value is served (marked stale) if a refresh attempt fails, so a transient
// GitHub outage does not break existing clients. Concurrent refreshes are
// collapsed into one upstream request; every caller that joins that request
// shares its result and reports Cached=false.
// latest 返回缓存的发布信息，过期时刷新；刷新失败时回退到过期缓存（标记
// stale），GitHub 短暂不可用不会让客户端完全拿不到信息。并发刷新会被合并成
// 一次上游请求，参与该请求的调用方共用结果并返回 Cached=false。
func (h *CLIUpgradeHandler) latest(ctx context.Context) (*dto.CLIUpgradeResponse, error) {
	if resp, cachedAt := h.freshCache(); resp != nil {
		return withCLICacheFlags(resp, cachedAt, true, false), nil
	}

	v, err, _ := h.fetchGroup.Do("latest", func() (any, error) {
		// Double-check: a flight that just finished may have refreshed the cache
		// between the caller's check and this flight starting.
		// 双检：可能刚好有上一轮刷新在调用方取缓存之后写入了新缓存。
		if resp, cachedAt := h.freshCache(); resp != nil {
			return cliUpgradeFetchFlight{resp: resp, cachedAt: cachedAt, fromCache: true}, nil
		}
		// Detach from the caller's request context so one client disconnecting
		// does not fail every waiter; fetch still applies cliUpgradeFetchTimeout.
		// 与调用方请求上下文解绑，单个客户端断开不会让所有等待者一起失败；
		// 耗时仍由 fetch 内部的 cliUpgradeFetchTimeout 限制。
		fetched, err := h.fetch(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		now := time.Now()
		h.mu.Lock()
		h.cached = fetched
		h.cachedAt = now
		h.mu.Unlock()
		return cliUpgradeFetchFlight{resp: fetched, cachedAt: now}, nil
	})
	if err != nil {
		// Failure is not cached; the next caller may retry upstream. Fall back to
		// the previous cache when there is one.
		// 失败不写缓存，下一次请求可重新发起；有旧缓存则回退并标记 stale。
		if stale, cachedAt := h.snapshotCache(); stale != nil {
			staleFlag := time.Since(cachedAt) >= cliUpgradeCacheTTL
			return withCLICacheFlags(stale, cachedAt, true, staleFlag), nil
		}
		return nil, err
	}

	flight := v.(cliUpgradeFetchFlight)
	return withCLICacheFlags(flight.resp, flight.cachedAt, flight.fromCache, false), nil
}

// snapshotCache copies the current cache entry under the lock.
// snapshotCache 在锁内复制当前缓存条目。
func (h *CLIUpgradeHandler) snapshotCache() (*dto.CLIUpgradeResponse, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cached == nil {
		return nil, time.Time{}
	}
	cp := *h.cached
	return &cp, h.cachedAt
}

// freshCache returns a copy of the cache only when it is still within TTL.
// freshCache 仅在缓存仍在有效期内时返回其副本。
func (h *CLIUpgradeHandler) freshCache() (*dto.CLIUpgradeResponse, time.Time) {
	resp, cachedAt := h.snapshotCache()
	if resp == nil || time.Since(cachedAt) >= cliUpgradeCacheTTL {
		return nil, time.Time{}
	}
	return resp, cachedAt
}

// withCLICacheFlags stamps the cache flags on a caller-owned copy.
// withCLICacheFlags 在调用方私有的副本上填充缓存标记。
func withCLICacheFlags(resp *dto.CLIUpgradeResponse, cachedAt time.Time, cached, stale bool) *dto.CLIUpgradeResponse {
	out := *resp
	out.Cached = cached
	out.Stale = stale
	out.CachedAt = cachedAt.UTC().Format(time.RFC3339)
	return &out
}

// githubRelease is the subset of the GitHub release payload we consume.
// githubRelease 只取用得到的 GitHub 发布字段。
type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// fetch performs one whitelisted upstream request and maps the payload.
// fetch 执行一次白名单内的上游请求并映射结果。
func (h *CLIUpgradeHandler) fetch(ctx context.Context) (*dto.CLIUpgradeResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, cliUpgradeFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, h.upstreamURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "fast-note-sync-service-cli-upgrade-relay")

	resp, err := h.httpClint.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request upstream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a little to allow connection reuse without buffering everything.
		// 少量读取以复用连接，避免整包缓冲。
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		return nil, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	var rel githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode upstream payload: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, errors.New("upstream payload missing tag_name")
	}

	assets := make([]dto.CLIAsset, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		assets = append(assets, dto.CLIAsset{
			Name:        a.Name,
			DownloadURL: a.BrowserDownloadURL,
			Size:        a.Size,
		})
	}

	return &dto.CLIUpgradeResponse{
		Repository:  CLIUpgradeRepository,
		Version:     rel.TagName,
		Name:        rel.Name,
		PublishedAt: rel.PublishedAt,
		HTMLURL:     rel.HTMLURL,
		Body:        rel.Body,
		Assets:      assets,
		Prerelease:  rel.Prerelease,
	}, nil
}
