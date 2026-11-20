package websocket_router

// 批量存在性查询出错时本轮 NoteSync 必须失败：回普通错误、不执行删除、不下发
// NoteSyncEnd 推进游标。否则客户端上报的删除会被当成"服务端没有"全部跳过，
// 下一轮服务端存活笔记会把客户端已删的笔记复活。

import (
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNoteSync_ExistsPathsBatchErrorFailsRound(t *testing.T) {
	noteSvc := new(svcmocks.MockNoteService)
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil)
	testApp := app.NewTestApp(&app.Services{NoteService: noteSvc, VaultService: vaultSvc})

	noteSvc.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).
		Return([]*dto.NoteDTO{}, nil).Once()
	noteSvc.On("ExistsPathsBatch", mock.Anything, int64(1), "vault", []string{"gone.md"}).
		Return(map[string]bool(nil), code.ErrorDBQuery.WithDetails("boom")).Once()

	c, messages := newSyncDeletePeer(t, testApp)
	NewNoteWSHandler(testApp).doNoteSync(c, &dto.NoteSyncRequest{
		Context: "batch-error", Vault: "vault",
		DelNotes: []dto.NoteSyncDelNote{{Path: "gone.md", PathHash: "gone-hash"}},
	})

	select {
	case msg := <-messages:
		// 普通错误回应（无动作名前缀），错误码是客户端认识的 301。
		require.Contains(t, msg, `"code":301`)
	case <-time.After(time.Second):
		t.Fatal("批量存在性查询失败后未回错误响应")
	}
	select {
	case extra := <-messages:
		t.Fatalf("失败的一轮不应再下发其它消息（尤其不能有 NoteSyncEnd）：%q", extra)
	case <-time.After(100 * time.Millisecond):
	}
	noteSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)
	noteSvc.AssertExpectations(t)
}
