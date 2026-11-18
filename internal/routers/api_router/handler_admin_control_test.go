package api_router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// 公开配置返回全文索引的实际生效状态（前端据此决定是否提供内容检索）；
// 管理设置表单回显保存的配置值（修改后重启生效），不因平台限制被改写。
func TestAdminControlHandler_FTSReportsEffectiveState(t *testing.T) {
	handler, a, userSvc := newTestAdminHandler()
	// 模拟配置请求启用、平台限制使实际管理器禁用的情况。
	a.Config().App.FtsBleveEnabled = util.Ptr(true)
	userSvc.On("IsRegisterEnabled", mock.Anything).Return(true)
	c, w := newAdminTestContext("GET", "/api/webgui/config", "", 0)
	handler.Config(c)
	var public struct {
		Data dto.AdminWebGUIConfig `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &public))
	assert.False(t, public.Data.FtsBleveEnabled)
	c, w = newAdminTestContext("GET", "/api/admin/config", "", 1)
	handler.GetConfig(c)
	var admin struct {
		Data dto.AdminConfig `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &admin))
	if assert.NotNil(t, admin.Data.FtsBleveEnabled) {
		assert.True(t, *admin.Data.FtsBleveEnabled)
	}
	assert.True(t, *a.Config().App.FtsBleveEnabled, "保留保存到配置文件的请求值")
}

func newAdminTestContext(method, url, body string, uid int64) (*gin.Context, *httptest.ResponseRecorder) {
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

func newTestAdminHandler() (*AdminControlHandler, *app.App, *mocks.MockUserService) {
	mockUserSvc := new(mocks.MockUserService)
	svcs := &app.Services{
		UserService: mockUserSvc,
	}
	testApp := app.NewTestApp(svcs)
	testApp.Dao = dao.New(nil, context.Background(),
		dao.WithBleveManager(dao.NewBleveManager(util.Ptr(false), nil, nil)))
	// Set mock config values
	cfg := testApp.Config()
	cfg.User.AdminUID = 1
	cfg.WebGUI.FontSet = "Inter"

	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	return NewAdminControlHandler(testApp, wss), testApp, mockUserSvc
}

func TestAdminControlHandler_Config_Success(t *testing.T) {
	handler, _, mockUserSvc := newTestAdminHandler()
	c, w := newAdminTestContext("GET", "/api/webgui/config", "", 0)

	mockUserSvc.On("IsRegisterEnabled", mock.Anything).Return(true)

	handler.Config(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())

	var resp struct {
		Data dto.AdminWebGUIConfig `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "Inter", resp.Data.FontSet)
	assert.True(t, resp.Data.RegisterIsEnable)

	mockUserSvc.AssertExpectations(t)
}

func TestAdminControlHandler_Config_RegisterDisabled(t *testing.T) {
	handler, _, mockUserSvc := newTestAdminHandler()
	c, w := newAdminTestContext("GET", "/api/webgui/config", "", 0)

	mockUserSvc.On("IsRegisterEnabled", mock.Anything).Return(false)

	handler.Config(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())

	var resp struct {
		Data dto.AdminWebGUIConfig `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.False(t, resp.Data.RegisterIsEnable)

	mockUserSvc.AssertExpectations(t)
}

func TestAdminControlHandler_GetConfig_Success(t *testing.T) {
	handler, _, _ := newTestAdminHandler()
	c, w := newAdminTestContext("GET", "/api/admin/config", "", 1) // UID 1 is admin

	handler.GetConfig(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
}

func TestAdminControlHandler_GetConfig_Forbidden(t *testing.T) {
	handler, _, _ := newTestAdminHandler()
	c, w := newAdminTestContext("GET", "/api/admin/config", "", 2) // UID 2 is not admin

	handler.GetConfig(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.ErrorUserIsNotAdmin.Code())
}

func TestAdminControlHandler_GetSystemInfo_Success(t *testing.T) {
	handler, _, _ := newTestAdminHandler()
	c, w := newAdminTestContext("GET", "/api/admin/system/info", "", 1)

	handler.GetSystemInfo(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	assert.Contains(t, w.Body.String(), `"uptime"`)
}

func TestAdminControlHandler_GC_Success(t *testing.T) {
	handler, _, _ := newTestAdminHandler()
	c, w := newAdminTestContext("GET", "/api/admin/gc", "", 1)

	handler.GC(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	assert.Contains(t, w.Body.String(), "Manual GC completed")
}

func TestAdminControlHandler_Upgrade_InvalidVersion(t *testing.T) {
	handler, _, _ := newTestAdminHandler()
	// Test with invalid version format
	c, w := newAdminTestContext("GET", "/api/admin/upgrade?version=../../invalid", "", 1)

	handler.Upgrade(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.ErrorInvalidParams.Code())
	assert.Contains(t, w.Body.String(), "invalid version format")
}

func TestAdminControlHandler_GetConfig_WithSecurityFields(t *testing.T) {
	handler, testApp, _ := newTestAdminHandler()
	cfg := testApp.Config()
	cfg.Security.WebGUILoginTokenExpiry = "14d"
	bindIP := false
	cfg.Security.WebGUILoginTokenBindIP = &bindIP
	bindUA := false
	cfg.Security.WebGUILoginTokenBindUA = &bindUA
	cfg.Git.AllowedPrivateHosts = []string{"gitea.internal", "10.0.0.0/8"}
	cfg.Server.CustomResponseHeaders = map[string]string{
		"X-Test-Get": "GetValue",
	}

	c, w := newAdminTestContext("GET", "/api/admin/config", "", 1)
	handler.GetConfig(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())

	var resp struct {
		Data dto.AdminConfig `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "14d", *resp.Data.WebGUILoginTokenExpiry)
	assert.Equal(t, false, *resp.Data.WebGUILoginTokenBindIP)
	assert.Equal(t, false, *resp.Data.WebGUILoginTokenBindUA)
	assert.Equal(t, []string{"gitea.internal", "10.0.0.0/8"}, *resp.Data.GitAllowedPrivateHosts)
	assert.NotNil(t, resp.Data.CustomResponseHeaders)
	assert.Equal(t, "GetValue", (*resp.Data.CustomResponseHeaders)["X-Test-Get"])
}

func TestAdminControlHandler_UpdateConfig_Success(t *testing.T) {
	handler, testApp, _ := newTestAdminHandler()
	cfg := testApp.Config()

	// Create mock config file for Save() to succeed
	tempFile, err := os.CreateTemp("", "config_test_*.yaml")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())
	cfg.File = tempFile.Name()

	reqBody := `{"webguiLoginTokenExpiry":"30d","webguiLoginTokenBindIp":false,"webguiLoginTokenBindUa":false,"gitAllowedPrivateHosts":["gitea.internal","10.0.0.0/8"],"customResponseHeaders":{"X-Test-Update":"UpdateValue"}}`
	c, w := newAdminTestContext("POST", "/api/admin/config", reqBody, 1)

	handler.UpdateConfig(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.Success.Code())
	assert.Equal(t, "30d", cfg.Security.WebGUILoginTokenExpiry)
	assert.Equal(t, false, *cfg.Security.WebGUILoginTokenBindIP)
	assert.Equal(t, false, *cfg.Security.WebGUILoginTokenBindUA)
	assert.Equal(t, []string{"gitea.internal", "10.0.0.0/8"}, cfg.Git.AllowedPrivateHosts)
	assert.Equal(t, "UpdateValue", cfg.Server.CustomResponseHeaders["X-Test-Update"])
}

func TestAdminControlHandler_UpdateConfig_InvalidExpiry(t *testing.T) {
	handler, _, _ := newTestAdminHandler()

	reqBody := `{"webguiLoginTokenExpiry":"invalid_duration"}`
	c, w := newAdminTestContext("POST", "/api/admin/config", reqBody, 1)

	handler.UpdateConfig(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assertResponseCode(t, w, code.ErrorInvalidParams.Code())
	assert.Contains(t, w.Body.String(), "webguiLoginTokenExpiry format invalid")
}
