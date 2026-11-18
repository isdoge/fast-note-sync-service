package api_router

// haierkeys/obsidian-fast-note-sync#252：
// 配置了独立 share-port 而未配 share-base-url 时，分享链接的 BaseUrl 之前
// 回落到 c.Request.Host（主服务端口），端口错了打不开。修复后应使用
// 请求主机名 + share-port。已配置 share-base-url / ext-api-url 时行为不变。
// 修复前：TestShareHandler_BaseUrl_SharePort 在旧代码上失败（端口是主端口的）。

import (
	"net/http"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestShareHandler_BaseUrl_SharePort：share-port 已配、share-base-url 未配 → 主机名+share-port
func TestShareHandler_BaseUrl_SharePort(t *testing.T) {
	shareSvc := new(svcmocks.MockShareService)
	shareSvc.On("ShareGenerate", mock.Anything, int64(1), "main", "test.md", "hash_123", "", int64(0)).
		Return(&dto.ShareCreateResponse{ID: 1, Token: "st"}, nil)

	handler := newTestShareHandler(shareSvc, nil)
	cfg := handler.App.Config()
	cfg.Server.WebGuiPort = ":9200"
	cfg.Server.SharePort = ":9100"

	// 请求打到主服务 9000 端口；修复后 BaseUrl 应是主机名 + 9100
	c, w := newShareTestContext("POST", "/api/share", `{"vault":"main", "path":"test.md", "pathHash":"hash_123", "password":""}`, 1)
	c.Request.Host = "sync.example.com:9000"
	handler.Create(c)

	assert.Equal(t, http.StatusOK, w.Code)
	res := decodeRes(t, w)
	data, _ := res["data"].(map[string]any)
	baseURL, _ := data["baseUrl"].(string)
	assert.Equal(t, "http://sync.example.com:9100", baseURL,
		"#252: 配了 share-port 未配 share-base-url 时应返回主机名+分享端口，而非主服务端口")
}

// TestShareHandler_BaseUrl_ShareBaseUrlConfigured：配了 share-base-url 时优先返回它
func TestShareHandler_BaseUrl_ShareBaseUrlConfigured(t *testing.T) {
	shareSvc := new(svcmocks.MockShareService)
	shareSvc.On("ShareGenerate", mock.Anything, int64(1), "main", "test.md", "hash_123", "", int64(0)).
		Return(&dto.ShareCreateResponse{ID: 1, Token: "st"}, nil)

	handler := newTestShareHandler(shareSvc, nil)
	cfg := handler.App.Config()
	cfg.Server.WebGuiPort = ":9200"
	cfg.Server.SharePort = ":9100"
	cfg.Server.ShareBaseUrl = "https://share.example.com"

	c, w := newShareTestContext("POST", "/api/share", `{"vault":"main", "path":"test.md", "pathHash":"hash_123", "password":""}`, 1)
	c.Request.Host = "sync.example.com:9000"
	handler.Create(c)

	res := decodeRes(t, w)
	data, _ := res["data"].(map[string]any)
	baseURL, _ := data["baseUrl"].(string)
	assert.Equal(t, "https://share.example.com", baseURL)
}

// TestShareHandler_BaseUrl_ExtApiUrlConfigured：配了 ext-api-url 时优先返回它
func TestShareHandler_BaseUrl_ExtApiUrlConfigured(t *testing.T) {
	shareSvc := new(svcmocks.MockShareService)
	shareSvc.On("ShareGenerate", mock.Anything, int64(1), "main", "test.md", "hash_123", "", int64(0)).
		Return(&dto.ShareCreateResponse{ID: 1, Token: "st"}, nil)

	handler := newTestShareHandler(shareSvc, nil)
	cfg := handler.App.Config()
	cfg.Server.SharePort = ":9100"
	cfg.Server.ExtApiUrl = "https://api.example.com"

	c, w := newShareTestContext("POST", "/api/share", `{"vault":"main", "path":"test.md", "pathHash":"hash_123", "password":""}`, 1)
	c.Request.Host = "sync.example.com:9000"
	handler.Create(c)

	res := decodeRes(t, w)
	data, _ := res["data"].(map[string]any)
	baseURL, _ := data["baseUrl"].(string)
	assert.Equal(t, "https://api.example.com", baseURL)
}

// TestShareHandler_BaseUrl_NoSharePort：未配 share-port 时维持原行为（请求 Host）
func TestShareHandler_BaseUrl_NoSharePort(t *testing.T) {
	shareSvc := new(svcmocks.MockShareService)
	shareSvc.On("ShareGenerate", mock.Anything, int64(1), "main", "test.md", "hash_123", "", int64(0)).
		Return(&dto.ShareCreateResponse{ID: 1, Token: "st"}, nil)

	handler := newTestShareHandler(shareSvc, nil)

	c, w := newShareTestContext("POST", "/api/share", `{"vault":"main", "path":"test.md", "pathHash":"hash_123", "password":""}`, 1)
	c.Request.Host = "sync.example.com:9000"
	handler.Create(c)

	res := decodeRes(t, w)
	data, _ := res["data"].(map[string]any)
	baseURL, _ := data["baseUrl"].(string)
	assert.Equal(t, "http://sync.example.com:9000", baseURL)
}
