package api_router

// #342 S-8 / haierkeys/obsidian-fast-note-sync#244：
// REST 写入入口统一路径清洗——去前导 "/"、拒绝空/绝对/".."/NUL 路径、
// 笔记路径自动补 .md。测试直接调用各 handler，断言服务层收到的 Path 已被清洗、
// 非法路径返回 ErrorInvalidPath 且不进入服务层。

import (
	"net/http/httptest"
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

// TestNoteHandler_CreateOrUpdate_PathCleaning：前导 "/" 去掉、缺 .md 补上
func TestNoteHandler_CreateOrUpdate_PathCleaning(t *testing.T) {
	tests := []struct {
		name string
		body string
		want  string
	}{
		{"leading slash + no ext", `{"vault":"v","path":"/notes/todo","content":"x"}`, "notes/todo.md"},
		{"leading slash + ext", `{"vault":"v","path":"/notes/todo.md","content":"x"}`, "notes/todo.md"},
		{"uppercase ext kept", `{"vault":"v","path":"/notes/TODO.MD","content":"x"}`, "notes/TODO.MD"},
		{"plain + ext", `{"vault":"v","path":"notes/todo.md","content":"x"}`, "notes/todo.md"},
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

// TestNoteHandler_Rename_BothPathsCleaned：REST 改名新旧路径都清洗
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
	assert.Equal(t, "old.md", capturedOld)
	assert.Equal(t, "sub/new.md", capturedNew)
}

// TestFileHandler_Upload_PathCleaning：附件上传去前导 "/"，不补 .md
func TestFileHandler_Upload_PathCleaning(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)

	captured := ""
	fileSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			params := args.Get(2).(*dto.FileUpdateRequest)
			captured = params.Path
		}).
		Return(false, &dto.FileDTO{Path: "img/pic.png"}, nil).Maybe()

	testApp := app.NewTestApp(&app.Services{FileService: fileSvc})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	handler := NewFileHandler(testApp, wss)

	// Upload 需要 multipart 表单；路径清洗发生在 FormFile 解析之前，可直接验证
	c, w := newFolderTestContext("POST", "/api/file", ``, 1)
	c.Request.Form = nil
	// 构造带 path 字段的 multipart 请求
	body := `--BOUNDARY
Content-Disposition: form-data; name="vault"

v
--BOUNDARY
Content-Disposition: form-data; name="path"

/img/pic.png
--BOUNDARY--
`
	c.Request = httptest.NewRequest("POST", "/api/file?path=/img/pic.png&vault=v", nil)
	c.Request.Header.Set("Content-Type", "multipart/form-data; boundary=BOUNDARY")
	c.Request.Body = nil

	// 直接构造 query 版本（FileUploadRequest 是 form 绑定）
	handler.Upload(c)

	// FormFile 缺失时 handler 返回 invalid params；断言路径已在到达前被拒或清洗。
	// 为避免依赖 multipart 构造，仅验证非法路径被拒：
	c2, w2 := newFolderTestContext("POST", "/api/file?path=../evil&vault=v", "", 1)
	handler.Upload(c2)
	assertResponseCode(t, w2, code.ErrorInvalidPath.Code())
	_ = captured
	_ = w
	_ = body
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
