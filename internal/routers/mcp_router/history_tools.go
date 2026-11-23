package mcp_router

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

// registerHistoryTools wires the note-history tools (list/get/restore). They reuse
// the REST history service and repeat its vault-allowlist checks because the tools
// address a history row by ID, which carries only a VaultID.
// registerHistoryTools 注册笔记历史工具（列表/详情/恢复）。它们复用 REST 历史服务，并重复其
// 库白名单校验——工具按 ID 寻址历史行，而行内只有 VaultID。
func registerHistoryTools(srv *mcpsrv.MCPServer, appContainer *app.App, wss *pkgapp.WebsocketServer) {
	cfg := appContainer.Config()
	historySvc := appContainer.NoteHistoryService

	// 1. note_history_list -------------------------------------------------
	toolHistoryList := mcp.NewTool("note_history_list",
		mcp.WithDescription("List the stored version history of one note, newest first, metadata only. Use it to find a version ID before calling note_history_get or note_history_restore."),
		mcp.WithString("vault", mcp.Description("Vault name. Omitting this or providing 'default' will use the client-configured default vault.")),
		mcp.WithString("path", mcp.Required(), mcp.Description("Exact vault-relative note path whose history to list.")),
		mcp.WithBoolean("isRecycle", mcp.Description("Set true to look the note up in the recycle bin (default false).")),
		mcpListPageOption(),
		mcpListPageSizeOption(),
		mcp.WithOutputSchema[dto.McpNoteHistoryListOutput](),
	)
	srv.AddTool(readOnlyMCPTool(toolHistoryList, cfg, "notes:read"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := checkPermission(ctx, "note_r"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		uid, uidErr := requireMCPUID(ctx)
		if uidErr != nil {
			return mcp.NewToolResultError(uidErr.Error()), nil
		}
		args := getArgs(req)
		vault := resolveVaultArg(ctx, appContainer, args)
		if err := checkVaultAccess(ctx, vault); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		path, _ := args["path"].(string)
		path, pathErr := cleanToolPath(path)
		if pathErr != nil {
			return mcp.NewToolResultError(pathErr.Error()), nil
		}
		isRecycle, _ := args["isRecycle"].(bool)
		pager := mcpListPager(args)

		list, total, err := historySvc.List(ctx, uid, &dto.NoteHistoryListRequest{
			Vault:     vault,
			Path:      path,
			PathHash:  util.EncodeHash32(path),
			IsRecycle: isRecycle,
		}, pager)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out := &dto.McpNoteHistoryListOutput{
			Vault:      vault,
			Path:       path,
			Count:      len(list),
			Page:       pager.Page,
			PageSize:   pager.PageSize,
			TotalCount: total,
			HasMore:    mcpListHasMore(pager, len(list), int(total)),
			Histories:  make([]*dto.McpNoteHistorySummaryDTO, 0, len(list)),
		}
		for _, h := range list {
			out.Histories = append(out.Histories, &dto.McpNoteHistorySummaryDTO{
				ID:            h.ID,
				NoteID:        h.NoteID,
				Path:          h.Path,
				Version:       h.Version,
				ClientName:    h.ClientName,
				ClientType:    h.ClientType,
				ClientVersion: h.ClientVersion,
				CreatedAt:     formatNoteTime(time.Time(h.CreatedAt)),
			})
		}
		return mcp.NewToolResultStructured(out, mcpListSummary("history versions", pager, len(list), int(total))), nil
	})

	// 2. note_history_get --------------------------------------------------
	toolHistoryGet := mcp.NewTool("note_history_get",
		mcp.WithDescription("Get the full content of one stored history version by its history ID. The version must belong to a vault this token may access."),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("History version ID from note_history_list.")),
		mcp.WithOutputSchema[dto.McpNoteHistoryOutput](),
	)
	srv.AddTool(readOnlyMCPTool(toolHistoryGet, cfg, "notes:read"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := checkPermission(ctx, "note_r"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		uid, uidErr := requireMCPUID(ctx)
		if uidErr != nil {
			return mcp.NewToolResultError(uidErr.Error()), nil
		}
		args := getArgs(req)
		id := getInt64Arg(args, "id")
		if id <= 0 {
			return mcp.NewToolResultError("id is required"), nil
		}

		history, err := historySvc.Get(ctx, uid, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// 越权防护：历史行只带 VaultID，必须解析出库名再对照令牌白名单。
		// IDOR guard: the history row only carries VaultID, resolve it and check the allowlist.
		if err := checkVaultIDAccess(ctx, appContainer, uid, history.VaultID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out := &dto.McpNoteHistoryOutput{
			Vault:   historyVaultName(ctx, appContainer, uid, history.VaultID),
			History: historyDetailDTO(history),
		}
		fallback := fmt.Sprintf("History version %d of %s (v%d)", history.ID, history.Path, history.Version)
		return mcp.NewToolResultStructured(out, fallback), nil
	})

	// 3. note_history_restore ---------------------------------------------
	toolHistoryRestore := mcp.NewTool("note_history_restore",
		mcp.WithDescription("Restore a note to the content recorded by one history version. The version must belong to a vault this token may write."),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("History version ID to restore.")),
		mcp.WithOutputSchema[mcpNoteMutationOutput](),
	)
	srv.AddTool(writeMCPTool(toolHistoryRestore, cfg, true, "notes:write"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := checkPermission(ctx, "note_w"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		uid, uidErr := requireMCPUID(ctx)
		if uidErr != nil {
			return mcp.NewToolResultError(uidErr.Error()), nil
		}
		args := getArgs(req)
		id := getInt64Arg(args, "id")
		if id <= 0 {
			return mcp.NewToolResultError("id is required"), nil
		}

		// 恢复会改写正文：先按历史所属库校验写权限范围内的库白名单。
		// Restoring rewrites the body: check the vault allowlist before touching it.
		history, err := historySvc.Get(ctx, uid, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := checkVaultIDAccess(ctx, appContainer, uid, history.VaultID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		note, err := historySvc.RestoreFromHistory(ctx, uid, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		vault := historyVaultName(ctx, appContainer, uid, history.VaultID)
		if wss != nil {
			wss.BroadcastToUser(uid, code.Success.WithData(note).WithVault(vault), "NoteSyncModify")
		}
		fallback := fmt.Sprintf("Restored %s from history version %d", note.Path, id)
		return mcp.NewToolResultStructured(mcpNoteMutationOutput{
			Vault:     vault,
			Operation: "history_restore",
			Note:      note.ToMcpNoteDTO(),
		}, fallback), nil
	})
}

// historyDetailDTO converts a history DTO into the tool's detail shape.
// historyDetailDTO 把历史 DTO 转为工具的详情结构。
func historyDetailDTO(h *dto.NoteHistoryDTO) *dto.McpNoteHistoryDetail {
	if h == nil {
		return nil
	}
	return &dto.McpNoteHistoryDetail{
		ID:            h.ID,
		NoteID:        h.NoteID,
		Path:          h.Path,
		Content:       h.Content,
		ContentHash:   h.ContentHash,
		Version:       h.Version,
		ClientName:    h.ClientName,
		ClientType:    h.ClientType,
		ClientVersion: h.ClientVersion,
		CreatedAt:     formatNoteTime(time.Time(h.CreatedAt)),
	}
}

// historyVaultName resolves a vault name for output; an unresolved ID yields "".
// historyVaultName 解析用于输出的库名；解析失败时返回空串。
func historyVaultName(ctx context.Context, appContainer *app.App, uid, vaultID int64) string {
	if appContainer.VaultService == nil {
		return ""
	}
	vault, err := appContainer.VaultService.Get(ctx, uid, vaultID)
	if err != nil || vault == nil {
		return ""
	}
	return vault.Name
}

// registerFolderListTool registers folder_list, which lists the immediate child
// folders of a vault prefix through the existing FolderService.
// registerFolderListTool 注册 folder_list，通过现有 FolderService 列出库内某前缀的直接子目录。
func registerFolderListTool(srv *mcpsrv.MCPServer, appContainer *app.App) {
	cfg := appContainer.Config()
	folderSvc := appContainer.FolderService

	toolFolderList := mcp.NewTool("folder_list",
		mcp.WithDescription("List the immediate child folders under a vault path prefix, e.g. 'projects'. Omitting path lists the vault root. Returns folder paths only; use note_list_by_prefix for the notes inside."),
		mcp.WithString("vault", mcp.Description("Vault name. Omitting this or providing 'default' will use the client-configured default vault.")),
		mcp.WithString("path", mcp.Description("Parent folder path without a leading slash. Empty or omitted lists the vault root.")),
		mcp.WithOutputSchema[dto.McpFolderListOutput](),
	)
	srv.AddTool(readOnlyMCPTool(toolFolderList, cfg, "notes:read"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := checkPermission(ctx, "note_r"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		uid, uidErr := requireMCPUID(ctx)
		if uidErr != nil {
			return mcp.NewToolResultError(uidErr.Error()), nil
		}
		args := getArgs(req)
		vault := resolveVaultArg(ctx, appContainer, args)
		if err := checkVaultAccess(ctx, vault); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		path, _ := args["path"].(string)
		path = strings.Trim(strings.ReplaceAll(path, `\`, "/"), "/")
		if path != "" {
			var pathErr error
			path, pathErr = cleanToolPath(path)
			if pathErr != nil {
				return mcp.NewToolResultError(pathErr.Error()), nil
			}
		}

		folders, err := folderSvc.WithClient(getClientInfoFromContext(ctx)).List(ctx, uid, &dto.FolderListRequest{
			Vault:    vault,
			Path:     path,
			PathHash: util.EncodeHash32(path),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out := &dto.McpFolderListOutput{
			Vault:   vault,
			Path:    path,
			Count:   len(folders),
			Folders: make([]string, 0, len(folders)),
		}
		for _, f := range folders {
			out.Folders = append(out.Folders, f.Path)
		}
		fallback := fmt.Sprintf("%d folders under '%s' in %s", out.Count, path, vault)
		return mcp.NewToolResultStructured(out, fallback), nil
	})
}
