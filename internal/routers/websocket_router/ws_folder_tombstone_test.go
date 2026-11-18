package websocket_router

import (
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/mock"
)

// newFolderSyncTestClient 构造一个能驱动 doFolderSync 的最小 WebsocketClient。
// conn 为 nil 时 writeMessage 只返回错误不 panic；空 UserClients 让
// BroadcastResponse 直接返回，测试通过 mock 断言服务层调用。
// ToResponse 内部解引用私有 c.app，因此必须经 NewWebsocketServer.NewTestClient 注入容器。
func newFolderSyncTestClient(testApp *app.App) *pkgapp.WebsocketClient {
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)
	return wss.NewTestClient(1)
}

// newFolderSyncTestApp 构造注入 mock FolderService 的最小 App。
// mock 的 WithClient 返回自身，使 GetFolderService 透传到 mock。
func newFolderSyncTestApp(folderSvc *svcmocks.MockFolderService, vaultSvc *svcmocks.MockVaultService) *app.App {
	folderSvc.On("WithClient", "", "", "").Return(folderSvc).Maybe()
	return app.NewTestApp(&app.Services{
		FolderService: folderSvc,
		VaultService:  vaultSvc,
	})
}

// TestFolderSync_TombstoneNotRevivedByClientReport（#152）
// 客户端上报的目录里包含服务端已删除（墓碑）的路径时，doFolderSync 必须跳过，
// 不得 UpdateOrCreate 复活并广播。IsStaleRevive 返回 (true, nil) 的场景。
func TestFolderSync_TombstoneNotRevivedByClientReport(t *testing.T) {
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil).Maybe()

	folderSvc.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "vault", int64(0)).
		Return([]*dto.FolderDTO{}, nil)
	// 客户端上报 "ghost" 目录，服务端墓碑判定为陈旧复活
	folderSvc.On("IsStaleRevive", mock.Anything, int64(1), "vault", "ghosthash", mock.AnythingOfType("int64")).
		Return(true, nil)

	testApp := newFolderSyncTestApp(folderSvc, vaultSvc)
	h := NewFolderWSHandler(testApp)
	c := newFolderSyncTestClient(testApp)
	t.Cleanup(c.WsCancel)

	h.doFolderSync(c, &dto.FolderSyncRequest{
		Context: "ctx-1",
		Vault:   "vault",
		Folders: []dto.FolderSyncCheckRequest{
			{Path: "ghost", PathHash: "ghosthash", Mtime: 1},
		},
	})

	folderSvc.AssertNotCalled(t, "UpdateOrCreate", mock.Anything, mock.Anything, mock.Anything)
	folderSvc.AssertExpectations(t)
}

// TestFolderSync_TombstoneCheckErrorSkipsRevive（#152 根因）
// IsStaleRevive 查询出错时，修复前 err == nil && stale 短路会放行，
// 墓碑目录被 UpdateOrCreate 复活并广播到所有设备（完整同步后多出目录的一个通道）。
// 修复后出错必须保守跳过该目录。
func TestFolderSync_TombstoneCheckErrorSkipsRevive(t *testing.T) {
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil).Maybe()

	folderSvc.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "vault", int64(0)).
		Return([]*dto.FolderDTO{}, nil)
	// 查询错误：修复前 (err == nil && stale) 为 false → 放行复活
	folderSvc.On("IsStaleRevive", mock.Anything, int64(1), "vault", "ghosthash", mock.AnythingOfType("int64")).
		Return(false, errTombstoneLookup)

	testApp := newFolderSyncTestApp(folderSvc, vaultSvc)
	h := NewFolderWSHandler(testApp)
	c := newFolderSyncTestClient(testApp)
	t.Cleanup(c.WsCancel)

	h.doFolderSync(c, &dto.FolderSyncRequest{
		Context: "ctx-1",
		Vault:   "vault",
		Folders: []dto.FolderSyncCheckRequest{
			{Path: "ghost", PathHash: "ghosthash", Mtime: 1},
		},
	})

	folderSvc.AssertNotCalled(t, "UpdateOrCreate", mock.Anything, mock.Anything, mock.Anything,
		"Bug: IsStaleRevive 出错时放行会把墓碑目录复活并广播（#152）")
	folderSvc.AssertExpectations(t)
}

// TestFolderSync_FreshFolderStillCreated
// 正常目录（无墓碑/未删除）的上报仍要照常 UpdateOrCreate，不被闸门误伤。
func TestFolderSync_FreshFolderStillCreated(t *testing.T) {
	folderSvc := new(svcmocks.MockFolderService)
	vaultSvc := new(svcmocks.MockVaultService)
	vaultSvc.On("GetOrCreate", mock.Anything, int64(1), "vault").Return(nil, nil).Maybe()

	folderSvc.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "vault", int64(0)).
		Return([]*dto.FolderDTO{}, nil)
	folderSvc.On("IsStaleRevive", mock.Anything, int64(1), "vault", "okhash", mock.AnythingOfType("int64")).
		Return(false, nil)
	folderSvc.On("UpdateOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.FolderCreateRequest")).
		Return(&dto.FolderDTO{Path: "ok", PathHash: "okhash"}, nil)

	testApp := newFolderSyncTestApp(folderSvc, vaultSvc)
	h := NewFolderWSHandler(testApp)
	c := newFolderSyncTestClient(testApp)
	t.Cleanup(c.WsCancel)

	h.doFolderSync(c, &dto.FolderSyncRequest{
		Context: "ctx-2",
		Vault:   "vault",
		Folders: []dto.FolderSyncCheckRequest{
			{Path: "ok", PathHash: "okhash", Mtime: 1},
		},
	})

	folderSvc.AssertCalled(t, "UpdateOrCreate", mock.Anything, int64(1), mock.AnythingOfType("*dto.FolderCreateRequest"))
	folderSvc.AssertExpectations(t)
}

// errTombstoneLookup 模拟 IsStaleRevive 查询库失败。
type tombstoneLookupError struct{}

func (tombstoneLookupError) Error() string { return "db lookup failed" }

var errTombstoneLookup = tombstoneLookupError{}
