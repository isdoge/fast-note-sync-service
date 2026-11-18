package api_router

// #342 S-8 / haierkeys/obsidian-fast-note-sync#244：
// REST 写入入口路径规则：去前导 "/"，按路径段（"/" 与 "\" 都算分隔符）拒绝
// 整段 ".."、空路径与 NUL，不做 URL 解码（"a..b.md"、"等等...md"、"100%.md"
// 等正常笔记名合法）。一律不补 ".md"——REST 也被第三方客户端用于写非笔记文件
// （x.canvas 之类不得被改名）。测试直接调用各 handler，断言服务层收到的 Path
// 符合规则、非法路径返回 ErrorInvalidPath 且不进入服务层。

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

// newTestPathNoteHandler 构造捕获 ModifyOrCreate 参数的 NoteHandler
func newTestPathNoteHandler(noteSvc *svcmocks.MockNoteService, captured *string) *NoteHandler {
	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	// CreateOrUpdate 先走 UpdateCheckWithNote
	noteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.Anything).
		Return("", nil, nil, nil).Maybe()
	noteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.NoteModifyOrCreateRequest)
			*captured = params.Path
		}).
		Return(false, &dto.NoteDTO{Path: "ok.md"}, nil).Maybe()

	testApp := app.NewTestApp(&app.Services{NoteService: noteSvc})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	return NewNoteHandler(testApp, wss)
}

// TestNoteHandler_CreateOrUpdate_PathCleaning：去前导 "/"，一律不补 .md；
// 文件名里的 ..、省略号、% 不影响合法性
func TestNoteHandler_CreateOrUpdate_PathCleaning(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"leading slash stripped, no suffix added", `{"vault":"v","path":"/notes/todo","content":"x"}`, "notes/todo"},
		{"leading slash stripped, ext kept", `{"vault":"v","path":"/notes/todo.md","content":"x"}`, "notes/todo.md"},
		{"uppercase ext kept", `{"vault":"v","path":"/notes/TODO.MD","content":"x"}`, "notes/TODO.MD"},
		{"plain + ext", `{"vault":"v","path":"notes/todo.md","content":"x"}`, "notes/todo.md"},
		{"backslash normalized", `{"vault":"v","path":"notes\\todo.md","content":"x"}`, "notes/todo.md"},
		{"dot-dot inside filename is legal", `{"vault":"v","path":"a..b.md","content":"x"}`, "a..b.md"},
		{"ellipsis filename is legal", `{"vault":"v","path":"等等...md","content":"x"}`, "等等...md"},
		{"percent filename is legal", `{"vault":"v","path":"100%.md","content":"x"}`, "100%.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captured := ""
			noteSvc := new(svcmocks.MockNoteService)
			handler := newTestPathNoteHandler(noteSvc, &captured)

			c, w := newNoteTestContext("POST", "/api/note", tt.body, 1)
			handler.CreateOrUpdate(c)

			assert.Equal(t, code.Success.Code(), int(decodeRes(t, w)["code"].(float64)))
			assert.Equal(t, tt.want, captured)
		})
	}
}

// TestNoteHandler_CreateOrUpdate_KeepsCanvasExt：REST 新建 x.canvas 不被改名
// （第三方客户端经 REST 写非笔记文件；补 .md 会变成 x.canvas.md）
func TestNoteHandler_CreateOrUpdate_KeepsCanvasExt(t *testing.T) {
	captured := ""
	noteSvc := new(svcmocks.MockNoteService)
	handler := newTestPathNoteHandler(noteSvc, &captured)

	c, w := newNoteTestContext("POST", "/api/note", `{"vault":"v","path":"x.canvas","content":"{}"}`, 1)
	handler.CreateOrUpdate(c)

	assert.Equal(t, code.Success.Code(), int(decodeRes(t, w)["code"].(float64)))
	assert.Equal(t, "x.canvas", captured, "REST 一律不补 .md，x.canvas 必须原样")
}

// TestNoteHandler_CreateOrUpdate_RejectsBadPath：越界路径必须被拒
func TestNoteHandler_CreateOrUpdate_RejectsBadPath(t *testing.T) {
	// 空 path 由 DTO 的 binding:"required" 拦截（BindAndValid 层），
	// 这里只测路径清洗层能拒绝的形态。
	badBodies := []string{
		`{"vault":"v","path":"../etc/passwd","content":"x"}`,
		`{"vault":"v","path":"notes/../../x","content":"x"}`,
	}
	for _, body := range badBodies {
		t.Run("reject "+body, func(t *testing.T) {
			captured := "SENTINEL"
			noteSvc := new(svcmocks.MockNoteService)
			handler := newTestPathNoteHandler(noteSvc, &captured)

			c, w := newNoteTestContext("POST", "/api/note", body, 1)
			handler.CreateOrUpdate(c)

			assertResponseCode(t, w, code.ErrorInvalidPath.Code())
			assert.Equal(t, "SENTINEL", captured, "service must not be called with a bad path")
			noteSvc.AssertNotCalled(t, "ModifyOrCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// TestNoteHandler_EditTools_RejectBadPath：append/prepend/replace/delete/restore/frontmatter 的拒绝路径
func TestNoteHandler_EditTools_RejectBadPath(t *testing.T) {
	cases := []struct {
		name   string
		call   func(h *NoteHandler, c *gin.Context)
		method string
		url    string
		body   string
	}{
		{"append", func(h *NoteHandler, c *gin.Context) { h.Append(c) }, "POST", "/api/note/append", `{"vault":"v","path":"../evil.md","content":"x"}`},
		{"prepend", func(h *NoteHandler, c *gin.Context) { h.Prepend(c) }, "POST", "/api/note/prepend", `{"vault":"v","path":"../evil.md","content":"x"}`},
		{"replace", func(h *NoteHandler, c *gin.Context) { h.Replace(c) }, "POST", "/api/note/replace", `{"vault":"v","path":"../evil.md","find":"a","replace":"b"}`},
		{"delete", func(h *NoteHandler, c *gin.Context) { h.Delete(c) }, "DELETE", "/api/note", `{"vault":"v","path":"../evil.md"}`},
		{"restore", func(h *NoteHandler, c *gin.Context) { h.Restore(c) }, "PUT", "/api/note/restore", `{"vault":"v","path":"../evil.md"}`},
		{"frontmatter", func(h *NoteHandler, c *gin.Context) { h.PatchFrontmatter(c) }, "PATCH", "/api/note/frontmatter", `{"vault":"v","path":"../evil.md","updates":{"tags":["t"]}}`},
		{"rename", func(h *NoteHandler, c *gin.Context) { h.Rename(c) }, "POST", "/api/note/rename", `{"vault":"v","path":"new.md","oldPath":"../evil.md"}`},
		{"rename bad new", func(h *NoteHandler, c *gin.Context) { h.Rename(c) }, "POST", "/api/note/rename", `{"vault":"v","path":"../evil.md","oldPath":"old.md"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noteSvc := new(svcmocks.MockNoteService)
			noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
			// Delete/Restore 会先 Get；统一 Maybe 以便失败表现为断言失败而非 panic
			noteSvc.On("Get", mock.Anything, int64(1), mock.Anything).
				Return(&dto.NoteDTO{Path: "ok.md"}, nil).Maybe()
			testApp := app.NewTestApp(&app.Services{NoteService: noteSvc})
			wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
			handler := NewNoteHandler(testApp, wss)

			c, w := newNoteTestContext(tc.method, tc.url, tc.body, 1)
			tc.call(handler, c)

			assertResponseCode(t, w, code.ErrorInvalidPath.Code())
		})
	}
}

// TestNoteHandler_Rename_BothPathsCleaned：REST 改名新旧路径都只去前导 "/"，不补 .md
func TestNoteHandler_Rename_BothPathsCleaned(t *testing.T) {
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

	testApp := app.NewTestApp(&app.Services{NoteService: noteSvc})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	handler := NewNoteHandler(testApp, wss)

	c, w := newNoteTestContext("POST", "/api/note/rename", `{"vault":"v","path":"/sub/new","oldPath":"/old"}`, 1)
	handler.Rename(c)

	assertResponseCode(t, w, code.Success.Code())
	assert.Equal(t, "old", capturedOld, "REST 改名旧路径只去前导 /，不补 .md")
	assert.Equal(t, "sub/new", capturedNew, "REST 改名新路径只去前导 /，不补 .md")
}

// TestFolderHandler_CreateDelete_PathCleaning：目录创建/删除去前导 "/"，拒绝越界
func TestFolderHandler_CreateDelete_PathCleaning(t *testing.T) {
	folderSvc := new(svcmocks.MockFolderService)
	capturedCreate, capturedDelete := "", ""
	folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc)
	folderSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FolderCreateRequest)
			capturedCreate = params.Path
		}).
		Return(&dto.FolderDTO{Path: "projects"}, nil).Maybe()
	folderSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FolderDeleteRequest)
			capturedDelete = params.Path
		}).
		Return(&dto.FolderDTO{Path: "projects"}, nil).Maybe()

	handler := newTestFolderHandler(folderSvc)

	// Create：去前导 "/"
	c, w := newFolderTestContext("POST", "/api/folder", `{"vault":"v","path":"/projects"}`, 1)
	handler.Create(c)
	assertResponseCode(t, w, code.Success.Code())
	assert.Equal(t, "projects", capturedCreate)

	// Delete：去前导 "/"
	c, w = newFolderTestContext("DELETE", "/api/folder", `{"vault":"v","path":"/projects"}`, 1)
	handler.Delete(c)
	assertResponseCode(t, w, code.Success.Code())
	assert.Equal(t, "projects", capturedDelete)

	// Create：越界拒绝
	c, w = newFolderTestContext("POST", "/api/folder", `{"vault":"v","path":"../projects"}`, 1)
	handler.Create(c)
	assertResponseCode(t, w, code.ErrorInvalidPath.Code())

	// Delete：越界拒绝
	c, w = newFolderTestContext("DELETE", "/api/folder", `{"vault":"v","path":"../projects"}`, 1)
	handler.Delete(c)
	assertResponseCode(t, w, code.ErrorInvalidPath.Code())
}

// TestFileHandler_RestoreDelete_PathCleaning：file restore/delete 拒绝越界
func TestFileHandler_RestoreDelete_PathCleaning(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	testApp := app.NewTestApp(&app.Services{FileService: fileSvc})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	handler := NewFileHandler(testApp, wss)

	c, w := newFolderTestContext("PUT", "/api/file/restore", `{"vault":"v","path":"../evil.png","pathHash":"h"}`, 1)
	handler.Restore(c)
	assertResponseCode(t, w, code.ErrorInvalidPath.Code())

	c, w = newFolderTestContext("DELETE", "/api/file", `{"vault":"v","path":"../evil.png","pathHash":"h"}`, 1)
	handler.Delete(c)
	assertResponseCode(t, w, code.ErrorInvalidPath.Code())

	// RecycleClear：路径可空（清全部），非空时校验
	c, w = newFolderTestContext("DELETE", "/api/file/recycle-clear", `{"vault":"v","path":"../evil.png"}`, 1)
	handler.RecycleClear(c)
	assertResponseCode(t, w, code.ErrorInvalidPath.Code())
}

// TestFileHandler_Upload_RejectsTraversal：附件上传拒绝越界路径
func TestFileHandler_Upload_RejectsTraversal(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	testApp := app.NewTestApp(&app.Services{FileService: fileSvc})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	handler := NewFileHandler(testApp, wss)

	c, w := newFolderTestContext("POST", "/api/file?path=../evil&vault=v", "", 1)
	handler.Upload(c)
	assertResponseCode(t, w, code.ErrorInvalidPath.Code())
}
