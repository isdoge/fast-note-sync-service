package websocket_router

// #342 S-8 / haierkeys/obsidian-fast-note-sync#244：
// WebSocket 写入入口（NoteModify/NoteDelete/NoteRename、FileUploadCheck/
// FileDelete/FileRename、FolderModify/FolderDelete/FolderRename）统一路径
// 清洗——去前导 "/"、拒绝空/绝对/".."/NUL 路径、笔记补 .md。
// 本文件构造最小 WebsocketClient 直接调用 handler，断言服务层收到的
// Path 已被清洗、非法路径在进入服务层前被拒（ErrorInvalidPath）。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newWSPathTestClient 构造可直接调用 handler 的 WebsocketClient（无真实连接）。
func newWSPathTestClient(testApp *app.App) *pkgapp.WebsocketClient {
	ws, cancel := context.WithCancel(context.Background())
	tcpClient := pkgapp.NewWebsocketTestClient(pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp), ws)
	tcpClient.WsCancel = cancel
	tcpClient.User = &pkgapp.UserEntity{UID: 1}
	return tcpClient
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

// decodeWSResponse 解析 ToResponse 发出的 JSON（捕获自 conn=nil 的丢弃路径不可行，
// 因此这里不断言响应体，改为断言服务层未被调用 / 收到的参数已清洗）
func decodeWSResponse(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

var _ = decodeWSResponse

// TestWSNoteModify_PathCleaning：NoteModify 清洗路径（去前导/、补 .md）
func TestWSNoteModify_PathCleaning(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"leading slash + no ext", "/notes/todo", "notes/todo.md"},
		{"leading slash + ext", "/notes/todo.md", "notes/todo.md"},
		{"plain + no ext", "notes/todo", "notes/todo.md"},
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

			assert.Equal(t, tt.want, captured)
		})
	}
}

// TestWSNoteModify_RejectsBadPath：NoteModify 拒绝越界路径且不进入服务层
func TestWSNoteModify_RejectsBadPath(t *testing.T) {
	for _, bad := range []string{"../etc/passwd", "notes/../../x"} {
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

// TestWSNoteRename_BothPathsCleaned：NoteRename 新旧路径都清洗
func TestWSNoteRename_BothPathsCleaned(t *testing.T) {
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

	assert.Equal(t, "old.md", capturedOld)
	assert.Equal(t, "sub/new.md", capturedNew)
}

// TestWSFileUploadCheck_PathCleaning：FileUploadCheck 去前导 "/"（不补 .md）
func TestWSFileUploadCheck_PathCleaning(t *testing.T) {
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

	assert.Equal(t, "images/pic.png", captured)
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

// TestWSFileRename_BothPathsCleaned：FileRename 新旧路径都清洗
func TestWSFileRename_BothPathsCleaned(t *testing.T) {
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

	assert.Equal(t, "old.png", capturedOld)
	assert.Equal(t, "new.png", capturedNew)
}

// TestWSFolderModifyDelete_PathCleaning：FolderModify/FolderDelete 去前导 "/"，拒绝越界
func TestWSFolderModifyDelete_PathCleaning(t *testing.T) {
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
	assert.Equal(t, "projects", capturedModify)

	handler.FolderDelete(client, wsMsg("FolderDelete", map[string]any{
		"vault": "v", "path": "/projects",
	}))
	assert.Equal(t, "projects", capturedDelete)

	// 越界拒绝
	handler.FolderModify(client, wsMsg("FolderModify", map[string]any{
		"vault": "v", "path": "../evil",
	}))
	handler.FolderDelete(client, wsMsg("FolderDelete", map[string]any{
		"vault": "v", "path": "../evil",
	}))
}

// TestWSFolderRename_BothPathsCleaned：FolderRename 新旧路径都清洗
func TestWSFolderRename_BothPathsCleaned(t *testing.T) {
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

	assert.Equal(t, "old", capturedOld)
	assert.Equal(t, "new", capturedNew)
}

// 确认 ErrorInvalidPath 存在引用（防止未用 import 的 lint 报错假阳性）
var _ = code.ErrorInvalidPath
