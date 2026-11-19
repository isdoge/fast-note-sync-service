package websocket_router

// 上传会话复用：只有仍在接收分片的会话可以复用；复用发生在会话锁内，并刷新
// Ctime / Mtime。最后一片正在收尾（分片已齐）的会话不得复用，否则客户端会拿到
// "全部分片已上传" 的响应却永远等不到 FileUploadAck。
// 并发用例专门配合 go test -race 运行。

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestUploadSessionReuseRefreshesTimestamps 复用时刷新 Ctime / Mtime（仅 mtime 变动的
// 文件走到复用路径，落库必须用新时间戳）。
func TestUploadSessionReuseRefreshesTimestamps(t *testing.T) {
	a := app.NewTestApp(&app.Services{})
	a.Config().App.TempPath = t.TempDir()
	c := newWSPathTestClient(a)
	t.Cleanup(c.WsCancel)
	h := NewFileWSHandler(a)

	first, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, 1000, 2000, "ctx")
	require.NoError(t, err)
	t.Cleanup(first.Cleanup)

	next, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, 3000, 4000, "ctx")
	require.NoError(t, err)
	require.Same(t, first, next)
	require.Equal(t, int64(3000), next.Ctime)
	require.Equal(t, int64(4000), next.Mtime)
}

// TestUploadSessionReuseRefusedAfterChunksComplete 分片已经收齐（完成流程尚未把会话
// 标记 completed / 移除）时不得复用，必须清理重建。
func TestUploadSessionReuseRefusedAfterChunksComplete(t *testing.T) {
	a := app.NewTestApp(&app.Services{})
	a.Config().App.TempPath = t.TempDir()
	c := newWSPathTestClient(a)
	t.Cleanup(c.WsCancel)
	h := NewFileWSHandler(a)

	first, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, "ctx")
	require.NoError(t, err)
	t.Cleanup(first.Cleanup)

	// 模拟最后一片已写入、正在关闭句柄/落库的窗口：分片数已齐但 isCompleted 仍为 false。
	first.mu.Lock()
	first.UploadedChunks = first.TotalChunks
	first.mu.Unlock()

	next, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, "ctx2")
	require.NoError(t, err)
	t.Cleanup(next.Cleanup)
	require.NotEqual(t, first.ID, next.ID)
	require.Nil(t, c.Server.GetSession(c.User.ID, first.ID))
	require.Same(t, next, c.Server.GetSession(c.User.ID, next.ID))
}

// TestUploadSessionReuseConcurrentWithCompletion 让最后一片的写入与并发的会话复用竞争，
// 配合 -race 验证共享字段都在会话锁内访问。
func TestUploadSessionReuseConcurrentWithCompletion(t *testing.T) {
	f := new(svcmocks.MockFileService)
	v := new(svcmocks.MockVaultService)
	f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()
	f.On("UploadComplete", mock.Anything, int64(1), mock.Anything).
		Return(false, &dto.FileDTO{Path: "a.bin", PathHash: "h", Action: "create", ContentHash: "same", Mtime: baseMT, UpdatedTimestamp: 42}, nil).Maybe()

	a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
	a.Config().App.TempPath = t.TempDir()
	c, messages := newSyncDeletePeer(t, a)
	h := NewFileWSHandler(a)

	first, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, "ctx")
	require.NoError(t, err)
	t.Cleanup(first.Cleanup)

	frame := make([]byte, 0, 40+3)
	frame = append(frame, []byte(first.ID)...)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], 0)
	frame = append(frame, idx[:]...)
	frame = append(frame, "abc"...)

	const reuseAttempts = 50
	created := make([]*FileUploadBinaryChunkSession, reuseAttempts)
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		h.FileUploadChunkBinary(c, frame)
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < reuseAttempts; i++ {
			session, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, "ctx-reuse")
			if err == nil {
				created[i] = session
			}
		}
	}()
	close(start)
	wg.Wait()

	for _, session := range created {
		if session != nil {
			session.Cleanup()
		}
	}
	// 排空完成路径可能写出的 ack（不关心内容，避免阻塞）。
	for draining := true; draining; {
		select {
		case <-messages:
		default:
			draining = false
		}
	}
}

// TestUploadCompleteFailureRemovesSessionAndTempFile 最后一片落库失败时必须立即销毁会话
// 与临时文件：超时定时器已停，留下僵尸会话会让客户端重试拿到"全部分片已上传"却永远
// 等不到 FileUploadAck，该附件在当前连接生命周期内永久死锁。
func TestUploadCompleteFailureRemovesSessionAndTempFile(t *testing.T) {
	f := new(svcmocks.MockFileService)
	v := new(svcmocks.MockVaultService)
	f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()
	f.On("UploadComplete", mock.Anything, int64(1), mock.Anything).
		Return(false, (*dto.FileDTO)(nil), errors.New("disk full")).Once()

	a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
	a.Config().App.TempPath = t.TempDir()
	c, messages := newSyncDeletePeer(t, a)
	h := NewFileWSHandler(a)

	session, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, "ctx")
	require.NoError(t, err)
	tempPath := session.SavePath

	frame := make([]byte, 0, 40+3)
	frame = append(frame, []byte(session.ID)...)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], 0)
	frame = append(frame, idx[:]...)
	frame = append(frame, "abc"...)
	h.FileUploadChunkBinary(c, frame)

	select {
	case raw := <-messages:
		var res struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &res))
		require.Greater(t, res.Code, 299)
		require.Equal(t, session.ID, res.Data["sessionID"])
		require.Equal(t, "a.bin", res.Data["path"])
	case <-time.After(time.Second):
		t.Fatal("UploadComplete 失败后未回错误响应")
	}
	require.Nil(t, c.Server.GetSession(c.User.ID, session.ID), "僵尸会话必须被移除")
	_, statErr := os.Stat(tempPath)
	require.True(t, os.IsNotExist(statErr), "临时文件必须被删除，实际 err=%v", statErr)
}
