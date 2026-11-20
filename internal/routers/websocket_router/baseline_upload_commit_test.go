package websocket_router

// 附件分片上传的条件提交测试：会话绑定客户端基线，最终分片提交时在服务层临界区内
// 再校验一次；期间远端被另一设备改动则不得替换活跃附件，而是回 530 + SyncConflict
// 并回收会话与临时文件（设计 §2.4、§3.6、§3.5）。
//
// Conditional-commit tests for chunked attachment upload: the session binds the
// client baseline and the final chunk commit re-checks it, so a remote change during
// the upload can never be overwritten.

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// uploadChunkFrame 构造单个分片的二进制帧（会话 ID + 分片序号 + 数据）。
// uploadChunkFrame builds one chunk frame (session id + chunk index + payload).
func uploadChunkFrame(sessionID string, index uint32, payload string) []byte {
	frame := make([]byte, 0, 40+len(payload))
	frame = append(frame, []byte(sessionID)...)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], index)
	frame = append(frame, idx[:]...)
	return append(frame, payload...)
}

// TestUploadChunk_FinalCommitRejectedWhenRemoteChanged 上传期间远端被改：最终提交必须被
// 拒绝，活跃附件保持远端版本，会话与临时文件被回收。
func TestUploadChunk_FinalCommitRejectedWhenRemoteChanged(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	vaultSvc := new(svcmocks.MockVaultService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc).Maybe()
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()

	// 服务层在临界区内发现令牌已变，返回冲突。
	// The service layer finds a moved revision inside its critical section.
	var sawBaseline *dto.BaselineCommit
	fileSvc.On("UploadComplete", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.FileUpdateRequest) bool {
		sawBaseline = req.Baseline
		return true
	})).Return(false, (*dto.FileDTO)(nil), &dto.BaselineConflictError{
		Reason:         dto.BaselineRejectRevisionMoved,
		ServerActive:   true,
		ServerRevision: testRevisionV2,
		ServerHash:     "remote-hash",
	}).Once()

	testApp := app.NewTestApp(&app.Services{FileService: fileSvc, VaultService: vaultSvc})
	testApp.Config().App.TempPath = t.TempDir()
	c, messages := newSyncDeletePeer(t, testApp)
	c.SetSyncBaselineVersionForTest(1)
	h := NewFileWSHandler(testApp)

	session, err := h.handleFileUploadSessionCreateWithBaseline(c, "v", "a.bin", "fh", "local-hash", 3, baseMT, baseMT, "ctx", baselineClientState{
		LocalHash: "local-hash", BaseHash: "base-hash", HasBase: true, BaseRevision: testRevisionV1, IsFile: true,
	})
	require.NoError(t, err)
	tempPath := session.SavePath

	h.FileUploadChunkBinary(c, uploadChunkFrame(session.ID, 0, "abc"))

	res := syncDeleteMessage(t, messages, string(SyncConflict))
	require.Equal(t, float64(530), res["code"], "条件提交被拒必须回 530")
	data := res["data"].(map[string]any)
	require.Equal(t, "a.bin", data["path"])
	require.Equal(t, dto.SyncConflictKindFile, data["kind"])
	require.Equal(t, dto.SyncConflictReasonRevisionChanged, data["reason"])
	require.Equal(t, testRevisionV2, data["serverRevision"])
	require.Equal(t, false, data["serverDeleted"])

	require.NotNil(t, sawBaseline, "会话基线必须传给 UploadComplete")
	require.Equal(t, testRevisionV1, sawBaseline.BaseRevision)
	require.Equal(t, "base-hash", sawBaseline.BaseHash)
	require.True(t, sawBaseline.HasBase)

	require.Nil(t, c.Server.GetSession(c.User.ID, session.ID), "冲突后必须回收上传会话")
	_, statErr := os.Stat(tempPath)
	require.True(t, os.IsNotExist(statErr), "冲突后必须删除临时文件，实际 err=%v", statErr)
	fileSvc.AssertExpectations(t)
}

// TestUploadChunk_LegacySessionHasNoBaseline 版本 0 的会话不携带条件提交契约，
// 最终提交保持旧的无条件覆盖行为。
func TestUploadChunk_LegacySessionHasNoBaseline(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	vaultSvc := new(svcmocks.MockVaultService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc).Maybe()
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()

	var sawBaseline *dto.BaselineCommit
	baselineSet := false
	fileSvc.On("UploadComplete", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.FileUpdateRequest) bool {
		sawBaseline = req.Baseline
		baselineSet = true
		return true
	})).Return(false, &dto.FileDTO{
		Path: "a.bin", PathHash: "fh", Action: "create", ContentHash: "local-hash",
		Revision: testRevisionV2, Mtime: baseMT, UpdatedTimestamp: 42,
	}, nil).Once()

	testApp := app.NewTestApp(&app.Services{FileService: fileSvc, VaultService: vaultSvc})
	testApp.Config().App.TempPath = t.TempDir()
	c, messages := newSyncDeletePeer(t, testApp)
	h := NewFileWSHandler(testApp)

	// 旧连接：版本 0，走原会话创建路径。
	// Legacy connection: version 0, the original session path.
	session, err := h.handleFileUploadSessionCreate(c, "v", "a.bin", "fh", "local-hash", 3, baseMT, baseMT, "ctx")
	require.NoError(t, err)
	require.Zero(t, session.SyncBaselineVersion)

	h.FileUploadChunkBinary(c, uploadChunkFrame(session.ID, 0, "abc"))

	res := syncDeleteMessage(t, messages, string(FileUploadAck))
	require.Equal(t, float64(1), res["code"], "旧连接写入必须照常成功")
	data := res["data"].(map[string]any)
	require.Equal(t, "local-hash", data["contentHash"])
	require.Equal(t, testRevisionV2, data["revision"], "ack 必须带上刷新后的令牌")
	require.True(t, baselineSet, "必须观察到 UploadComplete 调用")
	require.Nil(t, sawBaseline, "版本 0 的会话不得附加条件提交契约")
	fileSvc.AssertExpectations(t)
}

// TestUploadCheck_BaselineModeBindsSession 基线模式的 FileUploadCheck 必须把基线绑进会话。
func TestUploadCheck_BaselineModeBindsSession(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	vaultSvc := new(svcmocks.MockVaultService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc).Maybe()
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()
	fileSvc.On("UploadCheck", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.FileUpdateCheckRequest) bool {
		return req.Baseline != nil && req.Baseline.BaseRevision == testRevisionV1 && req.Baseline.HasBase
	})).Return("UpdateContent", &dto.FileDTO{Path: "a.bin", ContentHash: "base-hash"}, nil).Once()

	testApp := app.NewTestApp(&app.Services{FileService: fileSvc, VaultService: vaultSvc})
	testApp.Config().App.TempPath = t.TempDir()
	c, messages := newSyncDeletePeer(t, testApp)
	c.SetSyncBaselineVersionForTest(1)
	h := NewFileWSHandler(testApp)

	h.FileUploadCheck(c, wsMsg("FileUploadCheck", map[string]any{
		"vault": "v", "path": "a.bin", "pathHash": "fh",
		"contentHash": "local-hash", "size": 12, "ctime": baseMT, "mtime": baseMT,
		"context": "ctx-upload-check", "baseHash": "base-hash",
		"baseRevision": testRevisionV1,
	}))

	res := syncDeleteMessage(t, messages, string(FileUpload))
	data := res["data"].(map[string]any)
	sessionID, _ := data["sessionId"].(string)
	require.NotEmpty(t, sessionID)

	session, ok := c.Server.GetSession(c.User.ID, sessionID).(*FileUploadBinaryChunkSession)
	require.True(t, ok)
	t.Cleanup(session.Cleanup)
	require.EqualValues(t, 1, session.SyncBaselineVersion)
	require.Equal(t, "base-hash", session.BaseHash)
	require.Equal(t, testRevisionV1, session.BaseRevision)
	require.False(t, session.BaseHashMissing)
	fileSvc.AssertExpectations(t)
}
