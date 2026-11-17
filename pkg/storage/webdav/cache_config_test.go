package webdav

import (
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient_PasswordChangeDoesNotReuseStaleClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	first, err := NewClient(&Config{Endpoint: server.URL, User: "cache-user", Password: "old-password"})
	require.NoError(t, err)
	second, err := NewClient(&Config{Endpoint: server.URL, User: "cache-user", Password: "new-password"})
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.Equal(t, "new-password", second.Config.Password)
}
