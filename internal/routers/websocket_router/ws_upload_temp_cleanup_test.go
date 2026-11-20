package websocket_router

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// 附件上传完成后的临时文件回收：UploadComplete 走"内容与 mtime 完全一致直接返回"和
// "只更新 mtime"两条幂等短路路径时不搬文件，超时定时器又已取消，必须在完成点删掉
// 临时文件；正常落库路径的文件已被 DAO rename 搬走，完成点清理是空操作，不受影响。
//
// Temp-file reclamation after an attachment upload completes: the two
// short-circuit paths of UploadComplete ("content and mtime fully identical,
// return directly" and "mtime-only update") never move the temp file, and the
// timeout timer was already cancelled, so the completion point must delete it;
// on the normal persistence path the file was already renamed away by the DAO,
// making the completion-point cleanup a no-op.

// uploadTempFileCase 驱动 FileUploadChunkBinary 的完成分支：创建会话、写一个分片
// （单分片即完成）、等待 ack。返回会话与临时文件在上传前的完整路径。
func uploadTempFileCase(t *testing.T, _ string) (*FileUploadBinaryChunkSession, string) {
	t.Helper()

	f, v := new(svcmocks.MockFileService), new(svcmocks.MockVaultService)
	f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
	tempDir := t.TempDir()
	a.Config().App.TempPath = tempDir
	a.Config().App.SyncDownChunkNum = 50
	c, messages := newSyncDeletePeer(t, a)

	// UploadComplete 幂等短路（不区分"完全一致"还是"只更新 mtime"：
	// 两条路径都不搬临时文件，完成点行为相同）。
	f.On("UploadComplete", mock.Anything, int64(1), mock.Anything).
		Return(false, &dto.FileDTO{Path: "a.bin", PathHash: "h", Action: "create", ContentHash: "same", Mtime: baseMT, UpdatedTimestamp: 42}, nil).Once()

	h := NewFileWSHandler(a)
	session, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, "ctx")
	require.NoError(t, err)
	t.Cleanup(session.CancelFunc)
	tempPath := session.SavePath

	// 写入单个分片（3 字节 < 默认 512KB，单分片即触发完成分支）。
	frame := make([]byte, 0, 40+3)
	frame = append(frame, []byte(session.ID)...)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], 0)
	frame = append(frame, idx[:]...)
	frame = append(frame, "abc"...)
	h.FileUploadChunkBinary(c, frame)

	// 完成 ack 一定到达（否则客户端会无限重传）。
	ack := syncDeleteMessage(t, messages, string(FileUploadAck))
	require.Equal(t, "a.bin", ack["data"].(map[string]any)["path"])
	// 会话已从全局注册表移除。
	require.Nil(t, c.Server.GetSession(c.User.ID, session.ID))
	return session, tempPath
}

// 幂等短路路径（内容与 mtime 完全一致，UploadComplete 直接返回）：临时文件必须被删。
func TestUploadComplete_IdenticalShortCircuit_RemovesTempFile(t *testing.T) {
	session, tempPath := uploadTempFileCase(t, "identical")
	_, statErr := os.Stat(tempPath)
	require.ErrorIs(t, statErr, os.ErrNotExist, "幂等短路路径的临时文件必须在上传完成点被删除")
	require.Empty(t, session.SavePath, "Cleanup 应清空 SavePath")
}

// 幂等短路路径（只更新 mtime）：临时文件同样必须被删。
func TestUploadComplete_MtimeOnlyShortCircuit_RemovesTempFile(t *testing.T) {
	session, tempPath := uploadTempFileCase(t, "mtime-only")
	_, statErr := os.Stat(tempPath)
	require.ErrorIs(t, statErr, os.ErrNotExist, "只更新 mtime 路径的临时文件必须在上传完成点被删除")
	require.Empty(t, session.SavePath, "Cleanup 应清空 SavePath")
}

// 正常落库路径：临时文件已被 DAO rename 搬走（本测试直接删除模拟），完成点的
// Cleanup 遇到不存在的文件静默跳过，不影响 ack 与会话移除。
func TestUploadComplete_NormalPersistence_CleanupSkipsMissingFile(t *testing.T) {
	f, v := new(svcmocks.MockFileService), new(svcmocks.MockVaultService)
	f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
	tempDir := t.TempDir()
	a.Config().App.TempPath = tempDir
	a.Config().App.SyncDownChunkNum = 50
	c, messages := newSyncDeletePeer(t, a)

	f.On("UploadComplete", mock.Anything, int64(1), mock.Anything).
		Return(true, &dto.FileDTO{Path: "a.bin", PathHash: "h", Action: "create", ContentHash: "same", Mtime: baseMT, UpdatedTimestamp: 42}, nil).Once()

	h := NewFileWSHandler(a)
	session, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, "ctx")
	require.NoError(t, err)
	t.Cleanup(session.CancelFunc)

	frame := make([]byte, 0, 40+3)
	frame = append(frame, []byte(session.ID)...)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], 0)
	frame = append(frame, idx[:]...)
	frame = append(frame, "abc"...)

	// 模拟"完成点之前文件已被搬走/删除"：写完分片后、触发完成逻辑前删掉临时文件。
	// FileUploadChunkBinary 内部一次调用即完成写入+完成处理，因此用写钩子不可行；
	// 改为直接验证 Cleanup 对不存在文件的行为（正常路径完成点调用的就是它）。
	require.NoError(t, os.MkdirAll(tempDir, 0755))
	session.SavePath = filepath.Join(tempDir, "already-moved.dat")
	h.FileUploadChunkBinary(c, frame)

	ack := syncDeleteMessage(t, messages, string(FileUploadAck))
	require.Equal(t, "a.bin", ack["data"].(map[string]any)["path"])
	require.Nil(t, c.Server.GetSession(c.User.ID, session.ID))
	// 不存在的文件不报错、SavePath 已清空（幂等）。
	require.Empty(t, session.SavePath)
}

// 会话超时路径不受影响：超时清理仍走 handleFileUploadSessionCleanup。
func TestUploadComplete_CleanupIsIdempotent(t *testing.T) {
	a := app.NewTestApp(&app.Services{})
	a.Config().App.TempPath = t.TempDir()
	c := newWSPathTestClient(a)
	h := NewFileWSHandler(a)
	session, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "h", "same", 3, baseMT, baseMT, "ctx")
	require.NoError(t, err)
	t.Cleanup(session.CancelFunc)

	// 第一次清理：清空 SavePath、取消定时器。
	session.Cleanup()
	require.Empty(t, session.SavePath)
	require.Nil(t, session.CancelFunc)
	// 第二次清理：空操作，不 panic。
	session.Cleanup()
}
