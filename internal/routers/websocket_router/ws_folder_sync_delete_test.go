package websocket_router

// doFolderSync 客户端删除目录检查：
// - 带完整路径查询，避免哈希碰撞造成歧义而漏删；
// - 查询出错（非 not-found）时记日志跳过，不盲目广播删除；
// - 只读令牌在任何分支都不广播 FolderSyncDelete。

import (
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newFolderSyncDeleteFixture(t *testing.T, scope string) (*FolderWSHandler, *svcmocks.MockFolderService, *pkgapp.WebsocketClient, <-chan string) {
	t.Helper()
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil).Maybe()
	folderSvc.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "vault", int64(0)).
		Return([]*dto.FolderDTO{}, nil).Maybe()

	testApp := app.NewTestApp(&app.Services{FolderService: folderSvc, VaultService: vaultSvc})
	folderSvc.On("WithClient", "", "", "").Return(folderSvc).Maybe()

	c, _ := newSyncDeletePeer(t, testApp)
	c.Scope = scope
	other, otherMessages := newSyncDeletePeer(t, testApp)
	declareVaultForTest(t, other, "vault")
	c.UserClients = pkgapp.ConnStorage{nil: other}
	return NewFolderWSHandler(testApp), folderSvc, c, otherMessages
}

func TestFolderSyncDeleteConditions(t *testing.T) {
	t.Run("read-only token never broadcasts delete", func(t *testing.T) {
		h, folderSvc, c, otherMessages := newFolderSyncDeleteFixture(t, "f:note_r")
		folderSvc.On("Get", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.FolderGetRequest) bool {
			return req.Path == "docs" && req.PathHash == "docs-hash"
		})).Return((*dto.FolderDTO)(nil), code.ErrorFolderNotFound).Once()

		h.doFolderSync(c, &dto.FolderSyncRequest{
			Context: "delete-readonly", Vault: "vault",
			DelFolders: []dto.FolderSyncDelFolder{{Path: "docs", PathHash: "docs-hash"}},
		})

		select {
		case msg := <-otherMessages:
			t.Fatalf("只读令牌不得广播 FolderSyncDelete：%q", msg)
		case <-time.After(100 * time.Millisecond):
		}
		folderSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("lookup error skips instead of broadcasting", func(t *testing.T) {
		h, folderSvc, c, otherMessages := newFolderSyncDeleteFixture(t, "")
		folderSvc.On("Get", mock.Anything, int64(1), mock.Anything).
			Return((*dto.FolderDTO)(nil), code.ErrorFolderGetFailed.WithDetails("db down")).Once()

		h.doFolderSync(c, &dto.FolderSyncRequest{
			Context: "delete-error", Vault: "vault",
			DelFolders: []dto.FolderSyncDelFolder{{Path: "docs", PathHash: "docs-hash"}},
		})

		select {
		case msg := <-otherMessages:
			t.Fatalf("查询出错时不得广播删除：%q", msg)
		case <-time.After(100 * time.Millisecond):
		}
		folderSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("missing folder with write permission broadcasts delete", func(t *testing.T) {
		h, folderSvc, c, otherMessages := newFolderSyncDeleteFixture(t, "")
		folderSvc.On("Get", mock.Anything, int64(1), mock.MatchedBy(func(req *dto.FolderGetRequest) bool {
			return req.Path == "docs" && req.PathHash == "docs-hash"
		})).Return((*dto.FolderDTO)(nil), code.ErrorFolderNotFound).Once()

		h.doFolderSync(c, &dto.FolderSyncRequest{
			Context: "delete-missing", Vault: "vault",
			DelFolders: []dto.FolderSyncDelFolder{{Path: "docs", PathHash: "docs-hash"}},
		})

		res := syncDeleteMessage(t, otherMessages, string(FolderSyncDelete))
		require.Equal(t, float64(1), res["code"])
		require.Equal(t, "docs", res["data"].(map[string]any)["path"])
	})
}
