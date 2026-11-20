package websocket_router

import (
	"encoding/binary"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Reusing a matching upload always overwrites the context, including clearing a
// retired round's context with a context-free request.
// 复用属性相同的上传会话时无条件覆盖 context，包括用不带 context 的新请求清空旧轮的值。
func TestUploadSessionReuseContext(t *testing.T) {
	for _, tc := range []struct {
		name, first, next, want string
	}{
		{"nonempty then empty", "sync-first", "", ""},
		{"empty then nonempty", "", "sync-next", "sync-next"},
		{"nonempty then nonempty", "sync-first", "sync-next", "sync-next"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := app.NewTestApp(&app.Services{})
			a.Config().App.TempPath = t.TempDir()
			c := newWSPathTestClient(a)
			t.Cleanup(c.WsCancel)
			h := NewFileWSHandler(a)

			first, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, tc.first)
			require.NoError(t, err)
			t.Cleanup(first.Cleanup)
			require.Equal(t, tc.first, first.Context)

			next, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, tc.next)
			require.NoError(t, err)
			require.Equal(t, first.ID, next.ID)
			require.Same(t, first, next)
			require.Equal(t, tc.want, next.Context)
		})
	}
}

// A size change replaces the session even when the path and content hash match.
// 即使路径与内容哈希相同，大小变化也必须替换会话。
func TestUploadSessionSizeChangeReplacesSession(t *testing.T) {
	a := app.NewTestApp(&app.Services{})
	a.Config().App.TempPath = t.TempDir()
	c := newWSPathTestClient(a)
	t.Cleanup(c.WsCancel)
	h := NewFileWSHandler(a)

	first, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, "sync-first")
	require.NoError(t, err)
	t.Cleanup(first.Cleanup)
	next, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 4, baseMT, baseMT, "")
	require.NoError(t, err)
	t.Cleanup(next.Cleanup)
	require.NotEqual(t, first.ID, next.ID)
	require.Nil(t, c.Server.GetSession(c.User.ID, first.ID))
	require.Same(t, next, c.Server.GetSession(c.User.ID, next.ID))
	require.Empty(t, next.Context)
}

// A context-free watcher check reuses the session and clears its sync context:
// the completion ack must not carry a context the plugin has already retired.
// 无 context 的监听检查复用会话时会清空旧同步 context：完成确认不能再带插件
// 已作废的 context，否则会被当作迟到应答丢弃。
func TestFileUploadCheck_ReusedSessionClearsRetiredContext(t *testing.T) {
	f := new(svcmocks.MockFileService)
	f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
	f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.FileDTO{}, nil).Once()
	f.On("UploadCheck", mock.Anything, int64(1), mock.Anything).
		Return("Create", &dto.FileDTO{}, nil).Once()
	f.On("UploadComplete", mock.Anything, int64(1), mock.Anything).
		Return(false, &dto.FileDTO{Path: "/images/pic.png", PathHash: "ph", UpdatedTimestamp: 42}, nil).Once()
	a := newUploadCheckApp(t, f)
	a.Config().App.TempPath = t.TempDir()
	c, messages := newSyncDeletePeer(t, a)
	h := NewFileWSHandler(a)
	const syncContext = "sync-round"
	t.Cleanup(func() { syncDownloadDelete(syncContext, "file") })

	h.doFileSync(c, &dto.FileSyncRequest{
		Vault: "v", Context: syncContext,
		Files: []dto.FileSyncCheckRequest{{
			Path: "/images/pic.png", PathHash: "ph", ContentHash: "ch",
			Size: 12, Ctime: baseMT, Mtime: baseMT,
		}},
	})
	end := syncDeleteMessage(t, messages, FileSyncEnd)
	require.Equal(t, float64(1), end["data"].(map[string]any)["needUploadCount"])
	session, ok := c.Server.GetSessionByPathHash(c.User.ID, "ph", "v", "/images/pic.png").(*FileUploadBinaryChunkSession)
	require.True(t, ok)
	t.Cleanup(session.Cleanup)
	require.Equal(t, syncContext, session.Context)

	h.FileUploadCheck(c, uploadCheckMsg(""))
	upload := syncDeleteMessage(t, messages, FileUpload)
	require.Empty(t, upload["context"])
	require.Equal(t, session.ID, upload["data"].(map[string]any)["sessionId"])
	require.Empty(t, session.Context, "无 context 的复用必须清掉旧轮的 context")

	frame := append([]byte(session.ID), make([]byte, 4)...)
	binary.BigEndian.PutUint32(frame[36:40], 0)
	frame = append(frame, []byte("hello world!")...)
	h.FileUploadChunkBinary(c, frame)

	ack := syncDeleteMessage(t, messages, string(FileUploadAck))
	require.Empty(t, ack["context"])
	require.Equal(t, "v", ack["vault"])
	require.Equal(t, "/images/pic.png", ack["data"].(map[string]any)["path"])
	require.Equal(t, float64(42), ack["data"].(map[string]any)["lastTime"])
	require.Nil(t, c.Server.GetSession(c.User.ID, session.ID))
	f.AssertExpectations(t)
}
