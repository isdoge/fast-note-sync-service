package websocket_router

// 基线模式批量上传的回归测试：FileSync 决定让客户端上传附件时，上传会话必须使用
// 客户端清单里上报的 Size/Ctime/Mtime，而不是服务端旧行（新建时为 0）的字段。
// 否则会话 TotalChunks 与真实大小不符，首个分片就被判定完成，落库 size 错误、内容截断。
//
// Regression tests for baseline-mode batch upload: when FileSync asks the client to
// upload an attachment, the session must use the Size/Ctime/Mtime reported in the
// client's manifest, not the old (or absent) server row.

import (
	"os"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// baselineUploadSizeApp 构造附件批量基线同步所需的最小 App（4 字节分片，便于构造多分片）。
func baselineUploadSizeApp(fileSvc *svcmocks.MockFileService, vaultSvc *svcmocks.MockVaultService, tempDir string) *app.App {
	testApp := app.NewTestApp(&app.Services{FileService: fileSvc, VaultService: vaultSvc})
	testApp.Config().App.TempPath = tempDir
	testApp.Config().App.FileChunkSize = "4B"
	testApp.Config().App.SyncDownChunkNum = 10
	return testApp
}

// TestFileSyncBaseline_UploadSessionUsesClientSizeForNewPath 服务端没有该路径、客户端上报
// 大小 N（跨多个分片）时，会话必须按 N 计算分片数，全部上传后落库 size == N 且内容完整。
func TestFileSyncBaseline_UploadSessionUsesClientSizeForNewPath(t *testing.T) {
	const payload = "0123456789" // 10 字节，4 字节分片 → 3 片

	fileSvc := new(svcmocks.MockFileService)
	vaultSvc := new(svcmocks.MockVaultService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc).Maybe()
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()
	fileSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.FileDTO{}, nil).Once()
	fileSvc.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", []string{"big.bin"}).
		Return(map[string]*dto.FileDTO{}, nil).Once()

	var submitted *dto.FileUpdateRequest
	var submittedBody []byte
	fileSvc.On("UploadComplete", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.FileUpdateRequest) bool {
		submitted = req
		submittedBody, _ = os.ReadFile(req.SavePath)
		return true
	})).Return(false, &dto.FileDTO{
		Path: "big.bin", PathHash: "fh", Action: "create", ContentHash: "local-hash",
		Size: int64(len(payload)), Ctime: baseMT, Mtime: baseMT + 1000,
		Revision: testRevisionV2, UpdatedTimestamp: 42,
	}, nil).Once()

	testApp := baselineUploadSizeApp(fileSvc, vaultSvc, t.TempDir())
	c, messages := baselineNotePeer(t, testApp)
	h := NewFileWSHandler(testApp)
	const ctx = "baseline-upload-size-new"

	h.doFileSyncBaseline(c, &dto.FileSyncRequest{
		Context: ctx, Vault: "v",
		Files: []dto.FileSyncCheckRequest{{
			Path: "big.bin", PathHash: "fh", ContentHash: "local-hash",
			Size: int64(len(payload)), Ctime: baseMT, Mtime: baseMT + 1000,
		}},
	})

	end := syncDeleteMessage(t, messages, string(FileSyncEnd))
	require.Equal(t, float64(1), end["data"].(map[string]any)["needUploadCount"])

	entry := baselineQueue(t, ctx, "file")
	require.Equal(t, FileUpload, entry.MessageQueue[0].Action)
	uploadMsg, ok := entry.MessageQueue[0].Data.(dto.FileSyncUploadMessage)
	require.True(t, ok)

	session, ok := c.Server.GetSession(c.User.ID, uploadMsg.SessionID).(*FileUploadBinaryChunkSession)
	require.True(t, ok)
	t.Cleanup(session.Cleanup)
	require.EqualValues(t, len(payload), session.Size, "会话大小必须取客户端上报值")
	require.EqualValues(t, 3, session.TotalChunks, "分片数必须按客户端大小计算")
	require.Equal(t, baseMT, session.Ctime, "会话创建时间必须取客户端上报值")
	require.Equal(t, baseMT+1000, session.Mtime, "会话修改时间必须取客户端上报值")

	parts := []string{payload[0:4], payload[4:8], payload[8:]}
	for i, part := range parts {
		h.FileUploadChunkBinary(c, uploadChunkFrame(session.ID, uint32(i), part))
	}

	ack := syncDeleteMessage(t, messages, string(FileUploadAck))
	require.Equal(t, float64(1), ack["code"])
	require.NotNil(t, submitted, "全部分片后必须提交一次")
	require.EqualValues(t, len(payload), submitted.Size, "落库大小必须等于客户端上报值")
	require.Equal(t, baseMT, submitted.Ctime)
	require.Equal(t, baseMT+1000, submitted.Mtime)
	require.Equal(t, payload, string(submittedBody), "上传内容必须完整、不得截断")
	fileSvc.AssertExpectations(t)
}

// TestFileSyncBaseline_UploadSessionUsesClientSizeForExistingPath 服务端已有旧版本、客户端
// 上传更大的新版本时，会话仍按新版客户端上报的大小计算，不得用旧行大小提前判定完成。
func TestFileSyncBaseline_UploadSessionUsesClientSizeForExistingPath(t *testing.T) {
	const payload = "0123456789" // 新版本 10 字节，旧版本只有 4 字节

	fileSvc := new(svcmocks.MockFileService)
	vaultSvc := new(svcmocks.MockVaultService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc).Maybe()
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()
	fileSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.FileDTO{{
			ID: 5, Path: "big.bin", PathHash: "fh", ContentHash: "old-hash",
			Size: 4, Ctime: baseMT - 9000, Mtime: baseMT - 9000,
			Revision: testRevisionV1, Action: "create", UpdatedTimestamp: 100,
		}}, nil).Once()

	var submitted *dto.FileUpdateRequest
	var submittedBody []byte
	fileSvc.On("UploadComplete", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.FileUpdateRequest) bool {
		submitted = req
		submittedBody, _ = os.ReadFile(req.SavePath)
		return true
	})).Return(false, &dto.FileDTO{
		Path: "big.bin", PathHash: "fh", Action: "create", ContentHash: "local-hash",
		Size: int64(len(payload)), Ctime: baseMT, Mtime: baseMT + 1000,
		Revision: testRevisionV2, UpdatedTimestamp: 43,
	}, nil).Once()

	testApp := baselineUploadSizeApp(fileSvc, vaultSvc, t.TempDir())
	c, messages := baselineNotePeer(t, testApp)
	h := NewFileWSHandler(testApp)
	const ctx = "baseline-upload-size-existing"

	h.doFileSyncBaseline(c, &dto.FileSyncRequest{
		Context: ctx, Vault: "v",
		Files: []dto.FileSyncCheckRequest{{
			Path: "big.bin", PathHash: "fh", ContentHash: "local-hash",
			Size: int64(len(payload)), Ctime: baseMT, Mtime: baseMT + 1000,
			BaseHash: "old-hash", BaseRevision: testRevisionV1,
		}},
	})

	end := syncDeleteMessage(t, messages, string(FileSyncEnd))
	require.Equal(t, float64(1), end["data"].(map[string]any)["needUploadCount"])

	entry := baselineQueue(t, ctx, "file")
	require.Equal(t, FileUpload, entry.MessageQueue[0].Action)
	uploadMsg, ok := entry.MessageQueue[0].Data.(dto.FileSyncUploadMessage)
	require.True(t, ok)

	session, ok := c.Server.GetSession(c.User.ID, uploadMsg.SessionID).(*FileUploadBinaryChunkSession)
	require.True(t, ok)
	t.Cleanup(session.Cleanup)
	require.EqualValues(t, len(payload), session.Size, "新版本会话不得沿用服务端旧行大小")
	require.EqualValues(t, 3, session.TotalChunks)
	require.Equal(t, baseMT, session.Ctime, "新版本会话不得沿用服务端旧行 ctime")
	require.Equal(t, baseMT+1000, session.Mtime, "新版本会话不得沿用服务端旧行 mtime")

	parts := []string{payload[0:4], payload[4:8], payload[8:]}
	for i, part := range parts {
		h.FileUploadChunkBinary(c, uploadChunkFrame(session.ID, uint32(i), part))
	}

	ack := syncDeleteMessage(t, messages, string(FileUploadAck))
	require.Equal(t, float64(1), ack["code"])
	require.NotNil(t, submitted)
	require.EqualValues(t, len(payload), submitted.Size)
	require.Equal(t, payload, string(submittedBody), "新版本上传内容必须完整、不得被旧大小截断")
	fileSvc.AssertExpectations(t)
}
