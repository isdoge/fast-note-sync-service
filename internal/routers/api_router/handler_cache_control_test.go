package api_router

// #342 S-22：
// - /api/file 的附件下载需要用户令牌鉴权，Cache-Control 必须是 private
//   （共享缓存可把私有附件缓存一年，令牌撤销后缓存期内仍可取回）。
// - /api/share/file 的分享附件：设了访问密码的分享不得 public；无密码公开分享
//   保持原行为。浏览器本地缓存（max-age）保留，Obsidian 云预览依赖它。
// 修复前：两处都是 "public, s-maxage=31536000, ..."（旧代码上测试失败）。

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// writeFileFixture 在临时目录写一个附件文件，返回路径
func writeFileFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "pic.png")
	require.NoError(t, os.WriteFile(p, []byte("png-bytes"), 0644))
	return p
}

// TestFileHandler_GetContent_CacheControlPrivate：鉴权附件下载必须是 private
func TestFileHandler_GetContent_CacheControlPrivate(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc)

	fixture := writeFileFixture(t)
	fileSvc.On("GetContentInfo", mock.Anything, int64(1), mock.Anything).
		Return(fixture, "image/png", int64(1700000000000), "etag", "pic.png", nil)

	testApp := app.NewTestApp(&app.Services{FileService: fileSvc})
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	handler := NewFileHandler(testApp, wss)

	c, w := newNoteTestContext("GET", "/api/file?vault=v&path=pic.png", "", 1)
	handler.GetInfo(c)

	assert.Equal(t, http.StatusOK, w.Code)
	cc := w.Header().Get("Cache-Control")
	assert.Contains(t, cc, "private", "#342 S-22: authenticated attachment download must not be publicly cacheable, got %q", cc)
	assert.NotContains(t, cc, "public", "#342 S-22: got public cache directive: %q", cc)
	assert.Contains(t, cc, "max-age=31536000", "browser-local caching must be preserved (Obsidian cloud preview relies on it)")
}

// TestShareHandler_FileGet_CacheControlByPassword：带密码分享 private、公开分享 public
func TestShareHandler_FileGet_CacheControlByPassword(t *testing.T) {
	fixture := writeFileFixture(t)

	tests := []struct {
		name       string
		hasPwd     bool
		wantHeader string
	}{
		{"password protected share is private", true, "private, max-age=31536000, must-revalidate"},
		{"public share keeps public cache", false, "public, s-maxage=31536000, max-age=31536000, must-revalidate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shareSvc := new(svcmocks.MockShareService)
			shareSvc.On("GetSharedFileInfo", mock.Anything, "stoken", int64(7), "pwd").
				Return(fixture, "image/png", int64(1700000000000), "etag", "pic.png", tt.hasPwd, nil)

			handler := newTestShareHandler(shareSvc, nil)
			c, w := newShareTestContext("GET", "/api/share/file?id=7&password=pwd", "", 0)
			c.Set("share_token", "stoken")
			handler.FileGet(c)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tt.wantHeader, w.Header().Get("Cache-Control"))
		})
	}
}
