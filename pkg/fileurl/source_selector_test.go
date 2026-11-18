package fileurl

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

type probeTransport func(*http.Request) (*http.Response, error)

func (f probeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func stubProbe(t *testing.T, status int) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = probeTransport(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodHead, req.Method)
		assert.Equal(t, "https://api.github.com/repos/isdoge/fast-note-sync-service/releases", req.URL.String())
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func TestSourceSelectorExplicitModes(t *testing.T) {
	// Legacy and unknown source settings must use GitHub without network access.
	// 旧源及未知配置均回落 GitHub，选源本身不发起网络请求。
	for _, mode := range []string{SourceAuto, SourceGitHub, "legacy-mirror", ""} {
		s := NewSourceSelector(mode)
		assert.True(t, s.IsGitHub())
		if mode == SourceAuto {
			assert.Equal(t, SourceAuto, s.Mode())
		} else {
			assert.Equal(t, SourceGitHub, s.Mode())
		}
		assert.Nil(t, s.Snapshot())
	}
}

func TestSourceSelectorSetModeInvalidatesSnapshot(t *testing.T) {
	stubProbe(t, http.StatusOK)
	s := NewSourceSelector(SourceAuto)
	s.Probe(context.Background())
	assert.NotNil(t, s.Snapshot())

	s.SetMode("legacy-mirror")
	assert.Nil(t, s.Snapshot())
	assert.True(t, s.IsGitHub())
	assert.Equal(t, SourceGitHub, s.Mode())
}

func TestSourceSelectorProbePopulatesSnapshot(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stubProbe(t, status)
			s := NewSourceSelector(SourceAuto)
			snap := s.Probe(context.Background())
			assert.Equal(t, status == http.StatusOK, snap.GitHub.OK)
			assert.True(t, snap.UseGitHub, "an unreachable GitHub must not select another repository")
			assert.Equal(t, snap.At, s.Snapshot().At)
		})
	}
}

func TestSourceSelectorConcurrentAccess(t *testing.T) {
	stubProbe(t, http.StatusOK)
	s := NewSourceSelector(SourceAuto)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(4)
		go func() { defer wg.Done(); _ = s.IsGitHub() }()
		go func() { defer wg.Done(); _ = s.Snapshot() }()
		go func() { defer wg.Done(); s.Probe(context.Background()) }()
		go func(j int) {
			defer wg.Done()
			if j%2 == 0 {
				s.SetMode(SourceAuto)
			} else {
				s.SetMode(SourceGitHub)
			}
		}(i)
	}
	wg.Wait()
}
