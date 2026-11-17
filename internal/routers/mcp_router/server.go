package mcp_router

import (
	"context"
	"fmt"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

func getUIDFromContext(ctx context.Context) int64 {
	if val := ctx.Value("uid"); val != nil {
		if uid, ok := val.(int64); ok {
			return uid
		}
	}
	return 1
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
	if !pkgapp.VerifyPermissions(scope, "mcp", cType, function) {
		return fmt.Errorf("permission denied: %s", function)
	}
	return nil
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

// cleanToolPath normalizes a vault-relative path for MCP write tools:
// it strips leading "/" and rejects empty paths, absolute paths, ".." and NUL
// via util.ValidatePath (#342 S-8). Backslashes are not converted.
// cleanToolPath 为 MCP 写入工具规范化库相对路径：去掉前导 "/"，并用
// util.ValidatePath 拒绝空路径、绝对路径、".." 与 NUL（#342 S-8）。
// 反斜杠不做转换。
func cleanToolPath(path string) (string, error) {
	cleaned, ok := util.CleanVaultPath(path)
	if !ok {
		return "", fmt.Errorf("invalid vault-relative path %q: must be relative to the vault root, separated by '/', and must not be empty, absolute, or contain '..'", path)
	}
	return cleaned, nil
}

// cleanNotePath additionally ensures note paths end in ".md" (case-insensitive),
// so notes synced to Obsidian are visible as markdown files instead of landing
// as extension-less files that later sync treats as attachments (#244).
// cleanNotePath 额外保证笔记路径以 ".md" 结尾（大小写不敏感），避免无扩展名笔记
// 同步到 Obsidian 后不可见、再同步时被当成附件（#244）。
func cleanNotePath(path string) (string, error) {
	cleaned, err := cleanToolPath(path)
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

	// Vault Tools
	registerVaultTools(srv, appContainer)

	// Knowledge-query tools: search, link-graph integrity, tags, recency,
	// folder listing and vault statistics.
	// 知识库查询工具：搜索、链接图完整性、标签、最近更新、目录清单与库统计。
	registerSearchTools(srv, appContainer)

	return srv
}
