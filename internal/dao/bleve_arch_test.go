package dao

import (
	"testing"

	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestBleveEnabledForArch(t *testing.T) {
	for _, arch := range []string{"arm", "arm64", "amd64", "386", "riscv64"} {
		for _, configured := range []*bool{nil, util.Ptr(true), util.Ptr(false)} {
			t.Run(arch+"/"+configName(configured), func(t *testing.T) {
				want := arch != "arm" && (configured == nil || *configured)
				require.Equal(t, want, bleveEnabledForArch(configured, arch))
				core, logs := observer.New(zap.WarnLevel)
				m := newBleveManagerForArch(configured, nil, zap.New(core), arch)
				t.Cleanup(func() { require.NoError(t, m.CloseAll()) })
				require.Equal(t, want, m.IsEnabled())
				if !want {
					require.Nil(t, m.ftsQueue, "disabled FTS must not start the index worker")
				}
				if arch == "arm" && (configured == nil || *configured) {
					require.Equal(t, 1, logs.Len())
					message := logs.All()[0].Message
					for _, text := range []string{"32-bit ARM", "zapx", "unaligned 64-bit atomic", "full-text search is unavailable", "other features continue normally"} {
						require.Contains(t, message, text)
					}
				} else {
					require.Zero(t, logs.Len())
				}
			})
		}
	}
}

func configName(value *bool) string {
	if value == nil {
		return "default"
	}
	if *value {
		return "enabled"
	}
	return "disabled"
}
