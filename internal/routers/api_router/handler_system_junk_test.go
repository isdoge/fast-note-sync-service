package api_router

// 系统垃圾文件写入拦截（REST 通道）：新建/覆盖/改名目标路径按开关拒收，
// 读取、列表、下载、删除、恢复与清理回收站一律不拦——库里已有的垃圾条目
// 仍可被读取和删除。测试直接调用各 handler，断言 444 与「服务层未被调用」。

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func newJunkNoteHandler(enabled bool) (*NoteHandler, *svcmocks.MockNoteService, *string) {
	captured := new(string)
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	noteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.Anything).
		Return("", nil, nil, nil).Maybe()
	noteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			*captured = args.Get(2).(*dto.NoteModifyOrCreateRequest).Path
		}).
		Return(false, &dto.NoteDTO{Path: "ok.md"}, nil).Maybe()
	noteSvc.On("Rename", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			*captured = args.Get(2).(*dto.NoteRenameRequest).Path
		}).
		Return(&dto.NoteDTO{Path: "old.md"}, &dto.NoteDTO{Path: "new.md"}, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{NoteService: noteSvc})
	testApp.Config().App.SyncExcludeSystemJunk = &enabled
	return NewNoteHandler(testApp, pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)), noteSvc, captured
}

func newJunkFolderHandler(enabled bool) (*FolderHandler, *svcmocks.MockFolderService, *string) {
	captured := new(string)
	folderSvc := new(svcmocks.MockFolderService)
	folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc)
	folderSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			*captured = args.Get(2).(*dto.FolderCreateRequest).Path
		}).
		Return(&dto.FolderDTO{Path: "ok"}, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{FolderService: folderSvc})
	testApp.Config().App.SyncExcludeSystemJunk = &enabled
	return NewFolderHandler(testApp, pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)), folderSvc, captured
}

func newJunkFileHandler(enabled bool) (*FileHandler, *svcmocks.MockFileService, *string, *string) {
	capturedNew, capturedOld := new(string), new(string)
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	fileSvc.On("Rename", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FileRenameRequest)
			*capturedNew = params.Path
			*capturedOld = params.OldPath
		}).
		Return(&dto.FileDTO{Path: "old.png"}, &dto.FileDTO{Path: "new.png"}, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{FileService: fileSvc})
	testApp.Config().App.SyncExcludeSystemJunk = &enabled
	return NewFileHandler(testApp, pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)), fileSvc, capturedNew, capturedOld
}

func newJunkSettingHandler(enabled bool) (*SettingHandler, *svcmocks.MockSettingService, *string) {
	captured := new(string)
	settingSvc := new(svcmocks.MockSettingService)
	settingSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(settingSvc)
	settingSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			*captured = args.Get(2).(*dto.SettingModifyOrCreateRequest).Path
		}).
		Return(false, &dto.SettingDTO{Path: "ok.json"}, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{SettingService: settingSvc})
	testApp.Config().App.SyncExcludeSystemJunk = &enabled
	return NewSettingHandler(testApp, pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)), settingSvc, captured
}

// TestREST_SystemJunkWriteTargetsRejected：四类资源在 REST 的新建/覆盖/改名目标
// 位置传入垃圾路径时统一返回 444，且不进入服务层。
func TestREST_SystemJunkWriteTargetsRejected(t *testing.T) {
	junkPaths := []string{".DS_Store", "notes/Thumbs.db", "sub/desktop.ini", "notes/._note.md"}

	t.Run("note create", func(t *testing.T) {
		noteHandler, noteSvc, _ := newJunkNoteHandler(true)
		for _, p := range junkPaths {
			c, w := newNoteTestContext("POST", "/api/note", `{"vault":"v","path":"`+p+`","content":"x"}`, 1)
			noteHandler.CreateOrUpdate(c)
			assertResponseCode(t, w, code.ErrorInvalidPath.Code())
		}
		noteSvc.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("note edit tools", func(t *testing.T) {
		noteHandler, noteSvc, _ := newJunkNoteHandler(true)
		cases := []struct {
			name   string
			call   func(h *NoteHandler, c *gin.Context)
			method string
			url    string
			body   string
		}{
			{"append", func(h *NoteHandler, c *gin.Context) { h.Append(c) }, "POST", "/api/note/append", `{"vault":"v","path":".DS_Store","content":"x"}`},
			{"prepend", func(h *NoteHandler, c *gin.Context) { h.Prepend(c) }, "POST", "/api/note/prepend", `{"vault":"v","path":"notes/._x.md","content":"x"}`},
			{"replace", func(h *NoteHandler, c *gin.Context) { h.Replace(c) }, "POST", "/api/note/replace", `{"vault":"v","path":"Thumbs.db","find":"a","replace":"b"}`},
			{"frontmatter", func(h *NoteHandler, c *gin.Context) { h.PatchFrontmatter(c) }, "PATCH", "/api/note/frontmatter", `{"vault":"v","path":"desktop.ini","updates":{"tags":["t"]}}`},
		}
		for _, tc := range cases {
			c, w := newNoteTestContext(tc.method, tc.url, tc.body, 1)
			tc.call(noteHandler, c)
			assertResponseCode(t, w, code.ErrorInvalidPath.Code())
		}
		noteSvc.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("note rename target", func(t *testing.T) {
		noteHandler, noteSvc, _ := newJunkNoteHandler(true)
		c, w := newNoteTestContext("POST", "/api/note/rename", `{"vault":"v","path":"sub/.DS_Store","oldPath":"old.md"}`, 1)
		noteHandler.Rename(c)
		assertResponseCode(t, w, code.ErrorInvalidPath.Code())
		noteSvc.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("file upload", func(t *testing.T) {
		fileHandler, fileSvc, _, _ := newJunkFileHandler(true)
		c, w := newFileTestContext("POST", "/api/file?path=img/.DS_Store&vault=v", "", 1)
		fileHandler.Upload(c)
		assertResponseCode(t, w, code.ErrorInvalidPath.Code())
		fileSvc.AssertNotCalled(t, "Upload", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("file rename target", func(t *testing.T) {
		fileHandler, fileSvc, _, _ := newJunkFileHandler(true)
		c, w := newFileTestContext("POST", "/api/file/rename", `{"vault":"v","path":"img/._new.png","oldPath":"img/old.png"}`, 1)
		fileHandler.Rename(c)
		assertResponseCode(t, w, code.ErrorInvalidPath.Code())
		fileSvc.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("folder create", func(t *testing.T) {
		folderHandler, folderSvc, _ := newJunkFolderHandler(true)
		c, w := newFolderTestContext("POST", "/api/folder", `{"vault":"v","path":"notes/.DS_Store"}`, 1)
		folderHandler.Create(c)
		assertResponseCode(t, w, code.ErrorInvalidPath.Code())
		folderSvc.AssertNotCalled(t, "UpdateOrCreate", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("setting create", func(t *testing.T) {
		settingHandler, settingSvc, _ := newJunkSettingHandler(true)
		c, w := newSettingTestContext("POST", "/api/setting", `{"vault":"v","path":".obsidian/._app.json","content":"{}"}`, 1)
		settingHandler.CreateOrUpdate(c)
		assertResponseCode(t, w, code.ErrorInvalidPath.Code())
		settingSvc.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("setting rename target", func(t *testing.T) {
		settingHandler, settingSvc, _ := newJunkSettingHandler(true)
		c, w := newSettingTestContext("POST", "/api/setting/rename", `{"vault":"v","newPath":".obsidian/Thumbs.db","oldPath":".obsidian/app.json"}`, 1)
		settingHandler.Rename(c)
		assertResponseCode(t, w, code.ErrorInvalidPath.Code())
		settingSvc.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything)
	})
}

// TestREST_SystemJunkOldPathAllowedOnRename：改名只看新路径，旧路径是垃圾的放行。
func TestREST_SystemJunkOldPathAllowedOnRename(t *testing.T) {
	noteHandler, _, capturedNew := newJunkNoteHandler(true)
	c, w := newNoteTestContext("POST", "/api/note/rename", `{"vault":"v","path":"notes/clean.md","oldPath":"notes/.DS_Store"}`, 1)
	noteHandler.Rename(c)
	assertResponseCode(t, w, code.Success.Code())
	assert.Equal(t, "notes/clean.md", *capturedNew)

	fileHandler, _, capturedFileNew, capturedFileOld := newJunkFileHandler(true)
	c, w = newFileTestContext("POST", "/api/file/rename", `{"vault":"v","path":"img/clean.png","oldPath":"img/._old.png"}`, 1)
	fileHandler.Rename(c)
	assertResponseCode(t, w, code.Success.Code())
	assert.Equal(t, "img/clean.png", *capturedFileNew)
	assert.Equal(t, "img/._old.png", *capturedFileOld, "旧路径不做垃圾判断")
}

// TestREST_SystemJunkDeleteStillAllowed：库里已有垃圾条目仍可删除，不被拦截。
func TestREST_SystemJunkDeleteStillAllowed(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	noteSvc.On("Get", mock.Anything, int64(1), mock.Anything).
		Return(&dto.NoteDTO{Path: ".DS_Store"}, nil)
	noteSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
		Return(&dto.NoteDTO{Path: ".DS_Store"}, nil)
	testApp := app.NewTestApp(&app.Services{NoteService: noteSvc})
	noteHandler := NewNoteHandler(testApp, pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp))

	c, w := newNoteTestContext("DELETE", "/api/note", `{"vault":"v","path":"notes/.DS_Store"}`, 1)
	noteHandler.Delete(c)
	assertResponseCode(t, w, code.Success.Code())
	noteSvc.AssertCalled(t, "Delete", mock.Anything, int64(1), mock.Anything)

	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	fileSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
		Return(&dto.FileDTO{Path: "img/Thumbs.db"}, nil)
	testApp = app.NewTestApp(&app.Services{FileService: fileSvc})
	fileHandler := NewFileHandler(testApp, pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp))
	c, w = newFileTestContext("DELETE", "/api/file", `{"vault":"v","path":"img/Thumbs.db","pathHash":"h"}`, 1)
	fileHandler.Delete(c)
	assertResponseCode(t, w, code.Success.Code())
	fileSvc.AssertCalled(t, "Delete", mock.Anything, int64(1), mock.Anything)

	folderSvc := new(svcmocks.MockFolderService)
	folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc)
	folderSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
		Return(&dto.FolderDTO{Path: "notes/._junk"}, nil)
	testApp = app.NewTestApp(&app.Services{FolderService: folderSvc})
	folderHandler := NewFolderHandler(testApp, pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp))
	c, w = newFolderTestContext("DELETE", "/api/folder", `{"vault":"v","path":"notes/._junk"}`, 1)
	folderHandler.Delete(c)
	assertResponseCode(t, w, code.Success.Code())
	folderSvc.AssertCalled(t, "Delete", mock.Anything, int64(1), mock.Anything)

	settingSvc := new(svcmocks.MockSettingService)
	settingSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(settingSvc)
	settingSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
		Return(&dto.SettingDTO{Path: ".obsidian/desktop.ini"}, nil)
	testApp = app.NewTestApp(&app.Services{SettingService: settingSvc})
	settingHandler := NewSettingHandler(testApp, pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp))
	c, w = newSettingTestContext("DELETE", "/api/setting", `{"vault":"v","path":".obsidian/desktop.ini"}`, 1)
	settingHandler.Delete(c)
	assertResponseCode(t, w, code.Success.Code())
	settingSvc.AssertCalled(t, "Delete", mock.Anything, int64(1), mock.Anything)
}

// TestREST_SystemJunkSwitchOffAllowsWrites：开关关闭后行为与旧版本一致。
func TestREST_SystemJunkSwitchOffAllowsWrites(t *testing.T) {
	noteHandler, _, captured := newJunkNoteHandler(false)
	c, w := newNoteTestContext("POST", "/api/note", `{"vault":"v","path":"notes/.DS_Store","content":"x"}`, 1)
	noteHandler.CreateOrUpdate(c)
	assertResponseCode(t, w, code.Success.Code())
	assert.Equal(t, "notes/.DS_Store", *captured)

	folderHandler, _, capturedFolder := newJunkFolderHandler(false)
	c, w = newFolderTestContext("POST", "/api/folder", `{"vault":"v","path":"notes/._junk"}`, 1)
	folderHandler.Create(c)
	assertResponseCode(t, w, code.Success.Code())
	assert.Equal(t, "notes/._junk", *capturedFolder)
}

// TestREST_SystemJunkLookalikeNamesAllowed：只是包含关键字的正常文件名不受影响。
func TestREST_SystemJunkLookalikeNamesAllowed(t *testing.T) {
	noteHandler, _, captured := newJunkNoteHandler(true)
	for _, p := range []string{"my._notes.md", "foo._.bar.md", "notes/Thumbs.db.backup", "notes/desktop.ini.txt"} {
		c, w := newNoteTestContext("POST", "/api/note", `{"vault":"v","path":"`+p+`","content":"x"}`, 1)
		noteHandler.CreateOrUpdate(c)
		assertResponseCode(t, w, code.Success.Code())
		assert.Equal(t, p, *captured)
	}
}
