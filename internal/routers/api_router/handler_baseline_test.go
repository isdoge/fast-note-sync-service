package api_router

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// decodeResData decodes the "data" object of a pkgapp.Res response.
// decodeResData 解码 pkgapp.Res 响应里的 data 对象。
func decodeResData(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body struct {
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data
}

// 无条件写：未提供任何基线字段时，Baseline 必须保持 nil，并沿用 UpdateCheckWithNote 的旧路径。
func TestRESTNoteCreateOrUpdate_NoBaselineKeepsLegacyPath(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)
	mockNoteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteUpdateCheckRequest")).
		Return("", (*domain.Note)(nil), (*dto.NoteDTO)(nil), nil).Once()

	var captured *dto.NoteModifyOrCreateRequest
	mockNoteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteModifyOrCreateRequest"), false, mock.Anything).
		Run(func(args mock.Arguments) { captured = args.Get(2).(*dto.NoteModifyOrCreateRequest) }).
		Return(true, &dto.NoteDTO{Path: "a.md", Revision: "rev1"}, nil).Once()

	handler := newTestNoteHandler(mockNoteSvc, nil)
	c, w := newNoteTestContext("POST", "/api/note", `{"vault":"main","path":"a.md","content":"hello"}`, 1)

	handler.CreateOrUpdate(c)

	assertResponseCode(t, w, code.Success.Code())
	require.NotNil(t, captured)
	assert.Nil(t, captured.Baseline, "未传基线字段时必须保持 nil，沿用无条件写入")
	mockNoteSvc.AssertExpectations(t)
}

// 条件写：提供 baseRevision 时必须跳过 UpdateCheckWithNote（它会写 mtime），
// 并把 Baseline 传入服务层 CAS。
func TestRESTNoteCreateOrUpdate_ConditionalSkipsUpdateCheck(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)

	var captured *dto.NoteModifyOrCreateRequest
	mockNoteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteModifyOrCreateRequest"), false, mock.Anything).
		Run(func(args mock.Arguments) { captured = args.Get(2).(*dto.NoteModifyOrCreateRequest) }).
		Return(false, &dto.NoteDTO{Path: "a.md", Revision: "rev2"}, nil).Once()

	handler := newTestNoteHandler(mockNoteSvc, nil)
	body := `{"vault":"main","path":"a.md","content":"hello","baseRevision":"rev1","baseHash":"h1"}`
	c, w := newNoteTestContext("POST", "/api/note", body, 1)

	handler.CreateOrUpdate(c)

	assertResponseCode(t, w, code.Success.Code())
	require.NotNil(t, captured)
	require.NotNil(t, captured.Baseline, "提供基线字段时必须启用条件写")
	assert.Equal(t, "rev1", captured.Baseline.BaseRevision)
	assert.True(t, captured.Baseline.HasBase)
	assert.Equal(t, "h1", captured.Baseline.BaseHash)
	// 条件写路径不得调用会写 mtime 的 UpdateCheckWithNote。
	mockNoteSvc.AssertNotCalled(t, "UpdateCheckWithNote", mock.Anything, mock.Anything, mock.Anything)
	mockNoteSvc.AssertExpectations(t)
}

// 条件写冲突：服务层返回 BaselineConflictError 时，REST 必须回 530 并附带结构化冲突体，
// 且不调用 UpdateCheckWithNote（零 mtime 副作用）。
func TestRESTNoteCreateOrUpdate_ConditionalConflictReturns530(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)
	conflict := &dto.BaselineConflictError{
		Reason:         dto.SyncConflictReasonRevisionChanged,
		ServerActive:   true,
		ServerRevision: "rev-server",
		ServerHash:     "h-server",
	}
	mockNoteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteModifyOrCreateRequest"), false, mock.Anything).
		Return(false, (*dto.NoteDTO)(nil), conflict).Once()
	// 冲突体回填活跃正文时会读一次笔记。
	mockNoteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(mockNoteSvc)
	mockNoteSvc.On("Get", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteGetRequest")).
		Return(&dto.NoteDTO{Path: "a.md", Content: "server-body"}, nil).Maybe()

	handler := newTestNoteHandler(mockNoteSvc, nil)
	body := `{"vault":"main","path":"a.md","content":"local","baseRevision":"rev-stale"}`
	c, w := newNoteTestContext("POST", "/api/note", body, 1)

	handler.CreateOrUpdate(c)

	assertResponseCode(t, w, code.ErrorSyncConflict.Code())
	data := decodeResData(t, w)
	assert.Equal(t, true, data["conflict"])
	assert.Equal(t, dto.SyncConflictReasonRevisionChanged, data["reason"])
	assert.Equal(t, "rev-server", data["serverRevision"])
	assert.Equal(t, "h-server", data["serverHash"])
	assert.Equal(t, "server-body", data["serverContent"])
	assert.Equal(t, dto.SyncConflictKindNote, data["kind"])
	mockNoteSvc.AssertNotCalled(t, "UpdateCheckWithNote", mock.Anything, mock.Anything, mock.Anything)
}

// 无条件删除保持原有的存在性预检；条件删除跳过预检并交由服务层 CAS。
func TestRESTNoteDelete_NoBaselinePrecheckRuns(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)
	mockNoteSvc.On("Get", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteGetRequest")).
		Return(&dto.NoteDTO{Path: "a.md"}, nil).Once()

	var captured *dto.NoteDeleteRequest
	mockNoteSvc.On("Delete", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteDeleteRequest")).
		Run(func(args mock.Arguments) { captured = args.Get(2).(*dto.NoteDeleteRequest) }).
		Return(&dto.NoteDTO{Path: "a.md"}, nil).Once()

	handler := newTestNoteHandler(mockNoteSvc, nil)
	c, w := newNoteTestContext("DELETE", "/api/note?vault=main&path=a.md", "", 1)

	handler.Delete(c)

	assertResponseCode(t, w, code.Success.Code())
	require.NotNil(t, captured)
	assert.Nil(t, captured.Baseline)
	mockNoteSvc.AssertExpectations(t)
}

// 条件删除冲突：服务层拒绝时回 530，且不广播、不返回成功 DTO。
func TestRESTNoteDelete_ConditionalConflictReturns530(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)
	conflict := &dto.BaselineConflictError{
		Reason:         dto.SyncConflictReasonLocalDeleted,
		ServerActive:   true,
		ServerRevision: "rev-live",
		ServerHash:     "h-live",
	}
	var captured *dto.NoteDeleteRequest
	mockNoteSvc.On("Delete", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteDeleteRequest")).
		Run(func(args mock.Arguments) { captured = args.Get(2).(*dto.NoteDeleteRequest) }).
		Return((*dto.NoteDTO)(nil), conflict).Once()
	mockNoteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(mockNoteSvc)
	mockNoteSvc.On("Get", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteGetRequest")).
		Return(&dto.NoteDTO{Path: "a.md", Content: "live"}, nil).Maybe()

	handler := newTestNoteHandler(mockNoteSvc, nil)
	c, w := newNoteTestContext("DELETE", "/api/note?vault=main&path=a.md&baseRevision=rev-stale", "", 1)

	handler.Delete(c)

	assertResponseCode(t, w, code.ErrorSyncConflict.Code())
	data := decodeResData(t, w)
	assert.Equal(t, true, data["conflict"])
	assert.Equal(t, dto.SyncConflictReasonLocalDeleted, data["reason"])
	assert.Equal(t, "rev-live", data["serverRevision"])
	require.NotNil(t, captured)
	require.NotNil(t, captured.Baseline)
	assert.Equal(t, "rev-stale", captured.Baseline.BaseRevision)
	// 条件删除跳过存在性预检：最多只允许冲突体回填正文的那一次 Get。
	mockNoteSvc.AssertNumberOfCalls(t, "Get", 1)
}

// 附件条件删除冲突：回 530，kind=file。
func TestRESTFileDelete_ConditionalConflictReturns530(t *testing.T) {
	mockFileSvc := new(svcmocks.MockFileService)
	conflict := &dto.BaselineConflictError{
		Reason:         dto.SyncConflictReasonLocalDeleted,
		ServerActive:   true,
		ServerRevision: "frev-live",
		ServerHash:     "fh-live",
	}
	var captured *dto.FileDeleteRequest
	mockFileSvc.On("Delete", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileDeleteRequest")).
		Run(func(args mock.Arguments) { captured = args.Get(2).(*dto.FileDeleteRequest) }).
		Return((*dto.FileDTO)(nil), conflict).Once()

	handler := newTestFileHandler(mockFileSvc)
	c, w := newFileTestContext("DELETE", "/api/file?vault=main&path=a.png&pathHash=h1&baseRevision=frev-stale", "", 1)

	handler.Delete(c)

	assertResponseCode(t, w, code.ErrorSyncConflict.Code())
	data := decodeResData(t, w)
	assert.Equal(t, true, data["conflict"])
	assert.Equal(t, dto.SyncConflictKindFile, data["kind"])
	assert.Equal(t, "frev-live", data["serverRevision"])
	require.NotNil(t, captured)
	require.NotNil(t, captured.Baseline)
}

// 附件上传无条件兼容：未传基线字段时 Baseline 保持 nil。
func TestRESTFileUpload_NoBaselineKeepsLegacyPath(t *testing.T) {
	t.Chdir(t.TempDir())
	mockFileSvc := new(svcmocks.MockFileService)
	var captured *dto.FileUpdateRequest
	mockFileSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileUpdateRequest"), false).
		Run(func(args mock.Arguments) { captured = args.Get(2).(*dto.FileUpdateRequest) }).
		Return(true, &dto.FileDTO{Path: "a.png", Revision: "frev1"}, nil).Once()

	handler := newTestFileHandler(mockFileSvc)
	c, w := newUploadContext(t, nil)

	handler.Upload(c)

	assertResponseCode(t, w, code.Success.Code())
	require.NotNil(t, captured)
	assert.Nil(t, captured.Baseline)
}

// 附件上传条件写：显式 baseRevision 时启用 Baseline，冲突回 530。
func TestRESTFileUpload_ConditionalConflictReturns530(t *testing.T) {
	t.Chdir(t.TempDir())
	mockFileSvc := new(svcmocks.MockFileService)
	conflict := &dto.BaselineConflictError{
		Reason:         dto.SyncConflictReasonRevisionChanged,
		ServerActive:   true,
		ServerRevision: "frev-server",
		ServerHash:     "fh-server",
	}
	var captured *dto.FileUpdateRequest
	mockFileSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileUpdateRequest"), false).
		Run(func(args mock.Arguments) { captured = args.Get(2).(*dto.FileUpdateRequest) }).
		Return(false, (*dto.FileDTO)(nil), conflict).Once()

	handler := newTestFileHandler(mockFileSvc)
	fields := map[string]string{"baseRevision": "frev-stale", "baseHash": "fh-old"}
	c, w := newUploadContext(t, fields)

	handler.Upload(c)

	assertResponseCode(t, w, code.ErrorSyncConflict.Code())
	data := decodeResData(t, w)
	assert.Equal(t, true, data["conflict"])
	assert.Equal(t, dto.SyncConflictKindFile, data["kind"])
	assert.Equal(t, "frev-server", data["serverRevision"])
	require.NotNil(t, captured)
	require.NotNil(t, captured.Baseline)
	assert.Equal(t, "frev-stale", captured.Baseline.BaseRevision)
	assert.True(t, captured.Baseline.HasBase)
}

// newUploadContext builds a multipart upload context, adding extra form fields.
// newUploadContext 构造 multipart 上传上下文，并附加额外表单字段。
func newUploadContext(t *testing.T, extra map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	require.NoError(t, writer.WriteField("vault", "main"))
	require.NoError(t, writer.WriteField("path", "a.png"))
	for k, v := range extra {
		require.NoError(t, writer.WriteField(k, v))
	}
	part, err := writer.CreateFormFile("file", "a.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("fake-png-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/file", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Client", "Web")
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_token", &pkgapp.UserEntity{UID: 1})
	return c, w
}

// 列表响应必须带 revision，REST 客户端才能从列表直接取版本令牌做条件写。
func TestRESTNoteList_IncludesRevision(t *testing.T) {
	mockNoteSvc := new(svcmocks.MockNoteService)
	mockNoteSvc.On("List", mock.Anything, int64(1), mock.AnythingOfType("*dto.NoteListRequest"), mock.AnythingOfType("*app.Pager")).
		Return([]*dto.NoteNoContentDTO{{ID: 1, Path: "a.md", Revision: "rev-list"}}, 1, nil).Once()

	handler := newTestNoteHandler(mockNoteSvc, nil)
	c, w := newNoteTestContext("GET", "/api/notes?vault=main&page=1&size=10", "", 1)

	handler.List(c)

	assertResponseCode(t, w, code.Success.Code())
	var body struct {
		Data struct {
			List []map[string]interface{} `json:"list"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data.List, 1)
	assert.Equal(t, "rev-list", body.Data.List[0]["revision"])
}
