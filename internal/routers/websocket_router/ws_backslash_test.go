package websocket_router

import (
	"encoding/json"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	v1 "github.com/haierkeys/fast-note-sync-service/internal/proto/v1"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestBackslashSyncBothProtocols(t *testing.T) {
	for _, binary := range []bool{false, true} {
		protocol := "JSON"
		if binary {
			protocol = "Protobuf"
		}
		for _, kind := range []string{"note", "file", "folder", "setting"} {
			for _, mode := range []string{"same", "upload", "download", "deleted"} {
				t.Run(protocol+"/"+kind+"/"+mode, func(t *testing.T) {
					n, f, d, s, v := new(svcmocks.MockNoteService), new(svcmocks.MockFileService), new(svcmocks.MockFolderService), new(svcmocks.MockSettingService), new(svcmocks.MockVaultService)
					n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
					f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
					d.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(d).Maybe()
					s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
					v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()
					a := app.NewTestApp(&app.Services{NoteService: n, FileService: f, FolderService: d, SettingService: s, VaultService: v})
					c, messages := newSyncDeletePeer(t, a)
					if binary {
						c.Protocol = "protobuf"
						c.Server.ClientInfo(c, wsMsg("ClientInfo", map[string]any{"protobuf": true}))
						syncDeleteMessage(t, messages, "ClientInfo")
						c.Server.ProtobufDecoder = DeReceiveProtobufToDTO
					}
					const path = "testdir/note.md"
					hash := util.EncodeHash32(path)
					mt := int64(1700000000000)
					clientMT := mt
					clientContent := "same"
					action := "create"
					if mode == "upload" {
						clientMT++
						clientContent = "new"
					}
					if mode == "download" {
						clientMT--
						clientContent = "old"
					}
					if mode == "deleted" {
						action = "delete"
					}
					ctx := t.Name()
					t.Cleanup(func() { syncDownloadDelete(ctx, kind) })
					listName := kind + "s"
					if kind == "setting" {
						listName = "settings"
					}
					clientItem := map[string]any{"path": `testdir\note.md`, "pathHash": "legacy-hash", "mtime": clientMT}
					if kind != "folder" {
						clientItem["contentHash"] = clientContent
					}
					payload, err := json.Marshal(map[string]any{"vault": "v", "context": ctx, listName: []map[string]any{clientItem}})
					require.NoError(t, err)
					var req any
					var pb proto.Message
					var receive, end string
					switch kind {
					case "note":
						req = &dto.NoteSyncRequest{}
						pb = &v1.NoteSyncRequest{}
						receive = NoteReceiveSync
						end = NoteSyncEnd
					case "file":
						req = &dto.FileSyncRequest{}
						pb = &v1.FileSyncRequest{}
						receive = FileReceiveSync
						end = FileSyncEnd
					case "folder":
						req = &dto.FolderSyncRequest{}
						pb = &v1.FolderSyncRequest{}
						receive = FolderReceiveSync
						end = FolderSyncEnd
					case "setting":
						req = &dto.SettingSyncRequest{}
						pb = &v1.SettingSyncRequest{}
						receive = SettingReceiveSync
						end = SettingSyncEnd
					}
					if binary {
						require.NoError(t, protojson.Unmarshal(payload, pb))
						payload, err = proto.Marshal(pb)
						require.NoError(t, err)
					}
					ok, errs := c.BindAndValidWithAction(receive, payload, req)
					require.True(t, ok, "%v", errs)
					// Check the actual decoded list before passing it into the existing diff logic.
					normalized, err := json.Marshal(req)
					require.NoError(t, err)
					var decoded map[string]any
					require.NoError(t, json.Unmarshal(normalized, &decoded))
					item := decoded[listName].([]any)[0].(map[string]any)
					require.Equal(t, path, item["path"])
					require.Equal(t, hash, item["pathHash"])
					switch kind {
					case "note":
						n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{{Path: path, PathHash: hash, ContentHash: "same", Mtime: mt, Action: action}}, nil)
						NewNoteWSHandler(a).doNoteSync(c, req.(*dto.NoteSyncRequest))
					case "file":
						f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{{Path: path, PathHash: hash, ContentHash: "same", Mtime: mt, Action: action}}, nil)
						NewFileWSHandler(a).doFileSync(c, req.(*dto.FileSyncRequest))
					case "setting":
						s.On("Sync", mock.Anything, int64(1), mock.Anything).Return([]*dto.SettingDTO{{Path: path, PathHash: hash, ContentHash: "same", Mtime: mt, Action: action}}, nil)
						NewSettingWSHandler(a).doSettingSync(c, req.(*dto.SettingSyncRequest))
					case "folder":
						rows := []*dto.FolderDTO{{Path: path, PathHash: hash, Action: action}}
						if mode == "upload" {
							rows = nil
						}
						if mode == "download" {
							req.(*dto.FolderSyncRequest).Folders = nil
						}
						d.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "v", int64(0)).Return(rows, nil)
						d.On("IsStaleRevive", mock.Anything, int64(1), "v", hash, mock.Anything, path).Return(false, nil).Maybe()
						d.On("UpdateOrCreate", mock.Anything, int64(1), mock.MatchedBy(func(r *dto.FolderCreateRequest) bool { return r.Path == path && r.PathHash == hash })).Return(&dto.FolderDTO{Path: path, PathHash: hash}, nil).Maybe()
						NewFolderWSHandler(a).doFolderSync(c, req.(*dto.FolderSyncRequest))
					}
					counts := syncDeleteMessage(t, messages, end)["data"].(map[string]any)
					down, up, deleted := float64(0), float64(0), float64(0)
					if mode == "download" {
						down = 1
					}
					if mode == "upload" {
						up = 1
					}
					if mode == "deleted" {
						deleted = 1
					}
					require.Equal(t, down, counts["needModifyCount"])
					require.Equal(t, deleted, counts["needDeleteCount"])
					if kind != "folder" {
						require.Equal(t, up, counts["needUploadCount"])
					} else if mode == "upload" {
						d.AssertNumberOfCalls(t, "UpdateOrCreate", 1)
					}
					if entry, ok := syncDownloadGet(ctx, kind); ok {
						for _, msg := range entry.MessageQueue {
							data, err := json.Marshal(msg.Data)
							require.NoError(t, err)
							var fields map[string]any
							require.NoError(t, json.Unmarshal(data, &fields))
							require.Equal(t, path, fields["path"])
							if h, ok := fields["pathHash"]; ok {
								require.Equal(t, hash, h)
							}
						}
					}
				})
			}
		}
	}
}

func TestBackslashDeletedAndMissingInventoryPaths(t *testing.T) {
	for _, binary := range []bool{false, true} {
		for _, tc := range []struct {
			kind, action, list, deleted, missing string
			pb                                   proto.Message
			req                                  any
		}{
			{"note", NoteReceiveSync, "notes", "delNotes", "missingNotes", &v1.NoteSyncRequest{}, &dto.NoteSyncRequest{}},
			{"file", FileReceiveSync, "files", "delFiles", "missingFiles", &v1.FileSyncRequest{}, &dto.FileSyncRequest{}},
			{"folder", FolderReceiveSync, "folders", "delFolders", "missingFolders", &v1.FolderSyncRequest{}, &dto.FolderSyncRequest{}},
			{"setting", SettingReceiveSync, "settings", "delSettings", "missingSettings", &v1.SettingSyncRequest{}, &dto.SettingSyncRequest{}},
		} {
			name := "JSON/"
			if binary {
				name = "Protobuf/"
			}
			t.Run(name+tc.kind, func(t *testing.T) {
				a := app.NewTestApp(&app.Services{})
				c := newWSPathTestClient(a)
				t.Cleanup(c.WsCancel)
				// ClientInfo enables the real Protobuf binder; a client without a socket simply drops its ACK.
				if binary {
					c.Protocol = "protobuf"
					c.Server.ClientInfo(c, wsMsg("ClientInfo", map[string]any{"protobuf": true}))
					c.Server.ProtobufDecoder = DeReceiveProtobufToDTO
				}
				paths := []map[string]any{{"path": `a\b.md`, "pathHash": "legacy", "mtime": int64(1700000000000)}}
				deltas := []map[string]any{{"path": `a\b.md`, "pathHash": "legacy"}}
				data, err := json.Marshal(map[string]any{"vault": "v", "context": t.Name(), tc.list: paths, tc.deleted: deltas, tc.missing: deltas})
				require.NoError(t, err)
				if binary {
					require.NoError(t, protojson.Unmarshal(data, tc.pb))
					data, err = proto.Marshal(tc.pb)
					require.NoError(t, err)
				}
				ok, errs := c.BindAndValidWithAction(tc.action, data, tc.req)
				require.True(t, ok, "%v", errs)
				out, err := json.Marshal(tc.req)
				require.NoError(t, err)
				var fields map[string]any
				require.NoError(t, json.Unmarshal(out, &fields))
				for _, key := range []string{tc.list, tc.deleted, tc.missing} {
					items := fields[key].([]any)
					require.Len(t, items, 1)
					item := items[0].(map[string]any)
					require.Equal(t, "a/b.md", item["path"])
					require.Equal(t, util.EncodeHash32("a/b.md"), item["pathHash"])
				}
			})
		}
	}
}
