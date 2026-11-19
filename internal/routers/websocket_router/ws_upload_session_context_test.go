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

// Reusing matching uploads preserves a non-empty context unless a newer non-empty context replaces it.
// 复用属性相同的上传会话时保留非空 context，只有新的非空 context 才会覆盖。
func TestUploadSessionReuseContext(t *testing.T) {
	for _, tc := range []struct {
		name, first, next, want string
	}{
		{"nonempty then empty", "sync-first", "", "sync-first"},
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

// A context-free watcher check keeps its own response context while the completion ack retains the sync context.
// 无 context 的监听检查响应仍用自己的 context，上传完成确认则保留同步 context。
func TestFileUploadCheck_ReusedSessionAckKeepsSyncContext(t *testing.T) {
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
	require.Equal(t, syncContext, session.Context)

	frame := append([]byte(session.ID), make([]byte, 4)...)
	binary.BigEndian.PutUint32(frame[36:40], 0)
	frame = append(frame, []byte("hello world!")...)
	h.FileUploadChunkBinary(c, frame)

	ack := syncDeleteMessage(t, messages, string(FileUploadAck))
	require.Equal(t, syncContext, ack["context"])
	require.Equal(t, "v", ack["vault"])
	require.Equal(t, "/images/pic.png", ack["data"].(map[string]any)["path"])
	require.Equal(t, float64(42), ack["data"].(map[string]any)["lastTime"])
	require.Nil(t, c.Server.GetSession(c.User.ID, session.ID))
	f.AssertExpectations(t)
}
