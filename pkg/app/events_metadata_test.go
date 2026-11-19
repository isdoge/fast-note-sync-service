package app_test

import (
	"encoding/json"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/stretchr/testify/require"
)

// Pin every allowed action to the actual DTOs used by WS, REST and MCP callers.
// 用 WS、REST 和 MCP 调用点的真实 DTO 固定每个白名单动作的字段约定。
func TestEventMetadataForEveryBroadcastAction(t *testing.T) {
	basic := app.BroadcastMeta{Path: "p", PathHash: "ph", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}
	hashed := basic
	hashed.ContentHash = "ch"
	renamed := hashed
	renamed.OldPath, renamed.OldPathHash = "old", "oh"
	folderRename := basic
	folderRename.OldPath, folderRename.OldPathHash = "old", "oh"
	for _, test := range []struct {
		action string
		data   any
		want   app.BroadcastMeta
	}{
		{"NoteSyncModify", dto.NoteSyncModifyMessage{Path: "p", PathHash: "ph", Content: "private body", ContentHash: "ch", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, hashed},
		{"NoteSyncModify", &dto.NoteDTO{Path: "p", PathHash: "ph", Content: "private body", ContentHash: "ch", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, hashed},
		{"NoteSyncDelete", dto.NoteSyncDeleteMessage{Path: "p", PathHash: "ph", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, basic},
		{"NoteSyncDelete", &dto.NoteDTO{Path: "p", PathHash: "ph", Content: "private body", ContentHash: "ch", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, hashed},
		{"NoteSyncRename", dto.NoteSyncRenameMessage{Path: "p", PathHash: "ph", OldPath: "old", OldPathHash: "oh", ContentHash: "ch", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, renamed},
		{"FolderSyncModify", dto.FolderSyncModifyMessage{Path: "p", PathHash: "ph", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, basic},
		{"FolderSyncDelete", dto.FolderSyncDeleteMessage{Path: "p", PathHash: "ph", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, basic},
		{"FolderSyncRename", dto.FolderSyncRenameMessage{Path: "p", PathHash: "ph", OldPath: "old", OldPathHash: "oh", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, folderRename},
		{"FileSyncUpdate", dto.FileSyncModifyMessage{Path: "p", PathHash: "ph", ContentHash: "ch", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, hashed},
		{"FileSyncUpdate", &dto.FileDTO{Path: "p", PathHash: "ph", ContentHash: "ch", SavePath: "private storage", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, hashed},
		{"FileSyncDelete", dto.FileSyncDeleteMessage{Path: "p", PathHash: "ph", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, basic},
		{"FileSyncDelete", &dto.FileDTO{Path: "p", PathHash: "ph", ContentHash: "ch", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, hashed},
		{"FileSyncRename", dto.FileSyncRenameMessage{Path: "p", PathHash: "ph", OldPath: "old", OldPathHash: "oh", ContentHash: "ch", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, renamed},
		{"SettingSyncModify", dto.SettingSyncModifyMessage{Path: "p", PathHash: "ph", Content: "private body", ContentHash: "ch", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, hashed},
		{"SettingSyncDelete", dto.SettingSyncDeleteMessage{Path: "p", PathHash: "ph", Ctime: 11, Mtime: 22, UpdatedTimestamp: 33}, basic},
		{"SettingSyncClear", nil, app.BroadcastMeta{}},
		{"ShareSyncRefresh", nil, app.BroadcastMeta{}},
	} {
		t.Run(test.action, func(t *testing.T) {
			hub := app.NewEventHub()
			sub, err := hub.Subscribe("1", "", nil)
			require.NoError(t, err)
			hub.Publish("1", "A", test.action, test.data)
			select {
			case event := <-sub.Events():
				require.Equal(t, test.want, event.BroadcastMeta)
				require.Equal(t, test.action, event.Action)
				require.Equal(t, "A", event.Vault)
				data, err := json.Marshal(event)
				require.NoError(t, err)
				require.NotContains(t, string(data), "private body")
				require.NotContains(t, string(data), "private storage")
				require.NotContains(t, string(data), `"content":`)
			default:
				t.Fatal("白名单动作没有产生事件")
			}
		})
	}
}
