package task

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"go.uber.org/zap"
	"golang.org/x/mod/semver"
)

const (
	GitHubServiceReleaseURL = "https://api.github.com/repos/isdoge/fast-note-sync-service/releases"
	GitHubPluginReleaseURL  = "https://api.github.com/repos/isdoge/obsidian-fast-note-sync/releases"
	ServiceRepoPath         = "isdoge/fast-note-sync-service"
	ServiceRepoURL          = "https://github.com/" + ServiceRepoPath
	PluginRepoPath          = "isdoge/obsidian-fast-note-sync"
	PluginRepoURL           = "https://github.com/" + PluginRepoPath

	// Bound each GitHub release-list request.
	// 限制单次 GitHub release 列表请求的耗时。
	fetchHTTPTimeout = 8 * time.Second
)

type GitHubAsset struct {
	Name  string `json:"name"`  // Asset name // 资源包名称
	State string `json:"state"` // Upload state // 上传状态
}

type GitHubRelease struct {
	TagName    string        `json:"tag_name"`
	Prerelease bool          `json:"prerelease"`
	Body       string        `json:"body"`   // Release description (changelog) // 版本发布说明（更新日志）
	Assets     []GitHubAsset `json:"assets"` // Release assets // 资源列表
}

type GitHubTag struct {
	Name string `json:"name"`
}

type CheckVersionTask struct {
	app *app.App
}

func init() {
	RegisterWithApp(func(appContainer *app.App) (Task, error) {
		return &CheckVersionTask{
			app: appContainer,
		}, nil
	})
}

func (t *CheckVersionTask) Name() string {
	return "check_version"
}

func (t *CheckVersionTask) Run(ctx context.Context) error {
	// All configured source modes use the independently maintained GitHub releases.
	// 所有旧下载源配置均回落到独立维护的 GitHub releases。
	serviceReleases, serviceLink, serviceChangelog, serviceChangelogContent, err :=
		t.fetchReleases(ctx, GitHubServiceReleaseURL, true)
	if err != nil {
		return fmt.Errorf("fetch service releases: %w", err)
	}

	pluginReleases, pluginLink, pluginChangelog, pluginChangelogContent, perr :=
		t.fetchReleases(ctx, GitHubPluginReleaseURL, false)
	if perr != nil {
		// Plugin version is best-effort; don't fail the whole task if only plugin fetch errors.
		// 插件版本尽力而为：仅插件抓取失败不应导致整个任务失败。
		t.app.Logger().Warn("check_version: plugin releases fetch failed, keeping service result only", zap.Error(perr))
		pluginReleases = nil
	}

	var serviceLatest, pluginLatest string
	if len(serviceReleases) > 0 {
		serviceLatest = serviceReleases[0].Version
	}
	if len(pluginReleases) > 0 {
		pluginLatest = pluginReleases[0].Version
	}

	currentServiceVersion := t.app.Version().Version
	if !strings.HasPrefix(currentServiceVersion, "v") {
		currentServiceVersion = "v" + currentServiceVersion
	}

	if serviceLatest != "" && !strings.HasPrefix(serviceLatest, "v") {
		serviceLatest = "v" + serviceLatest
	}

	if pluginLatest != "" && !strings.HasPrefix(pluginLatest, "v") {
		pluginLatest = "v" + pluginLatest
	}

	info := pkgapp.CheckVersionInfo{
		GithubAvailable:                  true,
		VersionNewName:                   serviceLatest,
		VersionIsNew:                     serviceLatest != "" && semver.Compare(serviceLatest, currentServiceVersion) > 0,
		VersionNewLink:                   serviceLink,
		VersionNewChangelog:              serviceChangelog,
		VersionNewChangelogContent:       serviceChangelogContent,
		PluginVersionNewName:             pluginLatest,
		PluginVersionNewLink:             pluginLink,
		PluginVersionNewChangelog:        pluginChangelog,
		PluginVersionNewChangelogContent: pluginChangelogContent,
	}

	// 更新 App 中的版本信息和发布列表
	t.app.SetCheckVersionInfo(info)
	t.app.SetCheckVersionReleases(serviceReleases, pluginReleases)

	// 推送版本信息给所有已连接客户端
	t.app.BroadcastClientInfo()

	return nil
}

// fetchReleases fetches GitHub releases and assembles the latest release links.
// fetchReleases 获取 GitHub 版本列表并拼装最新版本的链接。
func (t *CheckVersionTask) fetchReleases(ctx context.Context, url string, isService bool) ([]pkgapp.HistoricalVersion, string, string, string, error) {
	releases, err := t.fetchGitHubReleasesCtx(ctx, url)
	if err != nil {
		return nil, "", "", "", err
	}
	link, changelogLink, changelogContent := buildLinks(releases, isService)
	return releases, link, changelogLink, changelogContent, nil
}

func buildLinks(releases []pkgapp.HistoricalVersion, isService bool) (link, changelogLink, changelogContent string) {
	if len(releases) == 0 {
		return "", "", ""
	}
	latest := strings.TrimPrefix(releases[0].Version, "v")
	base := ServiceRepoURL
	if !isService {
		base = PluginRepoURL
	}
	return base + "/releases/tag/" + latest, base + "/releases/download/" + latest + "/changelog.txt", releases[0].ChangelogContent
}

// hasValidAssets checks if there is at least one uploaded zip or tar.gz file
// hasValidAssets 检查是否包含至少一个已成功上传的 zip 或 tar.gz 资源文件
func hasValidAssets(assets []GitHubAsset) bool {
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		if strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") {
			if asset.State == "" || asset.State == "uploaded" {
				return true
			}
		}
	}
	return false
}

func (t *CheckVersionTask) fetchGitHubReleasesCtx(ctx context.Context, url string) ([]pkgapp.HistoricalVersion, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	cli := &http.Client{Timeout: fetchHTTPTimeout}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var releases []GitHubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, err
	}

	releaseChannel := t.app.Config().App.PullReleaseChannel
	var result []pkgapp.HistoricalVersion
	for _, release := range releases {
		if releaseChannel == "stable" && release.Prerelease {
			continue
		}
		if !hasValidAssets(release.Assets) {
			continue
		}
		tagName := release.TagName
		if !strings.HasPrefix(tagName, "v") {
			tagName = "v" + tagName
		}
		result = append(result, pkgapp.HistoricalVersion{
			Version:          tagName,
			ChangelogContent: release.Body,
		})
	}

	return result, nil
}

func (t *CheckVersionTask) LoopInterval() time.Duration {
	return 10 * time.Minute
}

func (t *CheckVersionTask) IsStartupRun() bool {
	return true
}
