package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestWebSocketHeartbeatConfig(t *testing.T) {
	for _, tc := range []struct {
		body           string
		interval, wait int
		invalid        bool
	}{
		{"app: {}", 25, 60, false},
		{"app:\n  ws-ping-interval: 30\n  ws-ping-wait: 180", 30, 180, false},
		{"app:\n  ws-ping-interval: -1", 0, 0, true},
		{"app:\n  ws-ping-wait: -1", 0, 0, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.body), 0600))
			cfg, _, err := LoadConfig(path)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			data, err := yaml.Marshal(cfg.App)
			require.NoError(t, err)
			var fields map[string]interface{}
			require.NoError(t, yaml.Unmarshal(data, &fields))
			require.Equal(t, tc.interval, fields["ws-ping-interval"])
			require.Equal(t, tc.wait, fields["ws-ping-wait"])
		})
	}
}
