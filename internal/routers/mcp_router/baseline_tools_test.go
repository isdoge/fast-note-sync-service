package mcp_router

import (
	"context"
	"encoding/json"
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

// newBaselineMCPTestServer registers note/file/folder/history tools against mocks.
// newBaselineMCPTestServer 用 mock 注册 note/file/folder/history 工具。
func newBaselineMCPTestServer(
	t *testing.T,
	noteSvc *svcmocks.MockNoteService,
	fileSvc *svcmocks.MockFileService,
	folderSvc *svcmocks.MockFolderService,
	vaultSvc *svcmocks.MockVaultService,
	historySvc *svcmocks.MockNoteHistoryService,
) *mcpsrv.MCPServer {
	t.Helper()
	vaultSvc.On("List", mock.Anything, mock.Anything).Return([]*dto.VaultDTO{{ID: 1, Name: "vault-a"}}, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{
		NoteService:        noteSvc,
		FileService:        fileSvc,
		FolderService:      folderSvc,
		VaultService:       vaultSvc,
		NoteHistoryService: historySvc,
	})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	srv := mcpsrv.NewMCPServer("baseline-test", "1.0.0")
	registerNoteTools(srv, testApp, wss)
	registerFileTools(srv, testApp, wss)
	registerFolderTools(srv, testApp, wss)
	registerFolderListTool(srv, testApp)
	registerHistoryTools(srv, testApp, wss)
	return srv
}

// mcpToolCtxWithVaults builds a tool context with a uid, scope and vault allowlist.
// mcpToolCtxWithVaults 构造带 uid、scope 与库白名单的工具上下文。
func mcpToolCtxWithVaults(scope, vaults string) context.Context {
	ctx := context.WithValue(context.Background(), "uid", int64(1))
	ctx = context.WithValue(ctx, "client_type", "t")
	ctx = context.WithValue(ctx, "scope", scope)
	return context.WithValue(ctx, "vaults", vaults)
}

// structuredAs decodes the tool's structured content, which callMCPTool round-trips
// through JSON and therefore arrives as map[string]interface{}.
// structuredAs 解码工具的结构化内容——callMCPTool 经 JSON 往返，因此以 map 形式返回。
func structuredAs[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	var out T
	raw, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// structuredConflict decodes the conflict body from the result's text content.
// Conflict results intentionally carry no structured content, so per the MCP spec
// clients never validate it against the tool's declared output schema.
// structuredConflict 从结果的文本内容解析冲突体。冲突结果刻意不填结构化内容，
// 以免客户端按工具声明的输出结构去校验而把整个调用当成协议错误。
func structuredConflict(t *testing.T, result *mcp.CallToolResult) *dto.McpBaselineConflictDTO {
	t.Helper()
	require.NotEmpty(t, result.Content, "冲突结果应带文本内容")
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "首段内容应为文本")
	var out mcpConditionalConflictOutput
	require.NoError(t, json.Unmarshal([]byte(text.Text), &out))
	require.NotNil(t, out.Conflict)
	return out.Conflict
}

// 无条件 note_create_or_update：未传基线参数时 Baseline 必须为 nil。
func TestMCPNoteCreateOrUpdate_NoBaselineKeepsLegacy(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc).Maybe()
	var captured *dto.NoteModifyOrCreateRequest
	noteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteModifyOrCreateRequest"), false, mock.Anything).
		Run(func(args mock.Arguments) { captured = args.Get(2).(*dto.NoteModifyOrCreateRequest) }).
		Return(true, &dto.NoteDTO{Path: "a.md", Revision: "rev1"}, nil).Once()

	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:note_w"), srv, "note_create_or_update", map[string]any{
		"path": "a.md", "content": "hello",
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotNil(t, captured)
	require.Nil(t, captured.Baseline, "未传基线参数时必须保持 nil")
}

// 条件 note_create_or_update：base_revision 生效并写入 Baseline；返回带 revision。
func TestMCPNoteCreateOrUpdate_ConditionalBaseline(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc).Maybe()
	// 条件写路径不传预取 note（CAS 临界区自己读最新行）。
	var captured *dto.NoteModifyOrCreateRequest
	noteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteModifyOrCreateRequest"), false, mock.Anything).
		Run(func(args mock.Arguments) { captured = args.Get(2).(*dto.NoteModifyOrCreateRequest) }).
		Return(false, &dto.NoteDTO{Path: "a.md", Revision: "rev2"}, nil).Once()

	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:note_w"), srv, "note_create_or_update", map[string]any{
		"path": "a.md", "content": "hello", "base_revision": "rev1", "base_hash": "h1",
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotNil(t, captured)
	require.NotNil(t, captured.Baseline)
	require.Equal(t, "rev1", captured.Baseline.BaseRevision)
	require.True(t, captured.Baseline.HasBase)
	require.Equal(t, "h1", captured.Baseline.BaseHash)

	out := structuredAs[mcpNoteMutationOutput](t, result)
	require.Equal(t, "rev2", out.Note.Revision)
}

// 条件写冲突：IsError=true 且结构化冲突体含 conflict/reason/serverRevision/serverHash。
func TestMCPNoteCreateOrUpdate_ConflictStructured(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc).Maybe()
	noteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteModifyOrCreateRequest"), false, mock.Anything).
		Return(false, (*dto.NoteDTO)(nil), &dto.BaselineConflictError{
			Reason:         dto.SyncConflictReasonBothModified,
			ServerActive:   true,
			ServerRevision: "rev-server",
			ServerHash:     "h-server",
		}).Once()

	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:note_w"), srv, "note_create_or_update", map[string]any{
		"path": "a.md", "content": "hello", "base_revision": "rev-stale",
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	conflict := structuredConflict(t, result)
	require.True(t, conflict.Conflict)
	require.Equal(t, dto.SyncConflictReasonBothModified, conflict.Reason)
	require.Equal(t, "rev-server", conflict.ServerRevision)
	require.Equal(t, "h-server", conflict.ServerHash)
}

// 条件 file_delete 冲突：kind=file，结构化冲突体。
func TestMCPFileDelete_ConflictStructured(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc).Maybe()
	fileSvc.On("Delete", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileDeleteRequest")).
		Return((*dto.FileDTO)(nil), &dto.BaselineConflictError{
			Reason:         dto.SyncConflictReasonLocalDeleted,
			ServerActive:   true,
			ServerRevision: "frev-live",
		}).Once()

	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:file_w"), srv, "file_delete", map[string]any{
		"path": "a.png", "base_revision": "frev-stale",
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	conflict := structuredConflict(t, result)
	require.Equal(t, dto.SyncConflictKindFile, conflict.Kind)
	require.Equal(t, "frev-live", conflict.ServerRevision)
}

// folder_list 列出子目录，返回路径列表。
func TestMCPFolderList_ReturnsChildPaths(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc).Maybe()
	folderSvc.On("List", mock.Anything, int64(1), mock.AnythingOfType("*dto.FolderListRequest")).
		Return([]*dto.FolderDTO{{Path: "projects/a"}, {Path: "projects/b"}}, nil).Once()

	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:note_r"), srv, "folder_list", map[string]any{"path": "projects"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	out := structuredAs[dto.McpFolderListOutput](t, result)
	require.Equal(t, 2, out.Count)
	require.Equal(t, []string{"projects/a", "projects/b"}, out.Folders)
}

// note_history_list 复用历史服务并返回版本列表。
func TestMCPNoteHistoryList(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	historySvc.On("List", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteHistoryListRequest"), mock.Anything).
		Return([]*dto.NoteHistoryNoContentDTO{{ID: 7, NoteID: 3, Path: "a.md", Version: 2}}, int64(1), nil).Once()

	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:note_r"), srv, "note_history_list", map[string]any{"path": "a.md"})
	require.NoError(t, err)
	require.False(t, result.IsError)
	out := structuredAs[dto.McpNoteHistoryListOutput](t, result)
	require.Equal(t, 1, out.Count)
	require.EqualValues(t, 1, out.TotalCount)
	require.Equal(t, int64(7), out.Histories[0].ID)
}

// note_history_get 越权防护：历史属于白名单外的库时必须拒绝，且不泄露正文。
func TestMCPNoteHistoryGet_CrossVaultRejected(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	historySvc.On("Get", mock.Anything, int64(1), int64(7)).
		Return(&dto.NoteHistoryDTO{ID: 7, VaultID: 2, Path: "secret.md", Content: "secret"}, nil).Once()
	vaultSvc.On("Get", mock.Anything, int64(1), int64(2)).
		Return(&dto.VaultDTO{ID: 2, Name: "vault-b"}, nil).Once()

	result, err := callMCPTool(mcpToolCtxWithVaults("p:mcp c:t f:note_r", "vault-a"), srv, "note_history_get", map[string]any{"id": float64(7)})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "vault access restricted")
}

// note_history_restore 越权防护：历史属于白名单外的库时必须拒绝，且不执行恢复。
func TestMCPNoteHistoryRestore_CrossVaultRejected(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	historySvc.On("Get", mock.Anything, int64(1), int64(7)).
		Return(&dto.NoteHistoryDTO{ID: 7, VaultID: 2, Path: "secret.md"}, nil).Once()
	vaultSvc.On("Get", mock.Anything, int64(1), int64(2)).
		Return(&dto.VaultDTO{ID: 2, Name: "vault-b"}, nil).Once()

	result, err := callMCPTool(mcpToolCtxWithVaults("p:mcp c:t f:note_w", "vault-a"), srv, "note_history_restore", map[string]any{"id": float64(7)})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "vault access restricted")
	historySvc.AssertNotCalled(t, "RestoreFromHistory", mock.Anything, mock.Anything, mock.Anything)
}

// note_history_restore 正常路径：同库允许恢复并返回 note。
func TestMCPNoteHistoryRestore_SameVault(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	historySvc.On("Get", mock.Anything, int64(1), int64(7)).
		Return(&dto.NoteHistoryDTO{ID: 7, VaultID: 1, Path: "a.md"}, nil).Once()
	vaultSvc.On("Get", mock.Anything, int64(1), int64(1)).
		Return(&dto.VaultDTO{ID: 1, Name: "vault-a"}, nil).Maybe()
	historySvc.On("RestoreFromHistory", mock.Anything, int64(1), int64(7)).
		Return(&dto.NoteDTO{Path: "a.md", Revision: "rev-restored"}, nil).Once()

	result, err := callMCPTool(mcpToolCtxWithVaults("p:mcp c:t f:note_w", "vault-a"), srv, "note_history_restore", map[string]any{"id": float64(7)})
	require.NoError(t, err)
	require.False(t, result.IsError)
	out := structuredAs[mcpNoteMutationOutput](t, result)
	require.Equal(t, "history_restore", out.Operation)
	require.Equal(t, "rev-restored", out.Note.Revision)
}

// 历史工具无 note_r/note_w 权限时必须拒绝。
func TestMCPHistoryTools_RequireScope(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	result, err := callMCPTool(mcpToolCtx("p:mcp c:t f:config_r"), srv, "note_history_list", map[string]any{"path": "a.md"})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "note_r")

	result, err = callMCPTool(mcpToolCtx("p:mcp c:t f:note_r"), srv, "note_history_restore", map[string]any{"id": float64(1)})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "note_w")
}

// 删除工具的条件删除必须同时带 base_revision 与 base_hash，且不再声明 base_hash_missing；
// 写入工具仍保留 base_hash_missing。
// Delete tools require both base_revision and base_hash and no longer declare
// base_hash_missing; write tools keep base_hash_missing.
func TestMCPDeleteTools_BaselineArgsMatchContract(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	srv := newBaselineMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc, historySvc)

	for _, name := range []string{"note_delete", "file_delete"} {
		tool := srv.GetTool(name)
		require.NotNil(t, tool, "工具 %s 必须已注册", name)
		props := tool.Tool.InputSchema.Properties
		require.NotContains(t, props, "base_hash_missing", "删除工具 %s 不应声明 base_hash_missing", name)
		require.Contains(t, props, "base_revision", "删除工具 %s 必须声明 base_revision", name)
		require.Contains(t, props, "base_hash", "删除工具 %s 必须声明 base_hash", name)
	}

	// 写入工具仍保留 base_hash_missing。
	// Write tools keep base_hash_missing.
	for _, name := range []string{"note_create_or_update", "file_write"} {
		tool := srv.GetTool(name)
		require.NotNil(t, tool, "工具 %s 必须已注册", name)
		props := tool.Tool.InputSchema.Properties
		require.Contains(t, props, "base_hash_missing", "写入工具 %s 必须保留 base_hash_missing", name)
		require.Contains(t, props, "base_revision", "写入工具 %s 必须声明 base_revision", name)
		require.Contains(t, props, "base_hash", "写入工具 %s 必须声明 base_hash", name)
	}
}

// 条件写字段只在显式传入时生效：空 base_revision 不触发条件写。
func TestMCPBaselineFromArgs_EmptyIsNil(t *testing.T) {
	require.Nil(t, mcpBaselineFromArgs(map[string]interface{}{}))
	require.Nil(t, mcpBaselineFromArgs(map[string]interface{}{"base_revision": ""}))
	require.Nil(t, mcpBaselineFromArgs(map[string]interface{}{"base_revision": "", "base_hash": ""}))
	commit := mcpBaselineFromArgs(map[string]interface{}{"base_revision": "r1"})
	require.NotNil(t, commit)
	require.Equal(t, "r1", commit.BaseRevision)
	require.False(t, commit.HasBase)
}
