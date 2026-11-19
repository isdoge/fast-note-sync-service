package websocket_router

// FileUploadCheck 的 default（无需更新）分支回 FileUploadAck（LastTime 留 0），
// 与 ws_note.go NoteModifyCheck 的 NoteModifyAck 处理同源：默认配置
// is-return-sussess=false 下 SuccessNoUpdate 什么都不回，官方插件发检查前占用的
// 并发名额只有收到 FileUpload/FileSyncMtime/FileUploadAck 才释放，被分页上传
// 抢先传完的附件其监听检查若不回包，名额被成批占住，整轮同步不收尾。
// JSON 走 newSyncDeletePeer 真实管道端到端断言；Protobuf 沿用
// ws_sync_envelope_pageindex_test.go 的直接编码-解码手法（piped server 不注册
// ProtobufEncoder 钩子，无法走全链路）。

import (
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func newUploadCheckApp(t *testing.T, fileSvc *svcmocks.MockFileService) *app.App {
	t.Helper()
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()
	return app.NewTestApp(&app.Services{FileService: fileSvc, VaultService: vaultSvc})
}

func uploadCheckMsg(context string) *pkgapp.WebSocketMessage {
	return wsMsg("FileUploadCheck", map[string]any{
		"vault": "v", "path": "/images/pic.png", "pathHash": "ph", "contentHash": "ch",
		"size": 12, "ctime": 1700000000000, "mtime": 1700000000000, "context": context,
	})
}

// TestFileUploadCheck_NoUpdateSendsAck：默认配置（不回普通成功）下，"无需更新"的检查
// 恰好发出一条消息：动作名 FileUploadAck，path/pathHash 为客户端上报值、库名与
// context 齐全、lastTime 为 0；fileSvc 为 nil 时仍不发消息（保持原行为）。
func TestFileUploadCheck_NoUpdateSendsAck(t *testing.T) {
	t.Run("no update acks exactly once with lastTime 0", func(t *testing.T) {
		fileSvc := new(svcmocks.MockFileService)
		// 服务端已有同样内容（UpdateCheck 走 default 的条件），DTO 路径故意与客户端不同，
		// 证明 ack 里的 path 用客户端上报的原样路径（插件按它释放并发名额）。
		fileSvc.On("UploadCheck", mock.Anything, int64(1), mock.Anything).
			Return("", &dto.FileDTO{Path: "server-side-path.png", PathHash: "other-hash", ContentHash: "ch", Mtime: 1700000000000}, nil).Once()
		testApp := newUploadCheckApp(t, fileSvc)
		c, messages := newSyncDeletePeer(t, testApp)
		NewFileWSHandler(testApp).FileUploadCheck(c, uploadCheckMsg("ctx-ack"))

		res := syncDeleteMessage(t, messages, "FileUploadAck")
		require.Equal(t, float64(1), res["code"])
		require.Equal(t, "v", res["vault"])
		require.Equal(t, "ctx-ack", res["context"])
		data := res["data"].(map[string]any)
		require.Equal(t, "/images/pic.png", data["path"])
		require.Equal(t, "ph", data["pathHash"])
		require.Zero(t, data["lastTime"])

		// 恰好一条：不得再有任何消息（包括没有带默认配置会被吞掉的普通成功帧）。
		select {
		case extra := <-messages:
			t.Fatalf("无需更新应只回一条 FileUploadAck，多收到：%s", extra)
		default:
		}
		fileSvc.AssertExpectations(t)
	})

	t.Run("nil fileSvc still sends nothing", func(t *testing.T) {
		fileSvc := new(svcmocks.MockFileService)
		fileSvc.On("UploadCheck", mock.Anything, int64(1), mock.Anything).
			Return("", nil, nil).Once()
		testApp := newUploadCheckApp(t, fileSvc)
		c, messages := newSyncDeletePeer(t, testApp)
		NewFileWSHandler(testApp).FileUploadCheck(c, uploadCheckMsg("ctx-nil"))

		select {
		case msg := <-messages:
			t.Fatalf("fileSvc 为 nil 应回保持原行为（不发包），实际收到：%s", msg)
		default:
		}
		fileSvc.AssertExpectations(t)
	})
}

// TestFileUploadCheck_OtherModesUnchanged：UpdateMtime 仍回 FileSyncMtime，
// Create / UpdateContent 仍回 FileUpload（带会话号），不被本次改动影响。
func TestFileUploadCheck_OtherModesUnchanged(t *testing.T) {
	for _, mode := range []string{"Create", "UpdateContent", "UpdateMtime"} {
		t.Run(mode, func(t *testing.T) {
			fileSvc := new(svcmocks.MockFileService)
			fileSvc.On("UploadCheck", mock.Anything, int64(1), mock.Anything).
				Return(mode, &dto.FileDTO{
					Path: "/images/pic.png", PathHash: "ph", ContentHash: "ch",
					Ctime: 1700000000000, Mtime: 1700000000000, UpdatedTimestamp: 42,
				}, nil).Once()
			testApp := newUploadCheckApp(t, fileSvc)
			testApp.Config().App.TempPath = t.TempDir()
			c, messages := newSyncDeletePeer(t, testApp)
			NewFileWSHandler(testApp).FileUploadCheck(c, uploadCheckMsg("ctx-"+mode))

			wantAction := "FileUpload"
			if mode == "UpdateMtime" {
				wantAction = "FileSyncMtime"
			}
			res := syncDeleteMessage(t, messages, wantAction)
			data := res["data"].(map[string]any)
			if mode == "UpdateMtime" {
				require.Equal(t, float64(1700000000000), data["mtime"])
				require.Equal(t, float64(42), data["lastTime"])
			} else {
				require.NotEmpty(t, data["sessionId"])
				require.Equal(t, "ph", data["pathHash"])
			}
			select {
			case extra := <-messages:
				t.Fatalf("%s 应只回一条 %s，多收到：%s", mode, wantAction, extra)
			default:
			}
			fileSvc.AssertExpectations(t)
		})
	}
}

// TestFileUploadCheck_NoUpdateAckProtobuf：确认 FileUploadAck 在 Protobuf 映射里存在、
// 零值 LastTime 能正常编码（proto3 零值省略，解码回 0），path/pathHash 原样落线。
func TestFileUploadCheck_NoUpdateAckProtobuf(t *testing.T) {
	res := &pkgapp.Res{
		Code:    1,
		Status:  true,
		Data:    dto.FileUploadAckMessage{Path: "/images/pic.png", PathHash: "ph"},
		Vault:   "v",
		Context: "ctx-pb",
	}
	raw, err := EnSendDTOToProtobuf(FileUploadAck, res)
	require.NoError(t, err)
	require.Greater(t, len(raw), 2)
	require.Equal(t, "pb", string(raw[:2]))

	var env v1.WSMessage
	require.NoError(t, proto.Unmarshal(raw[2:], &env))
	require.Equal(t, "FileUploadAck", env.Type)
	var wsResp v1.WSResponse
	require.NoError(t, proto.Unmarshal(env.Data, &wsResp))
	require.Equal(t, "v", wsResp.GetVault())
	require.Equal(t, "ctx-pb", wsResp.GetContext())

	var ack v1.FileUploadAckMessage
	require.NoError(t, proto.Unmarshal(wsResp.GetData(), &ack))
	require.Equal(t, "/images/pic.png", ack.GetPath())
	require.Equal(t, "ph", ack.GetPathHash())
	require.Zero(t, ack.GetLastTime())
}
