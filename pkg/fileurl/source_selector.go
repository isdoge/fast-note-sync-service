package fileurl

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const (
	SourceAuto   = "auto"
	SourceGitHub = "github"

	// Probe the maintained fork release endpoint.
	GitHubProbeURL = "https://api.github.com/repos/isdoge/fast-note-sync-service/releases"

	// defaultProbeTimeout is the per-source probe timeout.
	// 单源探测超时。
	defaultProbeTimeout = 3 * time.Second
	// defaultCacheTTL is how long a probe snapshot is reused before re-probing.
	// 探测快照缓存时长；避免后台任务每 10 分钟轮询时反复打探测请求。
	defaultCacheTTL = 5 * time.Minute
)

// ProbeResult is the reachability + latency result of a single source probe.
// ProbeResult 单源探测结果（可达性 + 延迟）。
type ProbeResult struct {
	OK        bool  // true if the source responded with a 2xx/3xx status
	LatencyMs int64 // round-trip time in milliseconds (0 if request failed before any response)
}

// ProbeSnapshot contains the GitHub reachability result for the latency panel.
// ProbeSnapshot 记录 GitHub 可达性，供前端测速面板展示。
type ProbeSnapshot struct {
	GitHub    ProbeResult
	UseGitHub bool      // always true; legacy source settings also use GitHub
	At        time.Time // when this snapshot was taken
}

// SourceSelector retains source configuration compatibility while using GitHub only.
// SourceSelector 保留选源配置兼容性，实际仅使用 GitHub。
//
// Concurrency: IsGitHub / Probe / Snapshot / SetMode are all goroutine-safe.
// 并发安全：所有公开方法均可被多个 goroutine 并发调用（后台任务、HTTP handler、
// WebSocket 广播都会调用 IsGitHub）。
type SourceSelector struct {
	mu       sync.Mutex
	mode     string
	snapshot *ProbeSnapshot
	cacheTTL time.Duration
}

// NewSourceSelector creates a new SourceSelector.
// NewSourceSelector 创建一个新的 SourceSelector。
func NewSourceSelector(mode string) *SourceSelector {
	return &SourceSelector{
		mode:     normalizeSourceMode(mode),
		cacheTTL: defaultCacheTTL,
	}
}

// SetMode accepts legacy values and maps unsupported sources to GitHub.
// SetMode 将不再支持的旧源设置回落到 GitHub。
func (s *SourceSelector) SetMode(mode string) {
	s.mu.Lock()
	s.mode = normalizeSourceMode(mode)
	s.snapshot = nil // invalidate cached snapshot so the new mode takes effect immediately
	s.mu.Unlock()
}

func normalizeSourceMode(mode string) string {
	if mode == SourceAuto {
		return SourceAuto
	}
	return SourceGitHub
}

// IsGitHub always returns true, including for legacy source settings.
// IsGitHub 始终返回 true，兼容旧下载源设置。
func (s *SourceSelector) IsGitHub() bool {
	return true
}

// Probe refreshes the GitHub latency snapshot for the webgui.
// Probe 为 webgui 刷新 GitHub 延迟快照。
func (s *SourceSelector) Probe(ctx context.Context) ProbeSnapshot {
	snap := probeGitHub(ctx)
	s.mu.Lock()
	s.snapshot = &snap
	s.mu.Unlock()
	return snap
}

// Snapshot returns the cached probe snapshot if still fresh, otherwise nil.
// Snapshot 返回仍在有效期内的缓存快照；过期或从未探测过则返回 nil（前端展示用）。
func (s *SourceSelector) Snapshot() *ProbeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil || time.Since(s.snapshot.At) > s.cacheTTL {
		return nil
	}
	snap := *s.snapshot
	return &snap
}

// Mode returns the current selection mode.
// Mode 返回当前选源模式。
func (s *SourceSelector) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func probeGitHub(ctx context.Context) ProbeSnapshot {
	return ProbeSnapshot{
		GitHub:    probeOne(ctx, GitHubProbeURL, ""),
		UseGitHub: true,
		At:        time.Now(),
	}
}

// probeOne issues a HEAD request against url with a bounded timeout and reports
// reachability + latency. Client and server errors count as unavailable.
// probeOne 对 url 发起带超时的 HEAD 请求，返回可达性与延迟。
func probeOne(ctx context.Context, url, token string) ProbeResult {
	cli := &http.Client{Timeout: defaultProbeTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return ProbeResult{OK: false}
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	start := time.Now()
	resp, err := cli.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return ProbeResult{OK: false, LatencyMs: latency}
	}
	defer resp.Body.Close()
	ok := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest
	return ProbeResult{OK: ok, LatencyMs: latency}
}
