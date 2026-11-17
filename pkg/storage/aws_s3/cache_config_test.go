package aws_s3

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNewClient_ConfigChangesDoNotReuseStaleClient(t *testing.T) {
	base := Config{AccessKeyID: "cache-regression-key", AccessKeySecret: "secret", BucketName: "old-bucket", CustomPath: "old-path", Region: "us-east-1"}
	first, err := NewClient(&base)
	require.NoError(t, err)
	for _, field := range []string{"bucket", "path"} {
		t.Run(field, func(t *testing.T) {
			next := base
			if field == "bucket" {
				next.BucketName = "new-bucket"
			} else {
				next.CustomPath = "new-path"
			}
			second, err := NewClient(&next)
			require.NoError(t, err)
			require.NotSame(t, first, second)
			require.Equal(t, next.BucketName, second.Config.BucketName)
			require.Equal(t, next.CustomPath, second.Config.CustomPath)
			cached, err := NewClient(&next)
			require.NoError(t, err)
			require.Same(t, second, cached)
		})
	}
}
