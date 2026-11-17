package app

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigRejectsInvalidTokenExpiry(t *testing.T) {
	for _, field := range []string{"token-expiry", "share-token-expiry", "webgui-login-token-expiry"} {
		for _, value := range []string{"3650000000000000d", "106752d", "-7d", "0s"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("security:\n  "+field+": "+value+"\n"), 0600))
				_, _, err := LoadConfig(path)
				require.Error(t, err)
				require.Contains(t, err.Error(), "security."+field)
			})
		}
	}
}
