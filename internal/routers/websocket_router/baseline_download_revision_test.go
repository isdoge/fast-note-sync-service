package websocket_router

// 附件下载就绪消息的回归测试：FileSyncChunkDownload 必须携带该附件当前的 revision，
// 否则插件下载完成后建不起“已确认基线”，之后对它的改名/修改会被服务端当成无基线冲突。
//
// Regression test for the download-ready message: FileSyncChunkDownload must carry the
// attachment's current revision, otherwise the plugin cannot record a confirmed baseline
// after downloading and every later rename/edit is rejected as a no-baseline conflict.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestFileChunkDownload_ReadyMessageCarriesRevision 下载就绪消息里的 revision 必须等于
// 服务端该附件行当前的 revision。
func TestFileChunkDownload_ReadyMessageCarriesRevision(t *testing.T) {
	savePath := filepath.Join(t.TempDir(), "a.bin")
	require.NoError(t, os.WriteFile(savePath, []byte("data"), 0o644))

	fileSvc := new(svcmocks.MockFileService)
	vaultSvc := new(svcmocks.MockVaultService)
	fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(fileSvc).Maybe()
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()
	fileSvc.On("Get", mock.Anything, int64(1), mock.Anything).Return(&dto.FileDTO{
		Path: "a.bin", PathHash: "fh", ContentHash: "ch", Size: 4,
		Ctime: baseMT, Mtime: baseMT + 1, SavePath: savePath, Revision: testRevisionV1,
	}, nil).Once()

	testApp := app.NewTestApp(&app.Services{FileService: fileSvc, VaultService: vaultSvc})
	c, messages := newSyncDeletePeer(t, testApp)
	h := NewFileWSHandler(testApp)

	h.FileChunkDownload(c, wsMsg("FileChunkDownload", map[string]any{
		"vault": "v", "path": "a.bin", "pathHash": "fh",
	}))

	res := syncDeleteMessage(t, messages, string(FileSyncChunkDownload))
	data := res["data"].(map[string]any)
	require.Equal(t, "a.bin", data["path"])
	require.Equal(t, testRevisionV1, data["revision"], "下载就绪消息必须带附件当前 revision")
	fileSvc.AssertExpectations(t)
}

// TestFileChunkDownload_ProtobufCarriesRevision Protobuf 协商连接的下载就绪消息同样要
// 带上 revision（插件默认使用 Protobuf，不能只在 JSON 路径生效）。
func TestFileChunkDownload_ProtobufCarriesRevision(t *testing.T) {
	raw := decodePBResponse(t, FileSyncChunkDownload, &pkgapp.Res{Data: dto.FileSyncDownloadMessage{
		Path: "a.bin", ContentHash: "ch", Ctime: baseMT, Mtime: baseMT + 1,
		SessionID: "sess", ChunkSize: 4, TotalChunks: 1, Size: 4, Revision: testRevisionV1,
	}})

	var pbMsg v1.FileSyncDownloadMessage
	require.NoError(t, proto.Unmarshal(raw, &pbMsg))
	require.Equal(t, "a.bin", pbMsg.GetPath())
	require.Equal(t, testRevisionV1, pbMsg.GetRevision())
}
