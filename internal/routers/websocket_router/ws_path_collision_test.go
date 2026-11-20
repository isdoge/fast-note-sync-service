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

// 内存管道驱动真实同步比较与消息计数，不监听端口。
func TestSyncPathCollision(t *testing.T) {
	for _, prefix := range []string{"", "x/"} {
		for _, kind := range []string{"note", "file", "folder"} {
			for _, mode := range []string{"same", "upload", "download"} {
				t.Run(prefix+kind+"/"+mode, func(t *testing.T) {
					n, f, d, v := new(svcmocks.MockNoteService), new(svcmocks.MockFileService), new(svcmocks.MockFolderService), new(svcmocks.MockVaultService)
					n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
					f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
					d.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(d).Maybe()
					v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
					a := app.NewTestApp(&app.Services{NoteService: n, FileService: f, FolderService: d, VaultService: v})
					c, messages := newSyncDeletePeer(t, a)
					p, q := prefix+"Aa.md", prefix+"BB.md"
					h := util.EncodeHash32(p)
					require.Equal(t, h, util.EncodeHash32(q))
					ctx := t.Name()
					t.Cleanup(func() { syncDownloadDelete(ctx, kind) })
					const mt = int64(1700000000000)
					end := NoteSyncEnd
					switch kind {
					case "note":
						server := []*dto.NoteDTO{{Path: p, PathHash: h, ContentHash: "A", Mtime: mt}, {Path: q, PathHash: h, ContentHash: "B", Mtime: mt}}
						client := []dto.NoteSyncCheckRequest{{Path: p, PathHash: h, ContentHash: "A", Mtime: mt}, {Path: q, PathHash: h, ContentHash: "B", Mtime: mt}}
						if mode == "upload" {
							server = nil
						}
						if mode == "download" {
							client = client[:1]
						}
						n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return(server, nil)
						NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{Vault: "v", Context: ctx, Notes: client})
					case "file":
						end = FileSyncEnd
						server := []*dto.FileDTO{{Path: p, PathHash: h, ContentHash: "A", Mtime: mt}, {Path: q, PathHash: h, ContentHash: "B", Mtime: mt}}
						client := []dto.FileSyncCheckRequest{{Path: p, PathHash: h, ContentHash: "A", Mtime: mt}, {Path: q, PathHash: h, ContentHash: "B", Mtime: mt}}
						if mode == "upload" {
							server = nil
						}
						if mode == "download" {
							client = client[:1]
						}
						f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return(server, nil)
						NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{Vault: "v", Context: ctx, Files: client})
					case "folder":
						end = FolderSyncEnd
						server := []*dto.FolderDTO{{Path: p, PathHash: h}, {Path: q, PathHash: h}}
						client := []dto.FolderSyncCheckRequest{{Path: p, PathHash: h}, {Path: q, PathHash: h}}
						if mode == "upload" {
							server = nil
						}
						if mode == "download" {
							client = client[:1]
						}
						d.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "v", int64(0)).Return(server, nil)
						d.On("IsStaleRevive", mock.Anything, int64(1), "v", h, mock.Anything, mock.Anything).Return(false, nil).Maybe()
						d.On("UpdateOrCreate", mock.Anything, int64(1), mock.Anything).Return(&dto.FolderDTO{Path: p, PathHash: h}, nil).Maybe()
						NewFolderWSHandler(a).doFolderSync(c, &dto.FolderSyncRequest{Vault: "v", Context: ctx, Folders: client})
					}
					counts := syncDeleteMessage(t, messages, end)["data"].(map[string]any)
					wantDown := float64(0)
					if mode == "download" {
						wantDown = 1
					}
					require.Equal(t, wantDown, counts["needModifyCount"])
					if kind != "folder" {
						wantUp := float64(0)
						if mode == "upload" {
							wantUp = 2
						}
						require.Equal(t, wantUp, counts["needUploadCount"])
					} else if mode == "upload" {
						d.AssertNumberOfCalls(t, "UpdateOrCreate", 2)
					}
				})
			}
		}
	}
}

func TestUploadSessionPathCollision(t *testing.T) {
	a := app.NewTestApp(&app.Services{})
	a.Config().App.TempPath = t.TempDir()
	c := newWSPathTestClient(a)
	t.Cleanup(c.WsCancel)
	h := NewFileWSHandler(a)
	hash := util.EncodeHash32("Aa.md")
	first, err := h.handleFileUploadSessionCreate(c, "v", "Aa.md", hash, "same-content", 12, 0, 0, "a")
	require.NoError(t, err)
	t.Cleanup(first.CancelFunc)
	second, err := h.handleFileUploadSessionCreate(c, "v", "BB.md", hash, "same-content", 12, 0, 0, "b")
	require.NoError(t, err)
	t.Cleanup(second.CancelFunc)
	require.NotEqual(t, first.ID, second.ID, "碰撞路径不能共用上传会话")
	require.Same(t, first, c.Server.GetSession(c.User.ID, first.ID))
	require.Same(t, second, c.Server.GetSession(c.User.ID, second.ID))
	// 相同路径的新内容只替换本路径会话，另一条路径继续上传。
	replacement, err := h.handleFileUploadSessionCreate(c, "v", "Aa.md", hash, "new-content", 12, 0, 0, "c")
	require.NoError(t, err)
	t.Cleanup(replacement.CancelFunc)
	require.Nil(t, c.Server.GetSession(c.User.ID, first.ID))
	require.Same(t, second, c.Server.GetSession(c.User.ID, second.ID))
}

// 无路径项使用不同哈希及碰撞哈希，覆盖覆盖丢失、路径优先和单次消费。
func TestSyncOptionalPath(t *testing.T) {
	type item struct {
		path, hash string
	}
	p, q, r := "Aa.md", "BB.md", "other.md"
	h, rh := util.EncodeHash32(p), util.EncodeHash32(r)
	require.Equal(t, h, util.EncodeHash32(q))
	for _, kind := range []string{"note", "file", "folder"} {
		for _, tc := range []struct {
			name             string
			server, client   []item
			down, up, pathUp int
		}{
			{"hash_only_same", []item{{p, h}, {r, rh}}, []item{{"", h}, {"", rh}}, 0, 0, 0},
			{"hash_only_upload", nil, []item{{"", h}, {"", rh}}, 0, 2, 0},
			{"hash_only_download", []item{{p, h}, {r, rh}, {"new.md", "new"}}, []item{{"", h}, {"", rh}}, 1, 0, 0},
			{"mixed_path_first", []item{{p, h}, {q, h}}, []item{{"", h}, {p, h}}, 0, 0, 0},
			{"mixed_server_order_reversed", []item{{q, h}, {p, h}}, []item{{"", h}, {p, h}}, 0, 0, 0},
			{"mixed_unmatched", nil, []item{{"", h}, {p, h}}, 0, 2, 1},
			{"hash_only_consumed_once", []item{{p, h}, {q, h}}, []item{{"", h}}, 1, 0, 0},
			{"hash_only_collision_queue", []item{{p, h}, {q, h}}, []item{{"", h}, {"", h}}, 0, 0, 0},
			{"hash_only_same_hash_kept", nil, []item{{"", h}, {"", h}}, 0, 2, 0},
			{"path_item_not_hash_fallback", []item{{q, h}}, []item{{p, h}}, 1, 1, 1},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				n, f, d, v := new(svcmocks.MockNoteService), new(svcmocks.MockFileService), new(svcmocks.MockFolderService), new(svcmocks.MockVaultService)
				n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
				f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
				d.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(d).Maybe()
				v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
				a := app.NewTestApp(&app.Services{NoteService: n, FileService: f, FolderService: d, VaultService: v})
				a.Config().App.TempPath = t.TempDir()
				c, messages := newSyncDeletePeer(t, a)
				ctx := t.Name()
				t.Cleanup(func() { syncDownloadDelete(ctx, kind) })
				const mt = int64(1700000000000)
				end := NoteSyncEnd
				switch kind {
				case "note":
					var server []*dto.NoteDTO
					var client []dto.NoteSyncCheckRequest
					for _, it := range tc.server {
						server = append(server, &dto.NoteDTO{Path: it.path, PathHash: it.hash, ContentHash: "same", Mtime: mt})
					}
					for _, it := range tc.client {
						client = append(client, dto.NoteSyncCheckRequest{Path: it.path, PathHash: it.hash, ContentHash: "same", Mtime: mt})
					}
					n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return(server, nil)
					NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{Vault: "v", Context: ctx, Notes: client})
				case "file":
					end = FileSyncEnd
					var server []*dto.FileDTO
					var client []dto.FileSyncCheckRequest
					for _, it := range tc.server {
						server = append(server, &dto.FileDTO{Path: it.path, PathHash: it.hash, ContentHash: "same", Mtime: mt})
					}
					for _, it := range tc.client {
						client = append(client, dto.FileSyncCheckRequest{Path: it.path, PathHash: it.hash, ContentHash: "same", Mtime: mt})
					}
					f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return(server, nil)
					NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{Vault: "v", Context: ctx, Files: client})
				case "folder":
					end = FolderSyncEnd
					var server []*dto.FolderDTO
					var client []dto.FolderSyncCheckRequest
					for _, it := range tc.server {
						server = append(server, &dto.FolderDTO{Path: it.path, PathHash: it.hash})
					}
					for _, it := range tc.client {
						client = append(client, dto.FolderSyncCheckRequest{Path: it.path, PathHash: it.hash, Mtime: mt})
					}
					d.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "v", int64(0)).Return(server, nil)
					d.On("IsStaleRevive", mock.Anything, int64(1), "v", h, mt, p).Return(false, nil).Maybe()
					d.On("UpdateOrCreate", mock.Anything, int64(1), mock.Anything).Return(&dto.FolderDTO{Path: p, PathHash: h}, nil).Maybe()
					NewFolderWSHandler(a).doFolderSync(c, &dto.FolderSyncRequest{Vault: "v", Context: ctx, Folders: client})
				}
				counts := syncDeleteMessage(t, messages, end)["data"].(map[string]any)
				require.Equal(t, float64(tc.down), counts["needModifyCount"])
				wantUp := tc.up
				if kind != "note" {
					// 附件/目录原有安全校验不允许用空路径创建资源。
					wantUp = tc.pathUp
				}
				if kind == "folder" {
					d.AssertNumberOfCalls(t, "UpdateOrCreate", wantUp)
				} else {
					require.Equal(t, float64(wantUp), counts["needUploadCount"])
				}
			})
		}
	}
}

// 两种协议保留相同的可选路径清单，随后均由 *Sync 入口调用 do*Sync 比较。
func TestSyncOptionalPathProtocolMapping(t *testing.T) {
	for _, tc := range []struct {
		name, action, list string
		pb                 proto.Message
		jsonDTO, protoDTO  any
	}{
		{"note", NoteReceiveSync, "notes", &v1.NoteSyncRequest{}, &dto.NoteSyncRequest{DelNotes: []dto.NoteSyncDelNote{}, MissingNotes: []dto.NoteSyncDelNote{}}, &dto.NoteSyncRequest{}},
		{"file", FileReceiveSync, "files", &v1.FileSyncRequest{}, &dto.FileSyncRequest{DelFiles: []dto.FileSyncDelFile{}, MissingFiles: []dto.FileSyncDelFile{}}, &dto.FileSyncRequest{}},
		{"folder", FolderReceiveSync, "folders", &v1.FolderSyncRequest{}, &dto.FolderSyncRequest{DelFolders: []dto.FolderSyncDelFolder{}, MissingFolders: []dto.FolderSyncDelFolder{}}, &dto.FolderSyncRequest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"vault": "v", "context": t.Name(),
				tc.list: []map[string]any{
					{"pathHash": "h1", "mtime": 1700000000000},
					{"pathHash": "h2", "mtime": 1700000000000},
					{"path": "Aa.md", "pathHash": "h1", "mtime": 1700000000000},
				},
			})
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(payload, tc.jsonDTO))
			require.NoError(t, protojson.Unmarshal(payload, tc.pb))
			data, err := proto.Marshal(tc.pb)
			require.NoError(t, err)
			decoded, err := DeReceiveProtobufToDTO(tc.action, data, tc.protoDTO)
			require.NoError(t, err)
			require.True(t, decoded)
			require.Equal(t, tc.jsonDTO, tc.protoDTO)
			normalized, err := json.Marshal(tc.protoDTO)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(normalized, &got))
			items := got[tc.list].([]any)
			require.Len(t, items, 3)
			require.Equal(t, "", items[0].(map[string]any)["path"])
			require.Equal(t, "h1", items[0].(map[string]any)["pathHash"])
			require.Equal(t, "", items[1].(map[string]any)["path"])
			require.Equal(t, "h2", items[1].(map[string]any)["pathHash"])
			require.Equal(t, "Aa.md", items[2].(map[string]any)["path"])
		})
	}
}

func TestUploadSessionOptionalPath(t *testing.T) {
	a := app.NewTestApp(&app.Services{})
	a.Config().App.TempPath = t.TempDir()
	c := newWSPathTestClient(a)
	t.Cleanup(c.WsCancel)
	handler := NewFileWSHandler(a)
	h := util.EncodeHash32("Aa.md")
	create := func(vault, path string) *FileUploadBinaryChunkSession {
		s, err := handler.handleFileUploadSessionCreate(c, vault, path, h, "content", 12, 0, 0, "ctx")
		require.NoError(t, err)
		t.Cleanup(s.CancelFunc)
		return s
	}
	otherVault := create("other", "Aa.md")
	s := create("v", "Aa.md")
	require.Nil(t, c.Server.GetSessionByPathHash(c.User.ID, h, "v", "BB.md"))
	require.Same(t, s, c.Server.GetSessionByPathHash(c.User.ID, h, "v", ""))
	c.Server.CleanSessionsByPathHash(c.User.ID, h, "v", "")
	require.Nil(t, c.Server.GetSession(c.User.ID, s.ID))
	require.Same(t, otherVault, c.Server.GetSession(c.User.ID, otherVault.ID))
	s = create("v", "Aa.md")
	collision := create("v", "BB.md")
	require.Nil(t, c.Server.GetSessionByPathHash(c.User.ID, h, "v", ""))
	c.Server.CleanSessionsByPathHash(c.User.ID, h, "v", "")
	require.Same(t, s, c.Server.GetSession(c.User.ID, s.ID))
	require.Same(t, collision, c.Server.GetSession(c.User.ID, collision.ID))
}
