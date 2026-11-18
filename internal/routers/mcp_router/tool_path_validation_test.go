package mcp_router

// #342 S-8 / haierkeys/obsidian-fast-note-sync#244：
// MCP 写入工具路径规则：去掉前导 "/"，按路径段（"/" 与 "\" 都算分隔符）拒绝
// 整段 ".."、空路径与 NUL，不做 URL 解码（"a..b.md"、"等等...md"、"100%.md"
// 等正常笔记名合法）。补 ".md" 只发生在产生"新路径"的地方：note_create_or_update
// 的 path 与 note_rename 的 newPath；对已有笔记操作的工具（删除/恢复/追加/前插/
// 替换/改 frontmatter/改名的旧路径）不补，否则旧版本遗留的无扩展名坏数据
// （正是 #244 产生的那批）无法删除或改名修正。
// 本文件用 mcp-go 的 HandleMessage 直接调用注册好的工具，断言服务层收到的
// Path 符合上述规则，非法路径在进入服务层前被拒。

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newMCPTestServer 构造注册了 note/file/folder 工具的 MCP 服务器。
// noteSvc 的 ModifyOrCreate 用 mock 捕获传入的 Path，用于断言清洗结果。
func newMCPTestServer(t *testing.T, noteSvc *svcmocks.MockNoteService, fileSvc *svcmocks.MockFileService, folderSvc *svcmocks.MockFolderService, vaultSvc *svcmocks.MockVaultService) *mcpsrv.MCPServer {
	t.Helper()

	vaultSvc.On("List", mock.Anything, mock.Anything).Return([]*dto.VaultDTO{{ID: 1, Name: "test-vault"}}, nil).Maybe()

	testApp := app.NewTestApp(&app.Services{
		NoteService:   noteSvc,
		FileService:   fileSvc,
		FolderService: folderSvc,
		VaultService:  vaultSvc,
	})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)

	srv := mcpsrv.NewMCPServer("path-validation-test", "1.0.0")
	registerNoteTools(srv, testApp, wss)
	registerFileTools(srv, testApp, wss)
	registerFolderTools(srv, testApp, wss)
	return srv
}

// callMCPTool 通过 JSON-RPC 调用工具并返回 CallToolResult。
func callMCPTool(ctx context.Context, srv *mcpsrv.MCPServer, name string, args map[string]any) (*mcp.CallToolResult, error) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	})
	if err != nil {
		return nil, err
	}
	resp := srv.HandleMessage(ctx, payload)
	// HandleMessage 失败时返回 JSONRPCError（非 CallToolResult）
	if errResp, ok := resp.(*mcp.JSONRPCError); ok {
		return nil, fmt.Errorf("JSONRPC error: %v", errResp.Error)
	}
	// 成功路径是 JSONRPCResponse（值类型），Result 为工具的 CallToolResult
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return nil, err
	}
	var result mcp.CallToolResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// capturedPath 捕获 ModifyOrCreate 收到的路径
type capturedPath struct{ path string }

func newNoteModifyCapture(t *testing.T, noteSvc *svcmocks.MockNoteService, capture *capturedPath) {
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	noteSvc.On("ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.NoteModifyOrCreateRequest)
			capture.path = params.Path
		}).
		Return(false, &dto.NoteDTO{Path: "ok.md", PathHash: "h"}, nil).Maybe()
}

// TestMCPNoteCreateOrUpdate_PathCleaning：前导 "/" 被去掉、缺 .md 补后缀、大小写不敏感；
// 文件名里的 ..、省略号、% 不影响合法性
func TestMCPNoteCreateOrUpdate_PathCleaning(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"leading slash stripped and .md appended", "/notes/todo", "notes/todo.md"},
		{"leading slash stripped, .md kept", "/notes/todo.md", "notes/todo.md"},
		{"uppercase .MD kept as-is", "/notes/TODO.MD", "notes/TODO.MD"},
		{"plain path with .md", "notes/todo.md", "notes/todo.md"},
		{"backslash normalized", `notes\todo.md`, "notes/todo.md"},
		{"plain path without .md", "notes/todo", "notes/todo.md"},
		{"dot-dot inside filename is legal", "a..b.md", "a..b.md"},
		{"ellipsis filename is legal", "等等...md", "等等...md"},
		{"percent filename is legal", "100%.md", "100%.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noteSvc := new(svcmocks.MockNoteService)
			fileSvc := new(svcmocks.MockFileService)
			folderSvc := new(svcmocks.MockFolderService)
			vaultSvc := new(svcmocks.MockVaultService)
			capture := &capturedPath{}
			newNoteModifyCapture(t, noteSvc, capture)

			srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
			result, err := callMCPTool(context.Background(), srv, "note_create_or_update", map[string]any{
				"path": tt.input, "content": "hello",
			})
			require.NoError(t, err)
			require.False(t, result.IsError, "unexpected tool error: %v", result.Content)
			assert.Equal(t, tt.expected, capture.path, "service must receive the cleaned path")
		})
	}
}

// TestMCPNoteCreateOrUpdate_RejectsBadPath：空/越界/NUL 路径必须被拒且不进入服务层
func TestMCPNoteCreateOrUpdate_RejectsBadPath(t *testing.T) {
	badPaths := []string{
		"",              // empty
		"../etc/passwd", // traversal
		"notes/../../x", // inner traversal
		"notes\x00.md",  // NUL byte
	}
	for _, bad := range badPaths {
		t.Run(fmt.Sprintf("reject %q", bad), func(t *testing.T) {
			noteSvc := new(svcmocks.MockNoteService)
			fileSvc := new(svcmocks.MockFileService)
			folderSvc := new(svcmocks.MockFolderService)
			vaultSvc := new(svcmocks.MockVaultService)
			// ModifyOrCreate 绝不能被调用
			noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
			noteSvc.On("ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
				Return(false, nil, fmt.Errorf("must not be called")).Times(0)

			srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
			result, err := callMCPTool(context.Background(), srv, "note_create_or_update", map[string]any{
				"path": bad, "content": "hello",
			})
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.IsError, "path %q must be rejected", bad)
			noteSvc.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// TestMCPFileWrite_PathCleaning：附件写入去掉前导 "/"，但不得补 .md
func TestMCPFileWrite_PathCleaning(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	noteSvc := new(svcmocks.MockNoteService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)

	captured := ""
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	fileSvc.On("UpdateOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FileUpdateRequest)
			captured = params.Path
		}).
		Return(false, &dto.FileDTO{Path: "images/pic.png"}, nil)

	srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
	result, err := callMCPTool(context.Background(), srv, "file_write", map[string]any{
		"path": "/images/pic.png", "content": "",
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, "images/pic.png", captured, "file_write must strip the leading '/' without appending .md")
}

// TestMCPFileWrite_RejectsBadPath：附件写入拒绝越界路径
func TestMCPFileWrite_RejectsBadPath(t *testing.T) {
	for _, bad := range []string{"../etc/passwd", "notes\x00.md"} {
		t.Run(fmt.Sprintf("reject %q", bad), func(t *testing.T) {
			fileSvc := new(svcmocks.MockFileService)
			noteSvc := new(svcmocks.MockNoteService)
			folderSvc := new(svcmocks.MockFolderService)
			vaultSvc := new(svcmocks.MockVaultService)
			fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)

			srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
			result, err := callMCPTool(context.Background(), srv, "file_write", map[string]any{
				"path": bad, "content": "",
			})
			require.NoError(t, err)
			assert.True(t, result.IsError, "path %q must be rejected", bad)
			fileSvc.AssertNotCalled(t, "UpdateOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// TestMCPFolderDelete_PathCleaning：目录删除去掉前导 "/"，拒绝越界
func TestMCPFolderDelete_PathCleaning(t *testing.T) {
	folderSvc := new(svcmocks.MockFolderService)
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	vaultSvc := new(svcmocks.MockVaultService)

	captured := ""
	folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc)
	folderSvc.On("DeleteTree", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FolderDeleteRequest)
			captured = params.Path
		}).
		Return(&dto.FolderDTO{Path: "projects"}, nil)

	srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
	result, err := callMCPTool(context.Background(), srv, "folder_delete", map[string]any{
		"path": "/projects",
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, "projects", captured)

	// 越界路径必须被拒
	result, err = callMCPTool(context.Background(), srv, "folder_delete", map[string]any{
		"path": "../projects",
	})
	require.NoError(t, err)
	assert.True(t, result.IsError, "'..' path must be rejected")
}

// TestMCPNoteRename_BothPathsCleaned：改名的旧路径只去前导 "/"（不补 .md），
// 新路径去前导 "/" 且补 ".md"——旧路径指向已存在的记录，必须按原样匹配
func TestMCPNoteRename_BothPathsCleaned(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)

	oldNote := &dto.NoteDTO{Path: "old.md", PathHash: "oh"}
	newNote := &dto.NoteDTO{Path: "new.md", PathHash: "nh"}
	var capturedOld, capturedNew string
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	noteSvc.On("Rename", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.NoteRenameRequest)
			capturedOld = params.OldPath
			capturedNew = params.Path
		}).
		Return(oldNote, newNote, nil)

	srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
	result, err := callMCPTool(context.Background(), srv, "note_rename", map[string]any{
		"oldPath": "/old", "newPath": "/sub/new",
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, "old", capturedOld, "rename 的旧路径只去前导 /，不补 .md")
	assert.Equal(t, "sub/new.md", capturedNew, "rename 的新路径补 .md")
}

// TestMCPNoteRename_ExtensionlessOldPath：旧路径无扩展名时必须原样传给服务层
// （旧版本遗留的无扩展名记录要能改名修正，补了 .md 就找不到了）
func TestMCPNoteRename_ExtensionlessOldPath(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)

	var capturedOld, capturedNew string
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	noteSvc.On("Rename", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.NoteRenameRequest)
			capturedOld = params.OldPath
			capturedNew = params.Path
		}).
		Return(&dto.NoteDTO{Path: "folder/foo"}, &dto.NoteDTO{Path: "folder/foo.md"}, nil)

	srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
	result, err := callMCPTool(context.Background(), srv, "note_rename", map[string]any{
		"oldPath": "folder/foo", "newPath": "folder/foo",
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, "folder/foo", capturedOld, "旧路径必须原样（不补 .md）")
	assert.Equal(t, "folder/foo.md", capturedNew, "新路径补 .md")
}

// TestMCPNoteTools_OtherWriteToolsValidate：追加/前插/替换/删除/恢复/回收清理/改 frontmatter 的拒绝路径
func TestMCPNoteTools_OtherWriteToolsValidate(t *testing.T) {
	tools := []string{
		"note_append", "note_prepend", "note_replace",
		"note_delete", "note_restore", "note_patch_frontmatter",
	}
	for _, tool := range tools {
		t.Run(tool+" rejects traversal", func(t *testing.T) {
			noteSvc := new(svcmocks.MockNoteService)
			fileSvc := new(svcmocks.MockFileService)
			folderSvc := new(svcmocks.MockFolderService)
			vaultSvc := new(svcmocks.MockVaultService)
			noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)

			srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
			args := map[string]any{"path": "../etc/passwd"}
			switch tool {
			case "note_append", "note_prepend":
				args["content"] = "x"
			case "note_replace":
				args["find"], args["replace"] = "a", "b"
			case "note_patch_frontmatter":
				args["updates"] = "{}"
			}
			result, err := callMCPTool(context.Background(), srv, tool, args)
			require.NoError(t, err)
			assert.True(t, result.IsError, "%s must reject traversal path", tool)
		})
	}

	// recycle_clear：路径可空（清全部），非空时校验
	t.Run("note_recycle_clear rejects traversal", func(t *testing.T) {
		noteSvc := new(svcmocks.MockNoteService)
		fileSvc := new(svcmocks.MockFileService)
		folderSvc := new(svcmocks.MockFolderService)
		vaultSvc := new(svcmocks.MockVaultService)
		noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)

		srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
		result, err := callMCPTool(context.Background(), srv, "note_recycle_clear", map[string]any{
			"path": "../../etc",
		})
		require.NoError(t, err)
		assert.True(t, result.IsError)
	})

	// file_delete / file_restore / file_recycle_clear / file_rename 同样拒绝越界
	t.Run("file tools reject traversal", func(t *testing.T) {
		fileTools := []string{"file_delete", "file_restore", "file_recycle_clear"}
		for _, tool := range fileTools {
			noteSvc := new(svcmocks.MockNoteService)
			fileSvc := new(svcmocks.MockFileService)
			folderSvc := new(svcmocks.MockFolderService)
			vaultSvc := new(svcmocks.MockVaultService)
			fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)

			srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
			result, err := callMCPTool(context.Background(), srv, tool, map[string]any{
				"path": "../etc/passwd",
			})
			require.NoError(t, err)
			assert.True(t, result.IsError, "%s must reject traversal path", tool)
		}

		// file_rename：oldPath/newPath 任一越界即拒
		noteSvc := new(svcmocks.MockNoteService)
		fileSvc := new(svcmocks.MockFileService)
		folderSvc := new(svcmocks.MockFolderService)
		vaultSvc := new(svcmocks.MockVaultService)
		fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
		srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
		result, err := callMCPTool(context.Background(), srv, "file_rename", map[string]any{
			"oldPath": "a.png", "newPath": "../b.png",
		})
		require.NoError(t, err)
		assert.True(t, result.IsError, "file_rename must reject traversal newPath")
	})
}

// TestMCPNoteDelete_ExtensionlessPath：删除"已有笔记"不补 .md——无扩展名路径
// 必须原样传给服务层（旧版本坏数据要能删）
func TestMCPNoteDelete_ExtensionlessPath(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	fileSvc := new(svcmocks.MockFileService)
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)

	captured := ""
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	noteSvc.On("Delete", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.NoteDeleteRequest)
			captured = params.Path
		}).
		Return(&dto.NoteDTO{Path: "folder/foo", PathHash: "h"}, nil)

	srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
	result, err := callMCPTool(context.Background(), srv, "note_delete", map[string]any{
		"path": "folder/foo",
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, "folder/foo", captured, "note_delete 对已有笔记不得补 .md")
}

// TestMCPNoteAppend_AcceptsOrdinaryFilenames：对已有笔记的操作接受带 ..、省略号、% 的正常文件名
func TestMCPNoteAppend_AcceptsOrdinaryFilenames(t *testing.T) {
	for _, p := range []string{"a..b.md", "等等...md", "100%.md", "To be continued...md"} {
		t.Run("accept "+p, func(t *testing.T) {
			noteSvc := new(svcmocks.MockNoteService)
			fileSvc := new(svcmocks.MockFileService)
			folderSvc := new(svcmocks.MockFolderService)
			vaultSvc := new(svcmocks.MockVaultService)

			captured := ""
			noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
			noteSvc.On("AppendContent", mock.Anything, mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) {
					params := args.Get(2).(*dto.NoteAppendRequest)
					captured = params.Path
				}).
				Return(&dto.NoteDTO{Path: p}, nil)

			srv := newMCPTestServer(t, noteSvc, fileSvc, folderSvc, vaultSvc)
			result, err := callMCPTool(context.Background(), srv, "note_append", map[string]any{
				"path": p, "content": "x",
			})
			require.NoError(t, err)
			require.False(t, result.IsError, "正常文件名 %q 不得被拒绝", p)
			assert.Equal(t, p, captured, "正常文件名 %q 必须原样传给服务层", p)
		})
	}
}
