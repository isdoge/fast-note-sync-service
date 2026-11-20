package api_router

import (
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

// newFolderTestContext creates a gin.Context suitable for FolderHandler tests.
func newFolderTestContext(method, url, body string, uid int64) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, url, nil)
	}

	c, _ := gin.CreateTestContext(w)
	c.Request = req
	if uid > 0 {
		c.Set("user_token", &pkgapp.UserEntity{UID: uid})
	}
	return c, w
}

func newTestFolderHandler(folderSvc *svcmocks.MockFolderService) *FolderHandler {
	folderSvc.On("WithClient", "", "", "").Maybe().Return(folderSvc)
	testApp := app.NewTestApp(&app.Services{
		FolderService: folderSvc,
	})
	folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc)
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	return NewFolderHandler(testApp, wss)
}

// TestFolderHandler_Get_Success verifies successful folder fetch
func TestFolderHandler_Get_Success(t *testing.T) {
	mockSvc := new(svcmocks.MockFolderService)

	mockData := &dto.FolderDTO{
		ID:       1,
		Path:     "folder1",
		PathHash: "h1",
	}

	mockSvc.On("Get", mock.Anything, int64(1), mock.AnythingOfType("*dto.FolderGetRequest")).
		Return(mockData, nil)

	handler := newTestFolderHandler(mockSvc)
	c, w := newFolderTestContext("GET", "/api/folder", "", 1)
	c.Request.URL.RawQuery = "vault=main&path=folder1"

	handler.Get(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	mockSvc.AssertExpectations(t)
}

// TestFolderHandler_List_Success verifies successful folder list fetch
func TestFolderHandler_List_Success(t *testing.T) {
	mockSvc := new(svcmocks.MockFolderService)

	listData := []*dto.FolderDTO{
		{ID: 1, Path: "f1"},
		{ID: 2, Path: "f2"},
	}

	mockSvc.On("List", mock.Anything, int64(1), mock.AnythingOfType("*dto.FolderListRequest")).
		Return(listData, nil)

	handler := newTestFolderHandler(mockSvc)
	c, w := newFolderTestContext("GET", "/api/folders", "", 1)
	c.Request.URL.RawQuery = "vault=main"

	handler.List(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	mockSvc.AssertExpectations(t)
}

// TestFolderHandler_Create_Success verifies successful folder creation
func TestFolderHandler_Create_Success(t *testing.T) {
	mockSvc := new(svcmocks.MockFolderService)

	createdFolder := &dto.FolderDTO{ID: 3, Path: "f3"}
	mockSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.FolderCreateRequest")).
		Return(createdFolder, nil)

	handler := newTestFolderHandler(mockSvc)
	body := `{"vault":"main", "path":"f3"}`
	c, w := newFolderTestContext("POST", "/api/folder", body, 1)

	handler.Create(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	mockSvc.AssertExpectations(t)
}

// TestFolderHandler_Delete_Success verifies successful folder deletion
func TestFolderHandler_Delete_Success(t *testing.T) {
	mockSvc := new(svcmocks.MockFolderService)

	mockSvc.On("Delete", mock.Anything, int64(1), mock.AnythingOfType("*dto.FolderDeleteRequest")).
		Return(&dto.FolderDTO{ID: 3, Path: "f3", PathHash: "h3"}, nil)

	handler := newTestFolderHandler(mockSvc)
	body := `{"vault":"main", "path":"f3"}`
	c, w := newFolderTestContext("DELETE", "/api/folder", body, 1)

	handler.Delete(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	mockSvc.AssertExpectations(t)
}

func newFolderHandlerWithWSS(svc *svcmocks.MockFolderService) (*FolderHandler, *pkgapp.WebsocketServer) {
	svc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Maybe().Return(svc)
	testApp := app.NewTestApp(&app.Services{FolderService: svc})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	return NewFolderHandler(testApp, wss), wss
}

// REST 目录创建/删除必须像 WebSocket 路径一样广播同结构的 FolderSyncModify /
// FolderSyncDelete（带库名），让其它设备和 WebGUI 的实时流收到变更。
func TestFolderHandler_WriteBroadcastsFolderSyncEvents(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		mockSvc := new(svcmocks.MockFolderService)
		folder := &dto.FolderDTO{ID: 3, Path: "f3", PathHash: "h3", Ctime: 11, Mtime: 12, UpdatedTimestamp: 13}
		mockSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.FolderCreateRequest")).
			Return(folder, nil)
		handler, wss := newFolderHandlerWithWSS(mockSvc)
		sub, err := wss.SubscribeEvents("1", 0, "", nil)
		require.NoError(t, err)
		defer wss.UnsubscribeEvents("1", sub)
		other, err := wss.SubscribeEvents("2", 0, "", nil)
		require.NoError(t, err)
		defer wss.UnsubscribeEvents("2", other)

		c, w := newFolderTestContext("POST", "/api/folder", `{"vault":"main","path":"f3"}`, 1)
		handler.Create(c)

		assertResponseCode(t, w, code.Success.Code())
		select {
		case event := <-sub.Events():
			require.Equal(t, "FolderSyncModify", event.Action)
			require.Equal(t, "main", event.Vault)
			require.Equal(t, "f3", event.Path)
			require.Equal(t, "h3", event.PathHash)
			require.Equal(t, int64(13), event.UpdatedTimestamp)
		default:
			t.Fatal("目录创建未广播 FolderSyncModify")
		}
		select {
		case event := <-other.Events():
			t.Fatalf("广播不得送达其它用户: %+v", event)
		default:
		}
	})

	t.Run("delete", func(t *testing.T) {
		mockSvc := new(svcmocks.MockFolderService)
		folder := &dto.FolderDTO{ID: 3, Path: "f3", PathHash: "h3", Ctime: 21, Mtime: 22, UpdatedTimestamp: 23}
		mockSvc.On("Delete", mock.Anything, int64(1), mock.AnythingOfType("*dto.FolderDeleteRequest")).
			Return(folder, nil)
		handler, wss := newFolderHandlerWithWSS(mockSvc)
		sub, err := wss.SubscribeEvents("1", 0, "", nil)
		require.NoError(t, err)
		defer wss.UnsubscribeEvents("1", sub)

		c, w := newFolderTestContext("DELETE", "/api/folder", `{"vault":"main","path":"f3"}`, 1)
		handler.Delete(c)

		assertResponseCode(t, w, code.Success.Code())
		select {
		case event := <-sub.Events():
			require.Equal(t, "FolderSyncDelete", event.Action)
			require.Equal(t, "main", event.Vault)
			require.Equal(t, "f3", event.Path)
			require.Equal(t, "h3", event.PathHash)
		default:
			t.Fatal("目录删除未广播 FolderSyncDelete")
		}
	})
}
