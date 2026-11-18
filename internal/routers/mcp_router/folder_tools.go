package mcp_router

import (
	"context"
	"fmt"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
)

func registerFolderTools(srv *mcpsrv.MCPServer, appContainer *app.App, wss *pkgapp.WebsocketServer) {
	folderSvc := appContainer.FolderService
	cfg := appContainer.Config()

	toolDeleteFolder := mcp.NewTool("folder_delete",
		mcp.WithDescription("Recursively delete a folder in a vault. Requires the exact vault-relative folder path. The root folder cannot be deleted."),
		mcp.WithString("vault", mcp.Description("Vault name. Omitting this or providing 'default' will use the client-configured default vault.")),
		mcp.WithString("path", mcp.Required(), mcp.Description("Exact vault-relative folder path to delete recursively, separated by '/'; a leading '/' is stripped.")),
		mcp.WithOutputSchema[mcpFolderMutationOutput](),
	)
	srv.AddTool(writeMCPTool(toolDeleteFolder, cfg, true, "notes:write", "files:write"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// DeleteTree soft-deletes every note AND attachment under the folder,
		// so both note_w and file_w are required.
		// DeleteTree 会软删除目录下全部笔记与附件，因此需要 note_w 与 file_w。
		if err := checkPermission(ctx, "note_w"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := checkPermission(ctx, "file_w"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		uid, uidErr := requireMCPUID(ctx)
		if uidErr != nil {
			return mcp.NewToolResultError(uidErr.Error()), nil
		}
		args := getArgs(req)
		vault, _ := args["vault"].(string)
		if vault == "" || strings.EqualFold(vault, "default") {
			vault = getDefaultVaultName(ctx, appContainer)
		}
		if err := checkVaultAccess(ctx, vault); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		path, _ := args["path"].(string)
		path, cerr := cleanToolPath(path)
		if cerr != nil {
			return mcp.NewToolResultError(cerr.Error()), nil
		}
		folder, err := folderSvc.WithClient(getClientInfoFromContext(ctx)).DeleteTree(ctx, uid, &dto.FolderDeleteRequest{
			Vault:    vault,
			Path:     path,
			PathHash: util.EncodeHash32(path),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if wss != nil {
			wss.BroadcastToUser(uid, code.Success.WithData(dto.FolderSyncDeleteMessage{
				Path:             folder.Path,
				PathHash:         folder.PathHash,
				Ctime:            folder.Ctime,
				Mtime:            folder.Mtime,
				UpdatedTimestamp: folder.UpdatedTimestamp,
			}).WithVault(vault), "FolderSyncDelete")
		}
		fallback := fmt.Sprintf("Deleted folder recursively: %s", folder.Path)
		return mcp.NewToolResultStructured(mcpFolderMutationOutput{
			Vault:     vault,
			Operation: "delete",
			Folder:    folder,
		}, fallback), nil
	})
}
