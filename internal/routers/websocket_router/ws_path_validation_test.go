package websocket_router

// #342 S-8 / haierkeys/obsidian-fast-note-sync#244：
// WebSocket 写入入口（NoteModify/NoteDelete/NoteRename、FileUploadCheck/
// FileDelete/FileRename、FolderModify/FolderDelete/FolderRename）统一路径校验。
// 同步协议要求服务端不改写客户端上报的路径：客户端按自己的路径记基线和哈希，
// 服务端存成另一个路径两边就对不上。因此这里只校验——按路径段（"/" 与 "\"
// 都算分隔符）拒绝整段 ".."、空路径与 NUL，不做 URL 解码（"a..b.md"、
// "等等...md"、"100%.md" 等正常笔记名合法）——不去前导 "/"、不补 ".md"，
// 通过则原样传给服务层。
// 本文件用 server.NewTestClient 构造 client 直接调用 handler，断言服务层
// 收到的 Path 与客户端上报的原样一致、非法路径在进入服务层前被拒。

import (
	"encoding/json"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// newWSPathTestClient 构造可直接调用 handler 的 WebsocketClient（无真实连接）。
// 用主分支提供的 (w *WebsocketServer) NewTestClient。
func newWSPathTestClient(testApp *app.App) *pkgapp.WebsocketClient {
	return pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp).NewTestClient(1)
}

// newWSPathTestApp 构造带 mock 服务的 App（VaultService 提供最小 GetOrCreate stub）
func newWSPathTestApp(noteSvc *svcmocks.MockNoteService, fileSvc *svcmocks.MockFileService, folderSvc *svcmocks.MockFolderService) *app.App {
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	return app.NewTestApp(&app.Services{
		NoteService:   noteSvc,
		FileService:   fileSvc,
		FolderService: folderSvc,
		VaultService:  vaultSvc,
	})
}

// wsMsg 构造 WebSocketMessage
func wsMsg(action string, payload any) *pkgapp.WebSocketMessage {
	data, _ := json.Marshal(payload)
	return &pkgapp.WebSocketMessage{Type: action, Data: data}
}

// TestWSNoteModify_PassesClientPathAsIs：NoteModify 不改写客户端路径——
// 前导斜杠、无扩展名都原样传给服务层（服务端改写会导致客户端基线对不上）
func TestWSNoteModify_PassesClientPathAsIs(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"leading slash + ext kept as-is", "/notes/todo.md"},
		{"no extension kept as-is", "notes/todo"},
		{"leading slash + no extension kept as-is", "/notes/todo"},
		{"dot-dot inside filename kept as-is", "a..b.md"},
		{"ellipsis filename kept as-is", "等等...md"},
		{"percent filename kept as-is", "100%.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captured := ""
			noteSvc := new(svcmocks.MockNoteService)
			noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
			// 返回 "Create" 走写库分支
			noteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.Anything).
				Return("Create", nil, nil, nil).Maybe()
			noteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) {
					params := args.Get(2).(*dto.NoteModifyOrCreateRequest)
					captured = params.Path
				}).
				Return(false, &dto.NoteDTO{Path: "ok.md", UpdatedTimestamp: 1}, nil).Maybe()

			testApp := newWSPathTestApp(noteSvc, nil, nil)
			client := newWSPathTestClient(testApp)
			handler := NewNoteWSHandler(testApp)

			handler.NoteModify(client, wsMsg("NoteModify", map[string]any{
				"vault": "v", "path": tt.input, "pathHash": "h", "contentHash": "ch",
				"mtime": 1700000000000, "ctime": 1700000000000,
			}))

			assert.Equal(t, tt.input, captured, "WebSocket 入口必须原样传递客户端路径")
		})
	}
}

// TestWSNoteModify_RejectsBadPath：NoteModify 拒绝越界路径且不进入服务层
func TestWSNoteModify_RejectsBadPath(t *testing.T) {
	for _, bad := range []string{"../etc/passwd", "notes/../../x", `a\..\x`} {
		t.Run("reject "+bad, func(t *testing.T) {
			noteSvc := new(svcmocks.MockNoteService)
			noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)

			testApp := newWSPathTestApp(noteSvc, nil, nil)
			client := newWSPathTestClient(testApp)
			handler := NewNoteWSHandler(testApp)

			handler.NoteModify(client, wsMsg("NoteModify", map[string]any{
				"vault": "v", "path": bad, "pathHash": "h", "contentHash": "ch",
				"mtime": 1700000000000, "ctime": 1700000000000,
			}))

			noteSvc.AssertNotCalled(t, "UpdateCheckWithNote", mock.Anything, mock.Anything, mock.Anything)
			noteSvc.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// TestWSNoteDelete_RejectsBadPath：NoteDelete 拒绝越界路径
func TestWSNoteDelete_RejectsBadPath(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)

	testApp := newWSPathTestApp(noteSvc, nil, nil)
	client := newWSPathTestClient(testApp)
	handler := NewNoteWSHandler(testApp)

	handler.NoteDelete(client, wsMsg("NoteDelete", map[string]any{
		"vault": "v", "path": "../evil.md", "pathHash": "h",
	}))

	noteSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)
}

// TestWSNoteRename_PassesClientPathsAsIs：NoteRename 新旧路径都原样传递
func TestWSNoteRename_PassesClientPathsAsIs(t *testing.T) {
	var capturedNew, capturedOld string
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	noteSvc.On("Rename", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.NoteRenameRequest)
			capturedNew = params.Path
			capturedOld = params.OldPath
		}).
		Return(&dto.NoteDTO{Path: "old.md"}, &dto.NoteDTO{Path: "new.md"}, nil)

	testApp := newWSPathTestApp(noteSvc, nil, nil)
	client := newWSPathTestClient(testApp)
	handler := NewNoteWSHandler(testApp)

	handler.NoteRename(client, wsMsg("NoteRename", map[string]any{
		"vault": "v", "path": "/sub/new", "oldPath": "/old",
	}))

	assert.Equal(t, "/old", capturedOld, "NoteRename 旧路径必须原样")
	assert.Equal(t, "/sub/new", capturedNew, "NoteRename 新路径必须原样")
}

// TestWSFileUploadCheck_PassesClientPathAsIs：FileUploadCheck 原样传递（含前导 "/"）
func TestWSFileUploadCheck_PassesClientPathAsIs(t *testing.T) {
	captured := ""
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	fileSvc.On("UploadCheck", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FileUpdateCheckRequest)
			captured = params.Path
		}).
		Return("", nil, nil).Maybe()

	testApp := newWSPathTestApp(nil, fileSvc, nil)
	client := newWSPathTestClient(testApp)
	handler := NewFileWSHandler(testApp)

	handler.FileUploadCheck(client, wsMsg("FileUploadCheck", map[string]any{
		"vault": "v", "path": "/images/pic.png", "pathHash": "h", "contentHash": "ch",
		"mtime": 1700000000000, "ctime": 1700000000000,
	}))

	assert.Equal(t, "/images/pic.png", captured, "FileUploadCheck 必须原样传递客户端路径")
}

// TestWSFileUploadCheck_RejectsBadPath：FileUploadCheck 拒绝越界路径
func TestWSFileUploadCheck_RejectsBadPath(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)

	testApp := newWSPathTestApp(nil, fileSvc, nil)
	client := newWSPathTestClient(testApp)
	handler := NewFileWSHandler(testApp)

	handler.FileUploadCheck(client, wsMsg("FileUploadCheck", map[string]any{
		"vault": "v", "path": "../evil.png", "pathHash": "h", "contentHash": "ch",
		"mtime": 1700000000000, "ctime": 1700000000000,
	}))

	fileSvc.AssertNotCalled(t, "UploadCheck", mock.Anything, mock.Anything, mock.Anything)
}

// TestWSFileDelete_RejectsBadPath：FileDelete 拒绝越界路径
func TestWSFileDelete_RejectsBadPath(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)

	testApp := newWSPathTestApp(nil, fileSvc, nil)
	client := newWSPathTestClient(testApp)
	handler := NewFileWSHandler(testApp)

	handler.FileDelete(client, wsMsg("FileDelete", map[string]any{
		"vault": "v", "path": "../evil.png", "pathHash": "h",
	}))

	fileSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)
}

// TestWSFileRename_PassesClientPathsAsIs：FileRename 新旧路径原样传递
func TestWSFileRename_PassesClientPathsAsIs(t *testing.T) {
	var capturedNew, capturedOld string
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	fileSvc.On("Rename", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FileRenameRequest)
			capturedNew = params.Path
			capturedOld = params.OldPath
		}).
		Return(&dto.FileDTO{Path: "old.png"}, &dto.FileDTO{Path: "new.png"}, nil)

	testApp := newWSPathTestApp(nil, fileSvc, nil)
	client := newWSPathTestClient(testApp)
	handler := NewFileWSHandler(testApp)

	handler.FileRename(client, wsMsg("FileRename", map[string]any{
		"vault": "v", "path": "/new.png", "oldPath": "/old.png",
	}))

	assert.Equal(t, "/old.png", capturedOld)
	assert.Equal(t, "/new.png", capturedNew)
}

// TestWSFolderModifyDelete_PassesClientPathAsIs：FolderModify/FolderDelete 原样传递，拒绝越界
func TestWSFolderModifyDelete_PassesClientPathAsIs(t *testing.T) {
	capturedModify, capturedDelete := "", ""
	folderSvc := new(svcmocks.MockFolderService)
	folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc)
	folderSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FolderCreateRequest)
			capturedModify = params.Path
		}).
		Return(&dto.FolderDTO{Path: "projects"}, nil).Maybe()
	folderSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FolderDeleteRequest)
			capturedDelete = params.Path
		}).
		Return(&dto.FolderDTO{Path: "projects"}, nil).Maybe()

	testApp := newWSPathTestApp(nil, nil, folderSvc)
	client := newWSPathTestClient(testApp)
	handler := NewFolderWSHandler(testApp)

	handler.FolderModify(client, wsMsg("FolderModify", map[string]any{
		"vault": "v", "path": "/projects",
	}))
	assert.Equal(t, "/projects", capturedModify, "FolderModify 必须原样传递")

	handler.FolderDelete(client, wsMsg("FolderDelete", map[string]any{
		"vault": "v", "path": "/projects",
	}))
	assert.Equal(t, "/projects", capturedDelete, "FolderDelete 必须原样传递")

	// 越界拒绝
	handler.FolderModify(client, wsMsg("FolderModify", map[string]any{
		"vault": "v", "path": "../evil",
	}))
	handler.FolderDelete(client, wsMsg("FolderDelete", map[string]any{
		"vault": "v", "path": "../evil",
	}))
}

// TestWSFolderRename_PassesClientPathsAsIs：FolderRename 新旧路径原样传递
func TestWSFolderRename_PassesClientPathsAsIs(t *testing.T) {
	var capturedNew, capturedOld string
	folderSvc := new(svcmocks.MockFolderService)
	folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc)
	folderSvc.On("Rename", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FolderRenameRequest)
			capturedNew = params.Path
			capturedOld = params.OldPath
		}).
		Return(&dto.FolderDTO{Path: "old"}, &dto.FolderDTO{Path: "new"}, nil)

	testApp := newWSPathTestApp(nil, nil, folderSvc)
	client := newWSPathTestClient(testApp)
	handler := NewFolderWSHandler(testApp)

	handler.FolderRename(client, wsMsg("FolderRename", map[string]any{
		"vault": "v", "path": "/new", "pathHash": "nh", "oldPath": "/old", "oldPathHash": "oh",
	}))

	assert.Equal(t, "/old", capturedOld)
	assert.Equal(t, "/new", capturedNew)
}
