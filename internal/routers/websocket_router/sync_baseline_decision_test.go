package websocket_router

// 基线模式决策表测试（设计 §3.2 / §3.3 / §3.4 / §3.6）。
//
// 两部分：
//   1. 纯决策函数逐格覆盖：服务端活跃、墓碑、不存在三张表的每个格子，外加
//      空文本哈希 "0" 是合法基线、同内容重建（S==B 但令牌已换）不得被旧删除意图删掉
//      （#482）、恢复请求（missing*）不等于删除意图；
//   2. 路由层：用真实分页队列与 mock 服务验证批量同步实际排入的消息、计数与
//      "不在增量结果里 ≠ 不存在" 的批量元数据查询（#217 配置双改、附件一致性确认）。
//
// Baseline decision-table tests (design §3.2/§3.3/§3.4/§3.6): pure decision cells plus
// router-level assertions on the real paging queue and the batch metadata lookup.

import (
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	testRevisionV1 = "0123456789abcdef0123456789abcdef"
	testRevisionV2 = "fedcba9876543210fedcba9876543210"
)

// liveServer builds a live server row meta.
// liveServer 构造活跃服务端行元数据。
func liveServer(hash, revision string) baselineMeta {
	return baselineMeta{Found: true, ContentHash: hash, Revision: revision, Path: "a.md", PathHash: "h"}
}

// tombstoneServer builds a tombstone meta carrying the last pre-delete hash.
// tombstoneServer 构造带删除前最后内容哈希的墓碑元数据。
func tombstoneServer(hash, revision string) baselineMeta {
	return baselineMeta{Found: true, Deleted: true, ContentHash: hash, Revision: revision, Path: "a.md", PathHash: "h"}
}

// TestDecideBaseline_ActiveTable 覆盖 §3.2（服务端活跃）的每个格子。
func TestDecideBaseline_ActiveTable(t *testing.T) {
	tests := []struct {
		name    string
		client  baselineClientState
		server  baselineMeta
		action  baselineAction
		reason  string
		confirm bool
	}{
		{
			// 优先级 2：L == S 直接确认，即使没有任何基线。
			name:    "L==S no baseline confirms instead of conflicting",
			client:  baselineClientState{LocalHash: "S1", IsFile: true},
			server:  liveServer("S1", testRevisionV1),
			action:  baselineActionNone,
			confirm: true,
		},
		{
			name:    "L==S with stale baseline confirms",
			client:  baselineClientState{LocalHash: "S1", BaseHash: "B1", HasBase: true, BaseRevision: "stale", IsFile: true},
			server:  liveServer("S1", testRevisionV1),
			action:  baselineActionNone,
			confirm: true,
		},
		{
			// 有文件，L == B，S != B：本地没改，远端改了 → 下载，绝不反向上传。
			name:   "L==B and S!=B downloads",
			client: baselineClientState{LocalHash: "B1", BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: liveServer("S2", testRevisionV2),
			action: baselineActionDownload,
		},
		{
			// 有文件，L != B，S == B 且令牌一致：单侧本地修改 → 上传。
			name:   "L!=B S==B with matching revision uploads",
			client: baselineClientState{LocalHash: "L1", BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: liveServer("B1", testRevisionV1),
			action: baselineActionUpload,
		},
		{
			// 同内容重建：内容与基线相同，但服务端令牌已换 → 不得上传覆盖新对象。
			name:   "L!=B S==B with stale revision conflicts",
			client: baselineClientState{LocalHash: "L1", BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: liveServer("B1", testRevisionV2),
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonRevisionChanged,
		},
		{
			// 双侧修改：不按 mtime 覆盖（#4）。
			name:   "L!=B S!=B conflicts both_modified",
			client: baselineClientState{LocalHash: "L1", BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: liveServer("S2", testRevisionV2),
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonBothModified,
		},
		{
			// 有文件，无基线，内容不同：不得认定本地可被丢弃（#5）。
			name:   "no baseline with differing content conflicts",
			client: baselineClientState{LocalHash: "L1", IsFile: true},
			server: liveServer("S1", testRevisionV1),
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonNoBaseline,
		},
		{
			// 空文本哈希 "0" 是真实基线：L == S == "0" 应当确认一致，而不是冲突。
			name:    "empty text hash zero is a real baseline",
			client:  baselineClientState{LocalHash: dto.EmptyContentHash, BaseHash: dto.EmptyContentHash, HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server:  liveServer(dto.EmptyContentHash, testRevisionV1),
			action:  baselineActionNone,
			confirm: true,
		},
		{
			// 空文本基线 + 远端已改：L == B 且 S != B → 下载。
			name:   "empty text baseline with remote change downloads",
			client: baselineClientState{LocalHash: dto.EmptyContentHash, BaseHash: dto.EmptyContentHash, HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: liveServer("S2", testRevisionV2),
			action: baselineActionDownload,
		},
		{
			// 无文件，有基线，S == B 且令牌一致：条件删除。
			name:   "local delete with matching baseline deletes",
			client: baselineClientState{Deleted: true, BaseHash: "S1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: liveServer("S1", testRevisionV1),
			action: baselineActionDelete,
		},
		{
			// #482：删除后以相同内容重建，S == B 但令牌不同 → 不得执行旧删除。
			name:   "local delete on same-content rebuild conflicts",
			client: baselineClientState{Deleted: true, BaseHash: "S1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: liveServer("S1", testRevisionV2),
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonLocalDeleted,
		},
		{
			// 无令牌的删除意图永远不能授权破坏性删除。
			name:   "local delete without revision token conflicts",
			client: baselineClientState{Deleted: true, BaseHash: "S1", HasBase: true, IsFile: true},
			server: liveServer("S1", testRevisionV1),
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonLocalDeleted,
		},
		{
			// 远端已修改：旧删除意图不能作用于修改后的对象。
			name:   "local delete after remote modification conflicts",
			client: baselineClientState{Deleted: true, BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: liveServer("S2", testRevisionV2),
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonLocalDeleted,
		},
		{
			name:   "local delete without baseline conflicts",
			client: baselineClientState{Deleted: true, IsFile: true},
			server: liveServer("S1", testRevisionV1),
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonNoBaseline,
		},
		{
			// 恢复请求（missing*）不是删除意图：服务端活跃就下发。
			name:   "recovery request downloads the live row",
			client: baselineClientState{Missing: true, BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: liveServer("S1", testRevisionV1),
			action: baselineActionDownload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideBaseline(tt.client, tt.server)
			require.Equal(t, tt.action, got.Action, "action")
			require.Equal(t, tt.reason, got.Reason, "reason")
			require.Equal(t, tt.confirm, got.Confirmed, "confirmed")
		})
	}
}

// TestDecideBaseline_TombstoneTable 覆盖 §3.3（服务端为墓碑）的每个格子。
func TestDecideBaseline_TombstoneTable(t *testing.T) {
	tests := []struct {
		name   string
		client baselineClientState
		server baselineMeta
		action baselineAction
		reason string
	}{
		{
			// 有文件，L == B：本地没有未同步内容 → 应用远端删除。
			name:   "L==B applies remote delete",
			client: baselineClientState{LocalHash: "D1", BaseHash: "D1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: tombstoneServer("D1", testRevisionV2),
			action: baselineActionApplyDelete,
		},
		{
			// D != B 也一样：远端改过再删，客户端未改的旧副本无需保留。
			name:   "L==B with differing tombstone hash applies remote delete",
			client: baselineClientState{LocalHash: "B1", BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: tombstoneServer("D9", testRevisionV2),
			action: baselineActionApplyDelete,
		},
		{
			// #1：本地有未同步编辑 → 保留本地，服务端保持删除，绝不自动复活。
			name:   "L!=B keeps local edit and conflicts",
			client: baselineClientState{LocalHash: "L1", BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: tombstoneServer("D1", testRevisionV2),
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonRemoteDeleted,
		},
		{
			name:   "no baseline conflicts",
			client: baselineClientState{LocalHash: "L1", IsFile: true},
			server: tombstoneServer("D1", testRevisionV2),
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonNoBaseline,
		},
		{
			// 双方均已删除：幂等完成，消费删除意图。
			name:   "local delete on tombstone is idempotent",
			client: baselineClientState{Deleted: true, BaseHash: "D1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			server: tombstoneServer("D1", testRevisionV2),
			action: baselineActionConfirmDelete,
		},
		{
			name:   "recovery request on tombstone is a no-op",
			client: baselineClientState{Missing: true, IsFile: true},
			server: tombstoneServer("D1", testRevisionV2),
			action: baselineActionConfirmDelete,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideBaseline(tt.client, tt.server)
			require.Equal(t, tt.action, got.Action, "action")
			require.Equal(t, tt.reason, got.Reason, "reason")
		})
	}
}

// TestDecideBaseline_MissingTable 覆盖 §3.4（服务端不存在）的每个格子。
func TestDecideBaseline_MissingTable(t *testing.T) {
	absent := baselineMeta{}
	tests := []struct {
		name   string
		client baselineClientState
		action baselineAction
		reason string
	}{
		{
			// 有文件，无基线：正常新文件，允许"仍不存在"的条件创建。
			name:   "no baseline creates",
			client: baselineClientState{LocalHash: "L1", IsFile: true},
			action: baselineActionUpload,
		},
		{
			// 有文件，有基线：可能是墓碑已清理或服务端重置 → 保留本地（#3）。
			name:   "trusted baseline conflicts server_missing",
			client: baselineClientState{LocalHash: "L1", BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonServerMissing,
		},
		{
			// L != B 同样不能静默复活，也不能删掉唯一副本。
			name:   "trusted baseline with local edit conflicts server_missing",
			client: baselineClientState{LocalHash: "L2", BaseHash: "B1", HasBase: true, BaseRevision: testRevisionV1, IsFile: true},
			action: baselineActionConflict,
			reason: dto.SyncConflictReasonServerMissing,
		},
		{
			// 无文件，本地删除：确认删除已完成，不重试、不造空内容。
			name:   "local delete is already complete",
			client: baselineClientState{Deleted: true, BaseHash: "B1", HasBase: true, IsFile: true},
			action: baselineActionConfirmDelete,
		},
		{
			name:   "local delete without baseline is already complete",
			client: baselineClientState{Deleted: true, IsFile: true},
			action: baselineActionConfirmDelete,
		},
		{
			name:   "recovery request with no server row is a no-op",
			client: baselineClientState{Missing: true, IsFile: true},
			action: baselineActionConfirmDelete,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideBaseline(tt.client, absent)
			require.Equal(t, tt.action, got.Action, "action")
			require.Equal(t, tt.reason, got.Reason, "reason")
		})
	}
}

// TestDecideBaseline_FolderTable 覆盖 §3.6 目录规则：只按 BaseRevision 判断。
func TestDecideBaseline_FolderTable(t *testing.T) {
	live := baselineMeta{Found: true, Revision: testRevisionV1, Path: "docs", PathHash: "dh"}
	gone := baselineMeta{Found: true, Deleted: true, Revision: testRevisionV2, Path: "docs", PathHash: "dh"}

	require.Equal(t, baselineActionNone, decideBaselineFolderEntry(false, false, "", live).Action)
	require.True(t, decideBaselineFolderEntry(false, false, "", live).Confirmed, "目录一致要下发确认以建立基线")
	require.Equal(t, baselineActionDelete, decideBaselineFolderEntry(false, true, testRevisionV1, live).Action)
	require.Equal(t, dto.SyncConflictReasonLocalDeleted, decideBaselineFolderEntry(false, true, "", live).Reason,
		"没有令牌的目录删除意图不能授权删除")
	require.Equal(t, dto.SyncConflictReasonLocalDeleted, decideBaselineFolderEntry(false, true, testRevisionV2, live).Reason,
		"令牌不一致（重建）时不能执行旧删除")
	require.Equal(t, baselineActionConfirmDelete, decideBaselineFolderEntry(false, true, "", gone).Action)
	require.Equal(t, dto.SyncConflictReasonNoBaseline, decideBaselineFolderEntry(false, false, "", gone).Reason)
	require.Equal(t, baselineActionApplyDelete, decideBaselineFolderEntry(false, false, testRevisionV2, gone).Action)
	require.Equal(t, baselineActionConfirmDelete, decideBaselineFolderEntry(true, false, "", gone).Action)
}

// TestBaselineRevisionMatches 空令牌永远不算匹配。
func TestBaselineRevisionMatches(t *testing.T) {
	server := liveServer("S1", testRevisionV1)
	require.True(t, baselineRevisionMatches(testRevisionV1, server))
	require.False(t, baselineRevisionMatches("", server), "空 BaseRevision 不得授权条件删除")
	require.False(t, baselineRevisionMatches(testRevisionV2, server))
	require.False(t, baselineRevisionMatches(testRevisionV1, baselineMeta{Found: true, ContentHash: "S1"}),
		"服务端没有令牌时不得视为匹配")
}

// ---------------------------------------------------------------------------
// 路由层：批量同步实际排入的消息
// ---------------------------------------------------------------------------

// baselineNotePeer 构造一个已协商到基线模式的笔记库测试客户端。
// baselineNotePeer builds a note-vault test peer that negotiated baseline mode.
func baselineNotePeer(t *testing.T, testApp *app.App) (*pkgapp.WebsocketClient, <-chan string) {
	t.Helper()
	c, messages := newSyncDeletePeer(t, testApp)
	c.SetSyncBaselineVersionForTest(1)
	return c, messages
}

// baselineNoteApp 构造笔记基线同步所需的最小 App。
func baselineNoteApp(noteSvc *svcmocks.MockNoteService) *app.App {
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{NoteService: noteSvc, VaultService: vaultSvc})
	testApp.Config().App.SyncDownChunkNum = 10
	return testApp
}

// baselineQueue 取出本轮分页队列并登记清理。
func baselineQueue(t *testing.T, context, kind string) *syncDownloadEntry {
	t.Helper()
	entry, ok := syncDownloadGet(context, kind)
	require.True(t, ok, "必须保留分页队列")
	t.Cleanup(func() { syncDownloadDelete(context, kind) })
	return entry
}

// TestNoteSyncBaseline_ConsistentWithoutBaselineSendsConfirmation 内容一致但客户端还没有
// 可信基线时，必须下发带 ContentHash + Revision 的 *SyncMtime，否则客户端永远建不起基线。
func TestNoteSyncBaseline_ConsistentWithoutBaselineSendsConfirmation(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.NoteDTO{{ID: 7, Path: "a.md", PathHash: "h", ContentHash: "S1", Revision: testRevisionV1, Action: "create", Mtime: 1000, UpdatedTimestamp: 100}}, nil)
	testApp := baselineNoteApp(noteSvc)
	c, messages := baselineNotePeer(t, testApp)
	const context = "baseline-confirm"

	NewNoteWSHandler(testApp).doNoteSyncBaseline(c, &dto.NoteSyncRequest{
		Context: context, Vault: "vault",
		Notes: []dto.NoteSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: "S1", Mtime: 1000, BaseHashMissing: true}},
	})

	end := syncDeleteMessage(t, messages, string(NoteSyncEnd))
	counts := end["data"].(map[string]any)
	require.Equal(t, float64(1), counts["needSyncMtimeCount"])
	require.Equal(t, float64(0), counts["needConflictCount"])
	require.Equal(t, float64(0), counts["needUploadCount"])

	entry := baselineQueue(t, context, "note")
	require.Len(t, entry.MessageQueue, 1)
	require.Equal(t, NoteSyncMtime, entry.MessageQueue[0].Action)
	confirmation, ok := entry.MessageQueue[0].Data.(dto.NoteSyncMtimeMessage)
	require.True(t, ok)
	require.Equal(t, "S1", confirmation.ContentHash)
	require.Equal(t, testRevisionV1, confirmation.Revision)
}

// TestNoteSyncBaseline_SameContentRebuildNotDeletedByStaleIntent 是 #482 的路由级回归：
// 删除后以相同内容重建时 S == B，但令牌已换；旧删除意图不得删掉新对象。
func TestNoteSyncBaseline_SameContentRebuildNotDeletedByStaleIntent(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.NoteDTO{{ID: 7, Path: "a.md", PathHash: "h", ContentHash: "S1", Revision: testRevisionV2, Action: "create", Mtime: 1000, UpdatedTimestamp: 100}}, nil)
	noteSvc.On("GetByID", mock.Anything, int64(1), int64(7)).
		Return(&dto.NoteDTO{ID: 7, Path: "a.md", PathHash: "h", ContentHash: "S1", Revision: testRevisionV2, Content: "rebuilt"}, nil).Maybe()
	testApp := baselineNoteApp(noteSvc)
	c, messages := baselineNotePeer(t, testApp)
	const context = "baseline-rebuild"

	NewNoteWSHandler(testApp).doNoteSyncBaseline(c, &dto.NoteSyncRequest{
		Context: context, Vault: "vault",
		DelNotes: []dto.NoteSyncDelNote{{
			Path: "a.md", PathHash: "h",
			BaseHash: "S1", BaseRevision: testRevisionV1,
		}},
	})

	end := syncDeleteMessage(t, messages, string(NoteSyncEnd))
	counts := end["data"].(map[string]any)
	require.Equal(t, float64(1), counts["needConflictCount"], "同内容重建必须报冲突而不是删除")
	require.Equal(t, float64(0), counts["needDeleteCount"])
	noteSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)

	entry := baselineQueue(t, context, "note")
	require.Len(t, entry.MessageQueue, 1)
	require.Equal(t, SyncConflict, entry.MessageQueue[0].Action)
	conflict, ok := entry.MessageQueue[0].Data.(dto.SyncConflictMessage)
	require.True(t, ok)
	require.Equal(t, dto.SyncConflictKindNote, conflict.Kind)
	require.Equal(t, dto.SyncConflictReasonLocalDeleted, conflict.Reason)
	require.Equal(t, testRevisionV2, conflict.ServerRevision)
	require.False(t, conflict.ServerDeleted)
}

// TestNoteSyncBaseline_ConditionalDeleteUsesClientBaseline 令牌一致时执行条件删除，
// 并把客户端基线交给服务层（由服务层在写临界区内再校验一次）。
func TestNoteSyncBaseline_ConditionalDeleteUsesClientBaseline(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.NoteDTO{{ID: 7, Path: "a.md", PathHash: "h", ContentHash: "S1", Revision: testRevisionV1, Action: "create", Mtime: 1000, UpdatedTimestamp: 100}}, nil)
	noteSvc.On("Delete", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.NoteDeleteRequest) bool {
		return req.Baseline != nil &&
			req.Baseline.BaseRevision == testRevisionV1 &&
			req.Baseline.HasBase && req.Baseline.BaseHash == "S1"
	})).Return(&dto.NoteDTO{Path: "a.md", PathHash: "h", Action: "delete", Revision: testRevisionV2}, nil)
	testApp := baselineNoteApp(noteSvc)
	c, messages := baselineNotePeer(t, testApp)

	NewNoteWSHandler(testApp).doNoteSyncBaseline(c, &dto.NoteSyncRequest{
		Context: "baseline-delete", Vault: "vault",
		DelNotes: []dto.NoteSyncDelNote{{Path: "a.md", PathHash: "h", BaseHash: "S1", BaseRevision: testRevisionV1}},
	})

	syncDeleteMessage(t, messages, string(NoteSyncEnd))
	noteSvc.AssertExpectations(t)
	syncDownloadDelete("baseline-delete", "note")
}

// TestNoteSyncBaseline_OutOfWindowPathIsNotAbsent 覆盖 §3.5 的核心约束：不在增量结果里
// 不等于不存在，必须批量查当前元数据（含墓碑）后再决策。
func TestNoteSyncBaseline_OutOfWindowPathIsNotAbsent(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
	noteSvc.On("GetPathsMetaBatch", mock.Anything, int64(1), "vault", []string{"a.md"}).
		Return(map[string]*dto.NoteDTO{
			"a.md": {ID: 7, Path: "a.md", PathHash: "h", ContentHash: "S2", Revision: testRevisionV2, Action: "create", Mtime: 2000, UpdatedTimestamp: 200},
		}, nil)
	testApp := baselineNoteApp(noteSvc)
	c, messages := baselineNotePeer(t, testApp)
	const context = "baseline-out-of-window"

	NewNoteWSHandler(testApp).doNoteSyncBaseline(c, &dto.NoteSyncRequest{
		Context: context, Vault: "vault", LastTime: 50,
		Notes: []dto.NoteSyncCheckRequest{{
			Path: "a.md", PathHash: "h", ContentHash: "B1",
			BaseHash: "B1", BaseRevision: testRevisionV1, Mtime: 1000,
		}},
	})

	end := syncDeleteMessage(t, messages, string(NoteSyncEnd))
	counts := end["data"].(map[string]any)
	require.Equal(t, float64(1), counts["needModifyCount"], "窗口外但服务端活跃的条目必须下发")
	require.Equal(t, float64(0), counts["needUploadCount"], "绝不能把窗口外条目当成服务端不存在")

	entry := baselineQueue(t, context, "note")
	require.Equal(t, NoteSyncModify, entry.MessageQueue[0].Action)
	modify, ok := entry.MessageQueue[0].Data.(dto.NoteSyncModifyMessage)
	require.True(t, ok)
	require.Equal(t, "S2", modify.ContentHash)
	require.Equal(t, testRevisionV2, modify.Revision)
	noteSvc.AssertExpectations(t)
}

// TestNoteSyncBaseline_OutOfWindowTombstoneSendsDelete 窗口外墓碑同样必须下发删除，
// 而不是当作"不存在"再让客户端上传复活。
func TestNoteSyncBaseline_OutOfWindowTombstoneSendsDelete(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
	noteSvc.On("GetPathsMetaBatch", mock.Anything, int64(1), "vault", []string{"a.md"}).
		Return(map[string]*dto.NoteDTO{
			"a.md": {ID: 7, Path: "a.md", PathHash: "h", ContentHash: "B1", Revision: testRevisionV2, Action: "delete", Mtime: 2000, UpdatedTimestamp: 200},
		}, nil)
	testApp := baselineNoteApp(noteSvc)
	c, messages := baselineNotePeer(t, testApp)
	const context = "baseline-tombstone"

	NewNoteWSHandler(testApp).doNoteSyncBaseline(c, &dto.NoteSyncRequest{
		Context: context, Vault: "vault", LastTime: 50,
		Notes: []dto.NoteSyncCheckRequest{{
			Path: "a.md", PathHash: "h", ContentHash: "B1",
			BaseHash: "B1", BaseRevision: testRevisionV1, Mtime: 1000,
		}},
	})

	end := syncDeleteMessage(t, messages, string(NoteSyncEnd))
	require.Equal(t, float64(1), end["data"].(map[string]any)["needDeleteCount"])

	entry := baselineQueue(t, context, "note")
	require.Equal(t, NoteSyncDelete, entry.MessageQueue[0].Action)
	deletion, ok := entry.MessageQueue[0].Data.(dto.NoteSyncDeleteMessage)
	require.True(t, ok)
	require.Equal(t, testRevisionV2, deletion.Revision, "墓碑令牌必须下发")
	require.Equal(t, "B1", deletion.ContentHash, "删除前最后内容哈希必须下发")
}

// TestNoteSyncBaseline_ServerMissingKeepsLocal 服务端不存在但客户端有可信基线：
// 进入 server_missing 冲突，保留本地，不自动上传、不自动删除。
func TestNoteSyncBaseline_ServerMissingKeepsLocal(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	noteSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{}, nil)
	noteSvc.On("GetPathsMetaBatch", mock.Anything, int64(1), "vault", []string{"a.md"}).
		Return(map[string]*dto.NoteDTO{}, nil)
	testApp := baselineNoteApp(noteSvc)
	c, messages := baselineNotePeer(t, testApp)
	const context = "baseline-server-missing"

	NewNoteWSHandler(testApp).doNoteSyncBaseline(c, &dto.NoteSyncRequest{
		Context: context, Vault: "vault", LastTime: 50,
		Notes: []dto.NoteSyncCheckRequest{{
			Path: "a.md", PathHash: "h", ContentHash: "B1",
			BaseHash: "B1", BaseRevision: testRevisionV1, Mtime: 1000,
		}},
	})

	end := syncDeleteMessage(t, messages, string(NoteSyncEnd))
	counts := end["data"].(map[string]any)
	require.Equal(t, float64(1), counts["needConflictCount"])
	require.Equal(t, float64(0), counts["needUploadCount"], "server_missing 不得自动上传")

	entry := baselineQueue(t, context, "note")
	conflict, ok := entry.MessageQueue[0].Data.(dto.SyncConflictMessage)
	require.True(t, ok)
	require.Equal(t, dto.SyncConflictReasonServerMissing, conflict.Reason)
	require.True(t, conflict.BaseMissing, "没有证明共同祖先时必须标记 baseMissing")
	require.Empty(t, conflict.BaseContent, "绝不伪造共同祖先")
}

// TestSettingSyncBaseline_DoubleEditConflictsDespiteCover 是 #217 的回归：配置双改必须
// 报冲突，旧 `cover` 标志不得绕过基线保护。
func TestSettingSyncBaseline_DoubleEditConflictsDespiteCover(t *testing.T) {
	settingSvc := new(svcmocks.MockSettingService)
	settingSvc.On("Sync", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.SettingDTO{{
			ID: 3, Path: "User/Theme", PathHash: "sh", ContentHash: "S2",
			Revision: testRevisionV2, Action: "create", Content: "server", Mtime: 2000, UpdatedTimestamp: 200,
		}}, nil)
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{SettingService: settingSvc, VaultService: vaultSvc})
	testApp.Config().App.SyncDownChunkNum = 10
	c, messages := baselineNotePeer(t, testApp)
	const context = "baseline-setting-conflict"

	NewSettingWSHandler(testApp).doSettingSyncBaseline(c, &dto.SettingSyncRequest{
		Context: context, Vault: "vault", Cover: true,
		Settings: []dto.SettingSyncCheckRequest{{
			Path: "User/Theme", PathHash: "sh", ContentHash: "L1",
			BaseHash: "B1", BaseRevision: testRevisionV1, Mtime: 1000,
		}},
	})

	end := syncDeleteMessage(t, messages, string(SettingSyncEnd))
	counts := end["data"].(map[string]any)
	require.Equal(t, float64(1), counts["needConflictCount"], "cover=true 也不能让配置双改静默覆盖")
	require.Equal(t, float64(0), counts["needModifyCount"])
	require.Equal(t, float64(0), counts["needUploadCount"])

	entry := baselineQueue(t, context, "setting")
	conflict, ok := entry.MessageQueue[0].Data.(dto.SyncConflictMessage)
	require.True(t, ok)
	require.Equal(t, dto.SyncConflictKindSetting, conflict.Kind)
	require.Equal(t, dto.SyncConflictReasonBothModified, conflict.Reason)
	require.Equal(t, "server", conflict.ServerContent, "文本配置要带远端正文供对比")
}

// TestFileSyncBaseline_ConsistentWithoutBaselineSendsMtime 附件内容一致但客户端没有基线时，
// 同样要下发带 ContentHash + Revision 的 *SyncMtime 以建立基线。
func TestFileSyncBaseline_ConsistentWithoutBaselineSendsMtime(t *testing.T) {
	fileSvc := new(svcmocks.MockFileService)
	fileSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.FileDTO{{
			ID: 9, Path: "img.png", PathHash: "fh", ContentHash: "S1", Size: 10,
			Revision: testRevisionV1, Action: "create", Mtime: 1000, UpdatedTimestamp: 100,
		}}, nil)
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{FileService: fileSvc, VaultService: vaultSvc})
	testApp.Config().App.SyncDownChunkNum = 10
	c, messages := baselineNotePeer(t, testApp)
	const context = "baseline-file-confirm"

	NewFileWSHandler(testApp).doFileSyncBaseline(c, &dto.FileSyncRequest{
		Context: context, Vault: "vault",
		Files: []dto.FileSyncCheckRequest{{Path: "img.png", PathHash: "fh", ContentHash: "S1", Mtime: 1000, BaseHashMissing: true}},
	})

	end := syncDeleteMessage(t, messages, string(FileSyncEnd))
	require.Equal(t, float64(1), end["data"].(map[string]any)["needSyncMtimeCount"])

	entry := baselineQueue(t, context, "file")
	require.Equal(t, FileSyncMtime, entry.MessageQueue[0].Action)
	confirmation, ok := entry.MessageQueue[0].Data.(dto.FileSyncMtimeMessage)
	require.True(t, ok)
	require.Equal(t, "S1", confirmation.ContentHash)
	require.Equal(t, testRevisionV1, confirmation.Revision)
}

// TestFolderSyncBaseline_StaleDeleteIntentConflicts 目录令牌不一致时不得删除。
func TestFolderSyncBaseline_StaleDeleteIntentConflicts(t *testing.T) {
	folderSvc := new(svcmocks.MockFolderService)
	folderSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(folderSvc).Maybe()
	folderSvc.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "vault", int64(0)).
		Return([]*dto.FolderDTO{{Path: "docs", PathHash: "dh", Revision: testRevisionV2, Action: "create", Mtime: 1000, UpdatedTimestamp: 100}}, nil)
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil).Maybe()
	testApp := app.NewTestApp(&app.Services{FolderService: folderSvc, VaultService: vaultSvc})
	testApp.Config().App.SyncDownChunkNum = 10
	c, messages := baselineNotePeer(t, testApp)
	const context = "baseline-folder-conflict"

	NewFolderWSHandler(testApp).doFolderSyncBaseline(c, &dto.FolderSyncRequest{
		Context: context, Vault: "vault",
		DelFolders: []dto.FolderSyncDelFolder{{Path: "docs", PathHash: "dh", BaseRevision: testRevisionV1}},
	})

	end := syncDeleteMessage(t, messages, string(FolderSyncEnd))
	require.Equal(t, float64(1), end["data"].(map[string]any)["needConflictCount"])
	folderSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)

	entry := baselineQueue(t, context, "folder")
	conflict, ok := entry.MessageQueue[0].Data.(dto.SyncConflictMessage)
	require.True(t, ok)
	require.Equal(t, dto.SyncConflictKindFolder, conflict.Kind)
	require.Equal(t, dto.SyncConflictReasonLocalDeleted, conflict.Reason)
}

// TestSyncConflictIsNeverSentToLegacyConnections 版本 0 的连接绝不能收到 SyncConflict：
// 它的分发根本不会进入基线引擎。
func TestSyncConflictIsNeverSentToLegacyConnections(t *testing.T) {
	c := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, app.NewTestApp(&app.Services{})).NewTestClient(1)
	require.False(t, baselineModeEnabled(c), "未协商的连接必须走旧路径")
	c.SetSyncBaselineVersionForTest(1)
	require.True(t, baselineModeEnabled(c))
}

// 确保未使用的导入不残留（time 供分页等待语义使用）。
var _ = time.Second
