package mcp_router

// 系统垃圾文件写入拦截（MCP 通道）：新建/覆盖/改名目标路径按开关拒收；
// 读取、删除、恢复、清理回收站工具与改名旧路径一律不拦。

import (
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newJunkMCPTestServer(enabled bool, noteSvc *svcmocks.MockNoteService, fileSvc *svcmocks.MockFileService, folderSvc *svcmocks.MockFolderService, vaultSvc *svcmocks.MockVaultService) *mcpsrv.MCPServer {
	vaultSvc.On("List", mock.Anything, mock.Anything).
		Return([]*dto.VaultDTO{{ID: 1, Name: "test-vault"}}, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{
		NoteService:   noteSvc,
		FileService:   fileSvc,
		FolderService: folderSvc,
		VaultService:  vaultSvc,
	})
	testApp.Config().App.SyncExcludeSystemJunk = &enabled
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)

	srv := mcpsrv.NewMCPServer("system-junk-test", "1.0.0")
	registerNoteTools(srv, testApp, wss)
	registerFileTools(srv, testApp, wss)
	registerFolderTools(srv, testApp, wss)
	return srv
}

func newJunkMCPMocks() (*svcmocks.MockNoteService, *svcmocks.MockFileService, *svcmocks.MockFolderService, *svcmocks.MockVaultService) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	return noteSvc, fileSvc, folderSvc, vaultSvc
}

// TestMCP_SystemJunkWriteTargetsRejected：笔记/附件的写工具在目标路径为垃圾文件时
// 返回工具错误且不进入服务层。
func TestMCP_SystemJunkWriteTargetsRejected(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"note create update", "note_create_or_update", map[string]any{"path": ".DS_Store", "content": "x"}},
		{"note create update appledouble", "note_create_or_update", map[string]any{"path": "notes/._x.md", "content": "x"}},
		{"note patch frontmatter", "note_patch_frontmatter", map[string]any{"path": "notes/Thumbs.db", "updates": "{}"}},
		{"note append", "note_append", map[string]any{"path": "desktop.ini", "content": "x"}},
		{"note prepend", "note_prepend", map[string]any{"path": "notes/._x.md", "content": "x"}},
		{"note replace", "note_replace", map[string]any{"path": ".DS_Store", "find": "a", "replace": "b"}},
		{"note rename target", "note_rename", map[string]any{"oldPath": "notes/clean.md", "newPath": "notes/Thumbs.db"}},
		{"file write", "file_write", map[string]any{"path": "img/.DS_Store", "content": ""}},
		{"file rename target", "file_rename", map[string]any{"oldPath": "img/old.png", "newPath": "img/._new.png"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noteSvc, fileSvc, folderSvc, vaultSvc := newJunkMCPMocks()
			srv := newJunkMCPTestServer(true, noteSvc, fileSvc, folderSvc, vaultSvc)

			result, err := callMCPTool(mcpTestAuthContext(), srv, tc.tool, tc.args)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.IsError, "junk path must be rejected")

			noteSvc.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			noteSvc.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything)
			fileSvc.AssertNotCalled(t, "UpdateOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			fileSvc.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// TestMCP_SystemJunkRenameOldPathAllowed：改名只看新路径，旧路径是垃圾的放行。
func TestMCP_SystemJunkRenameOldPathAllowed(t *testing.T) {
	noteSvc, fileSvc, folderSvc, vaultSvc := newJunkMCPMocks()
	var capturedNoteNew string
	noteSvc.On("Rename", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedNoteNew = args.Get(2).(*dto.NoteRenameRequest).Path
		}).
		Return(&dto.NoteDTO{Path: "notes/.DS_Store", PathHash: "oh"}, &dto.NoteDTO{Path: "notes/clean.md", PathHash: "nh"}, nil)
	srv := newJunkMCPTestServer(true, noteSvc, fileSvc, folderSvc, vaultSvc)

	result, err := callMCPTool(mcpTestAuthContext(), srv, "note_rename", map[string]any{
		"oldPath": "notes/.DS_Store", "newPath": "notes/clean",
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "junk old path must not block rename: %v", result.Content)
	assert.Equal(t, "notes/clean.md", capturedNoteNew)

	var capturedFileNew, capturedFileOld string
	fileSvc.On("Rename", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FileRenameRequest)
			capturedFileNew = params.Path
			capturedFileOld = params.OldPath
		}).
		Return(&dto.FileDTO{Path: "img/._old.png"}, &dto.FileDTO{Path: "img/clean.png"}, nil)
	result, err = callMCPTool(mcpTestAuthContext(), srv, "file_rename", map[string]any{
		"oldPath": "img/._old.png", "newPath": "img/clean.png",
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "junk old path must not block rename: %v", result.Content)
	assert.Equal(t, "img/clean.png", capturedFileNew)
	assert.Equal(t, "img/._old.png", capturedFileOld, "旧路径不做垃圾判断")
}

// TestMCP_SystemJunkDeleteStillAllowed：MCP 删除工具对垃圾路径不设限（仍可清理旧数据）。
func TestMCP_SystemJunkDeleteStillAllowed(t *testing.T) {
	noteSvc, fileSvc, folderSvc, vaultSvc := newJunkMCPMocks()
	noteSvc.On("Delete", mock.Anything, mock.Anything, mock.Anything).
		Return(&dto.NoteDTO{Path: "notes/.DS_Store"}, nil)
	fileSvc.On("Delete", mock.Anything, mock.Anything, mock.Anything).
		Return(&dto.FileDTO{Path: "img/Thumbs.db"}, nil)
	srv := newJunkMCPTestServer(true, noteSvc, fileSvc, folderSvc, vaultSvc)

	result, err := callMCPTool(mcpTestAuthContext(), srv, "note_delete", map[string]any{"path": "notes/.DS_Store"})
	require.NoError(t, err)
	require.False(t, result.IsError, "deleting a legacy junk note must still work: %v", result.Content)
	noteSvc.AssertCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)

	result, err = callMCPTool(mcpTestAuthContext(), srv, "file_delete", map[string]any{"path": "img/Thumbs.db"})
	require.NoError(t, err)
	require.False(t, result.IsError, "deleting a legacy junk file must still work: %v", result.Content)
	fileSvc.AssertCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)
}

// TestMCP_SystemJunkSwitchOffAllowsWrites：开关关闭后行为与旧版本一致。
func TestMCP_SystemJunkSwitchOffAllowsWrites(t *testing.T) {
	noteSvc, fileSvc, folderSvc, vaultSvc := newJunkMCPMocks()
	captured := ""
	noteSvc.On("ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			captured = args.Get(2).(*dto.NoteModifyOrCreateRequest).Path
		}).
		Return(false, &dto.NoteDTO{Path: "ok.md", PathHash: "h"}, nil)
	srv := newJunkMCPTestServer(false, noteSvc, fileSvc, folderSvc, vaultSvc)

	result, err := callMCPTool(mcpTestAuthContext(), srv, "note_create_or_update", map[string]any{
		"path": "notes/._x", "content": "x",
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "switch off must allow junk paths: %v", result.Content)
	assert.Equal(t, "notes/._x.md", captured)
}

// TestMCP_SystemJunkLookalikeNamesAllowed：只是包含关键字的正常文件名不受影响。
func TestMCP_SystemJunkLookalikeNamesAllowed(t *testing.T) {
	noteSvc, fileSvc, folderSvc, vaultSvc := newJunkMCPMocks()
	captured := ""
	noteSvc.On("ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			captured = args.Get(2).(*dto.NoteModifyOrCreateRequest).Path
		}).
		Return(false, &dto.NoteDTO{Path: "my._notes.md", PathHash: "h"}, nil)
	srv := newJunkMCPTestServer(true, noteSvc, fileSvc, folderSvc, vaultSvc)

	result, err := callMCPTool(mcpTestAuthContext(), srv, "note_create_or_update", map[string]any{
		"path": "my._notes.md", "content": "x",
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "lookalike names must be allowed: %v", result.Content)
	assert.Equal(t, "my._notes.md", captured)
}
