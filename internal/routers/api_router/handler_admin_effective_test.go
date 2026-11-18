package api_router

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// AdminUID == 0 的语义从“人人都是管理员”收紧为“现存用户中 UID 最小者是管理员”：
// 单用户部署保持可用，多用户部署不再全开放；无用户时无人是管理员。
// 显式配置 AdminUID > 0 时行为不变。
func TestEffectiveAdminUID(t *testing.T) {
	build := func(adminUID int, uids []int64) (*app.App, *mocks.MockUserService) {
		mockUserSvc := new(mocks.MockUserService)
		testApp := app.NewTestApp(&app.Services{UserService: mockUserSvc})
		testApp.Config().User.AdminUID = adminUID
		mockUserSvc.On("GetAllUIDs", mock.Anything).Return(uids, nil).Maybe()
		return testApp, mockUserSvc
	}

	t.Run("explicit config wins", func(t *testing.T) {
		a, _ := build(7, []int64{1, 2, 7})
		require.Equal(t, int64(7), effectiveAdminUID(context.Background(), a))
	})

	t.Run("zero falls back to smallest uid", func(t *testing.T) {
		a, _ := build(0, []int64{5, 2, 9})
		require.Equal(t, int64(2), effectiveAdminUID(context.Background(), a))
	})

	t.Run("zero with no users means no admin", func(t *testing.T) {
		a, _ := build(0, nil)
		require.Equal(t, int64(0), effectiveAdminUID(context.Background(), a))
	})

	t.Run("zero with repo error means no admin", func(t *testing.T) {
		mockUserSvc := new(mocks.MockUserService)
		a := app.NewTestApp(&app.Services{UserService: mockUserSvc})
		a.Config().User.AdminUID = 0
		mockUserSvc.On("GetAllUIDs", mock.Anything).Return(nil, assert.AnError)
		require.Equal(t, int64(0), effectiveAdminUID(context.Background(), a))
	})

	t.Run("requireEffectiveAdmin boundaries", func(t *testing.T) {
		a, _ := build(0, []int64{1, 4})
		assert.True(t, requireEffectiveAdmin(context.Background(), a, 1), "最小 UID 是管理员")
		assert.False(t, requireEffectiveAdmin(context.Background(), a, 4), "其它用户不是")
		assert.False(t, requireEffectiveAdmin(context.Background(), a, 0), "uid=0 不是")
	})
}

// /api/admin/check 在 AdminUID == 0 时不再对任意登录用户返回 isAdmin=true。
func TestAdminControlHandler_CheckAdmin_ZeroAdminUID(t *testing.T) {
	for _, tc := range []struct {
		name  string
		uids  []int64
		asker int64
		want  bool
	}{
		{"smallest uid is admin", []int64{1, 2}, 1, true},
		{"other user is not admin", []int64{1, 2}, 2, false},
		{"no users means nobody", nil, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockUserSvc := new(mocks.MockUserService)
			testApp := app.NewTestApp(&app.Services{UserService: mockUserSvc})
			testApp.Config().User.AdminUID = 0
			mockUserSvc.On("GetAllUIDs", mock.Anything).Return(tc.uids, nil).Maybe()

			handler := NewAdminControlHandler(testApp, nil)
			c, w := newAdminTestContext("GET", "/api/admin/check", "", tc.asker)
			handler.CheckAdmin(c)

			var resp struct {
				Data dto.AdminCheckResponse `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tc.want, resp.Data.IsAdmin)
		})
	}
}

// GetConfig 的签名密钥只返回给生效管理员：非管理员即使带登录令牌也拿不到
// AuthTokenKey / ShareTokenKey（路由层已将其留在 WebGUI 组，此处覆盖 handler 层）。
func TestAdminControlHandler_GetConfig_NonAdminGetsNoSecrets(t *testing.T) {
	mockUserSvc := new(mocks.MockUserService)
	testApp := app.NewTestApp(&app.Services{UserService: mockUserSvc})
	cfg := testApp.Config()
	cfg.User.AdminUID = 0
	cfg.Security.AuthTokenKey = "secret-auth-key"
	cfg.Security.ShareTokenKey = "secret-share-key"
	mockUserSvc.On("GetAllUIDs", mock.Anything).Return([]int64{1, 2}, nil).Maybe()

	handler := NewAdminControlHandler(testApp, nil)

	c, w := newAdminTestContext("GET", "/api/admin/config", "", 2)
	handler.GetConfig(c)
	assertResponseCode(t, w, 411) // ErrorUserIsNotAdmin
	assert.NotContains(t, w.Body.String(), "secret-auth-key")
	assert.NotContains(t, w.Body.String(), "secret-share-key")

	c, w = newAdminTestContext("GET", "/api/admin/config", "", 1)
	handler.GetConfig(c)
	assertResponseCode(t, w, code.Success.Code())
	assert.Contains(t, w.Body.String(), "secret-auth-key")
	assert.Contains(t, w.Body.String(), "secret-share-key")
}

// AdminUID == 0 时其它管理入口对非“最小 UID”用户也必须拒绝。
func TestAdminControlHandler_ZeroAdminUID_DeniesNonSmallestUser(t *testing.T) {
	mockUserSvc := new(mocks.MockUserService)
	testApp := app.NewTestApp(&app.Services{UserService: mockUserSvc})
	testApp.Config().User.AdminUID = 0
	mockUserSvc.On("GetAllUIDs", mock.Anything).Return([]int64{1, 2}, nil).Maybe()

	handler := NewAdminControlHandler(testApp, nil)

	c, w := newAdminTestContext("GET", "/api/admin/upgrade?version=latest", "", 2)
	handler.Upgrade(c)
	assertResponseCode(t, w, 411)

	c, w = newAdminTestContext("GET", "/api/admin/ws_clients", "", 2)
	handler.GetWSClients(c)
	assertResponseCode(t, w, 411)

	c, w = newAdminTestContext(http.MethodDelete, "/api/admin/ws_client/trace-1", "", 2)
	handler.KickWSClient(c)
	assertResponseCode(t, w, 411)
}
