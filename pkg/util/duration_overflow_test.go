package util

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestParseDurationRejectsDayOverflow(t *testing.T) {
	for _, value := range []string{"106752d", "3650000000000000d", "-3650000000000000d", "999999999999999999999999d"} {
		_, err := ParseDuration(value)
		require.Error(t, err, value)
	}
	d, err := ParseDuration("106751d")
	require.NoError(t, err)
	require.Equal(t, time.Duration(106751)*24*time.Hour, d)
}
