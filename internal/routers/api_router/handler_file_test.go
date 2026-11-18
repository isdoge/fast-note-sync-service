package api_router

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newFileTestContext creates a gin.Context suitable for FileHandler tests.
func newFileTestContext(method, url, body string, uid int64) (*gin.Context, *httptest.ResponseRecorder) {
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

func newTestFileHandler(fileSvc *svcmocks.MockFileService) *FileHandler {
	testApp := app.NewTestApp(&app.Services{
		FileService: fileSvc,
	})
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	return NewFileHandler(testApp, wss)
}

// TestFileHandler_Get_Success verifies successful file fetch
// TestFileHandler_Get_Success 验证成功获取文件
func TestFileHandler_Get_Success(t *testing.T) {
	mockSvc := new(svcmocks.MockFileService)

	mockData := &dto.FileDTO{
		ID:       1,
		Path:     "file1.jpg",
		PathHash: "h1",
	}

	mockSvc.On("Get", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileGetRequest")).
		Return(mockData, nil)

	handler := newTestFileHandler(mockSvc)
	c, w := newFileTestContext("GET", "/api/file/info?vault=main&path=file1.jpg", "", 1)

	handler.Get(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
}

// TestFileHandler_Get_TombstoneIsNotFound verifies a soft-deleted row is reported as missing
// unless the recycle bin is requested.
// TestFileHandler_Get_TombstoneIsNotFound 软删除记录在未指定 isRecycle 时按"不存在"返回，
// 否则墓碑会被当成大小、时间、哈希都匹配的正常文件。
func TestFileHandler_Get_TombstoneIsNotFound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"default", "", code.ErrorFileNotFound.Code()},
		{"isRecycle=false", "&isRecycle=false", code.ErrorFileNotFound.Code()},
		{"isRecycle=true", "&isRecycle=true", code.Success.Code()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockSvc := new(svcmocks.MockFileService)
			mockSvc.On("Get", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileGetRequest")).
				Return(&dto.FileDTO{ID: 1, Path: "file1.jpg", PathHash: "h1", Size: 3, Mtime: 1700000000000, ContentHash: "c1", Action: "delete"}, nil)

			handler := newTestFileHandler(mockSvc)
			c, w := newFileTestContext("GET", "/api/file/info?vault=main&path=file1.jpg"+tc.query, "", 1)

			handler.Get(c)

			assert.Equal(t, http.StatusOK, w.Code)
			assertResponseCode(t, w, tc.want)
		})
	}
}

// TestFileHandler_List_Success verifies successful file list fetch
// TestFileHandler_List_Success 验证成功获取文件列表
func TestFileHandler_List_Success(t *testing.T) {
	mockSvc := new(svcmocks.MockFileService)

	listData := []*dto.FileDTO{
		{ID: 1, Path: "f1.jpg"},
		{ID: 2, Path: "f2.png"},
	}

	mockSvc.On("List", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileListRequest"), mock.AnythingOfType("*app.Pager")).
		Return(listData, 2, nil)

	handler := newTestFileHandler(mockSvc)
	c, w := newFileTestContext("GET", "/api/files?vault=main&page=1", "", 1)

	handler.List(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
}

// TestFileHandler_Delete_Success verifies successful file deletion
// TestFileHandler_Delete_Success 验证成功删除文件
func TestFileHandler_Delete_Success(t *testing.T) {
	mockSvc := new(svcmocks.MockFileService)

	deletedFile := &dto.FileDTO{ID: 3, Path: "f3.zip"}
	mockSvc.On("Delete", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileDeleteRequest")).
		Return(deletedFile, nil)

	handler := newTestFileHandler(mockSvc)
	body := `{"vault":"main", "path":"f3.zip", "pathHash":"f3_hash"}`
	c, w := newFileTestContext("DELETE", "/api/file", body, 1)

	handler.Delete(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
}

// TestFileHandler_Upload_NormalizesSecondTimestamps verifies #461 for the attachment write
// path: seconds-scale ctime/mtime form fields are normalized to milliseconds before the file
// metadata is written.
// TestFileHandler_Upload_NormalizesSecondTimestamps 验证附件写入路径的 #461：秒级
// ctime/mtime 表单字段在写入文件元数据前被归一化为毫秒。
func TestFileHandler_Upload_NormalizesSecondTimestamps(t *testing.T) {
	t.Chdir(t.TempDir())
	mockFileSvc := new(svcmocks.MockFileService)

	var writeParams *dto.FileUpdateRequest
	mockFileSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.FileUpdateRequest"), false).
		Run(func(args mock.Arguments) {
			writeParams = args.Get(2).(*dto.FileUpdateRequest)
		}).
		Return(true, &dto.FileDTO{ID: 1, Path: "a.png"}, nil)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	require.NoError(t, writer.WriteField("vault", "main"))
	require.NoError(t, writer.WriteField("path", "a.png"))
	require.NoError(t, writer.WriteField("ctime", "1651719914"))
	require.NoError(t, writer.WriteField("mtime", "1651719914"))
	part, err := writer.CreateFormFile("file", "a.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("fake-png-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	handler := newTestFileHandler(mockFileSvc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/file/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Client", "Web")
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_token", &pkgapp.UserEntity{UID: 1})

	handler.Upload(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	if assert.NotNil(t, writeParams) {
		assert.Equal(t, int64(1651719914000), writeParams.Mtime, "seconds mtime must be normalized to milliseconds before writing")
		assert.Equal(t, int64(1651719914000), writeParams.Ctime, "seconds ctime must be normalized to milliseconds before writing")
	}
}
