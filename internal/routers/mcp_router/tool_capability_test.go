package mcp_router

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// mcpToolCtx 构造带 uid、客户端与 scope 的工具调用上下文。
func mcpToolCtx(scope string) context.Context {
	ctx := context.WithValue(context.Background(), "uid", int64(1))
	ctx = context.WithValue(ctx, "client_type", "t")
	return context.WithValue(ctx, "scope", scope)
}

func newCapabilityTestServer(t *testing.T, noteSvc *svcmocks.MockNoteService, fileSvc *svcmocks.MockFileService, folderSvc *svcmocks.MockFolderService, vaultSvc *svcmocks.MockVaultService) *mcpsrv.MCPServer {
	t.Helper()
	vaultSvc.On("List", mock.Anything, mock.Anything).Return([]*dto.VaultDTO{{ID: 1, Name: "vault-a"}}, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{
		NoteService:   noteSvc,
		FileService:   fileSvc,
		FolderService: folderSvc,
		VaultService:  vaultSvc,
	})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	srv := mcpsrv.NewMCPServer("capability-test", "1.0.0")
	registerNoteTools(srv, testApp, wss)
	registerFileTools(srv, testApp, wss)
	registerFolderTools(srv, testApp, wss)
	registerVaultTools(srv, testApp)
	return srv
}

// #8：库级破坏工具要求专属功能点；f:* 令牌不受影响。
func TestMCPCapability_VaultToolsRequireVaultW(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	srv := newCapabilityTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)

	// note_w 令牌删除库被拒
	vaultSvc.On("Get", mock.Anything, mock.Anything, mock.Anything).Return(&dto.VaultDTO{ID: 1, Name: "vault-a"}, nil).Maybe()
	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:note_w"), srv, "vault_delete", map[string]any{"id": float64(1)})
	require.NoError(t, err)
	require.True(t, result.IsError, "note_w 不得删除库")
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "vault_w")

	// f:* 令牌可删除库
	vaultSvc.On("Delete", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
	result, err = callMCPTool(mcpToolCtx("p:mcp c:t f:*"), srv, "vault_delete", map[string]any{"id": float64(1)})
	require.NoError(t, err)
	require.False(t, result.IsError, "f:* 应可删除库")

	// note_w 令牌新建/改名库被拒
	result, err = callMCPTool(mcpToolCtx("p:mcp c:t f:note_w"), srv, "vault_create_or_update", map[string]any{"vault": "new-vault"})
	require.NoError(t, err)
	require.True(t, result.IsError, "note_w 不得新建库")
}

// vault_list / vault_get：vault_r、note_r、file_r 任一即可。
func TestMCPCapability_VaultReadAcceptsAnyReadScope(t *testing.T) {
	for _, scope := range []string{"f:vault_r", "f:note_r", "f:file_r"} {
		noteSvc := new(svcmocks.MockNoteService)
		fileSvc := new(svcmocks.MockFileService)
		folderSvc := new(svcmocks.MockFolderService)
		vaultSvc := new(svcmocks.MockVaultService)
		srv := newCapabilityTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)

		result, err := callMCPTool(mcpToolCtx("p:mcp c:t "+scope), srv, "vault_list", map[string]any{})
		require.NoError(t, err)
		require.False(t, result.IsError, "scope %s 应可列库", scope)
	}

	// 无任何读权限被拒
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	srv := newCapabilityTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:config_r"), srv, "vault_list", map[string]any{})
	require.NoError(t, err)
	require.True(t, result.IsError, "无读权限不得列库")
}

// 清空整个回收站要求 purge 能力；指定路径维持写权限。
func TestMCPCapability_RecycleClearRequiresPurgeForFullClear(t *testing.T) {
	t.Run("note full clear requires note_purge", func(t *testing.T) {
		noteSvc := new(svcmocks.MockNoteService)
		fileSvc := new(svcmocks.MockFileService)
		folderSvc := new(svcmocks.MockFolderService)
		vaultSvc := new(svcmocks.MockVaultService)
		srv := newCapabilityTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)

		result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:note_w"), srv, "note_recycle_clear", map[string]any{})
		require.NoError(t, err)
		require.True(t, result.IsError, "note_w 不得清空整个回收站")
		require.Contains(t, result.Content[0].(mcp.TextContent).Text, "note_purge")

		noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc).Maybe()
		noteSvc.On("RecycleClear", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		result, err = callMCPTool(mcpToolCtx("p:mcp c:t f:note_purge"), srv, "note_recycle_clear", map[string]any{})
		require.NoError(t, err)
		require.False(t, result.IsError, "note_purge 应可清空回收站")
	})

	t.Run("note single path stays note_w", func(t *testing.T) {
		noteSvc := new(svcmocks.MockNoteService)
		fileSvc := new(svcmocks.MockFileService)
		folderSvc := new(svcmocks.MockFolderService)
		vaultSvc := new(svcmocks.MockVaultService)
		srv := newCapabilityTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)

		noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc).Maybe()
		noteSvc.On("RecycleClear", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:note_w"), srv, "note_recycle_clear", map[string]any{"path": "a.md"})
		require.NoError(t, err)
		require.False(t, result.IsError, "note_w 应可清理指定路径")
	})

	t.Run("file full clear requires file_purge", func(t *testing.T) {
		fileSvc := new(svcmocks.MockFileService)
		noteSvc := new(svcmocks.MockNoteService)
		folderSvc := new(svcmocks.MockFolderService)
		vaultSvc := new(svcmocks.MockVaultService)
		srv := newCapabilityTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)

		result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:file_w"), srv, "file_recycle_clear", map[string]any{})
		require.NoError(t, err)
		require.True(t, result.IsError, "file_w 不得清空整个附件回收站")
		require.Contains(t, result.Content[0].(mcp.TextContent).Text, "file_purge")

		fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc).Maybe()
		fileSvc.On("RecycleClear", mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		result, err = callMCPTool(mcpToolCtx("p:mcp c:t f:file_purge"), srv, "file_recycle_clear", map[string]any{})
		require.NoError(t, err)
		require.False(t, result.IsError, "file_purge 应可清空附件回收站")
	})
}

// folder_delete 连附件一起删：note_w 与 file_w 缺一不可。
func TestMCPCapability_FolderDeleteRequiresNoteAndFileWrite(t *testing.T) {
	newSrv := func() (*mcpsrv.MCPServer, *svcmocks.MockFolderService) {
		noteSvc := new(svcmocks.MockNoteService)
		fileSvc := new(svcmocks.MockFileService)
		folderSvc := new(svcmocks.MockFolderService)
		vaultSvc := new(svcmocks.MockVaultService)
		srv := newCapabilityTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
		folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc).Maybe()
		return srv, folderSvc
	}

	srv, _ := newSrv()
	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:note_w"), srv, "folder_delete", map[string]any{"path": "dir"})
	require.NoError(t, err)
	require.True(t, result.IsError, "仅 note_w 不得递归删目录")

	srv, _ = newSrv()
	result, err = callMCPTool(mcpToolCtx("p:mcp c:t f:file_w"), srv, "folder_delete", map[string]any{"path": "dir"})
	require.NoError(t, err)
	require.True(t, result.IsError, "仅 file_w 不得递归删目录")

	srv, folderSvc := newSrv()
	folderSvc.On("DeleteTree", mock.Anything, mock.Anything, mock.Anything).Return(&dto.FolderDTO{Path: "dir"}, nil).Once()
	result, err = callMCPTool(mcpToolCtx("p:mcp c:t f:note_w,file_w"), srv, "folder_delete", map[string]any{"path": "dir"})
	require.NoError(t, err)
	require.False(t, result.IsError, "note_w + file_w 应可递归删目录")
}

// B9：无认证上下文（无 uid）时工具必须拒绝。
func TestMCPCapability_MissingAuthContextRejected(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	srv := newCapabilityTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)

	result, err := callMCPTool(context.Background(), srv, "vault_list", map[string]any{})
	require.NoError(t, err)
	require.True(t, result.IsError, "无认证上下文不得执行工具")
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "missing authenticated user context")
}
