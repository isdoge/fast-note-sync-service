package websocket_router

import (
	"testing"

	internalapp "github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"go.uber.org/zap"
)

// B11：受限令牌发不带 vault 字段的库级消息时必须拒绝，白名单内的用户级消息仍放行。
func TestCheckVaultAccess_MissingVaultFailsClosed(t *testing.T) {
	testApp := internalapp.NewTestApp(&internalapp.Services{})
	logger := zap.NewNop()
	wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, testApp)

	newClient := func(vaults string) *pkgapp.WebsocketClient {
		c := wss.NewTestClient(1)
		c.Vaults = vaults
		return c
	}

	t.Run("restricted token missing vault denied", func(t *testing.T) {
		c := newClient("vault-a")
		msg := &pkgapp.WebSocketMessage{Type: NoteReceiveModify, Data: []byte(`{"path":"a.md"}`)}
		if checkVaultAccess(c, msg, logger) {
			t.Fatal("库级消息缺少 vault 字段时不得放行")
		}
	})

	t.Run("restricted token allowed vault passes", func(t *testing.T) {
		c := newClient("vault-a")
		msg := &pkgapp.WebSocketMessage{Type: NoteReceiveModify, Data: []byte(`{"vault":"vault-a","path":"a.md"}`)}
		if !checkVaultAccess(c, msg, logger) {
			t.Fatal("白名单内的库应放行")
		}
	})

	t.Run("restricted token other vault denied", func(t *testing.T) {
		c := newClient("vault-a")
		msg := &pkgapp.WebSocketMessage{Type: NoteReceiveModify, Data: []byte(`{"vault":"vault-b","path":"a.md"}`)}
		if checkVaultAccess(c, msg, logger) {
			t.Fatal("白名单外的库应拒绝")
		}
	})

	t.Run("whitelisted user-level message passes", func(t *testing.T) {
		c := newClient("vault-a")
		msg := &pkgapp.WebSocketMessage{Type: SettingReceiveCheck, Data: []byte(`{"path":"a.md","pathHash":"h"}`)}
		if !checkVaultAccess(c, msg, logger) {
			t.Fatal("用户级白名单消息应放行")
		}
	})

	t.Run("unrestricted token unaffected", func(t *testing.T) {
		c := newClient("")
		msg := &pkgapp.WebSocketMessage{Type: NoteReceiveModify, Data: []byte(`{"path":"a.md"}`)}
		if !checkVaultAccess(c, msg, logger) {
			t.Fatal("未受限令牌不受影响")
		}
	})
}

// 全部接收动作都必须有归属：要么消息负载必带 vault（库级），要么在显式白名单（用户级）。
// 新增消息类型而未归类时此测试失败，防止再次出现缺字段即放行的面。
func TestVaultOptionalMessageTypesCoverAllReceiveActions(t *testing.T) {
	// 库级消息类型：负载按协议必须带 vault 字段。
	vaultScoped := map[string]struct{}{
		NoteReceiveModify:         {},
		NoteReceiveDelete:         {},
		NoteReceiveRename:         {},
		NoteReceiveRePush:         {},
		NoteReceiveCheck:          {},
		NoteReceiveSync:           {},
		NoteSyncPageAck:           {},
		FolderReceiveSync:         {},
		FolderReceiveModify:       {},
		FolderReceiveDelete:       {},
		FolderReceiveRename:       {},
		FolderSyncPageAck:         {},
		SettingReceiveModify:      {},
		SettingReceiveDelete:      {},
		SettingReceiveSync:        {},
		SettingReceiveClear:       {},
		SettingSyncPageAck:        {},
		FileReceiveSync:           {},
		FileReceiveUploadCheck:    {},
		FileReceiveRename:         {},
		FileReceiveDelete:         {},
		FileReceiveChunkDownload:  {},
		FileReceiveRePush:         {},
		FileSyncPageAck:           {},
	}

	all := map[string]struct{}{}
	for k := range vaultScoped {
		all[k] = struct{}{}
	}
	for k := range vaultOptionalMessageTypes {
		if _, dup := all[k]; dup {
			t.Fatalf("消息类型 %s 同时出现在库级集合与白名单", k)
		}
		all[k] = struct{}{}
	}

	// action.go 中全部接收动作常量；直接复用常量保证编译期同步。
	receiveActions := []string{
		ClientReceiveInfo, ClientReceiveAuth,
		FolderReceiveSync, FolderReceiveModify, FolderReceiveDelete, FolderReceiveRename,
		NoteReceiveSync, NoteReceiveModify, NoteReceiveDelete, NoteReceiveRename,
		NoteReceiveCheck, NoteReceiveRePush,
		FileReceiveSync, FileReceiveUploadCheck, FileReceiveDelete, FileReceiveRename,
		FileReceiveChunkDownload, FileReceiveRePush,
		SettingReceiveSync, SettingReceiveModify, SettingReceiveDelete,
		SettingReceiveCheck, SettingReceiveClear, SettingReceiveRePush,
		NoteSyncPageAck, FileSyncPageAck, SettingSyncPageAck, FolderSyncPageAck,
	}
	for _, action := range receiveActions {
		if _, ok := all[action]; !ok {
			t.Errorf("接收动作 %s 未归类（既不是库级也不在用户级白名单）", action)
		}
	}
	// 白名单不应包含任何库级动作。
	for action := range vaultOptionalMessageTypes {
		if _, bad := vaultScoped[action]; bad {
			t.Errorf("用户级白名单包含库级动作 %s", action)
		}
	}
}
