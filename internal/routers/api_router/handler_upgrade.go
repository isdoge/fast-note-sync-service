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
// GitHub outage does not break existing clients.
// latest 返回缓存的发布信息，过期时刷新；刷新失败时回退到过期缓存（标记
// stale），GitHub 短暂不可用不会让客户端完全拿不到信息。
func (h *CLIUpgradeHandler) latest(ctx context.Context) (*dto.CLIUpgradeResponse, error) {
	h.mu.Lock()
	cached := h.cached
	cachedAt := h.cachedAt
	h.mu.Unlock()

	if cached != nil && time.Since(cachedAt) < cliUpgradeCacheTTL {
		fresh := *cached
		fresh.Cached = true
		fresh.Stale = false
		fresh.CachedAt = cachedAt.UTC().Format(time.RFC3339)
		return &fresh, nil
	}

	fetched, err := h.fetch(ctx)
	if err != nil {
		if cached != nil {
			stale := *cached
			stale.Cached = true
			stale.Stale = true
			stale.CachedAt = cachedAt.UTC().Format(time.RFC3339)
			return &stale, nil
		}
		return nil, err
	}

	now := time.Now()
	h.mu.Lock()
	h.cached = fetched
	h.cachedAt = now
	h.mu.Unlock()

	fresh := *fetched
	fresh.Cached = false
	fresh.Stale = false
	fresh.CachedAt = now.UTC().Format(time.RFC3339)
	return &fresh, nil
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
