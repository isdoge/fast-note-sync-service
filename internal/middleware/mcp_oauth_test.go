package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
)

type commaVaultOAuthVerifier struct {
	claims *MCPOAuthClaims
}

func (f *commaVaultOAuthVerifier) VerifyBearerToken(ctx context.Context, token string, clientType string) (*MCPOAuthClaims, error) {
	return f.claims, nil
}

// OAuth 配置里的库名是一个完整字符串：注入到请求上下文的 vault 白名单
// 不得把一个含逗号的库名扩展成两个库的授权。
func TestMCPOAuthVaultAllowlistKeepsCommaNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(MCPMixedAuthWithConfig("secret", nil, MCPMixedAuthConfig{
		OAuthEnabled: true,
		Verifier: &commaVaultOAuthVerifier{claims: &MCPOAuthClaims{
			UID:      1,
			FNSScope: "p:mcp c:* f:*",
		}},
		OAuthVaults: util.JoinVaultAllowlist([]string{"项目,私密"}),
	}))
	router.GET("/api/mcp", func(c *gin.Context) {
		allowed := c.GetString("vaults")
		c.JSON(http.StatusOK, gin.H{
			"full":   util.VerifyVaultAccess(allowed, "项目,私密"),
			"prefix": util.VerifyVaultAccess(allowed, "项目"),
			"suffix": util.VerifyVaultAccess(allowed, "私密"),
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/mcp", nil)
	req.Header.Set("Authorization", "Bearer oauth-token")
	req.Header.Set("X-Client", "MCP")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var res struct {
		Full   bool `json:"full"`
		Prefix bool `json:"prefix"`
		Suffix bool `json:"suffix"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.True(t, res.Full, "含逗号的完整库名必须放行")
	require.False(t, res.Prefix, "逗号前的片段不得被授权")
	require.False(t, res.Suffix, "逗号后的片段不得被授权")
}
