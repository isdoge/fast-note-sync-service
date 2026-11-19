package websocket_router

import (
	"strings"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// 增量同步（lastTime > 0）窗口外条目的判定：客户端上报、但不在 updated_timestamp >
// lastTime 结果里的条目，仅当服务端该路径最新一行是存活行且内容哈希、修改时间都与
// 客户端上报一致时跳过；其余情形（修改时间不同、哈希不同、墓碑、服务端没有）保持
// 原有"要求上传"的判定。lastTime == 0 时不做额外批量查询。
//
// Out-of-window entries in incremental sync (lastTime > 0): entries the client
// reported but that are not in the updated_timestamp > lastTime result are skipped
// only when the server's latest row for the path is alive and both content hash and
// mtime match the client report; every other case (mtime differs, hash differs,
// tombstone, missing on server) keeps the existing "request upload" verdict.
// No extra batch query happens when lastTime == 0.

// syncWindowCase 描述一个窗口外条目用例：服务端窗口外行的状态与客户端上报的哈希/时间。
type syncWindowCase struct {
	name string

	serverAction string // 服务端最新行的 action；"" 表示服务端没有该路径
	serverHash   string
	serverMtime  int64
	clientHash   string
	clientMtime  int64

	wantUpload int // 期望 needUploadCount
}

// runNoteWindowCase 驱动 doNoteSync：窗口（ListByLastTime）为空，客户端上报一条
// 窗口外条目；lastTime > 0 时按路径查服务端当前行。
func runNoteWindowCase(t *testing.T, tc syncWindowCase, lastTime int64) map[string]any {
	t.Helper()
	n, v := new(svcmocks.MockNoteService), new(svcmocks.MockVaultService)
	n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{NoteService: n, VaultService: v})
	c, messages := newSyncDeletePeer(t, a)
	ctx := t.Name()
	t.Cleanup(func() { syncDownloadDelete(ctx, "note") })

	const mt = int64(1700000000000)
	serverRow := &dto.NoteDTO{Path: "a.md", PathHash: "h", ContentHash: tc.serverHash, Mtime: tc.serverMtime}
	if tc.serverAction != "" {
		serverRow.Action = tc.serverAction
	}
	metaBatch := map[string]*dto.NoteDTO{}
	if tc.serverAction != "" {
		metaBatch["a.md"] = serverRow
	}
	n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
	if lastTime > 0 {
		n.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", []string{"a.md"}).Return(metaBatch, nil).Once()
	}
	NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{
		Vault: "v", Context: ctx, LastTime: lastTime,
		Notes: []dto.NoteSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: tc.clientHash, Mtime: tc.clientMtime, Ctime: mt}},
	})
	return syncDeleteMessage(t, messages, NoteSyncEnd)["data"].(map[string]any)
}

func runFileWindowCase(t *testing.T, tc syncWindowCase, lastTime int64) map[string]any {
	t.Helper()
	f, v := new(svcmocks.MockFileService), new(svcmocks.MockVaultService)
	f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
	a.Config().App.TempPath = t.TempDir()
	c, messages := newSyncDeletePeer(t, a)
	ctx := t.Name()
	t.Cleanup(func() { syncDownloadDelete(ctx, "file") })

	const mt = int64(1700000000000)
	serverRow := &dto.FileDTO{Path: "a.md", PathHash: "h", ContentHash: tc.serverHash, Mtime: tc.serverMtime}
	if tc.serverAction != "" {
		serverRow.Action = tc.serverAction
	}
	metaBatch := map[string]*dto.FileDTO{}
	if tc.serverAction != "" {
		metaBatch["a.md"] = serverRow
	}
	f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{}, nil)
	if lastTime > 0 {
		f.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", []string{"a.md"}).Return(metaBatch, nil).Once()
	}
	NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{
		Vault: "v", Context: ctx, LastTime: lastTime,
		Files: []dto.FileSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: tc.clientHash, Mtime: tc.clientMtime, Ctime: mt}},
	})
	return syncDeleteMessage(t, messages, FileSyncEnd)["data"].(map[string]any)
}

func runSettingWindowCase(t *testing.T, tc syncWindowCase, lastTime int64) map[string]any {
	t.Helper()
	s, v := new(svcmocks.MockSettingService), new(svcmocks.MockVaultService)
	s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{SettingService: s, VaultService: v})
	c, messages := newSyncDeletePeer(t, a)
	ctx := t.Name()
	t.Cleanup(func() { syncDownloadDelete(ctx, "setting") })

	serverRow := &dto.SettingDTO{Path: "a.md", PathHash: "h", ContentHash: tc.serverHash, Mtime: tc.serverMtime}
	if tc.serverAction != "" {
		serverRow.Action = tc.serverAction
	}
	metaBatch := map[string]*dto.SettingDTO{}
	if tc.serverAction != "" {
		metaBatch["h"] = serverRow
	}
	s.On("Sync", mock.Anything, int64(1), mock.Anything).Return([]*dto.SettingDTO{}, nil)
	if lastTime > 0 {
		s.On("GetByPathHashesMetaBatch", mock.Anything, int64(1), "v", []string{"h"}).Return(metaBatch, nil).Once()
	}
	NewSettingWSHandler(a).doSettingSync(c, &dto.SettingSyncRequest{
		Vault: "v", Context: ctx, LastTime: lastTime,
		Settings: []dto.SettingSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: tc.clientHash, Mtime: tc.clientMtime}},
	})
	return syncDeleteMessage(t, messages, SettingSyncEnd)["data"].(map[string]any)
}

const (
	sameHash = "same"
	otherMT  = int64(1700000000001)
	baseMT   = int64(1700000000000)
)

// windowCases 覆盖测试清单第 1–5 条：一致跳过、修改时间不同、哈希不同、墓碑、服务端没有。
// 笔记、附件、配置三类共用同一组判定用例。
var windowCases = []syncWindowCase{
	{"identical_alive_row_skips_upload", "create", sameHash, baseMT, sameHash, baseMT, 0},
	{"mtime_differs_requests_upload", "create", sameHash, baseMT, sameHash, otherMT, 1},
	{"hash_differs_requests_upload", "create", "server", baseMT, "client", baseMT, 1},
	{"tombstone_requests_upload", "delete", sameHash, baseMT, sameHash, baseMT, 1},
	{"missing_on_server_requests_upload", "", "", 0, sameHash, baseMT, 1},
}

// 第 1–5 条：三类同步各自覆盖。
func TestSyncWindow_OutOfWindowVerdicts(t *testing.T) {
	for _, tc := range windowCases {
		t.Run("note/"+tc.name, func(t *testing.T) {
			counts := runNoteWindowCase(t, tc, 1)
			require.Equal(t, float64(tc.wantUpload), counts["needUploadCount"])
			require.Equal(t, float64(0), counts["needModifyCount"])
			require.Equal(t, float64(0), counts["needSyncMtimeCount"])
			require.Equal(t, float64(0), counts["needDeleteCount"])
		})
		t.Run("file/"+tc.name, func(t *testing.T) {
			counts := runFileWindowCase(t, tc, 1)
			require.Equal(t, float64(tc.wantUpload), counts["needUploadCount"])
			require.Equal(t, float64(0), counts["needModifyCount"])
			require.Equal(t, float64(0), counts["needSyncMtimeCount"])
			require.Equal(t, float64(0), counts["needDeleteCount"])
		})
		t.Run("setting/"+tc.name, func(t *testing.T) {
			counts := runSettingWindowCase(t, tc, 1)
			require.Equal(t, float64(tc.wantUpload), counts["needUploadCount"])
			require.Equal(t, float64(0), counts["needModifyCount"])
			require.Equal(t, float64(0), counts["needSyncMtimeCount"])
			require.Equal(t, float64(0), counts["needDeleteCount"])
		})
	}
}

// 第 6 条：lastTime == 0 时行为不变，且不发生额外批量查询（用 mock 的 Once 计数证明：
// 未注册 GetPathsMetaBatch 桩而代码调用了它，testify 会以 unexpected call 失败）。
func TestSyncWindow_ZeroLastTime_NoBatchQuery(t *testing.T) {
	for _, tc := range []syncWindowCase{
		{"identical_row_still_uploads", "create", sameHash, baseMT, sameHash, baseMT, 1},
	} {
		t.Run("note/"+tc.name, func(t *testing.T) {
			counts := runNoteWindowCase(t, tc, 0)
			require.Equal(t, float64(tc.wantUpload), counts["needUploadCount"])
		})
		t.Run("file/"+tc.name, func(t *testing.T) {
			counts := runFileWindowCase(t, tc, 0)
			require.Equal(t, float64(tc.wantUpload), counts["needUploadCount"])
		})
		t.Run("setting/"+tc.name, func(t *testing.T) {
			counts := runSettingWindowCase(t, tc, 0)
			require.Equal(t, float64(tc.wantUpload), counts["needUploadCount"])
		})
	}
}

// 第 7 条：两条不同路径、路径哈希相同（碰撞）时不会互相误判——服务端只有 BB.md 的行，
// 客户端上报 Aa.md（哈希相同）不得因哈希碰撞被判为一致。
func TestSyncWindow_PathHashCollisionNotConfused(t *testing.T) {
	p, q := "Aa.md", "BB.md"
	h := util.EncodeHash32(p)
	require.Equal(t, h, util.EncodeHash32(q))

	t.Run("note", func(t *testing.T) {
		n, v := new(svcmocks.MockNoteService), new(svcmocks.MockVaultService)
		n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
		v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
		a := app.NewTestApp(&app.Services{NoteService: n, VaultService: v})
		c, messages := newSyncDeletePeer(t, a)
		ctx := t.Name()
		t.Cleanup(func() { syncDownloadDelete(ctx, "note") })
		// 服务端只有 BB.md；客户端上报同哈希的 Aa.md 与 BB.md。
		n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
		n.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).
			Return(map[string]*dto.NoteDTO{q: {Path: q, PathHash: h, Action: "create", ContentHash: sameHash, Mtime: baseMT}}, nil).Once()
		NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{
			Vault: "v", Context: ctx, LastTime: 1,
			Notes: []dto.NoteSyncCheckRequest{
				{Path: p, PathHash: h, ContentHash: sameHash, Mtime: baseMT},
				{Path: q, PathHash: h, ContentHash: sameHash, Mtime: baseMT},
			},
		})
		counts := syncDeleteMessage(t, messages, NoteSyncEnd)["data"].(map[string]any)
		// Aa.md 服务端没有 → 要求上传；BB.md 与服务端一致 → 跳过。
		require.Equal(t, float64(1), counts["needUploadCount"])
	})

	t.Run("file", func(t *testing.T) {
		f, v := new(svcmocks.MockFileService), new(svcmocks.MockVaultService)
		f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
		v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
		a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
		a.Config().App.TempPath = t.TempDir()
		c, messages := newSyncDeletePeer(t, a)
		ctx := t.Name()
		t.Cleanup(func() { syncDownloadDelete(ctx, "file") })
		f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{}, nil)
		// 服务端只有 BB.md；客户端上报同哈希的 Aa.md 与 BB.md。
		f.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).
			Return(map[string]*dto.FileDTO{q: {Path: q, PathHash: h, Action: "create", ContentHash: sameHash, Mtime: baseMT}}, nil).Once()
		NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{
			Vault: "v", Context: ctx, LastTime: 1,
			Files: []dto.FileSyncCheckRequest{
				{Path: p, PathHash: h, ContentHash: sameHash, Mtime: baseMT},
				{Path: q, PathHash: h, ContentHash: sameHash, Mtime: baseMT},
			},
		})
		counts := syncDeleteMessage(t, messages, FileSyncEnd)["data"].(map[string]any)
		// Aa.md 服务端没有 → 要求上传；BB.md 与服务端一致 → 跳过。
		require.Equal(t, float64(1), counts["needUploadCount"])
	})

	t.Run("setting", func(t *testing.T) {
		s, v := new(svcmocks.MockSettingService), new(svcmocks.MockVaultService)
		s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
		v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
		a := app.NewTestApp(&app.Services{SettingService: s, VaultService: v})
		c, messages := newSyncDeletePeer(t, a)
		ctx := t.Name()
		t.Cleanup(func() { syncDownloadDelete(ctx, "setting") })
		// 配置清单按哈希索引：两条同哈希条目在去重后只剩一条（既有语义），
		// 服务端行路径为 BB.md，客户端条目路径 Aa.md 与之不等 → 不跳过。
		s.On("Sync", mock.Anything, int64(1), mock.Anything).Return([]*dto.SettingDTO{}, nil)
		s.On("GetByPathHashesMetaBatch", mock.Anything, int64(1), "v", mock.Anything).
			Return(map[string]*dto.SettingDTO{h: {Path: q, PathHash: h, Action: "create", ContentHash: sameHash, Mtime: baseMT}}, nil).Once()
		NewSettingWSHandler(a).doSettingSync(c, &dto.SettingSyncRequest{
			Vault: "v", Context: ctx, LastTime: 1,
			Settings: []dto.SettingSyncCheckRequest{{Path: p, PathHash: h, ContentHash: sameHash, Mtime: baseMT}},
		})
		counts := syncDeleteMessage(t, messages, SettingSyncEnd)["data"].(map[string]any)
		require.Equal(t, float64(1), counts["needUploadCount"])
	})
}

// 第 1 条附件补充：完全一致时跳过——不建上传会话、不计入需上传数。
func TestSyncWindow_FileSkipCreatesNoUploadSession(t *testing.T) {
	f, v := new(svcmocks.MockFileService), new(svcmocks.MockVaultService)
	f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
	a.Config().App.TempPath = t.TempDir()
	c, messages := newSyncDeletePeer(t, a)
	ctx := t.Name()
	t.Cleanup(func() { syncDownloadDelete(ctx, "file") })

	f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{}, nil)
	f.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).
		Return(map[string]*dto.FileDTO{"a.md": {Path: "a.md", PathHash: "h", Action: "create", ContentHash: sameHash, Mtime: baseMT}}, nil).Once()
	NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{
		Vault: "v", Context: ctx, LastTime: 1,
		Files: []dto.FileSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: sameHash, Mtime: baseMT, Size: 3}},
	})
	counts := syncDeleteMessage(t, messages, FileSyncEnd)["data"].(map[string]any)
	require.Equal(t, float64(0), counts["needUploadCount"])
	require.Nil(t, c.Server.GetSessionByPathHash(c.User.ID, "h"), "跳过的条目不得创建上传会话")
}

// 第 1 条附件补充：令牌没有 file_w 时，跳过的条目不打无权限警告。
// 四种保持现状的情形（修改时间不同、哈希不同、墓碑、服务端没有）在无权限令牌下
// 仍照旧走无权限分支（needUpload 为 0、无下行消息）。
func TestSyncWindow_FileSkipNoWarningWithoutFileWrite(t *testing.T) {
	for _, tc := range []syncWindowCase{
		{"mtime_differs", "create", sameHash, baseMT, sameHash, otherMT, 0},
		{"hash_differs", "create", "server", baseMT, "client", baseMT, 0},
		{"tombstone", "delete", sameHash, baseMT, sameHash, baseMT, 0},
		{"missing", "", "", 0, sameHash, baseMT, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, v := new(svcmocks.MockFileService), new(svcmocks.MockVaultService)
			f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
			v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
			a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
			a.Config().App.TempPath = t.TempDir()
			c, messages := newSyncDeletePeer(t, a)
			// 只授予附件读权限，无 file_w。
			c.Scope = "p:ws f:file_r"
			ctx := t.Name()
			t.Cleanup(func() { syncDownloadDelete(ctx, "file") })

			serverRow := &dto.FileDTO{Path: "a.md", PathHash: "h", ContentHash: tc.serverHash, Mtime: tc.serverMtime}
			if tc.serverAction != "" {
				serverRow.Action = tc.serverAction
			}
			metaBatch := map[string]*dto.FileDTO{}
			if tc.serverAction != "" {
				metaBatch["a.md"] = serverRow
			}
			f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{}, nil)
			f.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", []string{"a.md"}).Return(metaBatch, nil).Once()
			NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{
				Vault: "v", Context: ctx, LastTime: 1,
				Files: []dto.FileSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: tc.clientHash, Mtime: tc.clientMtime}},
			})
			counts := syncDeleteMessage(t, messages, FileSyncEnd)["data"].(map[string]any)
			require.Equal(t, float64(tc.wantUpload), counts["needUploadCount"])
			// 无权限分支不排任何下行消息（既有行为）。
			select {
			case raw := <-messages:
				t.Fatalf("无 file_w 令牌不应产生下行消息，收到 %s", raw)
			default:
			}
		})
	}
}

// 第 1 条配置补充：令牌没有 config_w 时，跳过的条目不打无权限警告、不发消息；
// 未跳过的条目仍按原样打警告（现有行为）。
func TestSyncWindow_SettingSkipNoWarningWithoutConfigWrite(t *testing.T) {
	s, v := new(svcmocks.MockSettingService), new(svcmocks.MockVaultService)
	s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{SettingService: s, VaultService: v})
	c, messages := newSyncDeletePeer(t, a)
	// 只授予配置读权限，无 config_w。
	c.Scope = "p:ws f:config_r"
	ctx := t.Name()
	t.Cleanup(func() { syncDownloadDelete(ctx, "setting") })

	s.On("Sync", mock.Anything, int64(1), mock.Anything).Return([]*dto.SettingDTO{}, nil)
	s.On("GetByPathHashesMetaBatch", mock.Anything, int64(1), "v", mock.Anything).
		Return(map[string]*dto.SettingDTO{
			"h1": {Path: "same.md", PathHash: "h1", Action: "create", ContentHash: sameHash, Mtime: baseMT},
		}, nil).Once()
	NewSettingWSHandler(a).doSettingSync(c, &dto.SettingSyncRequest{
		Vault: "v", Context: ctx, LastTime: 1,
		Settings: []dto.SettingSyncCheckRequest{
			{Path: "same.md", PathHash: "h1", ContentHash: sameHash, Mtime: baseMT}, // 与服务端一致 → 跳过
			{Path: "diff.md", PathHash: "h2", ContentHash: "other", Mtime: baseMT},  // 哈希不同 → 照旧走无权限分支
		},
	})
	counts := syncDeleteMessage(t, messages, SettingSyncEnd)["data"].(map[string]any)
	require.Equal(t, float64(0), counts["needUploadCount"])
	// 只收到 End 消息本身；无权限分支不排任何下行消息（既有行为）。
	select {
	case raw := <-messages:
		t.Fatalf("跳过与无权限条目都不应产生下行消息，收到 %s", raw)
	default:
	}
}

// 第 3 条笔记补充：哈希不同且离线策略为 ignoreTimeMerge 时，窗口外条目仍登记
// DiffMergePaths（与改动前行为一致）。
func TestSyncWindow_NoteHashDiffRegistersDiffMergePath(t *testing.T) {
	n, v := new(svcmocks.MockNoteService), new(svcmocks.MockVaultService)
	n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{NoteService: n, VaultService: v})
	c, messages := newSyncDeletePeer(t, a)
	// 通过 ClientInfo 设置离线策略。
	c.Server.ClientInfo(c, wsMsg("ClientInfo", map[string]any{"offlineSyncStrategy": "ignoreTimeMerge"}))
	syncDeleteMessage(t, messages, "ClientInfo")
	ctx := t.Name()
	t.Cleanup(func() { syncDownloadDelete(ctx, "note") })

	n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
	n.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).
		Return(map[string]*dto.NoteDTO{"a.md": {Path: "a.md", PathHash: "h", Action: "create", ContentHash: "server", Mtime: baseMT}}, nil).Once()
	NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{
		Vault: "v", Context: ctx, LastTime: 1,
		Notes: []dto.NoteSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: "client", Mtime: baseMT}},
	})
	counts := syncDeleteMessage(t, messages, NoteSyncEnd)["data"].(map[string]any)
	require.Equal(t, float64(1), counts["needUploadCount"])
	c.DiffMergePathsMu.RLock()
	_, ok := c.DiffMergePaths["a.md"]
	c.DiffMergePathsMu.RUnlock()
	require.True(t, ok, "哈希不同的窗口外条目应登记 DiffMergePaths")
}

// 第 9 条：窗口内与窗口外条目混在同一请求里时，窗口内的判定不受影响。
func TestSyncWindow_MixedInWindowAndOutOfWindow(t *testing.T) {
	n, v := new(svcmocks.MockNoteService), new(svcmocks.MockVaultService)
	n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{NoteService: n, VaultService: v})
	c, messages := newSyncDeletePeer(t, a)
	ctx := t.Name()
	t.Cleanup(func() { syncDownloadDelete(ctx, "note") })

	// 窗口内：b.md 哈希不同且服务端更新 → needModify；
	// 窗口外：a.md 与服务端一致 → 跳过；c.md 服务端没有 → 上传。
	n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.NoteDTO{{Path: "b.md", PathHash: "hb", Action: "create", ContentHash: "server", Mtime: otherMT}}, nil)
	n.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).
		Return(map[string]*dto.NoteDTO{"a.md": {Path: "a.md", PathHash: "ha", Action: "create", ContentHash: sameHash, Mtime: baseMT}}, nil).Once()
	NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{
		Vault: "v", Context: ctx, LastTime: 1,
		Notes: []dto.NoteSyncCheckRequest{
			{Path: "a.md", PathHash: "ha", ContentHash: sameHash, Mtime: baseMT},
			{Path: "b.md", PathHash: "hb", ContentHash: "client", Mtime: baseMT},
			{Path: "c.md", PathHash: "hc", ContentHash: "new", Mtime: baseMT},
		},
	})
	counts := syncDeleteMessage(t, messages, NoteSyncEnd)["data"].(map[string]any)
	require.Equal(t, float64(1), counts["needUploadCount"])
	require.Equal(t, float64(1), counts["needModifyCount"])
	require.Equal(t, float64(0), counts["needSyncMtimeCount"])
}

// 批量查询出错时退回现有行为：当作服务端没有，照旧要求上传，不让整轮同步失败。
func TestSyncWindow_BatchQueryErrorFallsBackToUpload(t *testing.T) {
	t.Run("note", func(t *testing.T) {
		n, v := new(svcmocks.MockNoteService), new(svcmocks.MockVaultService)
		n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
		v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
		a := app.NewTestApp(&app.Services{NoteService: n, VaultService: v})
		c, messages := newSyncDeletePeer(t, a)
		ctx := t.Name()
		t.Cleanup(func() { syncDownloadDelete(ctx, "note") })
		n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
		n.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).Return(nil, errBoom).Once()
		NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{
			Vault: "v", Context: ctx, LastTime: 1,
			Notes: []dto.NoteSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: sameHash, Mtime: baseMT}},
		})
		counts := syncDeleteMessage(t, messages, NoteSyncEnd)["data"].(map[string]any)
		require.Equal(t, float64(1), counts["needUploadCount"])
	})
	t.Run("file", func(t *testing.T) {
		f, v := new(svcmocks.MockFileService), new(svcmocks.MockVaultService)
		f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
		v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
		a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
		a.Config().App.TempPath = t.TempDir()
		c, messages := newSyncDeletePeer(t, a)
		ctx := t.Name()
		t.Cleanup(func() { syncDownloadDelete(ctx, "file") })
		f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{}, nil)
		f.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).Return(nil, errBoom).Once()
		NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{
			Vault: "v", Context: ctx, LastTime: 1,
			Files: []dto.FileSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: sameHash, Mtime: baseMT}},
		})
		counts := syncDeleteMessage(t, messages, FileSyncEnd)["data"].(map[string]any)
		require.Equal(t, float64(1), counts["needUploadCount"])
	})
	t.Run("setting", func(t *testing.T) {
		s, v := new(svcmocks.MockSettingService), new(svcmocks.MockVaultService)
		s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
		v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
		a := app.NewTestApp(&app.Services{SettingService: s, VaultService: v})
		c, messages := newSyncDeletePeer(t, a)
		ctx := t.Name()
		t.Cleanup(func() { syncDownloadDelete(ctx, "setting") })
		s.On("Sync", mock.Anything, int64(1), mock.Anything).Return([]*dto.SettingDTO{}, nil)
		s.On("GetByPathHashesMetaBatch", mock.Anything, int64(1), "v", mock.Anything).Return(nil, errBoom).Once()
		NewSettingWSHandler(a).doSettingSync(c, &dto.SettingSyncRequest{
			Vault: "v", Context: ctx, LastTime: 1,
			Settings: []dto.SettingSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: sameHash, Mtime: baseMT}},
		})
		counts := syncDeleteMessage(t, messages, SettingSyncEnd)["data"].(map[string]any)
		require.Equal(t, float64(1), counts["needUploadCount"])
	})
}

// 老客户端没带路径（仅路径哈希）时不做窗口外比对：服务端即使有同哈希行也照旧要求上传
// （比对需要路径核对，没有路径就无法安全判定一致）。
func TestSyncWindow_LegacyClientWithoutPathNotCompared(t *testing.T) {
	n, v := new(svcmocks.MockNoteService), new(svcmocks.MockVaultService)
	n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{NoteService: n, VaultService: v})
	c, messages := newSyncDeletePeer(t, a)
	ctx := t.Name()
	t.Cleanup(func() { syncDownloadDelete(ctx, "note") })

	n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
	// 无路径条目不进批量查询（paths 为空列表）。
	n.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", []string{}).Return(map[string]*dto.NoteDTO{}, nil).Once()
	NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{
		Vault: "v", Context: ctx, LastTime: 1,
		Notes: []dto.NoteSyncCheckRequest{{PathHash: "h", ContentHash: sameHash, Mtime: baseMT}},
	})
	counts := syncDeleteMessage(t, messages, NoteSyncEnd)["data"].(map[string]any)
	require.Equal(t, float64(1), counts["needUploadCount"])
}

// ensureErrBoom keeps a sentinel error used by the fallback tests.
var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }

// 第 8 条（分块 >500 条）：在 dao 层用 SQLite 实测（ws_sync_window_dao_test.go），
// 此处仅保证 handler 把全部窗口外路径传给了批量查询。
func TestSyncWindow_PassesAllRemainingPathsToBatchQuery(t *testing.T) {
	n, v := new(svcmocks.MockNoteService), new(svcmocks.MockVaultService)
	n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
	v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
	a := app.NewTestApp(&app.Services{NoteService: n, VaultService: v})
	c, messages := newSyncDeletePeer(t, a)
	ctx := t.Name()
	t.Cleanup(func() { syncDownloadDelete(ctx, "note") })

	n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
	var gotPaths []string
	n.On("GetPathsMetaBatch", mock.Anything, int64(1), "v", mock.Anything).
		Run(func(args mock.Arguments) { gotPaths = args.Get(3).([]string) }).
		Return(map[string]*dto.NoteDTO{}, nil).Once()
	notes := make([]dto.NoteSyncCheckRequest, 0, 600)
	for i := 0; i < 600; i++ {
		notes = append(notes, dto.NoteSyncCheckRequest{Path: strings.Repeat("n", i%3+1) + "-" + itoa(i) + ".md", PathHash: "h" + itoa(i), ContentHash: sameHash, Mtime: baseMT})
	}
	NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{Vault: "v", Context: ctx, LastTime: 1, Notes: notes})
	counts := syncDeleteMessage(t, messages, NoteSyncEnd)["data"].(map[string]any)
	require.Equal(t, float64(600), counts["needUploadCount"])
	require.Len(t, gotPaths, 600)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}
