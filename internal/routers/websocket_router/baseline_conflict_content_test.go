package websocket_router

// 实时写/删条件提交被拒（530 SyncConflict）时的载荷完整性测试（P0-03）：
// 服务端活跃时不得把远端正文写成空串，能证明共同祖先时必须带上真实 baseContent，
// 插件冲突界面因此不会把真实远端内容误判为空文件。

import (
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newConflictContentApp 构造只挂笔记/配置/历史服务的最小 App。
func newConflictContentApp(noteSvc *svcmocks.MockNoteService, settingSvc *svcmocks.MockSettingService, historySvc *svcmocks.MockNoteHistoryService) *app.App {
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), mock.Anything).Return(nil, nil).Maybe()
	return app.NewTestApp(&app.Services{
		NoteService:        noteSvc,
		SettingService:     settingSvc,
		NoteHistoryService: historySvc,
		VaultService:       vaultSvc,
	})
}

// TestRespondBaselineConflict_NoteCarriesRealRemoteAndBase 实时 530 冲突必须带上真实
// 远端正文，并在历史精确命中时带上共同祖先正文。
func TestRespondBaselineConflict_NoteCarriesRealRemoteAndBase(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	testApp := newConflictContentApp(noteSvc, nil, historySvc)

	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc).Maybe()
	noteSvc.On("Get", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.NoteGetRequest) bool {
		return req.Vault == "vault" && req.Path == "a.md" && !req.IsRecycle
	})).Return(&dto.NoteDTO{
		ID: 7, Path: "a.md", PathHash: "h", Content: "# remote body", ContentHash: "S2", Revision: testRevisionV2,
	}, nil).Once()
	historySvc.On("GetByNoteIDAndHash", mock.Anything, int64(1), int64(7), "B1").
		Return(&dto.NoteHistoryDTO{Content: "# ancestor body"}, nil).Once()

	c, messages := baselineNotePeer(t, testApp)
	h := NewNoteWSHandler(testApp)
	h.respondBaselineConflict(c, dto.SyncConflictKindNote, "vault", "a.md", "h", "B1", &dto.BaselineConflictError{
		Reason:         dto.SyncConflictReasonBothModified,
		ServerActive:   true,
		ServerRevision: testRevisionV2,
		ServerHash:     "S2",
	}, "a.md")

	res := syncDeleteMessage(t, messages, string(SyncConflict))
	require.Equal(t, float64(530), res["code"])
	data := res["data"].(map[string]any)
	require.Equal(t, "# remote body", data["serverContent"], "530 必须带真实远端正文，不能是空串")
	require.Equal(t, "# ancestor body", data["baseContent"], "历史精确命中时必须带共同祖先正文")
	require.Equal(t, false, data["baseMissing"])
	require.Equal(t, "S2", data["serverHash"])
	require.Equal(t, testRevisionV2, data["serverRevision"])
	noteSvc.AssertExpectations(t)
	historySvc.AssertExpectations(t)
}

// TestRespondBaselineConflict_NoteWithoutHistoryReportsBaseMissing 历史未命中时绝不伪造
// 祖先：baseMissing=true 且 baseContent 为空，但远端正文仍然如实带出。
func TestRespondBaselineConflict_NoteWithoutHistoryReportsBaseMissing(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	historySvc := new(svcmocks.MockNoteHistoryService)
	testApp := newConflictContentApp(noteSvc, nil, historySvc)

	noteSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(noteSvc).Maybe()
	noteSvc.On("Get", mock.Anything, int64(1), mock.Anything).Return(&dto.NoteDTO{
		ID: 7, Path: "a.md", PathHash: "h", Content: "remote", ContentHash: "S2", Revision: testRevisionV2,
	}, nil).Once()
	historySvc.On("GetByNoteIDAndHash", mock.Anything, int64(1), int64(7), "B1").
		Return((*dto.NoteHistoryDTO)(nil), nil).Once()

	c, messages := baselineNotePeer(t, testApp)
	h := NewNoteWSHandler(testApp)
	h.respondBaselineConflict(c, dto.SyncConflictKindNote, "vault", "a.md", "h", "B1", &dto.BaselineConflictError{
		Reason: dto.SyncConflictReasonBothModified, ServerActive: true, ServerRevision: testRevisionV2, ServerHash: "S2",
	}, "a.md")

	data := syncDeleteMessage(t, messages, string(SyncConflict))["data"].(map[string]any)
	require.Equal(t, "remote", data["serverContent"])
	require.Empty(t, data["baseContent"], "无法证明共同祖先时绝不能伪造正文")
	require.Equal(t, true, data["baseMissing"])
}

// TestRespondBaselineConflict_TombstoneHasNoRemoteBody 墓碑冲突如实为空：不读取正文，
// 也不把删除前内容当作"远端当前内容"。
func TestRespondBaselineConflict_TombstoneHasNoRemoteBody(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	testApp := newConflictContentApp(noteSvc, nil, nil)

	c, messages := baselineNotePeer(t, testApp)
	h := NewNoteWSHandler(testApp)
	h.respondBaselineConflict(c, dto.SyncConflictKindNote, "vault", "a.md", "h", "B1", &dto.BaselineConflictError{
		Reason: dto.SyncConflictReasonRemoteDeleted, ServerDeleted: true, ServerRevision: testRevisionV2,
	}, "a.md")

	data := syncDeleteMessage(t, messages, string(SyncConflict))["data"].(map[string]any)
	require.Equal(t, true, data["serverDeleted"])
	require.NotContains(t, data, "serverContent", "墓碑没有当前正文，载荷里不应出现远端内容")
	noteSvc.AssertNotCalled(t, "Get", mock.Anything, mock.Anything, mock.Anything)
}

// TestRespondBaselineConflict_SettingCarriesRealContent 配置冲突同样必须带真实远端正文。
func TestRespondBaselineConflict_SettingCarriesRealContent(t *testing.T) {
	settingSvc := new(svcmocks.MockSettingService)
	testApp := newConflictContentApp(nil, settingSvc, nil)

	settingSvc.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(settingSvc).Maybe()
	settingSvc.On("Get", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.SettingGetRequest) bool {
		return req.Vault == "vault" && req.PathHash == "sh"
	})).Return(&dto.SettingDTO{
		ID: 3, Path: "User/Theme", PathHash: "sh", Content: `{"theme":"remote"}`, ContentHash: "S2", Revision: testRevisionV2,
	}, nil).Once()

	c, messages := baselineNotePeer(t, testApp)
	h := NewWSHandler(testApp)
	h.respondBaselineConflict(c, dto.SyncConflictKindSetting, "vault", "User/Theme", "sh", "B1", &dto.BaselineConflictError{
		Reason: dto.SyncConflictReasonBothModified, ServerActive: true, ServerRevision: testRevisionV2, ServerHash: "S2",
	}, "User/Theme")

	data := syncDeleteMessage(t, messages, string(SyncConflict))["data"].(map[string]any)
	require.Equal(t, `{"theme":"remote"}`, data["serverContent"])
	settingSvc.AssertExpectations(t)
}
