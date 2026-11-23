package api_router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newCapabilityContext builds a context the way UserAuthTokenWithConfigStrict
// leaves it: user_token, scope, vaults and auth_rest_client set.
// newCapabilityContext 按 UserAuthTokenWithConfigStrict 的产物构造上下文：
// 设置 user_token、scope、vaults 与 auth_rest_client。
func newCapabilityContext(uid int64, scope, vaults, client string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/user/token-capabilities", nil)
	if client != "" {
		c.Request.Header.Set("x-client", client)
	}
	if uid > 0 {
		c.Set("user_token", &pkgapp.UserEntity{UID: uid})
	}
	c.Set("scope", scope)
	c.Set("vaults", vaults)
	c.Set("auth_rest_client", client)
	return c, w
}

func decodeCapabilities(t *testing.T, w *httptest.ResponseRecorder) dto.TokenCapabilitiesResponse {
	t.Helper()
	var resp struct {
		Code int64                         `json:"code"`
		Data dto.TokenCapabilitiesResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, int64(code.Success.Code()), resp.Code, "unexpected code: %s", w.Body.String())
	return resp.Data
}

// newCapabilityApp builds a test app with a mock user service so admin
// resolution works.
// newCapabilityApp 构建带 mock 用户服务的测试 App，使管理员判定可用。
func newCapabilityApp(t *testing.T, adminUID int, uids []int64) *app.App {
	t.Helper()
	mockUserSvc := new(mocks.MockUserService)
	testApp := app.NewTestApp(&app.Services{UserService: mockUserSvc})
	testApp.Config().User.AdminUID = adminUID
	mockUserSvc.On("GetAllUIDs", mock.Anything).Return(uids, nil).Maybe()
	return testApp
}

func TestTokenCapabilities_FullToken(t *testing.T) {
	testApp := newCapabilityApp(t, 1, []int64{1})
	handler := NewUserCapabilityHandler(testApp)

	c, w := newCapabilityContext(1, "p:rest,ws c:* f:*", "", "obsidian")
	handler.TokenCapabilities(c)

	data := decodeCapabilities(t, w)
	assert.Equal(t, int64(1), data.UID)
	assert.True(t, data.IsAdmin)
	assert.Equal(t, "rest,ws", data.Protocol)
	assert.False(t, data.Restricted, "全权令牌不受限")
	assert.False(t, data.ReadOnly, "全权令牌可写")
	assert.Empty(t, data.Vaults)
	assert.NotNil(t, data.Vaults, "vaults 必须序列化为 [] 而不是 null")
	assert.True(t, data.Permissions.NoteRead && data.Permissions.NoteWrite)
	assert.True(t, data.Permissions.FileRead && data.Permissions.FileWrite)
	assert.True(t, data.Permissions.SettingRead && data.Permissions.SettingWrite)
	assert.True(t, data.Permissions.VaultRead && data.Permissions.VaultWrite)
}

func TestTokenCapabilities_ReadOnlyRestrictedToken(t *testing.T) {
	testApp := newCapabilityApp(t, 1, []int64{1})
	handler := NewUserCapabilityHandler(testApp)

	c, w := newCapabilityContext(2, "p:rest c:CLI f:note_r,file_r", "", "CLI")
	handler.TokenCapabilities(c)

	data := decodeCapabilities(t, w)
	assert.Equal(t, int64(2), data.UID)
	assert.False(t, data.IsAdmin)
	assert.True(t, data.Restricted)
	assert.True(t, data.ReadOnly, "只读令牌 readOnly=true")
	assert.True(t, data.Permissions.NoteRead)
	assert.False(t, data.Permissions.NoteWrite)
	assert.True(t, data.Permissions.FileRead)
	assert.False(t, data.Permissions.FileWrite)
	assert.False(t, data.Permissions.SettingRead, "只读令牌没有设置权限")
	assert.False(t, data.Permissions.VaultWrite)
}

func TestTokenCapabilities_VaultScopedToken(t *testing.T) {
	testApp := newCapabilityApp(t, 1, []int64{1})
	handler := NewUserCapabilityHandler(testApp)

	c, w := newCapabilityContext(2, "p:rest c:* f:note_rw,file_rw", "doge,work", "CLI")
	handler.TokenCapabilities(c)

	data := decodeCapabilities(t, w)
	assert.True(t, data.Restricted, "限定库令牌 restricted=true")
	assert.Equal(t, []string{"doge", "work"}, data.Vaults)
	assert.False(t, data.ReadOnly)
	assert.True(t, data.Permissions.NoteWrite)
	assert.True(t, data.Permissions.FileWrite)
}

func TestTokenCapabilities_NoUserRejected(t *testing.T) {
	testApp := newCapabilityApp(t, 1, []int64{1})
	handler := NewUserCapabilityHandler(testApp)

	c, w := newCapabilityContext(0, "", "", "")
	handler.TokenCapabilities(c)

	assertResponseCode(t, w, code.ErrorInvalidUserAuthToken.Code())
}

func TestTokenCapabilities_AdminVsNonAdmin(t *testing.T) {
	testApp := newCapabilityApp(t, 0, []int64{1, 4})
	handler := NewUserCapabilityHandler(testApp)

	c, w := newCapabilityContext(1, "p:rest c:* f:*", "", "webgui")
	handler.TokenCapabilities(c)
	assert.True(t, decodeCapabilities(t, w).IsAdmin, "AdminUID=0 时最小 UID 是管理员")

	c, w = newCapabilityContext(4, "p:rest c:* f:*", "", "webgui")
	handler.TokenCapabilities(c)
	assert.False(t, decodeCapabilities(t, w).IsAdmin, "其它用户不是管理员")
}

func TestTokenCapabilities_LegacyEmptyScopeWildcard(t *testing.T) {
	testApp := newCapabilityApp(t, 1, []int64{1})
	handler := NewUserCapabilityHandler(testApp)

	// Legacy empty scope acts as a wildcard in non-strict mode; it is not
	// vault-restricted and not read-only.
	// 旧版空 scope 在非严格模式等同通配；不受库限制、也不是只读。
	c, w := newCapabilityContext(1, "", "", "")
	handler.TokenCapabilities(c)

	data := decodeCapabilities(t, w)
	assert.False(t, data.Restricted)
	assert.False(t, data.ReadOnly)
	assert.True(t, data.Permissions.NoteWrite)
}

// tokenId 必须来自中间件写入的数据库令牌 ID，且绝不包含原始令牌字符串。
func TestTokenCapabilities_ReportsTokenIDNotRawToken(t *testing.T) {
	testApp := newCapabilityApp(t, 1, []int64{1})
	handler := NewUserCapabilityHandler(testApp)

	c, w := newCapabilityContext(1, "p:rest c:* f:note_r", "", "CLI")
	c.Set("auth_token_id", int64(42))
	handler.TokenCapabilities(c)

	data := decodeCapabilities(t, w)
	assert.Equal(t, "42", data.TokenID)
	assert.NotContains(t, w.Body.String(), "Bearer")
	assert.NotContains(t, w.Body.String(), "secret")
}

// REST 与 WS 能力取并集：ws-only 写权限也应体现在报告中。
func TestTokenCapabilities_UnionOfRestAndWS(t *testing.T) {
	testApp := newCapabilityApp(t, 1, []int64{1})
	handler := NewUserCapabilityHandler(testApp)

	c, w := newCapabilityContext(2, "p:rest,ws c:CLI f:note_r", "", "CLI")
	handler.TokenCapabilities(c)
	data := decodeCapabilities(t, w)
	assert.Equal(t, "rest,ws", data.Protocol)
	assert.True(t, data.Permissions.NoteRead)

	// A ws-only write scope must still surface noteWrite=true.
	c, w = newCapabilityContext(2, "p:ws c:CLI f:note_rw", "", "CLI")
	handler.TokenCapabilities(c)
	data = decodeCapabilities(t, w)
	assert.True(t, data.Permissions.NoteWrite, "ws 通道的写权限应体现在并集中")
	assert.False(t, data.Permissions.FileWrite)
}
