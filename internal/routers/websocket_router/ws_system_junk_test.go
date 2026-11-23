package websocket_router

// 系统垃圾文件写入拦截（WebSocket 通道）：
//  1. 写动作（NoteModify/NoteRename 新路径、FileUploadCheck/FileRename 新路径、
//     FolderModify/FolderRename 新路径、SettingModify）返回 444 且 data.path 回显
//     客户端原始路径；删除动作不拦。
//  2. 全量同步清单比对中，客户端上报的垃圾路径在服务端没有存活行时不要求上传，
//     本轮计数保持为 0、结束消息照常下发；服务端已有的垃圾行仍照常下发。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// readWSFrame 读取一帧响应并解析 JSON，兼容带动作名前缀与纯 JSON 两种形态。
func readWSFrame(t *testing.T, messages <-chan string) map[string]any {
	t.Helper()
	select {
	case msg := <-messages:
		_, payload, ok := strings.Cut(msg, "|")
		if !ok {
			payload = msg
		}
		var res map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &res))
		return res
	case <-time.After(time.Second):
		t.Fatal("未收到响应帧")
		return nil
	}
}

func newJunkWSApp(enabled bool, noteSvc *svcmocks.MockNoteService, fileSvc *svcmocks.MockFileService, folderSvc *svcmocks.MockFolderService, settingSvc *svcmocks.MockSettingService) *app.App {
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{
		NoteService: noteSvc, FileService: fileSvc, FolderService: folderSvc,
		SettingService: settingSvc, VaultService: vaultSvc,
	})
	testApp.Config().App.SyncExcludeSystemJunk = &enabled
	return testApp
}

func newJunkWSMocks() (*svcmocks.MockNoteService, *svcmocks.MockFileService, *svcmocks.MockFolderService, *svcmocks.MockSettingService) {
	n, f, d, s := new(svcmocks.MockNoteService), new(svcmocks.MockFileService), new(svcmocks.MockFolderService), new(svcmocks.MockSettingService)
	n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
	f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
	d.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(d).Maybe()
	s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
	return n, f, d, s
}

// TestWS_SystemJunkWriteTargetsRejected：写动作返回 444，data.path 等于客户端原始路径。
func TestWS_SystemJunkWriteTargetsRejected(t *testing.T) {
	const rawBackslash = `notes\sub\.DS_Store`
	cases := []struct {
		name     string
		action   string
		payload  map[string]any
		wantPath string
	}{
		{
			name: "note modify", action: "NoteModify",
			payload: map[string]any{"vault": "v", "path": rawBackslash, "pathHash": "h",
				"content": "x", "contentHash": "ch", "mtime": 1700000000000, "ctime": 1700000000000},
			wantPath: rawBackslash,
		},
		{
			name: "note rename target", action: "NoteRename",
			payload: map[string]any{"vault": "v", "path": "notes/Thumbs.db", "pathHash": "h2",
				"oldPath": "notes/clean.md", "oldPathHash": "h3"},
			wantPath: "notes/Thumbs.db",
		},
		{
			name: "file upload check", action: "FileUploadCheck",
			payload: map[string]any{"vault": "v", "path": "img/.DS_Store", "pathHash": "h4",
				"contentHash": "ch", "size": 12, "ctime": 1700000000000, "mtime": 1700000000000},
			wantPath: "img/.DS_Store",
		},
		{
			name: "file rename target", action: "FileRename",
			payload: map[string]any{"vault": "v", "path": "img/._new.png", "pathHash": "h5",
				"oldPath": "img/old.png", "oldPathHash": "h6"},
			wantPath: "img/._new.png",
		},
		{
			name: "folder modify", action: "FolderModify",
			payload: map[string]any{"vault": "v", "path": "notes/._junk", "pathHash": "h7"},
			wantPath: "notes/._junk",
		},
		{
			name: "folder rename target", action: "FolderRename",
			payload: map[string]any{"vault": "v", "path": "notes/desktop.ini", "pathHash": "h8",
				"oldPath": "notes/old", "oldPathHash": "h9"},
			wantPath: "notes/desktop.ini",
		},
		{
			name: "setting modify", action: "SettingModify",
			payload: map[string]any{"vault": "v", "path": ".obsidian/._app.json", "pathHash": "h10",
				"content": "{}", "contentHash": "ch", "mtime": 1700000000000, "ctime": 1700000000000},
			wantPath: ".obsidian/._app.json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, f, d, s := newJunkWSMocks()
			testApp := newJunkWSApp(true, n, f, d, s)
			c, messages := newSyncDeletePeer(t, testApp)
			msg := wsMsg(tc.action, tc.payload)

			switch tc.action {
			case "NoteModify":
				NewNoteWSHandler(testApp).NoteModify(c, msg)
			case "NoteRename":
				NewNoteWSHandler(testApp).NoteRename(c, msg)
			case "FileUploadCheck":
				NewFileWSHandler(testApp).FileUploadCheck(c, msg)
			case "FileRename":
				NewFileWSHandler(testApp).FileRename(c, msg)
			case "FolderModify":
				NewFolderWSHandler(testApp).FolderModify(c, msg)
			case "FolderRename":
				NewFolderWSHandler(testApp).FolderRename(c, msg)
			case "SettingModify":
				NewSettingWSHandler(testApp).SettingModify(c, msg)
			}

			res := readWSFrame(t, messages)
			assert.Equal(t, float64(code.ErrorInvalidPath.Code()), res["code"], "垃圾写入必须返回 444")
			data, ok := res["data"].(map[string]any)
			require.True(t, ok, "响应必须带 data")
			assert.Equal(t, tc.wantPath, data["path"], "data.path 必须回显客户端占名额的原始路径")

			n.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			f.AssertNotCalled(t, "UploadCheck", mock.Anything, mock.Anything, mock.Anything)
			f.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything)
			d.AssertNotCalled(t, "UpdateOrCreate", mock.Anything, mock.Anything, mock.Anything)
			s.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// TestWS_SystemJunkDeleteStillAllowed：删除动作不拦，历史垃圾条目可清理。
func TestWS_SystemJunkDeleteStillAllowed(t *testing.T) {
	n, f, d, s := newJunkWSMocks()
	n.On("Get", mock.Anything, int64(1), mock.Anything).Return(&dto.NoteDTO{Path: "notes/.DS_Store"}, nil)
	n.On("Delete", mock.Anything, int64(1), mock.Anything).Return(&dto.NoteDTO{Path: "notes/.DS_Store"}, nil)
	f.On("Delete", mock.Anything, int64(1), mock.Anything).Return(&dto.FileDTO{Path: "img/Thumbs.db"}, nil)
	d.On("Delete", mock.Anything, int64(1), mock.Anything).Return(&dto.FolderDTO{Path: "notes/._junk"}, nil)
	s.On("Delete", mock.Anything, int64(1), mock.Anything).Return(&dto.SettingDTO{Path: ".obsidian/desktop.ini"}, nil)
	testApp := newJunkWSApp(true, n, f, d, s)
	c := newWSPathTestClient(testApp)

	NewNoteWSHandler(testApp).NoteDelete(c, wsMsg("NoteDelete", map[string]any{
		"vault": "v", "path": "notes/.DS_Store", "pathHash": "h"}))
	n.AssertCalled(t, "Delete", mock.Anything, int64(1), mock.Anything)

	NewFileWSHandler(testApp).FileDelete(c, wsMsg("FileDelete", map[string]any{
		"vault": "v", "path": "img/Thumbs.db", "pathHash": "h"}))
	f.AssertCalled(t, "Delete", mock.Anything, int64(1), mock.Anything)

	NewFolderWSHandler(testApp).FolderDelete(c, wsMsg("FolderDelete", map[string]any{
		"vault": "v", "path": "notes/._junk", "pathHash": "h"}))
	d.AssertCalled(t, "Delete", mock.Anything, int64(1), mock.Anything)

	NewSettingWSHandler(testApp).SettingDelete(c, wsMsg("SettingDelete", map[string]any{
		"vault": "v", "path": ".obsidian/desktop.ini", "pathHash": "h"}))
	s.AssertCalled(t, "Delete", mock.Anything, int64(1), mock.Anything)
}

// TestWS_SystemJunkSwitchOffAllowsWrites：开关关闭后垃圾路径照常写入。
func TestWS_SystemJunkSwitchOffAllowsWrites(t *testing.T) {
	n, f, d, s := newJunkWSMocks()
	var notePath, folderPath string
	n.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.Anything).
		Return("Create", nil, nil, nil)
	n.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			notePath = args.Get(2).(*dto.NoteModifyOrCreateRequest).Path
		}).
		Return(false, &dto.NoteDTO{Path: "notes/.DS_Store"}, nil)
	d.On("UpdateOrCreate", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			folderPath = args.Get(2).(*dto.FolderCreateRequest).Path
		}).
		Return(&dto.FolderDTO{Path: "notes/._junk"}, nil)
	testApp := newJunkWSApp(false, n, f, d, s)
	c := newWSPathTestClient(testApp)

	NewNoteWSHandler(testApp).NoteModify(c, wsMsg("NoteModify", map[string]any{
		"vault": "v", "path": "notes/.DS_Store", "pathHash": "h", "content": "x",
		"contentHash": "ch", "mtime": 1700000000000, "ctime": 1700000000000}))
	n.AssertCalled(t, "ModifyOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything, mock.Anything)
	assert.Equal(t, "notes/.DS_Store", notePath)

	NewFolderWSHandler(testApp).FolderModify(c, wsMsg("FolderModify", map[string]any{
		"vault": "v", "path": "notes/._junk", "pathHash": "h"}))
	d.AssertCalled(t, "UpdateOrCreate", mock.Anything, int64(1), mock.Anything)
	assert.Equal(t, "notes/._junk", folderPath)
}

// TestWS_SystemJunkLookalikeNamesAllowed：只是包含关键字的正常文件名不受影响。
func TestWS_SystemJunkLookalikeNamesAllowed(t *testing.T) {
	n, f, d, s := newJunkWSMocks()
	var captured string
	n.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.Anything).
		Return("Create", nil, nil, nil)
	n.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			captured = args.Get(2).(*dto.NoteModifyOrCreateRequest).Path
		}).
		Return(false, &dto.NoteDTO{Path: "my._notes.md"}, nil)
	testApp := newJunkWSApp(true, n, f, d, s)
	c := newWSPathTestClient(testApp)

	NewNoteWSHandler(testApp).NoteModify(c, wsMsg("NoteModify", map[string]any{
		"vault": "v", "path": "my._notes.md", "pathHash": "h", "content": "x",
		"contentHash": "ch", "mtime": 1700000000000, "ctime": 1700000000000}))
	assert.Equal(t, "my._notes.md", captured)
}

// TestWS_SystemJunkInventorySkipsUpload：全量清单里服务端没有的垃圾路径不要求上传，
// 本轮正常结束；服务端已有的垃圾行仍照常下发。
func TestWS_SystemJunkInventorySkipsUpload(t *testing.T) {
	const junkPath = "notes/.DS_Store"
	const normalPath = "notes/normal.md"
	const junkHash = "junk-hash"
	const normalHash = "normal-hash"
	const ctx = "junk-inventory"

	t.Run("note missing junk not requested", func(t *testing.T) {
		n, f, d, s := newJunkWSMocks()
		n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
		testApp := newJunkWSApp(true, n, f, d, s)
		testApp.Config().App.SyncDownChunkNum = 10
		c, messages := newSyncDeletePeer(t, testApp)

		NewNoteWSHandler(testApp).doNoteSync(c, &dto.NoteSyncRequest{
			Context: ctx, Vault: "v",
			Notes: []dto.NoteSyncCheckRequest{
				{Path: junkPath, PathHash: junkHash, ContentHash: "ch", Mtime: 1000},
				{Path: normalPath, PathHash: normalHash, ContentHash: "ch", Mtime: 1000},
			},
		})
		end := syncDeleteMessage(t, messages, string(NoteSyncEnd))
		counts := end["data"].(map[string]any)
		assert.Equal(t, float64(1), counts["needUploadCount"], "正常路径仍要求上传")
	})

	t.Run("note existing server junk still delivered", func(t *testing.T) {
		n, f, d, s := newJunkWSMocks()
		n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
			Return([]*dto.NoteDTO{{Path: junkPath, PathHash: junkHash, ContentHash: "server",
				Mtime: 1700000001000, Action: "create"}}, nil)
		testApp := newJunkWSApp(true, n, f, d, s)
		testApp.Config().App.SyncDownChunkNum = 10
		c, messages := newSyncDeletePeer(t, testApp)

		NewNoteWSHandler(testApp).doNoteSync(c, &dto.NoteSyncRequest{
			Context: ctx, Vault: "v",
			Notes: []dto.NoteSyncCheckRequest{{Path: junkPath, PathHash: junkHash, ContentHash: "client", Mtime: 1700000000000}},
		})
		end := syncDeleteMessage(t, messages, string(NoteSyncEnd))
		counts := end["data"].(map[string]any)
		assert.Equal(t, float64(1), counts["needModifyCount"], "服务端已有的垃圾行仍要下发")
		assert.Equal(t, float64(0), counts["needUploadCount"])
	})

	t.Run("file missing junk not requested", func(t *testing.T) {
		n, f, d, s := newJunkWSMocks()
		f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{}, nil)
		testApp := newJunkWSApp(true, n, f, d, s)
		testApp.Config().App.SyncDownChunkNum = 10
		c, messages := newSyncDeletePeer(t, testApp)

		NewFileWSHandler(testApp).doFileSync(c, &dto.FileSyncRequest{
			Context: ctx, Vault: "v",
			Files: []dto.FileSyncCheckRequest{{Path: junkPath, PathHash: junkHash, ContentHash: "ch", Mtime: 1000, Ctime: 1000}},
		})
		end := syncDeleteMessage(t, messages, string(FileSyncEnd))
		counts := end["data"].(map[string]any)
		assert.Equal(t, float64(0), counts["needUploadCount"], "服务端缺失的垃圾文件不再要求上传")
	})

	t.Run("folder missing junk not created", func(t *testing.T) {
		n, f, d, s := newJunkWSMocks()
		d.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "v", int64(0)).Return([]*dto.FolderDTO{}, nil)
		testApp := newJunkWSApp(true, n, f, d, s)
		testApp.Config().App.SyncDownChunkNum = 10
		c, messages := newSyncDeletePeer(t, testApp)

		NewFolderWSHandler(testApp).doFolderSync(c, &dto.FolderSyncRequest{
			Context: ctx, Vault: "v",
			Folders: []dto.FolderSyncCheckRequest{{Path: junkPath, PathHash: junkHash, Mtime: 1000}},
		})
		syncDeleteMessage(t, messages, string(FolderSyncEnd))
		d.AssertNotCalled(t, "UpdateOrCreate", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("setting missing junk not requested", func(t *testing.T) {
		n, f, d, s := newJunkWSMocks()
		s.On("Sync", mock.Anything, int64(1), mock.Anything).Return([]*dto.SettingDTO{}, nil)
		testApp := newJunkWSApp(true, n, f, d, s)
		testApp.Config().App.SyncDownChunkNum = 10
		c, messages := newSyncDeletePeer(t, testApp)

		NewSettingWSHandler(testApp).doSettingSync(c, &dto.SettingSyncRequest{
			Context: ctx, Vault: "v",
			Settings: []dto.SettingSyncCheckRequest{{Path: junkPath, PathHash: junkHash, ContentHash: "ch", Mtime: 1000}},
		})
		end := syncDeleteMessage(t, messages, string(SettingSyncEnd))
		counts := end["data"].(map[string]any)
		assert.Equal(t, float64(0), counts["needUploadCount"])
	})
}

// TestWS_SystemJunkBaselineSkipsUpload：基线模式下服务端缺失的垃圾路径同样不要求上传。
func TestWS_SystemJunkBaselineSkipsUpload(t *testing.T) {
	const junkPath = "notes/.DS_Store"
	hash := util.EncodeHash32(junkPath)
	const ctx = "junk-baseline"

	t.Run("note", func(t *testing.T) {
		n, f, d, s := newJunkWSMocks()
		n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
		n.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).Return(map[string]*dto.NoteDTO{}, nil)
		testApp := newJunkWSApp(true, n, f, d, s)
		testApp.Config().App.SyncDownChunkNum = 10
		c, messages := baselineNotePeer(t, testApp)

		NewNoteWSHandler(testApp).doNoteSyncBaseline(c, &dto.NoteSyncRequest{
			Context: ctx, Vault: "v",
			Notes: []dto.NoteSyncCheckRequest{{Path: junkPath, PathHash: hash, ContentHash: "ch", Mtime: 1000, BaseHashMissing: true}},
		})
		end := syncDeleteMessage(t, messages, string(NoteSyncEnd))
		assert.Equal(t, float64(0), end["data"].(map[string]any)["needUploadCount"])
	})

	t.Run("file", func(t *testing.T) {
		n, f, d, s := newJunkWSMocks()
		f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{}, nil)
		f.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).Return(map[string]*dto.FileDTO{}, nil)
		testApp := newJunkWSApp(true, n, f, d, s)
		testApp.Config().App.SyncDownChunkNum = 10
		c, messages := newSyncDeletePeer(t, testApp)
		c.SetSyncBaselineVersionForTest(1)

		NewFileWSHandler(testApp).doFileSyncBaseline(c, &dto.FileSyncRequest{
			Context: ctx, Vault: "v",
			Files: []dto.FileSyncCheckRequest{{Path: junkPath, PathHash: hash, ContentHash: "ch", Mtime: 1000, Ctime: 1000, BaseHashMissing: true}},
		})
		end := syncDeleteMessage(t, messages, string(FileSyncEnd))
		assert.Equal(t, float64(0), end["data"].(map[string]any)["needUploadCount"])
	})

	t.Run("folder", func(t *testing.T) {
		n, f, d, s := newJunkWSMocks()
		d.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "v", int64(0)).Return([]*dto.FolderDTO{}, nil)
		d.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).Return(map[string]*dto.FolderDTO{}, nil)
		testApp := newJunkWSApp(true, n, f, d, s)
		testApp.Config().App.SyncDownChunkNum = 10
		c, messages := newSyncDeletePeer(t, testApp)
		c.SetSyncBaselineVersionForTest(1)

		NewFolderWSHandler(testApp).doFolderSyncBaseline(c, &dto.FolderSyncRequest{
			Context: ctx, Vault: "v",
			Folders: []dto.FolderSyncCheckRequest{{Path: junkPath, PathHash: hash, Mtime: 1000}},
		})
		syncDeleteMessage(t, messages, string(FolderSyncEnd))
		d.AssertNotCalled(t, "UpdateOrCreate", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("setting", func(t *testing.T) {
		n, f, d, s := newJunkWSMocks()
		s.On("Sync", mock.Anything, int64(1), mock.Anything).Return([]*dto.SettingDTO{}, nil)
		s.On("GetByPathHashesMetaBatch", mock.Anything, int64(1), "v", mock.Anything).Return(map[string]*dto.SettingDTO{}, nil)
		testApp := newJunkWSApp(true, n, f, d, s)
		testApp.Config().App.SyncDownChunkNum = 10
		c, messages := newSyncDeletePeer(t, testApp)
		c.SetSyncBaselineVersionForTest(1)

		NewSettingWSHandler(testApp).doSettingSyncBaseline(c, &dto.SettingSyncRequest{
			Context: ctx, Vault: "v",
			Settings: []dto.SettingSyncCheckRequest{{Path: junkPath, PathHash: hash, ContentHash: "ch", Mtime: 1000, BaseHashMissing: true}},
		})
		end := syncDeleteMessage(t, messages, string(SettingSyncEnd))
		assert.Equal(t, float64(0), end["data"].(map[string]any)["needUploadCount"])
	})
}
