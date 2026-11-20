package api_router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// newNoteTestContext creates a gin.Context suitable for NoteHandler tests.
func newNoteTestContext(method, url, body string, uid int64) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	req.Header.Set("X-Client", "Web")

	c, _ := gin.CreateTestContext(w)
	c.Request = req
	if uid > 0 {
		c.Set("user_token", &pkgapp.UserEntity{UID: uid})
	}
	return c, w
}

// newTestNoteHandler creates a NoteHandler with mock services.
func newTestNoteHandler(noteSvc *svcmocks.MockNoteService, fileSvc *svcmocks.MockFileService) *NoteHandler {
	testApp := app.NewTestApp(&app.Services{
		NoteService: noteSvc,
		FileService: fileSvc,
	})
	if noteSvc != nil {
		noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc)
	}
	if fileSvc != nil {
		fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	}
	// WSS is used in NoteHandler for broadcasting
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	return NewNoteHandler(testApp, wss)
}

// TestNoteHandler_Get_Success verifies successful note fetch
// TestNoteHandler_Get_Success 验证成功获取笔记
func TestNoteHandler_Get_Success(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)
	mockFileSvc := new(svcmocks.MockFileService)

	mockData := &dto.NoteDTO{
		ID:       1,
		Path:     "test.md",
		PathHash: "hash123",
		Content:  "content",
	}

	mockNoteSvc.On("Get", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteGetRequest")).
		Return(mockData, nil)

	mockFileSvc.On("ResolveEmbedLinks", mock.Anything, int64(1), "main", "test.md", "content").
		Return(map[string]string{}, nil)

	handler := newTestNoteHandler(mockNoteSvc, mockFileSvc)
	c, w := newNoteTestContext("GET", "/api/note", "", 1)

	// Simulate binding query params
	c.Request.URL.RawQuery = "vault=main&path=test.md"
	handler.Get(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
}

// TestNoteHandler_List_Success verifies successful note list fetch
// TestNoteHandler_List_Success 验证成功获取笔记列表
func TestNoteHandler_List_Success(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)
	listData := []*dto.NoteNoContentDTO{
		{ID: 1, Path: "test1.md"},
		{ID: 2, Path: "test2.md"},
	}

	mockNoteSvc.On("List", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteListRequest"), mock.AnythingOfType("*app.Pager")).
		Return(listData, 2, nil)

	handler := newTestNoteHandler(mockNoteSvc, nil)
	c, w := newNoteTestContext("GET", "/api/notes", "", 1)
	c.Request.URL.RawQuery = "vault=main&page=1&size=10"

	handler.List(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
}

// TestNoteHandler_CreateOrUpdate_Success verifies successful note creation/update
// TestNoteHandler_CreateOrUpdate_Success 验证成功创建或更新笔记
func TestNoteHandler_CreateOrUpdate_Success(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)

	mockNoteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteUpdateCheckRequest")).
		Return("", (*domain.Note)(nil), (*dto.NoteDTO)(nil), nil)

	createdNote := &dto.NoteDTO{ID: 2, Path: "create.md"}
	mockNoteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteModifyOrCreateRequest"), false, mock.Anything).
		Return(true, createdNote, nil)

	handler := newTestNoteHandler(mockNoteSvc, nil)
	body := `{"vault":"main", "path":"create.md", "content":"hello"}`
	c, w := newNoteTestContext("POST", "/api/note", body, 1)

	handler.CreateOrUpdate(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
}

// TestNoteHandler_Delete_Success verifies successful note deletion
// TestNoteHandler_Delete_Success 验证成功删除笔记
func TestNoteHandler_Delete_Success(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)

	existingNote := &dto.NoteDTO{ID: 3, Path: "del.md", Action: ""}
	mockNoteSvc.On("Get", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteGetRequest")).
		Return(existingNote, nil)

	mockNoteSvc.On("Delete", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteDeleteRequest")).
		Return(existingNote, nil)

	handler := newTestNoteHandler(mockNoteSvc, nil)
	c, w := newNoteTestContext("DELETE", "/api/note", "", 1)
	c.Request.URL.RawQuery = "vault=main&path=del.md"

	handler.Delete(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
}

// TestNoteHandler_CreateOrUpdate_NormalizesSecondTimestamps verifies #461: when a third-party
// client sends ctime/mtime in seconds, the handler normalizes them to milliseconds before the
// values reach the update check and the write path, so stored notes no longer display as 1970.
// TestNoteHandler_CreateOrUpdate_NormalizesSecondTimestamps 验证 #461：第三方客户端传秒级
// ctime/mtime 时，handler 在进入更新检查与写入路径前归一化为毫秒，避免笔记时间显示为 1970 年。
func TestNoteHandler_CreateOrUpdate_NormalizesSecondTimestamps(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)

	var checkParams *dto.NoteUpdateCheckRequest
	mockNoteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteUpdateCheckRequest")).
		Run(func(args mock.Arguments) {
			checkParams = args.Get(2).(*dto.NoteUpdateCheckRequest)
		}).
		Return("", (*domain.Note)(nil), (*dto.NoteDTO)(nil), nil)

	var writeParams *dto.NoteModifyOrCreateRequest
	createdNote := &dto.NoteDTO{ID: 2, Path: "create.md"}
	mockNoteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteModifyOrCreateRequest"), false, mock.Anything).
		Run(func(args mock.Arguments) {
			writeParams = args.Get(2).(*dto.NoteModifyOrCreateRequest)
		}).
		Return(true, createdNote, nil)

	handler := newTestNoteHandler(mockNoteSvc, nil)
	body := `{"vault":"main", "path":"create.md", "content":"hello", "ctime":1651719914, "mtime":1651719914}`
	c, w := newNoteTestContext("POST", "/api/note", body, 1)

	handler.CreateOrUpdate(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	if assert.NotNil(t, writeParams) {
		assert.Equal(t, int64(1651719914000), writeParams.Mtime, "seconds mtime must be normalized to milliseconds before writing")
		assert.Equal(t, int64(1651719914000), writeParams.Ctime, "seconds ctime must be normalized to milliseconds before writing")
	}
	if assert.NotNil(t, checkParams) {
		assert.Equal(t, int64(1651719914000), checkParams.Mtime, "seconds mtime must be normalized before the update check comparison")
	}
}

// TestNoteHandler_CreateOrUpdate_KeepsMillisecondTimestamps verifies millisecond values pass
// through unchanged, so existing clients (plugin, CLI) are unaffected by the #461 fix.
// TestNoteHandler_CreateOrUpdate_KeepsMillisecondTimestamps 验证毫秒值原样透传，既有客户端
// （插件、CLI）不受 #461 修复影响。
func TestNoteHandler_CreateOrUpdate_KeepsMillisecondTimestamps(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)

	mockNoteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteUpdateCheckRequest")).
		Return("", (*domain.Note)(nil), (*dto.NoteDTO)(nil), nil)

	var writeParams *dto.NoteModifyOrCreateRequest
	createdNote := &dto.NoteDTO{ID: 2, Path: "create.md"}
	mockNoteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteModifyOrCreateRequest"), false, mock.Anything).
		Run(func(args mock.Arguments) {
			writeParams = args.Get(2).(*dto.NoteModifyOrCreateRequest)
		}).
		Return(true, createdNote, nil)

	handler := newTestNoteHandler(mockNoteSvc, nil)
	body := `{"vault":"main", "path":"create.md", "content":"hello", "ctime":1651719914000, "mtime":1651719914000}`
	c, w := newNoteTestContext("POST", "/api/note", body, 1)

	handler.CreateOrUpdate(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	if assert.NotNil(t, writeParams) {
		assert.Equal(t, int64(1651719914000), writeParams.Mtime)
		assert.Equal(t, int64(1651719914000), writeParams.Ctime)
	}
}

// withRestAuthScope injects the entry-time authentication values that handler-level
// function checks read (middleware sets them on the real request path).
// withRestAuthScope 注入 handler 级功能点检查读取的入口鉴权上下文
// （真实请求由中间件写入）。
func withRestAuthScope(c *gin.Context, scope, client string) {
	c.Set("scope", scope)
	c.Set("auth_rest_client", client)
	c.Set("auth_strict_empty_protocol", false)
}

// 整库清空回收站需要专属 note_purge；单条清理维持 note_w。路由层接受任一能力，
// 由 handler 按请求体区分，与 MCP note_recycle_clear 一致。
func TestNoteHandler_RecycleClear_WholeBinRequiresPurge(t *testing.T) {
	cases := []struct {
		name       string
		scope      string
		body       string
		wantDenied bool
	}{
		{"note_w cannot clear whole bin", "p:rest c:* f:note_w", `{"vault":"main","path":""}`, true},
		{"note_purge clears whole bin", "p:rest c:* f:note_purge", `{"vault":"main","path":""}`, false},
		{"note_w clears single path", "p:rest c:* f:note_w", `{"vault":"main","path":"a.md"}`, false},
		{"note_purge cannot clear single path", "p:rest c:* f:note_purge", `{"vault":"main","path":"a.md"}`, true},
		{"path hash counts as single path", "p:rest c:* f:note_w", `{"vault":"main","path":"","pathHash":"h1"}`, false},
		{"login token clears whole bin", "p:rest c:* f:*", `{"vault":"main","path":""}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockNoteSvc := new(svcmocks.MockNoteService)
			if !tc.wantDenied {
				mockNoteSvc.On("RecycleClear", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteRecycleClearRequest")).
					Return(nil).Once()
			}
			handler := newTestNoteHandler(mockNoteSvc, nil)
			c, w := newNoteTestContext("DELETE", "/api/note/recycle-clear", tc.body, 1)
			withRestAuthScope(c, tc.scope, "*")

			handler.RecycleClear(c)

			assert.Equal(t, http.StatusOK, w.Code)
			if tc.wantDenied {
				assertResponseCode(t, w, code.ErrorAuthTokenScopeRestricted.Code())
			} else {
				assertResponseCode(t, w, code.Success.Code())
			}
			if tc.wantDenied {
				mockNoteSvc.AssertNotCalled(t, "RecycleClear", mock.Anything, mock.Anything, mock.Anything)
			} else {
				mockNoteSvc.AssertExpectations(t)
			}
		})
	}
}

// TestNoteHandler_Restore_PurgedTombstoneReturns532 覆盖 E2E-2 / MultiDB-2：正文已被
// 清理的墓碑不在回收站可恢复集合里（recycle 查询过滤 rename=0 且 content_purged=0），
// 预查未命中时必须交给 NoteService.Restore 判定，最终返回 532 ErrorContentPurged，
// 而不是提前短路成 430 Note does not exist。
// A purged tombstone is outside the restorable recycle set, so a pre-check miss must
// delegate to NoteService.Restore and surface 532 instead of 430.
func TestNoteHandler_Restore_PurgedTombstoneReturns532(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)

	// 预查命中不到可恢复墓碑：服务层按回收站语义返回 430。
	mockNoteSvc.On("Get", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.NoteGetRequest) bool {
		return req.IsRecycle
	})).Return(nil, code.ErrorNoteNotFound).Once()
	// 真正的判定在 Restore 内部：已清理墓碑返回 532。
	mockNoteSvc.On("Restore", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteRestoreRequest")).
		Return(nil, code.ErrorContentPurged).Once()

	handler := newTestNoteHandler(mockNoteSvc, nil)
	c, w := newNoteTestContext("PUT", "/api/note/restore", `{"vault":"main","path":"purged.md"}`, 1)

	handler.Restore(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.ErrorContentPurged.Code())
	mockNoteSvc.AssertExpectations(t)
}

// TestNoteHandler_Restore_RestorableTombstoneSucceeds 保证预查放行后的正常恢复路径不变：
// 回收站命中删除态 → 执行恢复并返回成功。
func TestNoteHandler_Restore_RestorableTombstoneSucceeds(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)

	mockNoteSvc.On("Get", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.NoteGetRequest) bool {
		return req.IsRecycle
	})).Return(&dto.NoteDTO{ID: 9, Path: "del.md", Action: "delete"}, nil).Once()
	mockNoteSvc.On("Restore", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteRestoreRequest")).
		Return(&dto.NoteDTO{ID: 9, Path: "del.md", Action: "modify", Revision: "rev-2"}, nil).Once()

	handler := newTestNoteHandler(mockNoteSvc, nil)
	c, w := newNoteTestContext("PUT", "/api/note/restore", `{"vault":"main","path":"del.md"}`, 1)

	handler.Restore(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	mockNoteSvc.AssertExpectations(t)
}
