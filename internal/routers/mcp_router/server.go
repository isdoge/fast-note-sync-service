package mcp_router

import (
	"context"
	"fmt"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

// requireMCPUID resolves the authenticated uid from the request context.
// Unlike the legacy helper it returns 0 instead of silently defaulting to
// uid=1 (the first user): a path that reaches a tool without authentication
// context must fail loudly instead of acting on the first user's data.
// requireMCPUID 从请求上下文解析已认证的 uid。
// 与旧实现不同，取不到时返回 0 而不是静默回退到 uid=1（首个用户）：
// 未经认证上下文却进入工具处理的路径必须显式失败，而不是操作第一个用户的数据。
func requireMCPUID(ctx context.Context) (int64, error) {
	if val := ctx.Value("uid"); val != nil {
		if uid, ok := val.(int64); ok && uid > 0 {
			return uid, nil
		}
	}
	return 0, fmt.Errorf("permission denied: missing authenticated user context")
}

// getUIDFromContext keeps the historical name for read-only lookup.
// getUIDFromContext 保留原有名称用于只读查询（无认证上下文时返回 0）。
func getUIDFromContext(ctx context.Context) int64 {
	uid, _ := requireMCPUID(ctx)
	return uid
}

func getClientInfoFromContext(ctx context.Context) (string, string, string) {
	var cType, cName, cVer string
	if val := ctx.Value("client_type"); val != nil {
		cType, _ = val.(string)
	}
	if val := ctx.Value("client_name"); val != nil {
		cName, _ = val.(string)
	}
	if val := ctx.Value("client_version"); val != nil {
		cVer, _ = val.(string)
	}
	return cType, cName, cVer
}

func getDefaultVaultName(ctx context.Context, appContainer *app.App) string {
	// 1. From context (Header X-Default-Vault-Name)
	if val := ctx.Value("default_vault_name"); val != nil {
		if name, ok := val.(string); ok && name != "" {
			return name
		}
	}

	uid := getUIDFromContext(ctx)

	// 2. From user settings (placeholder, assuming there might be a default vault setting)
	// We can try to list vaults and pick the first one as a fallback for now
	vaults, err := appContainer.VaultService.List(ctx, uid)
	if err == nil && len(vaults) > 0 {
		return vaults[0].Name
	}

	return "Default"
}

func getArgs(req mcp.CallToolRequest) map[string]interface{} {
	if req.Params.Arguments != nil {
		if args, ok := req.Params.Arguments.(map[string]interface{}); ok {
			return args
		}
	}
	return make(map[string]interface{})
}

func checkPermission(ctx context.Context, function string) error {
	scope, _ := ctx.Value("scope").(string)
	cType, _, _ := getClientInfoFromContext(ctx)
	strict, _ := ctx.Value("strict_empty_protocol").(bool)
	if !pkgapp.VerifyPermissionsWithStrictEmptyProtocol(scope, "mcp", cType, function, strict) {
		return fmt.Errorf("permission denied: %s", function)
	}
	return nil
}

// checkPermissionAny passes when at least one of the functions is permitted.
// Used by read paths that several restricted read-only token shapes must keep
// working (e.g. vault_list accepts vault_r, note_r or file_r).
// checkPermissionAny 任一功能点有权限即通过。用于多种受限只读令牌都必须
// 继续可用的读路径（例如 vault_list 接受 vault_r、note_r 或 file_r）。
func checkPermissionAny(ctx context.Context, functions ...string) error {
	for _, function := range functions {
		if err := checkPermission(ctx, function); err == nil {
			return nil
		}
	}
	return fmt.Errorf("permission denied: %s", strings.Join(functions, "|"))
}

// checkVaultAccess verifies whether the target vault is permitted by the token's vault allowlist.
// If the token has no vault restriction (vaults == ""), all vaults are allowed.
// checkVaultAccess 验证目标笔记库是否在 token 的允许列表中。
// 如果 token 没有配置 vault 限制（vaults 为空），则允许访问所有库。
func checkVaultAccess(ctx context.Context, vault string) error {
	allowedVaults, _ := ctx.Value("vaults").(string)
	if allowedVaults == "" {
		return nil
	}
	if !util.VerifyVaultAccess(allowedVaults, vault) {
		return fmt.Errorf("vault access restricted: %s", vault)
	}
	return nil
}

// checkVaultIDAccess enforces the token's vault allowlist for tools that address a
// resource by ID rather than by vault name (note_history_get/restore). The resource
// carries only its VaultID, so the vault name has to be resolved first; otherwise a
// restricted token could read or rewrite a note in another vault by guessing an ID
// (#458, same rule as the REST history handlers).
// checkVaultIDAccess 对按 ID 寻址资源（note_history_get/restore）的工具执行令牌库白名单校验。
// 资源只带 VaultID，必须先解析库名；否则受限令牌可以靠猜 ID 读取或改写其它库的笔记
// （#458，与 REST 历史处理器同一规则）。
func checkVaultIDAccess(ctx context.Context, appContainer *app.App, uid, vaultID int64) error {
	allowedVaults, _ := ctx.Value("vaults").(string)
	if allowedVaults == "" {
		return nil
	}
	if appContainer.VaultService == nil {
		return fmt.Errorf("vault access restricted: vault service unavailable")
	}
	vault, err := appContainer.VaultService.Get(ctx, uid, vaultID)
	if err != nil || vault == nil {
		return fmt.Errorf("vault access restricted: unknown vault %d", vaultID)
	}
	if !util.VerifyVaultAccess(allowedVaults, vault.Name) {
		return fmt.Errorf("vault access restricted: %s", vault.Name)
	}
	return nil
}

// cleanToolPath normalizes a vault-relative path for MCP write tools:
// it strips leading "/" and rejects empty paths, absolute paths, ".."-segment
// and NUL paths, segment-wise with no URL decoding (#342 S-8). Backslashes are
// converted to forward slashes (#451).
// cleanToolPath 为 MCP 写入工具规范化库相对路径：去掉前导 "/"，按路径段拒绝
// 空路径、绝对路径、整段 ".." 与 NUL（不做 URL 解码；#342 S-8）。
// 反斜杠统一转换为正斜杠。
func cleanToolPath(path string) (string, error) {
	cleaned, ok := util.CleanVaultPath(path)
	if !ok {
		return "", fmt.Errorf("invalid vault-relative path %q: must be relative to the vault root, separated by '/', and must not be empty, absolute, or contain a '..' segment", path)
	}
	return cleaned, nil
}

// cleanNewNotePath is for MCP tools that create a NEW note path
// (note_create_or_update's path, note_rename's newPath): it additionally
// ensures the resulting path ends in ".md" (case-insensitive) so notes synced
// to Obsidian are visible markdown files instead of extension-less files that
// later sync treats as attachments (#244). Tools operating on notes that may
// already exist (delete/restore/append/prepend/replace/frontmatter/rename's
// oldPath) must NOT append ".md": records written by older versions without
// the suffix would become unreachable for deletion or renaming.
// cleanNewNotePath 只用于会产生"新路径"的 MCP 工具（note_create_or_update 的
// path、note_rename 的 newPath）：额外保证路径以 ".md" 结尾（大小写不敏感），
// 避免无扩展名笔记同步到 Obsidian 后不可见、再同步被当成附件（#244）。
// 对"已有笔记"操作的工具（删除/恢复/追加/前插/替换/改 frontmatter/改名的旧路径）
// 不得补后缀：旧版本遗留的无扩展名记录（正是 #244 产生的坏数据）会因此无法
// 删除、无法改名修正。
func cleanNewNotePath(path string) (string, error) {
	cleaned, err := cleanToolPath(path)
	if err != nil {
		return "", err
	}
	return util.EnsureMDExt(cleaned), nil
}

// cleanWriteToolPath is cleanToolPath plus the optional system junk rejection for
// MCP tools that create or overwrite an entry. Read/delete/restore/recycle-clear
// tools and rename old paths must keep using cleanToolPath so legacy junk entries
// stay readable, deletable and fixable.
// cleanWriteToolPath 在 cleanToolPath 基础上，对新建或覆盖条目的 MCP 工具按开关
// 拒收系统垃圾文件。读取、删除、恢复、清理回收站工具以及改名的旧路径必须继续用
// cleanToolPath，以保证库里已有的垃圾条目仍可读取、删除和改名修正。
func cleanWriteToolPath(cfg *app.AppConfig, path string) (string, error) {
	cleaned, err := cleanToolPath(path)
	if err != nil {
		return "", err
	}
	if cfg != nil && cfg.App.SyncExcludeSystemJunkEnabled() && util.IsSystemJunkPath(cleaned) {
		return "", fmt.Errorf("system junk file is not synced: %s", cleaned)
	}
	return cleaned, nil
}

// cleanNewWriteNotePath is cleanNewNotePath plus the system junk rejection; it is
// for tools that create a new note path.
// cleanNewWriteNotePath 在 cleanNewNotePath 基础上拒收系统垃圾文件，用于会产生
// 新笔记路径的工具。
func cleanNewWriteNotePath(cfg *app.AppConfig, path string) (string, error) {
	cleaned, err := cleanWriteToolPath(cfg, path)
	if err != nil {
		return "", err
	}
	return util.EnsureMDExt(cleaned), nil
}

func NewMCPServer(appContainer *app.App, wss *pkgapp.WebsocketServer) *mcpsrv.MCPServer {
	// Create MCP server
	srv := mcpsrv.NewMCPServer(
		"fast-note-sync-service",
		appContainer.Version().Version,
	)

	// Note Tools
	registerNoteTools(srv, appContainer, wss)

	// File Tools
	registerFileTools(srv, appContainer, wss)

	// Folder Tools
	registerFolderTools(srv, appContainer, wss)
	registerFolderListTool(srv, appContainer)

	// Note history tools
	registerHistoryTools(srv, appContainer, wss)

	// Vault Tools
	registerVaultTools(srv, appContainer)

	// Knowledge-query tools: search, link-graph integrity, tags, recency,
	// folder listing and vault statistics.
	// 知识库查询工具：搜索、链接图完整性、标签、最近更新、目录清单与库统计。
	registerSearchTools(srv, appContainer)

	return srv
}
