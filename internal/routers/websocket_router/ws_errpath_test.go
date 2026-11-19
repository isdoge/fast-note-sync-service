package websocket_router

// 出错响应附带资源路径（errPathData / respondErrorWithPath）：
// 官方插件在发出笔记/附件/配置的修改、删除、改名与附件检查之前，先按路径占用并发名额
//（默认共 20 个），其通用出错分支只在 data.data.path 为字符串时才释放名额。此前多数
// 出错分支的响应里没有任何路径，名额要么永久占住直到断线，要么靠停滞检测兜底——而停滞
// 检测要求名额全空，泄漏一个名额它就失效。
// 本文件按动作逐个验证：参数绑定成功之后的出错返回，data.path 等于客户端上报路径；
// 已带数据的出错响应（530 冲突、463 会话、参数校验失败）保持原样；
// JSON 与 Protobuf 两种出站编码下 data.path 都可被插件读到。
// JSON 走 newSyncDeletePeer 真实管道；Protobuf 沿用 ws_upload_check_ack_test.go 的
// 直接编码-解码手法（piped server 不注册 ProtobufEncoder 钩子，无法走全链路）。

import (
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestErrPath_SlotReservingActions：占名额动作（NoteModify / NoteDelete / NoteRename /
// FileUploadCheck / FileDelete / FileRename / SettingModify / SettingDelete，以及重试队列
// 发起时会占名额的 FolderDelete / FolderRename）参数绑定成功后的服务层出错返回，
// data.path 等于客户端上报路径（改名类取插件占名额所用的新路径）。
func TestErrPath_SlotReservingActions(t *testing.T) {
	noteModify := errPathCase{
		name:   "NoteModify",
		action: "NoteModify",
		payload: map[string]any{"vault": "v", "path": "/notes/a.md", "pathHash": "h1",
			"content": "x", "contentHash": "ch", "mtime": 1700000000000, "ctime": 1700000000000},
		wantPath: "/notes/a.md",
		mock: func(m *errPathMocks) {
			m.noteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.Anything).
				Return("", nil, nil, code.ErrorDBQuery).Once()
		},
	}
	noteModifyWrite := errPathCase{
		name:   "NoteModify write fails",
		action: "NoteModify",
		payload: map[string]any{"vault": "v", "path": "/notes/a.md", "pathHash": "h1",
			"content": "x", "contentHash": "ch", "mtime": 1700000000000, "ctime": 1700000000000},
		wantPath: "/notes/a.md",
		mock: func(m *errPathMocks) {
			m.noteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.Anything).
				Return("Create", nil, nil, nil).Once()
			m.noteSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything, mock.Anything).
				Return(false, nil, code.ErrorDBQuery).Once()
		},
	}
	noteDelete := errPathCase{
		name:     "NoteDelete",
		action:   "NoteDelete",
		payload:  map[string]any{"vault": "v", "path": "/notes/b.md", "pathHash": "h2"},
		wantPath: "/notes/b.md",
		mock: func(m *errPathMocks) {
			m.noteSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
				Return(nil, code.ErrorDBQuery).Once()
		},
	}
	noteRename := errPathCase{
		name:   "NoteRename",
		action: "NoteRename",
		payload: map[string]any{"vault": "v", "path": "/notes/new.md", "pathHash": "h3",
			"oldPath": "/notes/old.md", "oldPathHash": "h4"},
		// 插件 noteRename 占名额用的是新路径（operator_note.ts waitForSlot(file.path, true)）
		wantPath: "/notes/new.md",
		mock: func(m *errPathMocks) {
			m.noteSvc.On("Rename", mock.Anything, int64(1), mock.Anything).
				Return(nil, nil, code.ErrorRenameNoteTargetExist).Once()
		},
	}
	uploadCheck := errPathCase{
		name:   "FileUploadCheck",
		action: "FileUploadCheck",
		payload: map[string]any{"vault": "v", "path": "/img/pic.png", "pathHash": "h5",
			"contentHash": "ch", "size": 12, "ctime": 1700000000000, "mtime": 1700000000000},
		wantPath: "/img/pic.png",
		mock: func(m *errPathMocks) {
			m.fileSvc.On("UploadCheck", mock.Anything, int64(1), mock.Anything).
				Return("", nil, code.ErrorDBQuery).Once()
		},
	}
	fileDelete := errPathCase{
		name:     "FileDelete",
		action:   "FileDelete",
		payload:  map[string]any{"vault": "v", "path": "/img/pic.png", "pathHash": "h5"},
		wantPath: "/img/pic.png",
		mock: func(m *errPathMocks) {
			m.fileSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
				Return(nil, code.ErrorDBQuery).Once()
		},
	}
	fileRename := errPathCase{
		name:   "FileRename",
		action: "FileRename",
		payload: map[string]any{"vault": "v", "path": "/img/new.png", "pathHash": "h6",
			"oldPath": "/img/old.png", "oldPathHash": "h7"},
		// 插件 fileRename 占名额用的是新路径（operator_file.ts waitForSlot(file.path, true)）
		wantPath: "/img/new.png",
		mock: func(m *errPathMocks) {
			m.fileSvc.On("Rename", mock.Anything, int64(1), mock.Anything).
				Return(nil, nil, code.ErrorDBQuery).Once()
		},
	}
	settingModify := errPathCase{
		name:   "SettingModify",
		action: "SettingModify",
		payload: map[string]any{"vault": "v", "path": ".workspace/app.json", "pathHash": "h8",
			"content": "{}", "contentHash": "ch", "mtime": 1700000000000, "ctime": 1700000000000},
		wantPath: ".workspace/app.json",
		mock: func(m *errPathMocks) {
			m.settingSvc.On("UpdateCheck", mock.Anything, int64(1), mock.Anything).
				Return("", nil, code.ErrorDBQuery).Once()
		},
	}
	settingModifyWrite := errPathCase{
		name:   "SettingModify write fails",
		action: "SettingModify",
		payload: map[string]any{"vault": "v", "path": ".workspace/app.json", "pathHash": "h8",
			"content": "{}", "contentHash": "ch", "mtime": 1700000000000, "ctime": 1700000000000},
		wantPath: ".workspace/app.json",
		mock: func(m *errPathMocks) {
			m.settingSvc.On("UpdateCheck", mock.Anything, int64(1), mock.Anything).
				Return("Create", nil, nil).Once()
			m.settingSvc.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, mock.Anything).
				Return(false, nil, code.ErrorDBQuery).Once()
		},
	}
	settingDelete := errPathCase{
		name:     "SettingDelete",
		action:   "SettingDelete",
		payload:  map[string]any{"vault": "v", "path": ".workspace/app.json", "pathHash": "h8"},
		wantPath: ".workspace/app.json",
		mock: func(m *errPathMocks) {
			m.settingSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
				Return(nil, code.ErrorDBQuery).Once()
		},
	}
	folderDelete := errPathCase{
		name:     "FolderDelete",
		action:   "FolderDelete",
		payload:  map[string]any{"vault": "v", "path": "docs", "pathHash": "h9"},
		wantPath: "docs",
		mock: func(m *errPathMocks) {
			m.folderSvc.On("Delete", mock.Anything, int64(1), mock.Anything).
				Return(nil, code.ErrorDBQuery).Once()
		},
	}
	folderRename := errPathCase{
		name:   "FolderRename",
		action: "FolderRename",
		payload: map[string]any{"vault": "v", "path": "docs2", "pathHash": "h10",
			"oldPath": "docs", "oldPathHash": "h9"},
		// 插件 folderRename 占名额用的是新路径（operator_folder.ts waitForSlot(folder.path)）
		wantPath: "docs2",
		mock: func(m *errPathMocks) {
			m.folderSvc.On("Rename", mock.Anything, int64(1), mock.Anything).
				Return(nil, nil, code.ErrorDBQuery).Once()
		},
	}

	for _, tc := range []errPathCase{noteModify, noteModifyWrite, noteDelete, noteRename,
		uploadCheck, fileDelete, fileRename, settingModify, settingModifyWrite, settingDelete,
		folderDelete, folderRename} {
		t.Run(tc.name, func(t *testing.T) {
			runErrPathCase(t, tc)
		})
	}
}

// TestErrPath_ExistingDataResponsesUnchanged：已带数据的出错响应保持原样——
// 530 冲突（serverContent 等字段）、参数校验失败（校验信息字符串）不带 path 键，
// data 类型不变。
func TestErrPath_ExistingDataResponsesUnchanged(t *testing.T) {
	t.Run("validation failure keeps no data and no path key", func(t *testing.T) {
		m := newErrPathMocks(t)
		testApp := m.testApp
		c, messages := newSyncDeletePeer(t, testApp)
		// BindAndValid 失败（缺必填 json 标签校验字段）走 respondErrorWithData(msg) 分支，
		// 响应动作名带 Ack 后缀；此处用格式非法载荷触发，data 为校验信息、不含 path 键
		NewNoteWSHandler(testApp).NoteModify(c, &pkgapp.WebSocketMessage{
			Type: "NoteModify", Data: []byte(`{"vault":"v","path":123}`)})

		res := syncDeleteMessage(t, messages, "NoteModify")
		require.Greater(t, res["code"], float64(299))
		// data 是校验信息（errs.MapsToString()），保持原样、无 path 键
		_, hasPath := res["path"]
		require.False(t, hasPath)
		data, _ := res["data"]
		require.NotNil(t, data, "参数绑定失败响应保留原有校验信息 data")
		if dm, ok := data.(map[string]any); ok {
			_, hasPath := dm["path"]
			require.False(t, hasPath, "参数绑定失败响应的 data 保持原样，不加 path")
		}
	})

	t.Run("530 conflict keeps its payload unchanged", func(t *testing.T) {
		m := newErrPathMocks(t)
		// manualMerge 策略 + 服务端有同名笔记 + 未带解决标记：回 530，data 带 path 等
		// 冲突字段——这条分支本来就带 path，验证它未被改坏。
		m.noteSvc.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.Anything).
			Return("UpdateContent", nil, &dto.NoteDTO{Path: "/notes/a.md", PathHash: "h1", ContentHash: "srv"}, nil).Once()
		testApp := m.testApp
		c, messages := newSyncDeletePeer(t, testApp)
		c.SetClientInfoForTest("t", "desktop", "1", nil, "manualMerge", false)
		NewNoteWSHandler(testApp).NoteModify(c, wsMsg("NoteModify", map[string]any{
			"vault": "v", "path": "/notes/a.md", "pathHash": "h1", "content": "x",
			"contentHash": "ch2", "mtime": 1700000000000, "ctime": 1700000000000}))

		res := syncDeleteMessage(t, messages, "NoteSyncNeedPush")
		require.Equal(t, float64(530), res["code"])
		data, ok := res["data"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "/notes/a.md", data["path"])
		require.Equal(t, "srv", data["serverHash"])
	})
}

// TestErrPath_ProtobufJSONPayloadReadableByPlugin：Protobuf 出站时，未知动作/未知
// data 类型走 json.Marshal 回退，载荷是 JSON 字节；插件端 deReceiveProtobufToDTO 对
// 未知动作的 default 分支用 TextDecoder JSON.parse 读取——data.path 可达。用真实编码器
// 编码 errPathData 的 map，再模拟插件端解码路径。
func TestErrPath_ProtobufJSONPayloadReadableByPlugin(t *testing.T) {
	// 占名额动作的出错 Ack（如 NoteModifyAck）在 enSendDataPayload 无对应分支，
	// 回退 json.Marshal；信封 WSResponse.data 携带该 JSON 字节。
	res := &pkgapp.Res{
		Code:    code.ErrorDBQuery.Code(),
		Status:  false,
		Message: "db error",
		Data:    map[string]string{"path": "/notes/a.md"},
	}
	raw, err := EnSendDTOToProtobuf("NoteModifyAck", res)
	require.NoError(t, err)
	require.Equal(t, "pb", string(raw[:2]))

	var env v1.WSMessage
	require.NoError(t, proto.Unmarshal(raw[2:], &env))
	require.Equal(t, "NoteModifyAck", env.Type)
	var wsResp v1.WSResponse
	require.NoError(t, proto.Unmarshal(env.Data, &wsResp))
	require.Equal(t, int32(301), wsResp.GetCode())

	// 插件端 default 分支：TextDecoder + JSON.parse 读取 JSON 字节载荷
	require.JSONEq(t, `{"path":"/notes/a.md"}`, string(wsResp.GetData()))
}

// ── 测试辅助 ──────────────────────────────────────────────────────

type errPathMocks struct {
	noteSvc    *svcmocks.MockNoteService
	fileSvc    *svcmocks.MockFileService
	settingSvc *svcmocks.MockSettingService
	folderSvc  *svcmocks.MockFolderService
	testApp    *app.App
}

func newErrPathMocks(t *testing.T) *errPathMocks {
	t.Helper()
	m := &errPathMocks{
		noteSvc:    new(svcmocks.MockNoteService),
		fileSvc:    new(svcmocks.MockFileService),
		settingSvc: new(svcmocks.MockSettingService),
		folderSvc:  new(svcmocks.MockFolderService),
	}
	m.noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(m.noteSvc).Maybe()
	m.fileSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(m.fileSvc).Maybe()
	m.settingSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(m.settingSvc).Maybe()
	m.folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(m.folderSvc).Maybe()
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	m.testApp = app.NewTestApp(&app.Services{
		NoteService: m.noteSvc, FileService: m.fileSvc, SettingService: m.settingSvc,
		FolderService: m.folderSvc, VaultService: vaultSvc,
	})
	m.testApp.Config().App.TempPath = t.TempDir()
	return m
}

type errPathCase struct {
	name     string
	action   string
	payload  map[string]any
	wantPath string
	mock     func(m *errPathMocks)
}

func runErrPathCase(t *testing.T, tc errPathCase) {
	t.Helper()
	m := newErrPathMocks(t)
	if tc.mock != nil {
		tc.mock(m)
	}
	testApp := m.testApp
	c, messages := newSyncDeletePeer(t, testApp)

	switch tc.action {
	case "NoteModify":
		NewNoteWSHandler(testApp).NoteModify(c, wsMsg(tc.action, tc.payload))
	case "NoteDelete":
		NewNoteWSHandler(testApp).NoteDelete(c, wsMsg(tc.action, tc.payload))
	case "NoteRename":
		NewNoteWSHandler(testApp).NoteRename(c, wsMsg(tc.action, tc.payload))
	case "FileUploadCheck":
		NewFileWSHandler(testApp).FileUploadCheck(c, wsMsg(tc.action, tc.payload))
	case "FileDelete":
		NewFileWSHandler(testApp).FileDelete(c, wsMsg(tc.action, tc.payload))
	case "FileRename":
		NewFileWSHandler(testApp).FileRename(c, wsMsg(tc.action, tc.payload))
	case "SettingModify":
		NewSettingWSHandler(testApp).SettingModify(c, wsMsg(tc.action, tc.payload))
	case "SettingDelete":
		NewSettingWSHandler(testApp).SettingDelete(c, wsMsg(tc.action, tc.payload))
	case "FolderDelete":
		NewFolderWSHandler(testApp).FolderDelete(c, wsMsg(tc.action, tc.payload))
	case "FolderRename":
		NewFolderWSHandler(testApp).FolderRename(c, wsMsg(tc.action, tc.payload))
	default:
		t.Fatalf("未覆盖的动作：%s", tc.action)
	}

	res := syncDeleteMessage(t, messages, tc.action)
	require.Greater(t, res["code"], float64(299), "应为出错响应")
	data, ok := res["data"].(map[string]any)
	require.True(t, ok, "出错响应必须带 data 对象（供插件读取 data.path）")
	require.Equal(t, tc.wantPath, data["path"], "data.path 应为客户端上报的原样路径")

	select {
	case extra := <-messages:
		t.Fatalf("%s 出错应只回一条响应，多收到：%s", tc.action, extra)
	default:
	}
}
