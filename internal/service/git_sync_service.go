package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	appconfig "github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/safego"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// errNoChanges indicates that no changes were found after the Git sync check
// errNoChanges 表示 Git 同步检查后没有发现任何需要提交的变更
var errNoChanges = errors.New("no changes found")

const gitSyncBatchSize = 100

// gitSyncValidateTimeout 单次 Git 配置验证（ls-remote）的最长耗时。
// 远端无响应时此前完全依赖 TCP 层默认超时（分钟级），前端表现为永远“检测中”
// （haierkeys/obsidian-fast-note-sync#231）。显式超时后立刻返回明确错误。
// gitSyncValidateTimeout bounds a single Git config validation (ls-remote) call.
const gitSyncValidateTimeout = 20 * time.Second

// GitSyncService defines the Git synchronization business service interface
// GitSyncService 定义 Git 同步业务服务接口
type GitSyncService interface {
	GetConfigs(ctx context.Context, uid int64) ([]*dto.GitSyncConfigDTO, error)
	GetConfig(ctx context.Context, uid int64, vaultID int64) (*dto.GitSyncConfigDTO, error)
	UpdateConfig(ctx context.Context, uid int64, params *dto.GitSyncConfigRequest) (*dto.GitSyncConfigDTO, error)
	DeleteConfig(ctx context.Context, uid int64, id int64) error
	Validate(ctx context.Context, params *dto.GitSyncValidateRequest) error
	ExecuteSync(ctx context.Context, uid int64, id int64) error
	// GetVaultStatuses / TriggerByVault back the client-safe (vault-scoped) endpoints used
	// by the Obsidian plugin. They never expose repo URLs, credentials or webhook URLs.
	// GetVaultStatuses / TriggerByVault 支撑插件使用的库级安全接口，绝不暴露仓库地址、
	// 凭据或 Webhook 地址。
	GetVaultStatuses(ctx context.Context, uid int64, vaultName string) ([]*dto.GitSyncPublicStatusDTO, error)
	TriggerByVault(ctx context.Context, uid int64, vaultName string, configID int64) error
	CleanWorkspace(ctx context.Context, uid int64, configID int64) error
	ListHistory(ctx context.Context, uid int64, configID int64, pager *pkgapp.Pager) ([]*dto.GitSyncHistoryDTO, int64, error)
	NotifyUpdated(uid int64, vaultID int64)
	Shutdown(ctx context.Context) error
}

// gitSyncEnabledCacheTTL is the max time a "no enabled git sync config for this vault" cache
// entry is trusted before being re-verified against the DB, as a safety net alongside
// explicit invalidation on config write (UpdateConfig/DeleteConfig).
// gitSyncEnabledCacheTTL 是"该 vault 当前没有启用的 git 同步配置"缓存条目在被重新校验前的
// 最长信任时间，作为配置写入（UpdateConfig/DeleteConfig）时显式失效之外的兜底保险。
const gitSyncEnabledCacheTTL = 60 * time.Second

// gitSyncEnabledCacheKey identifies a (uid, vaultID) pair for the enabled-config cache.
// gitSyncEnabledCacheKey 标识 enabled-config 缓存中的 (uid, vaultID) 组合。
type gitSyncEnabledCacheKey struct {
	uid     int64
	vaultID int64
}

// gitSyncEnabledCacheEntry caches whether a vault currently has any enabled git sync config.
// gitSyncEnabledCacheEntry 缓存某个 vault 当前是否存在启用中的 git 同步配置。
type gitSyncEnabledCacheEntry struct {
	enabled  bool
	cachedAt time.Time
}

type gitSyncService struct {
	repo        domain.GitSyncRepository
	noteRepo    domain.NoteRepository
	folderRepo  domain.FolderRepository
	fileRepo    domain.FileRepository
	vaultRepo   domain.VaultRepository
	settingRepo domain.SettingRepository
	gitConf     *appconfig.GitConfig
	logger      *zap.Logger
	mu          sync.Mutex
	running     map[int64]context.CancelFunc // configID -> cancelFunc
	timers      map[int64]*time.Timer        // configID -> timer
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	// pendingRedo records configIDs that asked for another run while one was already
	// in flight. The running task re-runs itself once at the end instead of letting a
	// second goroutine race on the same workspace (invariant 5).
	// pendingRedo 记录运行期间又收到同步请求的 configID：当前任务收尾后自行补跑一轮，
	// 而不是并发启动第二个 goroutine 争抢同一个工作区（不变量 5）。
	pendingRedo map[int64]bool
	// runReports carries per-run results (skipped-post warnings, remote hook receipts)
	// from doSync back to syncTask without widening doSync's signature.
	// runReports 在单次运行内从 doSync 回传结果（跳过告警、远端 hook 回执）给 syncTask，
	// 避免改动 doSync 的签名。
	runReports map[int64]*gitSyncRunReport
	// Only _test.go enables local remotes, per service instance; production always rejects them.
	// 仅 _test.go 按实例开启本地远端；生产环境始终拒绝。
	allowLocalRepoPathsForTest atomic.Bool
	// enabledCache caches, per (uid, vaultID), whether NotifyUpdated found any enabled git
	// sync config on its last DB lookup, so writes to vaults without git sync configured
	// (the common case) skip the ListByVaultID query entirely.
	// enabledCache 按 (uid, vaultID) 缓存 NotifyUpdated 上次查库时是否找到启用的 git 同步配置，
	// 使未配置 git 同步的 vault（常见情况）的写入完全跳过 ListByVaultID 查询。
	enabledCache sync.Map    // gitSyncEnabledCacheKey -> *gitSyncEnabledCacheEntry
	gcTimer      *time.Timer // Timer for delayed GC // 延迟 GC 定时器
	gcMu         sync.Mutex  // Mutex for gcTimer // 保护 gcTimer 的互斥锁
}

// NewGitSyncService creates a GitSyncService instance
// NewGitSyncService 创建 GitSyncService 实例
func NewGitSyncService(repo domain.GitSyncRepository, noteRepo domain.NoteRepository, folderRepo domain.FolderRepository, fileRepo domain.FileRepository, vaultRepo domain.VaultRepository, settingRepo domain.SettingRepository, gitConf *appconfig.GitConfig, logger *zap.Logger) GitSyncService {
	ctx, cancel := context.WithCancel(context.Background())
	return &gitSyncService{
		repo:        repo,
		noteRepo:    noteRepo,
		folderRepo:  folderRepo,
		fileRepo:    fileRepo,
		vaultRepo:   vaultRepo,
		settingRepo: settingRepo,
		gitConf:     gitConf,
		logger:      logger,
		running:     make(map[int64]context.CancelFunc),
		timers:      make(map[int64]*time.Timer),
		pendingRedo: make(map[int64]bool),
		runReports:  make(map[int64]*gitSyncRunReport),
		ctx:         ctx,
		cancel:      cancel,
	}
}

func (s *gitSyncService) domainToDTO(conf *domain.GitSyncConfig) *dto.GitSyncConfigDTO {
	if conf == nil {
		return nil
	}
	res := &dto.GitSyncConfigDTO{
		ID:                  conf.ID,
		UID:                 conf.UID,
		RepoURL:             conf.RepoURL,
		Username:            conf.Username,
		Password:            "", // Do not expose Git password to frontend / 不回显 Git 密码给前端
		Branch:              conf.Branch,
		IsEnabled:           conf.IsEnabled,
		Delay:               conf.Delay,
		RetentionDays:       conf.RetentionDays,
		LastStatus:          conf.LastStatus,
		LastMessage:         conf.LastMessage,
		IncludeConfig:       conf.IncludeConfig,
		ConfigSyncRules:     conf.ConfigSyncRules,
		SourceDir:           conf.SourceDir,
		TargetSubdir:        conf.TargetSubdir,
		RequireFrontmatter:  conf.RequireFrontmatter,
		ValidateFrontmatter: conf.ValidateFrontmatter,
		BundleAssets:        conf.BundleAssets,
		WebhookURL:          conf.WebhookURL,
		CreatedAt:           timex.Time(conf.CreatedAt),
		UpdatedAt:           timex.Time(conf.UpdatedAt),
	}
	if conf.LastSyncTime != nil {
		res.LastSyncTime = timex.Time(*conf.LastSyncTime)
	}

	// Fetch vault name if possible
	if conf.VaultID > 0 {
		v, err := s.vaultRepo.GetByID(context.Background(), conf.VaultID, conf.UID)
		if err == nil {
			res.Vault = v.Name
		}
	}

	return res
}

func (s *gitSyncService) GetConfigs(ctx context.Context, uid int64) ([]*dto.GitSyncConfigDTO, error) {
	configs, err := s.repo.List(ctx, uid)
	if err != nil {
		return nil, code.ErrorDBQuery.WithDetails(err.Error())
	}
	var res []*dto.GitSyncConfigDTO
	for _, c := range configs {
		res = append(res, s.domainToDTO(c))
	}
	return res, nil
}

func (s *gitSyncService) GetConfig(ctx context.Context, uid int64, vaultID int64) (*dto.GitSyncConfigDTO, error) {
	conf, err := s.repo.GetByVaultID(ctx, vaultID, uid)
	if err != nil {
		return nil, code.ErrorDBQuery.WithDetails(err.Error())
	}
	if conf == nil {
		return nil, code.ErrorVaultNotFound
	}
	return s.domainToDTO(conf), nil
}

func (s *gitSyncService) UpdateConfig(ctx context.Context, uid int64, params *dto.GitSyncConfigRequest) (*dto.GitSyncConfigDTO, error) {
	// Validate Repository URL security and format
	// 验证仓库 URL 的安全性和格式
	if err := s.repoURLValidator().validateRepoURL(params.RepoURL); err != nil {
		return nil, err
	}

	// 子目录映射参数先清洗校验，再落库；Webhook 地址与仓库地址走同一套协议/私网校验。
	// Normalize the subdirectory mapping up front and validate the webhook URL with the
	// same protocol/private-host rules as the repo URL.
	sourceDir, mapErr := normalizeRelativeSubdir(params.SourceDir)
	if mapErr != nil {
		return nil, code.ErrorGitSyncValidateFailed.WithDetails("sourceDir: " + mapErr.Error())
	}
	targetSubdir, mapErr := normalizeRelativeSubdir(params.TargetSubdir)
	if mapErr != nil {
		return nil, code.ErrorGitSyncValidateFailed.WithDetails("targetSubdir: " + mapErr.Error())
	}
	webhookURL := strings.TrimSpace(params.WebhookURL)
	if webhookURL != "" {
		if err := s.repoURLValidator().validateRepoURL(webhookURL); err != nil {
			return nil, code.ErrorGitSyncValidateFailed.WithDetails("webhookUrl: " + err.Error())
		}
	}

	var conf *domain.GitSyncConfig
	var err error

	if params.ID > 0 {
		conf, err = s.repo.GetByID(ctx, params.ID, uid)
		if err != nil {
			return nil, code.ErrorDBQuery.WithDetails(err.Error())
		}
		if conf == nil {
			return nil, code.ErrorGitSyncNotFound
		}
	} else {
		conf = &domain.GitSyncConfig{
			UID: uid,
		}
	}

	oldVaultID := conf.VaultID
	oldRepoURL := conf.RepoURL
	oldBranch := conf.Branch
	oldSourceDir := conf.SourceDir
	oldTargetSubdir := conf.TargetSubdir
	oldRequireFrontmatter := conf.RequireFrontmatter
	oldValidateFrontmatter := conf.ValidateFrontmatter
	oldBundleAssets := conf.BundleAssets

	if params.Vault != "" {
		v, err := s.vaultRepo.GetByName(ctx, params.Vault, uid)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, code.ErrorVaultNotFound
			}
			return nil, code.ErrorDBQuery.WithDetails(err.Error())
		}
		conf.VaultID = v.ID
	}

	conf.RepoURL = params.RepoURL
	conf.Username = params.Username
	// An empty password from the edit form preserves the stored credential.
	if params.Password != "" {
		conf.Password = params.Password
	}
	conf.Branch = params.Branch
	if conf.Branch == "" {
		conf.Branch = "main"
	}
	conf.IsEnabled = params.IsEnabled
	conf.Delay = params.Delay
	conf.RetentionDays = params.RetentionDays
	conf.IncludeConfig = params.IncludeConfig
	conf.ConfigSyncRules = params.ConfigSyncRules
	conf.SourceDir = sourceDir
	conf.TargetSubdir = targetSubdir
	conf.RequireFrontmatter = strings.TrimSpace(params.RequireFrontmatter)
	conf.ValidateFrontmatter = params.ValidateFrontmatter
	conf.BundleAssets = params.BundleAssets
	conf.WebhookURL = webhookURL

	// 仓库地址、分支或子目录映射变更后，已有工作区里残留的是旧远端/旧分支/旧映射的状态。
	// 直接复用会让下一轮同步继续按旧规则导出（service#428）。最稳的做法是丢弃工作区镜像
	// 并重置同步游标，让下一次同步在新基线上完整重新导出。工作区只是 DB 内容的缓存，
	// 删除安全；远端仓库的提交不做任何改动。
	// Repo/branch/mapping changes invalidate the cached workspace: the next sync must clone
	// the new target from scratch and re-export the full DB content (service#428).
	saved, err := s.repo.Save(ctx, conf, uid)
	if err != nil {
		return nil, code.ErrorDBQuery.WithDetails(err.Error())
	}

	if params.ID > 0 && (oldRepoURL != saved.RepoURL || oldBranch != saved.Branch ||
		oldSourceDir != saved.SourceDir || oldTargetSubdir != saved.TargetSubdir ||
		oldRequireFrontmatter != saved.RequireFrontmatter ||
		oldValidateFrontmatter != saved.ValidateFrontmatter ||
		oldBundleAssets != saved.BundleAssets) {
		s.resetWorkspaceAfterTargetChange(uid, saved.ID)
		// 重置同步游标，让下一轮同步把 DB 里现有的笔记/附件/配置完整重新导出，
		// 而不是只导出自上次游标以来的增量。
		saved.LastSyncTime = nil
		if _, err := s.repo.Save(ctx, saved, uid); err != nil {
			return nil, code.ErrorDBQuery.WithDetails(err.Error())
		}
	}

	// 使 NotifyUpdated 的 enabled-config 缓存失效，让下一次调用立即感知最新的启用状态
	// Invalidate NotifyUpdated's enabled-config cache so the next call sees the latest enabled state immediately
	s.invalidateEnabledCache(uid, oldVaultID)
	s.invalidateEnabledCache(uid, saved.VaultID)

	return s.domainToDTO(saved), nil
}

// resetWorkspaceAfterTargetChange 在仓库地址或分支变更后取消该配置的进行中同步任务，
// 等其退出，再删除本地工作区镜像。这样下一次同步会从零 PlainClone 新目标，避免旧工作区
// 的 HEAD/remote 状态把同步导向旧分支（service#428）。不动远端任何分支或提交。
// resetWorkspaceAfterTargetChange cancels the in-flight sync for the config, waits for it
// to exit, then removes the cached workspace so the next sync clones the new target.
func (s *gitSyncService) resetWorkspaceAfterTargetChange(uid, configID int64) {
	s.mu.Lock()
	cancel, running := s.running[configID]
	// 目标变更时丢弃排队的补跑：它只会在旧映射下再跑一轮，且会让下面的等待多等一整轮。
	// Drop any queued follow-up: it would re-run under the old mapping and extend the wait.
	delete(s.pendingRedo, configID)
	s.mu.Unlock()

	if running {
		cancel()
		// 等旧任务退出再删目录，避免旧 goroutine 在已删除/重建的目录里继续写。
		// 用超时兜底，防止极端情况下 UpdateConfig 被永远卡住。
		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			s.logger.Warn("Timed out waiting for in-flight git sync to stop before workspace reset",
				zap.Int64("uid", uid), zap.Int64("configId", configID))
		}
	}

	wsPath := s.getWorkspacePath(uid, configID)
	if err := os.RemoveAll(wsPath); err != nil {
		s.logger.Warn("Failed to remove git workspace after repo/branch change",
			zap.Int64("uid", uid), zap.Int64("configId", configID), zap.String("path", wsPath), zap.Error(err))
	}
}

func (s *gitSyncService) DeleteConfig(ctx context.Context, uid int64, id int64) error {
	// 1. Verification identity // 1. 验证身份
	conf, err := s.repo.GetByID(ctx, id, uid)
	if err != nil {
		return code.ErrorDBQuery.WithDetails(err.Error())
	}
	if conf == nil {
		return code.ErrorGitSyncNotFound
	}

	err = s.repo.Delete(ctx, id, uid)
	if err != nil {
		return code.ErrorDBQuery.WithDetails(err.Error())
	}

	// 使 NotifyUpdated 的 enabled-config 缓存失效，让下一次调用立即感知配置已被删除
	// Invalidate NotifyUpdated's enabled-config cache so the next call immediately sees the config is gone
	s.invalidateEnabledCache(uid, conf.VaultID)

	// Clean workspace as well? User request said "Cleanup API". Delete config doesn't necessarily mean delete workspace.
	// But usually it's better to cleanup. However, I'll follow the plan and keep them separate as per the "Cleanup API" request.

	return nil
}

func (s *gitSyncService) Validate(ctx context.Context, params *dto.GitSyncValidateRequest) error {
	// Validate Repository URL security and format
	// 验证仓库 URL 的安全性和格式
	if err := s.repoURLValidator().validateRepoURL(params.RepoURL); err != nil {
		return err
	}

	branch := params.Branch
	if branch == "" {
		branch = "main"
	}

	auth := &githttp.BasicAuth{
		Username: params.Username,
		Password: params.Password,
	}

	// Try LsRemote to validate credentials and repo visibility
	rem := git.NewRemote(nil, &config.RemoteConfig{
		Name: "origin",
		URLs: []string{params.RepoURL},
	})

	restoreClients := s.repoURLValidator().installHTTPClients()
	defer restoreClients()

	// ls-remote 不传 context 时无法被调用方取消也没有超时，远端挂起会让“验证”按钮
	// 永远停在“检测中”（haierkeys/obsidian-fast-note-sync#231）。显式带超时上下文。
	listCtx, listCancel := context.WithTimeout(ctx, gitSyncValidateTimeout)
	defer listCancel()

	refs, err := rem.ListContext(listCtx, &git.ListOptions{
		Auth: auth,
	})
	if err != nil {
		if errors.Is(err, transport.ErrEmptyRemoteRepository) {
			// Remote is empty, validation success
			return nil
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(listCtx.Err(), context.DeadlineExceeded) {
			return code.ErrorGitSyncValidateFailed.WithDetails("git ls-remote timed out after " + gitSyncValidateTimeout.String())
		}
		return code.ErrorGitSyncValidateFailed.WithDetails(err.Error())
	}

	// Check if branch exists
	branchRef := plumbing.NewBranchReferenceName(branch)
	found := false
	for _, ref := range refs {
		if ref.Name() == branchRef || ref.Name() == plumbing.HEAD {
			found = true
			break
		}
	}

	if !found {
		// Even if branch not found, if it's an empty repo (though List usually returns ErrEmptyRemoteRepository),
		// we should have caught it above. If we are here, it means refs is not empty but branch not found.
		return code.ErrorGitSyncValidateFailed.WithDetails("Branch not found in remote")
	}

	return nil
}

// validateRepoURL validates repository URLs using the configured private-host allowlist.
func validateRepoURL(rawURL string, allowedPrivateHosts []string) error {
	return newGitRepoURLValidator(allowedPrivateHosts, nil).validateRepoURL(rawURL)
}

var (
	cgnatPrefix       = netip.MustParsePrefix("100.64.0.0/10")
	thisNetworkPrefix = netip.MustParsePrefix("0.0.0.0/8")
)

// isPrivateOrLocalIP checks if net.IP is loopback, link-local, private, CGNAT (100.64.0.0/10),
// unspecified (0.0.0.0/8, ::), or multicast.
// isPrivateOrLocalIP 检查 net.IP 是否属于回环、链路本地、私网、CGNAT (100.64.0.0/10)、
// 未指定地址 (0.0.0.0/8, ::) 或多播地址。
func isPrivateOrLocalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}

	if addr, ok := netip.AddrFromSlice(ip); ok {
		addr = addr.Unmap()
		return cgnatPrefix.Contains(addr) || thisNetworkPrefix.Contains(addr)
	}
	return false
}

func (s *gitSyncService) ExecuteSync(ctx context.Context, uid int64, id int64) error {
	conf, err := s.repo.GetByID(ctx, id, uid)
	if err != nil {
		return code.ErrorDBQuery.WithDetails(err.Error())
	}
	if conf == nil {
		return code.ErrorGitSyncNotFound
	}

	// Reject invalid stored URLs before cancelling or starting any background task.
	// 在取消或启动后台任务之前，先拒绝数据库中遗留的非法仓库地址。
	if err := s.repoURLValidator().validateRepoURL(conf.RepoURL); err != nil {
		return err
	}

	// Per-config single-flight: a run that is already pushing (possibly waiting on a
	// remote post-receive build) must not be cancelled mid-flight, and a second
	// goroutine must never touch the same workspace concurrently (invariant 5).
	// Instead the request is recorded and the running task re-runs once it finishes.
	// 单飞串行：正在 push（可能还在等远端 post-receive 构建）的任务不能被中途强杀，
	// 也不允许第二个 goroutine 并发操作同一个工作区（不变量 5）。请求登记后由当前
	// 任务收尾时补跑一轮。
	s.mu.Lock()
	if _, running := s.running[id]; running {
		s.pendingRedo[id] = true
		s.mu.Unlock()
		s.logger.Info("Git sync already running, scheduling a follow-up run",
			zap.Int64("uid", uid), zap.Int64("configId", id))
		return nil
	}

	// Create context for new task
	// 为新任务创建 context
	taskCtx, taskCancel := context.WithCancel(s.ctx)
	s.running[id] = taskCancel
	s.mu.Unlock()

	s.wg.Add(1)
	// Run in background
	safego.Go(s.logger, func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, id)
			redo := s.pendingRedo[id]
			delete(s.pendingRedo, id)
			shuttingDown := s.ctx.Err() != nil
			s.mu.Unlock()
			taskCancel()

			// 补跑一轮最新增量；关停时不续跑。
			// 先登记补跑（内部会 wg.Add）再 Done，避免 WaitGroup 计数短暂归零让
			// Shutdown 提前返回。Re-run the latest increment unless shutting down;
			// the follow-up registers on the WaitGroup before this one is released.
			if redo && !shuttingDown {
				_ = s.ExecuteSync(context.Background(), uid, id)
			}
			s.wg.Done()
		}()

		// Use the newly created task context
		// 使用新创建的任务 context
		s.syncTask(taskCtx, conf)
	})

	return nil
}

// GetVaultStatuses returns the client-safe status of every git sync config bound to the
// named vault. The DTO intentionally omits RepoURL, Username, Password and WebhookURL so a
// vault-scoped plugin token cannot learn private endpoints.
// GetVaultStatuses 返回某个库绑定配置的客户端安全状态；DTO 不含仓库地址、凭据与 Webhook。
func (s *gitSyncService) GetVaultStatuses(ctx context.Context, uid int64, vaultName string) ([]*dto.GitSyncPublicStatusDTO, error) {
	vaultName = strings.TrimSpace(vaultName)
	if vaultName == "" {
		return nil, code.ErrorInvalidParams.WithDetails("vault is required")
	}
	v, err := s.vaultRepo.GetByName(ctx, vaultName, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, code.ErrorVaultNotFound
		}
		return nil, code.ErrorDBQuery.WithDetails(err.Error())
	}
	if v == nil {
		return nil, code.ErrorVaultNotFound
	}

	configs, err := s.repo.ListByVaultID(ctx, v.ID, uid)
	if err != nil {
		return nil, code.ErrorDBQuery.WithDetails(err.Error())
	}

	res := make([]*dto.GitSyncPublicStatusDTO, 0, len(configs))
	for _, conf := range configs {
		item := &dto.GitSyncPublicStatusDTO{
			ID:           conf.ID,
			Vault:        v.Name,
			Branch:       conf.Branch,
			TargetSubdir: conf.TargetSubdir,
			IsEnabled:    conf.IsEnabled,
			LastStatus:   conf.LastStatus,
			LastMessage:  conf.LastMessage,
			UpdatedAt:    timex.Time(conf.UpdatedAt),
		}
		if conf.LastSyncTime != nil {
			item.LastSyncTime = timex.Time(*conf.LastSyncTime)
		}
		res = append(res, item)
	}
	return res, nil
}

// TriggerByVault starts a manual sync for the vault's configs. configID = 0 triggers every
// enabled config bound to the vault; a non-zero id must belong to that same vault, so a
// restricted token cannot address another vault's config by id.
// TriggerByVault 手动触发库内配置；configID 为 0 时触发该库全部启用配置，非 0 时必须属于该库。
func (s *gitSyncService) TriggerByVault(ctx context.Context, uid int64, vaultName string, configID int64) error {
	vaultName = strings.TrimSpace(vaultName)
	if vaultName == "" {
		return code.ErrorInvalidParams.WithDetails("vault is required")
	}
	v, err := s.vaultRepo.GetByName(ctx, vaultName, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return code.ErrorVaultNotFound
		}
		return code.ErrorDBQuery.WithDetails(err.Error())
	}
	if v == nil {
		return code.ErrorVaultNotFound
	}

	if configID > 0 {
		conf, err := s.repo.GetByID(ctx, configID, uid)
		if err != nil {
			return code.ErrorDBQuery.WithDetails(err.Error())
		}
		if conf == nil {
			return code.ErrorGitSyncNotFound
		}
		if conf.VaultID != v.ID {
			return code.ErrorGitSyncNotFound.WithDetails("config does not belong to the requested vault")
		}
		return s.ExecuteSync(ctx, uid, conf.ID)
	}

	configs, err := s.repo.ListByVaultID(ctx, v.ID, uid)
	if err != nil {
		return code.ErrorDBQuery.WithDetails(err.Error())
	}
	triggered := 0
	for _, conf := range configs {
		if !conf.IsEnabled {
			continue
		}
		if err := s.ExecuteSync(ctx, uid, conf.ID); err != nil {
			return err
		}
		triggered++
	}
	if triggered == 0 {
		return code.ErrorGitSyncNotFound.WithDetails("no enabled git sync config for this vault")
	}
	return nil
}

func (s *gitSyncService) CleanWorkspace(ctx context.Context, uid int64, configID int64) error {
	if configID > 0 {
		// 1. Reset database fields // 1. 重置数据库字段
		conf, err := s.repo.GetByID(ctx, configID, uid)
		if err != nil {
			return code.ErrorDBQuery.WithDetails(err.Error())
		}
		if conf == nil {
			return code.ErrorGitSyncNotFound
		}

		conf.LastSyncTime = nil
		conf.LastStatus = domain.GitSyncStatusIdle
		conf.LastMessage = ""

		_, err = s.repo.Save(ctx, conf, uid)
		if err != nil {
			return code.ErrorDBQuery.WithDetails(err.Error())
		}

		// 2. Delete History // 2. 删除历史记录
		_ = s.repo.DeleteHistory(ctx, uid, configID)

		// 3. Remove physical workspace // 3. 删除物理工作区
		path := s.getWorkspacePath(uid, configID)
		err = os.RemoveAll(path)
		if err != nil {
			s.logger.Warn("Failed to cleanup physical workspace", zap.String("path", path), zap.Error(err))
		}
	} else {
		// 1. Reset all database fields for user // 1. 重置用户的所有数据库字段
		configs, err := s.repo.List(ctx, uid)
		if err != nil {
			return code.ErrorDBQuery.WithDetails(err.Error())
		}
		for _, conf := range configs {
			conf.LastSyncTime = nil
			conf.LastStatus = domain.GitSyncStatusIdle
			conf.LastMessage = ""
			_, _ = s.repo.Save(ctx, conf, uid)
		}

		// 2. Delete All History for user // 2. 删除用户的所有历史记录
		_ = s.repo.DeleteHistory(ctx, uid, 0)

		// 3. Remove all physical workspaces for user // 3. 删除用户的所有物理工作区
		path := s.getUserWorkspacePath(uid)
		err = os.RemoveAll(path)
		if err != nil {
			s.logger.Warn("Failed to cleanup user workspaces", zap.String("path", path), zap.Error(err))
		}
	}

	return nil
}

func (s *gitSyncService) ListHistory(ctx context.Context, uid int64, configID int64, pager *pkgapp.Pager) ([]*dto.GitSyncHistoryDTO, int64, error) {
	histories, count, err := s.repo.ListHistory(ctx, uid, configID, pager.Page, pager.PageSize)
	if err != nil {
		return nil, 0, code.ErrorDBQuery.WithDetails(err.Error())
	}
	var res []*dto.GitSyncHistoryDTO
	for _, h := range histories {
		res = append(res, s.historyToDTO(h))
	}
	return res, count, nil
}

func (s *gitSyncService) Shutdown(ctx context.Context) error {
	s.cancel()

	s.mu.Lock()
	for _, cancel := range s.running {
		cancel()
	}
	for _, timer := range s.timers {
		timer.Stop()
	}
	// 关停后不再补跑任何一轮 pendingRedo。
	// No pending follow-up may run after shutdown starts.
	s.pendingRedo = make(map[int64]bool)
	s.mu.Unlock()

	s.gcMu.Lock()
	if s.gcTimer != nil {
		s.gcTimer.Stop()
	}
	s.gcMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Internal methods // 内部方法

func (s *gitSyncService) historyToDTO(h *domain.GitSyncHistory) *dto.GitSyncHistoryDTO {
	if h == nil {
		return nil
	}
	return &dto.GitSyncHistoryDTO{
		ID:        h.ID,
		ConfigID:  h.ConfigID,
		StartTime: timex.Time(h.StartTime),
		EndTime:   timex.Time(h.EndTime),
		Status:    h.Status,
		Message:   h.Message,
		CreatedAt: timex.Time(h.CreatedAt),
	}
}

func (s *gitSyncService) getWorkspacePath(uid, configID int64) string {
	return filepath.Join(s.getUserWorkspacePath(uid), fmt.Sprintf("%d", configID))
}

func (s *gitSyncService) getUserWorkspacePath(uid int64) string {
	return filepath.Join("storage", "git_workspace", fmt.Sprintf("%d", uid))
}

func (s *gitSyncService) syncTask(ctx context.Context, conf *domain.GitSyncConfig) {
	startTime := time.Now()
	s.logger.Info("Starting Git sync task", zap.Int64("configId", conf.ID), zap.Int64("uid", conf.UID))

	// Record status before running to recover if there are no changes
	// 记录运行前的状态，以便无变更时恢复
	prevStatus := conf.LastStatus

	// Update Config Status to Running
	conf.LastStatus = domain.GitSyncStatusRunning
	_, _ = s.repo.Save(ctx, conf, conf.UID)

	// 每轮开始时挂一份新的运行报告，导出层与 push 层往里写告警/回执。
	// A fresh per-run report collects export warnings and remote hook receipts.
	s.setRunReport(conf.ID, &gitSyncRunReport{})

	err := s.doSync(ctx, conf)
	report := s.takeRunReport(conf.ID)

	// No changes: restore original status, trigger Save only to update updated_at
	// Without writing history, without changing last_sync_time / last_status / last_message
	// 无变更：恢复原始状态，只触发 Save 更新 updated_at
	// 不写 history，不改 last_sync_time / last_status / last_message
	if errors.Is(err, errNoChanges) {
		s.logger.Info("No changes found, skipping history and status update", zap.Int64("configId", conf.ID))
		conf.LastStatus = prevStatus
		// 没有新提交但存在被跳过的文章时，不能静默：把原因留在 LastMessage 里，
		// 否则用户会以为全部导出成功。
		// Skipped posts must stay visible even when nothing was committed.
		if report != nil && len(report.skipped) > 0 {
			conf.LastMessage = report.skipSummary()
		}
		_, _ = s.repo.Save(context.Background(), conf, conf.UID)
		return
	}

	endTime := time.Now()
	var finalStatus int64
	var message string

	if ctx.Err() != nil {
		finalStatus = domain.GitSyncStatusShutdown
		message = "Sync stopped by system shutdown"
		if err != nil {
			message += ": " + err.Error()
		}
	} else if err != nil {
		s.logger.Error("Git sync task failed", zap.Int64("configId", conf.ID), zap.Error(err))
		finalStatus = domain.GitSyncStatusFailed
		message = err.Error()
		if report != nil && len(report.skipped) > 0 {
			message += " | " + report.skipSummary()
		}
	} else {
		s.logger.Info("Git sync task success", zap.Int64("configId", conf.ID))
		finalStatus = domain.GitSyncStatusSuccess
		message = "Sync completed at " + endTime.Format("2006-01-02 15:04:05")
		if report != nil {
			if receipt := report.hookReceipt; receipt != "" {
				message += " | " + receipt
			}
			if len(report.skipped) > 0 {
				message += " | " + report.skipSummary()
			}
		}
		// 游标取本次运行开始时间：运行期间写入的笔记/附件时间戳晚于 startTime，下一轮仍会导出；
		// 取结束时间会让运行窗口内的写入永久漏导（#474）。重复导出未变内容不产生提交。
		// Cursor = run start: writes during the run are newer than startTime and get exported next run;
		// using the end time silently drops writes made while the run was in flight (#474).
		conf.LastSyncTime = &startTime
	}

	// Update Config Final Status
	conf.LastStatus = finalStatus
	conf.LastMessage = message
	_, _ = s.repo.Save(context.Background(), conf, conf.UID)

	// Create History Record
	h := &domain.GitSyncHistory{
		ConfigID:  conf.ID,
		UID:       conf.UID,
		StartTime: startTime,
		EndTime:   endTime,
		Status:    finalStatus,
		Message:   message,
	}
	_, _ = s.repo.CreateHistory(context.Background(), h, conf.UID)

	// Automatically clean up expired history records
	// 自动清理过期历史记录
	if conf.RetentionDays != 0 {
		var cutoffTime time.Time
		if conf.RetentionDays == -1 {
			// -1 means retain only the current latest record
			// -1 表示仅保留当前最新的一条记录
			cutoffTime = startTime
		} else if conf.RetentionDays > 0 {
			// > 0 means clean up records exceeding specified days
			// > 0 表示清理超过指定天数的记录
			cutoffTime = time.Now().AddDate(0, 0, -int(conf.RetentionDays))
		}

		if !cutoffTime.IsZero() {
			if err := s.repo.DeleteOldHistory(context.Background(), conf.UID, conf.ID, cutoffTime); err != nil {
				s.logger.Error("Failed to delete old git sync history", zap.Error(err), zap.Int64("configId", conf.ID))
			}
		}
	}

	// Schedule delayed memory release after task completion (addressing Issue #113)
	// Return virtual memory to OS 30 minutes after high-pressure sync ends to avoid frequent operations
	// 任务结束后调度延迟内存释放 (针对 Issue #113)
	// 高压同步结束后 30 分钟再归还虚拟内存给操作系统，避免频繁操作
	s.scheduleGC()
}

// scheduleGC schedules a delayed GC and FreeOSMemory call (debounced)
// scheduleGC 调度一个延迟的 GC 和内存释放操作（防抖）
func (s *gitSyncService) scheduleGC() {
	s.gcMu.Lock()
	defer s.gcMu.Unlock()

	if s.gcTimer != nil {
		s.gcTimer.Stop()
	}

	s.gcTimer = time.AfterFunc(30*time.Minute, func() {
		s.logger.Info("Triggering delayed background GC and memory release to OS")
		runtime.GC()
		debug.FreeOSMemory()
	})
}

func (s *gitSyncService) doSync(ctx context.Context, conf *domain.GitSyncConfig) error {
	if err := s.repoURLValidator().validateRepoURL(conf.RepoURL); err != nil {
		return err
	}
	// 传输层捕获 receive-pack 响应，用来读取 post-receive 的 sideband 回执。
	// Capture receive-pack responses at the transport layer for post-receive receipts.
	capture := &sidebandCapture{}
	validator := s.repoURLValidator()
	validator.pushCapture = capture
	restoreClients := validator.installHTTPClients()
	defer restoreClients()

	wsPath := s.getWorkspacePath(conf.UID, conf.ID)
	// 子目录映射配置非法时整轮失败：宁可不同步，也不能在错误路径上写文件。
	// An invalid subdirectory mapping fails the whole run instead of writing to a wrong path.
	if _, _, err := fireflySandbox(wsPath, conf); err != nil {
		return fmt.Errorf("invalid sync mapping: %w", err)
	}
	auth := &githttp.BasicAuth{
		Username: conf.Username,
		Password: conf.Password,
	}

	var r *git.Repository
	var err error

	// 1. Check/Init Local Repo // 1. 检查/初始化本地仓库
	if _, err := os.Stat(filepath.Join(wsPath, ".git")); os.IsNotExist(err) {
		s.logger.Info("Initializing local git repo", zap.String("path", wsPath))
		_ = os.RemoveAll(wsPath)
		r, err = git.PlainClone(wsPath, false, &git.CloneOptions{
			URL:           conf.RepoURL,
			Auth:          auth,
			ReferenceName: plumbing.NewBranchReferenceName(conf.Branch),
			SingleBranch:  true,
			Tags:          git.NoTags,
		})
		if err != nil {
			var noMatchingRef git.NoMatchingRefSpecError
			if errors.Is(err, transport.ErrEmptyRemoteRepository) || errors.Is(err, plumbing.ErrReferenceNotFound) || errors.As(err, &noMatchingRef) {
				// 远端为空，或目标分支在远端不存在（如刚切换到新分支，service#428）。
				// 本地初始化，把 HEAD 直接指到目标分支，首次提交后由显式 refspec 的 push 创建分支。
				s.logger.Info("Remote is empty or branch not found, initializing locally",
					zap.String("path", wsPath), zap.String("branch", conf.Branch))
				_ = os.RemoveAll(wsPath)
				r, err = git.PlainInit(wsPath, false)
				if err != nil {
					return fmt.Errorf("git init failed: %w", err)
				}
				_, err = r.CreateRemote(&config.RemoteConfig{
					Name: "origin",
					URLs: []string{conf.RepoURL},
				})
				if err != nil {
					return fmt.Errorf("create remote failed: %w", err)
				}
				// 把 HEAD 符号引用写到目标分支；unborn HEAD 上不能用 Worktree.Checkout。
				headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(conf.Branch))
				if err := r.Storer.SetReference(headRef); err != nil {
					return fmt.Errorf("set HEAD to target branch failed: %w", err)
				}
			} else {
				return fmt.Errorf("git clone failed: %w", err)
			}
		}
	} else {
		r, err = git.PlainOpen(wsPath)
		if err != nil {
			// Try to re-init if open fails
			_ = os.RemoveAll(wsPath)
			return s.doSync(ctx, conf)
		}
	}

	wt, err := r.Worktree()
	if err != nil {
		return err
	}

	// 2. Pull latest // 2. 拉取最新变更
	s.logger.Info("Pulling latest changes", zap.Int64("configId", conf.ID))
	err = wt.Pull(&git.PullOptions{
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(conf.Branch),
		SingleBranch:  true,
		Force:         true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		switch {
		case errors.Is(err, git.ErrNonFastForwardUpdate):
			// 本地存在未推送的提交且远端已被推进（go-git 的 Force pull 不会强对齐分叉分支）：
			// 以远端最新 head 为基线重置工作区，后续步骤会把 DB 中的变更重新导出到该基线之上，
			// 避免永远卡在 pull 失败状态（#435）。远端提交全部保留，不做强推。
			s.logger.Warn("Local workspace diverged from remote, realigning to remote head",
				zap.Int64("configId", conf.ID), zap.Error(err))
			if recErr := s.resetWorkspaceToRemoteHead(r, wt, conf.Branch); recErr != nil {
				return fmt.Errorf("git pull failed: %w (realign to remote head failed: %v)", err, recErr)
			}
		case errors.Is(err, transport.ErrEmptyRemoteRepository) || errors.Is(err, git.ErrRemoteNotFound) || errors.Is(err, plumbing.ErrReferenceNotFound):
			s.logger.Info("Remote is empty or branch not found, skipping pull", zap.Int64("configId", conf.ID))
		default:
			return fmt.Errorf("git pull failed: %w", err)
		}
	}

	// 3. Extract DB content to Workspace and commit // 3. 提取 DB 内容到工作区并提交
	committed, err := s.exportAndCommit(ctx, conf, wt, wsPath)
	if err != nil {
		return err
	}

	// 4. Push when the local branch has commits the remote lacks // 4. 本地有远端缺少的提交时推送
	// 不能只看本次镜像是否有变更：上次 push 失败会留下未推送提交，此时镜像无新变更也
	// 必须补推，否则配置永远卡在失败状态无法自愈（#435）。
	pushNeeded, err := s.localAheadOfRemote(r, conf.Branch)
	if err != nil {
		return err
	}
	if !pushNeeded {
		s.logger.Info("No notes or attachments updated, skipping Git operations", zap.Int64("configId", conf.ID), zap.Bool("changed", committed))
		// 没有新提交可推，但配置了 Webhook 时允许手动触发一次远端构建重试
		// （例如上一轮 Git 已推送成功、远端 post-receive 因待确认设置失败）。
		// No commit to push: a configured webhook still gets one controlled retry call.
		s.fireDeployWebhook(ctx, conf)
		return errNoChanges
	}

	s.logger.Info("Pushing changes", zap.Int64("configId", conf.ID))
	return s.pushWithRecovery(ctx, r, wt, conf, auth, wsPath, capture)
}

// gitSyncPushMaxAttempts bounds push retries within one sync run: the first attempt plus one
// retry after realigning the workspace to the remote head. If the remote keeps moving, the
// run fails and the next scheduled sync retries (cursor is not advanced on failure).
// gitSyncPushMaxAttempts 限制单次同步内的 push 重试次数：首次尝试加一次对齐远端后的重试。
// 若远端持续变动，本轮失败并交由下一轮同步重试（失败时游标不前进）。
const gitSyncPushMaxAttempts = 2

// exportAndCommit mirrors DB content (notes, attachments, settings) into the workspace and
// commits when the worktree is dirty. It reports whether a commit was created.
// exportAndCommit 将 DB 内容（笔记、附件、设置）镜像到工作区，工作区有变更时提交。
// 返回是否产生了新提交。
func (s *gitSyncService) exportAndCommit(ctx context.Context, conf *domain.GitSyncConfig, wt *git.Worktree, wsPath string) (bool, error) {
	if _, err := s.mirrorNotesToWorkspace(ctx, conf, wsPath, conf.LastSyncTime); err != nil {
		return false, fmt.Errorf("mirror to workspace failed: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return false, err
	}
	if status.IsClean() {
		s.logger.Info("No changes to commit", zap.Int64("configId", conf.ID))
		return false, nil
	}

	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return false, err
	}

	name := s.gitConf.Name
	if name == "" {
		name = "FNS Service"
	}
	email := s.gitConf.Email
	if email == "" {
		email = "fns@email.com"
	}

	if _, err := wt.Commit("Update from Sync Service", &git.CommitOptions{
		Author: &object.Signature{
			Name:  name,
			Email: email,
			When:  time.Now(),
		},
	}); err != nil {
		return false, err
	}
	return true, nil
}

// localAheadOfRemote reports whether the local branch HEAD differs from the remote-tracking
// ref, i.e. there are local commits the remote does not have (or the remote has no such
// branch yet). A missing local HEAD (no commits yet) means there is nothing to push.
// localAheadOfRemote 判断本地分支 HEAD 是否与远端跟踪引用不一致，即本地存在远端没有的
// 提交（或远端尚无该分支）。本地还没有任何提交时无需推送。
func (s *gitSyncService) localAheadOfRemote(r *git.Repository, branch string) (bool, error) {
	head, err := r.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return false, nil
		}
		return false, err
	}
	remoteRef, err := r.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return true, nil
		}
		return false, err
	}
	return head.Hash() != remoteRef.Hash(), nil
}

// pushWithRecovery pushes and, when the remote rejects the push as non-fast-forward (the
// remote moved between pull and push, or the push session saw a stale expected SHA), realigns
// the workspace to the freshly fetched remote head, re-exports DB content on top of it and
// retries once. It never force-pushes: remote commits are always kept.
// pushWithRecovery 执行 push；当远端以 non-fast-forward 拒绝时（pull 与 push 之间远端被推进，
// 或 push 会话拿到过时的期望 SHA），先对齐到刚抓取的远端 head、在其上重新导出 DB 内容后重试
// 一次。全程不强推：远端已有提交始终保留。
func (s *gitSyncService) pushWithRecovery(ctx context.Context, r *git.Repository, wt *git.Worktree, conf *domain.GitSyncConfig, auth transport.AuthMethod, wsPath string, capture *sidebandCapture) error {
	var err error
	for attempt := 1; attempt <= gitSyncPushMaxAttempts; attempt++ {
		// 显式指定目标分支 refspec，避免依赖本地 HEAD 与远端分支名的隐式匹配
		// （分支变更后工作区重建可能短暂 HEAD 未对齐）。普通 fast-forward push，不强推。
		refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", conf.Branch, conf.Branch))
		// Progress 捕获远端 sideband 输出（post-receive 钩子的构建回执），容量受限。
		// Progress captures the remote sideband output (post-receive build receipts), bounded.
		progress := &boundedProgressBuffer{limit: gitSyncHookOutputLimit}
		err = r.Push(&git.PushOptions{Auth: auth, RefSpecs: []config.RefSpec{refSpec}, Progress: progress})
		if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
			// 传输层捕获优先：go-git 解码在状态报告的 flush 处结束，hook 输出通常落在其后。
			// Transport capture wins: go-git stops decoding at the status flush.
			hookOutput := progress.String()
			if capture != nil {
				if text := capture.get(); text != "" {
					hookOutput += text
				}
			}
			if hookErr := s.applyRemoteHookOutput(conf.ID, hookOutput); hookErr != nil {
				return hookErr
			}
			return nil
		}
		if !isNonFastForwardPushErr(err) || attempt == gitSyncPushMaxAttempts {
			break
		}
		s.logger.Warn("Git push rejected as non-fast-forward, realigning workspace to remote head and retrying",
			zap.Int64("configId", conf.ID), zap.Int("attempt", attempt), zap.Error(err))

		inSync, recErr := s.realignAndReexport(ctx, r, wt, conf, auth, wsPath)
		if recErr != nil {
			return fmt.Errorf("git push failed: %w (realign to remote head failed: %v)", err, recErr)
		}
		if inSync {
			return nil
		}
	}
	// push 彻底失败时把远端拒绝原因摘要一并带上，便于在状态里直接看到。
	// Include the remote rejection summary so the failure is actionable from the status.
	if summary := summarizeGitError(err); summary != "" {
		return fmt.Errorf("git push failed: %s", summary)
	}
	return fmt.Errorf("git push failed: %w", err)
}

// realignAndReexport fetches the latest remote branch head, resets the workspace to it and
// re-exports DB content on top, creating a fresh commit. It reports inSync=true when the
// remote already contains the local state (or the re-export produced no changes), meaning no
// further push is needed. Remote commits are never discarded: the reset target is always the
// fetched remote head and the retry push is a regular fast-forward push.
// realignAndReexport 抓取远端分支最新 head，将工作区重置到该处并在其上重新导出 DB 内容、
// 生成新提交。当远端已包含本地状态（或重新导出后无变更）时返回 inSync=true，表示无需再推。
// 远端提交不会被丢弃：重置目标始终是抓取到的远端 head，且重试用的是普通 fast-forward push。
func (s *gitSyncService) realignAndReexport(ctx context.Context, r *git.Repository, wt *git.Worktree, conf *domain.GitSyncConfig, auth transport.AuthMethod, wsPath string) (bool, error) {
	refSpec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", conf.Branch, conf.Branch))
	if err := r.Fetch(&git.FetchOptions{Auth: auth, RefSpecs: []config.RefSpec{refSpec}, Force: true, Tags: git.NoTags}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return false, fmt.Errorf("git fetch failed: %w", err)
	}

	remoteRef, err := r.Reference(plumbing.NewRemoteReferenceName("origin", conf.Branch), true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			// 远端还没有该分支（例如新建空仓库），直接重试 push 创建分支
			return false, nil
		}
		return false, err
	}

	if head, err := r.Head(); err == nil && head.Hash() == remoteRef.Hash() {
		// 远端已包含本地提交（例如上次 push 实际成功但状态回报丢失）
		return true, nil
	}

	if err := s.resetWorkspaceToRemoteHead(r, wt, conf.Branch); err != nil {
		return false, err
	}

	committed, err := s.exportAndCommit(ctx, conf, wt, wsPath)
	if err != nil {
		return false, err
	}
	return !committed, nil
}

// resetWorkspaceToRemoteHead hard-resets the worktree to the remote-tracking head of the
// branch. The workspace is a pure mirror of DB content, so discarding local commits is safe:
// the following mirror re-exports the same DB state on top of the remote head.
// resetWorkspaceToRemoteHead 将工作区硬重置到分支的远端跟踪 head。工作区只是 DB 内容的
// 镜像，丢弃本地提交是安全的：随后的镜像会把同样的 DB 状态重新导出到远端 head 之上。
func (s *gitSyncService) resetWorkspaceToRemoteHead(r *git.Repository, wt *git.Worktree, branch string) error {
	remoteRef, err := r.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		return fmt.Errorf("resolve remote head failed: %w", err)
	}
	if err := wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: remoteRef.Hash()}); err != nil {
		return fmt.Errorf("git reset to remote head failed: %w", err)
	}
	return nil
}

// isNonFastForwardPushErr reports whether a push error means the remote refused a
// non-fast-forward update. Client-side checks return "non-fast-forward update: <ref>";
// server-side report-status messages depend on the server (git returns "cannot lock ref
// ... but expected ...").
// isNonFastForwardPushErr 判断 push 错误是否为远端拒绝 non-fast-forward 更新。客户端本地
// 校验返回 "non-fast-forward update: <ref>"；服务端 report-status 文案因服务端实现而异
// （git 返回 "cannot lock ref ... but expected ..."）。
func isNonFastForwardPushErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "cannot lock ref")
}

func (s *gitSyncService) mirrorNotesToWorkspace(ctx context.Context, conf *domain.GitSyncConfig, wsPath string, lastSyncTime *time.Time) (bool, error) {
	v, err := s.vaultRepo.GetByID(ctx, conf.VaultID, conf.UID)
	if err != nil {
		return false, err
	}
	if v == nil {
		return false, fmt.Errorf("vault not found")
	}

	var ts int64
	if lastSyncTime != nil {
		ts = lastSyncTime.UnixMilli()
		s.logger.Info("Performing incremental sync to workspace", zap.Int64("configId", conf.ID), zap.Int64("sinceTs", ts))
	} else {
		s.logger.Info("Performing initial full sync to workspace (using unified incremental method)", zap.Int64("configId", conf.ID))
	}

	var actuallyChanged bool

	for offset := 0; ; offset += gitSyncBatchSize {
		notes, err := s.noteRepo.ListByUpdatedTimestampPage(ctx, ts, v.ID, conf.UID, offset, gitSyncBatchSize)
		if err != nil {
			return false, err
		}
		if len(notes) == 0 {
			break
		}

		batchChanged, err := s.processNotesBatch(notes, conf, wsPath)
		if err != nil {
			return false, err
		}
		if batchChanged {
			actuallyChanged = true
		}

		if len(notes) < gitSyncBatchSize {
			break
		}
	}

	for offset := 0; ; offset += gitSyncBatchSize {
		files, err := s.fileRepo.ListByUpdatedTimestampPage(ctx, ts, v.ID, conf.UID, offset, gitSyncBatchSize)
		if err != nil {
			return false, err
		}
		if len(files) == 0 {
			break
		}

		batchChanged, err := s.processFilesBatch(files, conf, wsPath)
		if err != nil {
			return false, err
		}
		if batchChanged {
			actuallyChanged = true
		}

		if len(files) < gitSyncBatchSize {
			break
		}
	}

	// 子目录挂载模式下不把 .obsidian/* 等配置写到 Astro 仓库根：仓库根不属于本配置，
	// 配置导出只在无 TargetSubdir 的整库镜像模式下生效（不变量 2）。
	// With TargetSubdir set the repo root is off-limits, so config export stays disabled.
	if conf.IncludeConfig && len(conf.ConfigSyncRules) > 0 && normalizeTargetSubdirForConfig(conf) == "" {
		settingsChanged, err := s.mirrorSettingsToWorkspace(ctx, conf, wsPath, lastSyncTime)
		if err != nil {
			return false, fmt.Errorf("mirror settings to workspace failed: %w", err)
		}
		if settingsChanged {
			actuallyChanged = true
		}
	}

	return actuallyChanged, nil
}

func (s *gitSyncService) mirrorSettingsToWorkspace(ctx context.Context, conf *domain.GitSyncConfig, wsPath string, lastSyncTime *time.Time) (bool, error) {
	v, err := s.vaultRepo.GetByID(ctx, conf.VaultID, conf.UID)
	if err != nil {
		return false, err
	}
	if v == nil {
		return false, fmt.Errorf("vault not found")
	}

	var ts int64
	if lastSyncTime != nil {
		ts = lastSyncTime.UnixMilli()
	}

	var actuallyChanged bool

	// Fetch all settings for the vault
	settings, err := s.settingRepo.ListByUpdatedTimestamp(ctx, ts, v.ID, conf.UID)
	if err != nil {
		return false, err
	}

	if len(settings) == 0 {
		return false, nil
	}

	batchChanged, err := s.processSettingsBatch(settings, conf, wsPath)
	if err != nil {
		return false, err
	}
	if batchChanged {
		actuallyChanged = true
	}

	return actuallyChanged, nil
}

func (s *gitSyncService) processSettingsBatch(settings []*domain.Setting, conf *domain.GitSyncConfig, wsPath string) (bool, error) {
	var actuallyChanged bool

	for _, st := range settings {
		// Filter by rules
		matched := false
		for _, rule := range conf.ConfigSyncRules {
			if rule == "" {
				continue
			}
			// Prefix match for directories, exact match for files
			if st.Path == rule || (len(st.Path) > len(rule) && st.Path[:len(rule)] == rule && (rule[len(rule)-1] == '/' || st.Path[len(rule)] == '/')) {
				matched = true
				break
			}
		}

		if !matched {
			continue
		}

		fullPath, pathErr := safeWorkspacePath(wsPath, st.Path)
		if pathErr != nil {
			s.logger.Warn("Skipping setting whose path escapes the workspace",
				zap.String("path", st.Path), zap.Error(pathErr))
			continue
		}

		if st.Action == domain.SettingActionDelete {
			if _, err := os.Stat(fullPath); err == nil {
				_ = os.Remove(fullPath)
				actuallyChanged = true
			}
			continue
		}

		_ = os.MkdirAll(filepath.Dir(fullPath), 0755)

		// Check if content is different before writing
		if oldFile, err := os.Open(fullPath); err == nil {
			defer oldFile.Close()
			if oldContent, err := io.ReadAll(oldFile); err == nil {
				if string(oldContent) == st.Content {
					continue // Skip writing if content is identical
				}
			}
		}

		if err := os.WriteFile(fullPath, []byte(st.Content), 0644); err != nil {
			return false, fmt.Errorf("failed to write setting to workspace: %w", err)
		} else {
			actuallyChanged = true
			if st.Mtime > 0 {
				mt := time.UnixMilli(st.Mtime)
				_ = os.Chtimes(fullPath, mt, mt)
			}
		}
	}

	return actuallyChanged, nil
}

// processNotesBatch exports one page of notes into the workspace, honoring the configured
// SourceDir prefix, TargetSubdir sandbox, frontmatter filter and Astro preflight.
// processNotesBatch 导出分页笔记，遵守 SourceDir 前缀、TargetSubdir 沙箱、Frontmatter 过滤与 Astro 预检。
func (s *gitSyncService) processNotesBatch(notes []*domain.Note, conf *domain.GitSyncConfig, wsPath string) (bool, error) {
	var actuallyChanged bool

	sandboxRoot, targetSubdir, err := fireflySandbox(wsPath, conf)
	if err != nil {
		return false, fmt.Errorf("invalid sync mapping: %w", err)
	}
	// 沙箱根必须先存在：safeWorkspacePath 的符号链接校验要求工作区根可解析。
	// The sandbox root must exist: safeWorkspacePath resolves the workspace root.
	if err := os.MkdirAll(sandboxRoot, 0755); err != nil {
		return false, fmt.Errorf("create sandbox directory: %w", err)
	}

	for _, n := range notes {
		notePath := n.Path
		if filepath.Ext(notePath) == "" {
			notePath += ".md"
		}

		// 子目录挂载模式下不把 .obsidian 配置带进 Astro 内容目录（不变量 2）。
		// With a subdirectory sandbox, .obsidian config never enters the content dir.
		if targetSubdir != "" && isObsidianConfigPath(notePath) {
			continue
		}
		relToRepo, relInTarget, matched := mapSourceToTargetPath(notePath, conf.SourceDir, targetSubdir)
		if !matched {
			continue
		}
		if isProtectedFireflyPath(relToRepo) {
			s.logger.Warn("Skipping note targeting a protected repository path",
				zap.String("path", n.Path), zap.String("repoPath", relToRepo))
			continue
		}

		fullPath, pathErr := safeWorkspacePath(sandboxRoot, filepath.FromSlash(relInTarget))
		if pathErr != nil {
			s.logger.Warn("Skipping note whose path escapes the workspace",
				zap.String("path", n.Path), zap.Error(pathErr))
			continue
		}

		// 删除、或不再满足 Frontmatter 过滤（如用户移除了 type: share）：撤下已导出的文件。
		// Delete, or no longer matching the frontmatter filter: withdraw the exported file.
		if n.Action == domain.NoteActionDelete || (conf.RequireFrontmatter != "" && !matchFrontmatterFilter(n.Content, conf.RequireFrontmatter)) {
			if _, statErr := os.Stat(fullPath); statErr == nil {
				_ = os.Remove(fullPath)
				actuallyChanged = true
			}
			continue
		}

		// 先把引用附件汇聚到 ./assets/，再校验本地相对图片是否存在：刚复制过来的图片
		// 必须参与存在性检查（设计稿要求顺序）。
		// Bundle referenced assets first, then validate local images: freshly copied files
		// must participate in the existence check.
		content := n.Content
		fileExists := func(repoRel string) bool {
			return fireflyRepoFileExists(sandboxRoot, targetSubdir, repoRel)
		}
		_ = os.MkdirAll(filepath.Dir(fullPath), 0755)
		bundled, assetsChanged, bundleErr := s.bundleNoteAssets(context.Background(), conf, notePath, content, fullPath, sandboxRoot)
		if bundleErr != nil {
			s.logger.Warn("Bundling note assets failed", zap.String("path", n.Path), zap.Error(bundleErr))
		} else if assetsChanged {
			actuallyChanged = true
		}
		content = bundled

		// Astro 预检失败：跳过写入，保留工作区已有旧合法版本，并把原因记入本轮告警。
		// Preflight failure: skip the write, keep the previous valid version, record the reason.
		if conf.ValidateFrontmatter {
			targetNoteDir := path.Dir(relToRepo)
			if targetNoteDir == "." {
				targetNoteDir = ""
			}
			if valErr := validateAstroPostFrontmatter(notePath, content, targetNoteDir, fileExists); valErr != nil {
				s.logger.Warn("Skipping note that fails Astro frontmatter validation",
					zap.String("path", n.Path), zap.Error(valErr))
				s.recordSkippedPost(conf.ID, notePath, valErr.Error())
				continue
			}
		}

		// Check if content is different before writing
		if oldContent, readErr := os.ReadFile(fullPath); readErr == nil {
			if string(oldContent) == content {
				continue // Skip writing if content is identical
			}
		}

		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			return false, fmt.Errorf("failed to write note to workspace: %w", err)
		}
		actuallyChanged = true
		if n.Mtime > 0 {
			mt := time.UnixMilli(n.Mtime)
			_ = os.Chtimes(fullPath, mt, mt)
		}
	}

	return actuallyChanged, nil
}

// processFilesBatch exports one page of attachments. With TargetSubdir configured the
// attachment is mapped into the sandbox (SourceDir prefix stripped) and anything that
// would land outside it is skipped.
// processFilesBatch 导出分页附件；配置 TargetSubdir 时映射进沙箱，越界一律跳过。
func (s *gitSyncService) processFilesBatch(files []*domain.File, conf *domain.GitSyncConfig, wsPath string) (bool, error) {
	var actuallyChanged bool

	sandboxRoot, targetSubdir, err := fireflySandbox(wsPath, conf)
	if err != nil {
		return false, fmt.Errorf("invalid sync mapping: %w", err)
	}
	if err := os.MkdirAll(sandboxRoot, 0755); err != nil {
		return false, fmt.Errorf("create sandbox directory: %w", err)
	}

	for _, f := range files {
		if targetSubdir != "" && isObsidianConfigPath(f.Path) {
			continue
		}
		relToRepo, relInTarget, matched := mapSourceToTargetPath(f.Path, conf.SourceDir, targetSubdir)
		if !matched {
			continue
		}
		if isProtectedFireflyPath(relToRepo) {
			s.logger.Warn("Skipping attachment targeting a protected repository path",
				zap.String("path", f.Path), zap.String("repoPath", relToRepo))
			continue
		}

		fullPath, pathErr := safeWorkspacePath(sandboxRoot, filepath.FromSlash(relInTarget))
		if pathErr != nil {
			s.logger.Warn("Skipping attachment whose path escapes the workspace",
				zap.String("path", f.Path), zap.Error(pathErr))
			continue
		}

		if f.Action == domain.FileActionDelete {
			if _, err := os.Stat(fullPath); err == nil {
				_ = os.Remove(fullPath)
				actuallyChanged = true
			}
			continue
		}

		_ = os.MkdirAll(filepath.Dir(fullPath), 0755)

		// Add physical file existence check to prevent source not existing from causing copyFileIfDifferent error interruption
		// 增加物理文件存在性检查，防止 src 不存在导致 copyFileIfDifferent 报错中断
		if _, err := os.Stat(f.SavePath); os.IsNotExist(err) {
			s.logger.Warn("Attachment file not found in storage, skipping mirror for this file",
				zap.Int64("uid", conf.UID),
				zap.Int64("vaultId", conf.VaultID),
				zap.String("path", f.Path),
				zap.String("savePath", f.SavePath))
			continue
		}

		copyChanged, err := s.copyFileIfDifferent(f.SavePath, fullPath)
		if err != nil {
			return false, fmt.Errorf("failed to copy attachment to workspace: %w", err)
		} else if copyChanged {
			actuallyChanged = true
			if f.Mtime > 0 {
				mt := time.UnixMilli(f.Mtime)
				_ = os.Chtimes(fullPath, mt, mt)
			}
		}
	}

	return actuallyChanged, nil
}

func (s *gitSyncService) copyFileIfDifferent(src, dst string) (bool, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false, err
	}

	dstInfo, err := os.Stat(dst)
	if err == nil {
		if srcInfo.Size() == dstInfo.Size() {
			// Sizes match, we could do deep comparison, but for sync service
			// relying on size and potentially mtime/hash in DB is safer and faster.
			// Here we assume if size matches, it's likely same (simplification to avoid full read).
			// If we really need deep compare, we should use streaming hash.
			return false, nil
		}
	}

	// Streaming copy
	srcFile, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return false, err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return false, err
	}

	return true, nil
}

func (s *gitSyncService) NotifyUpdated(uid int64, vaultID int64) {
	s.logger.Debug("NotifyUpdated called", zap.Int64("uid", uid), zap.Int64("vaultID", vaultID))

	cacheKey := gitSyncEnabledCacheKey{uid: uid, vaultID: vaultID}
	if v, ok := s.enabledCache.Load(cacheKey); ok {
		entry := v.(*gitSyncEnabledCacheEntry)
		if !entry.enabled && time.Since(entry.cachedAt) < gitSyncEnabledCacheTTL {
			// 缓存命中：该 vault 近期没有启用的 git 同步配置，跳过查库
			// Cache hit: this vault had no enabled git sync config recently, skip the DB query
			return
		}
	}

	configs, err := s.repo.ListByVaultID(context.Background(), vaultID, uid)
	if err != nil {
		s.logger.Error("NotifyUpdated: failed to list configs by vaultID", zap.Int64("uid", uid), zap.Int64("vaultID", vaultID), zap.Error(err))
		return
	}

	s.logger.Debug("NotifyUpdated: found configs", zap.Int64("uid", uid), zap.Int64("vaultID", vaultID), zap.Int("count", len(configs)))

	anyEnabled := false
	for _, conf := range configs {
		if !conf.IsEnabled || conf.Delay <= 0 {
			s.logger.Debug("NotifyUpdated: skipping config", zap.Int64("configId", conf.ID), zap.Bool("isEnabled", conf.IsEnabled), zap.Int64("delay", conf.Delay))
			continue
		}
		anyEnabled = true

		s.mu.Lock()
		if timer, ok := s.timers[conf.ID]; ok {
			timer.Stop()
			s.logger.Debug("NotifyUpdated: reset existing timer", zap.Int64("configId", conf.ID))
		}

		id := conf.ID
		configUid := uid
		s.logger.Info("NotifyUpdated: scheduling delayed sync", zap.Int64("configId", id), zap.Int64("delay", conf.Delay))
		s.timers[id] = time.AfterFunc(time.Duration(conf.Delay)*time.Second, func() {
			s.mu.Lock()
			delete(s.timers, id)
			s.mu.Unlock()

			ctx := context.Background()
			_ = s.ExecuteSync(ctx, configUid, id)
		})
		s.mu.Unlock()
	}

	s.enabledCache.Store(cacheKey, &gitSyncEnabledCacheEntry{enabled: anyEnabled, cachedAt: time.Now()})
}

// invalidateEnabledCache drops the cached enabled-config state for (uid, vaultID), forcing
// the next NotifyUpdated call to re-query the DB instead of trusting a stale cached value.
// invalidateEnabledCache 清除 (uid, vaultID) 缓存的 enabled-config 状态，
// 使下一次 NotifyUpdated 调用重新查库，而不是信任过期的缓存值。
func (s *gitSyncService) invalidateEnabledCache(uid, vaultID int64) {
	if vaultID <= 0 {
		return
	}
	s.enabledCache.Delete(gitSyncEnabledCacheKey{uid: uid, vaultID: vaultID})
}
