package websocket_router

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// 秒与对应毫秒应得到相同同步决策，不能把客户端误判为陈旧内容。
func TestSyncEntries_NormalizeMilli(t *testing.T) {
	for _, kind := range []string{"note", "file", "setting"} {
		for _, input := range []int64{1700000000, 1700000000000} {
			for _, sameContent := range []bool{true, false} {
				if kind == "setting" && !sameContent {
					continue
				}
				t.Run(kind+"/"+strconv.FormatInt(input, 10)+"/"+map[bool]string{true: "same", false: "changed"}[sameContent], func(t *testing.T) {
					n, f, s := new(svcmocks.MockNoteService), new(svcmocks.MockFileService), new(svcmocks.MockSettingService)
					v := new(svcmocks.MockVaultService)
					v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
					n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
					f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
					s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
					a := app.NewTestApp(&app.Services{NoteService: n, FileService: f, SettingService: s, VaultService: v})
					c, messages := newSyncDeletePeer(t, a)
					clientHash := "client"
					if sameContent {
						clientHash = "server"
					}
					context := t.Name()
					t.Cleanup(func() { syncDownloadDelete(context, kind) })
					endAction := NoteSyncEnd
					switch kind {
					case "note":
						n.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.NoteDTO{{Path: "a.md", PathHash: "h", ContentHash: "server", Mtime: 1700000000000}}, nil)
						NewNoteWSHandler(a).doNoteSync(c, &dto.NoteSyncRequest{Vault: "v", Context: context, Notes: []dto.NoteSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: clientHash, Mtime: input}}})
					case "file":
						f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{{Path: "a.md", PathHash: "h", ContentHash: "server", Mtime: 1700000000000}}, nil)
						NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{Vault: "v", Context: context, Files: []dto.FileSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: clientHash, Mtime: input}}})
						endAction = FileSyncEnd
					case "setting":
						s.On("Sync", mock.Anything, int64(1), mock.Anything).Return([]*dto.SettingDTO{{Path: "a.md", PathHash: "h", ContentHash: "server", Mtime: 1700000000000}}, nil)
						NewSettingWSHandler(a).doSettingSync(c, &dto.SettingSyncRequest{Vault: "v", Context: context, Settings: []dto.SettingSyncCheckRequest{{Path: "a.md", PathHash: "h", ContentHash: clientHash, Mtime: input}}})
						endAction = SettingSyncEnd
					}
					counts := syncDeleteMessage(t, messages, endAction)["data"].(map[string]any)
					require.Equal(t, float64(0), counts["needModifyCount"])
					require.Equal(t, float64(0), counts["needSyncMtimeCount"])
				})
			}
		}
	}
}

func TestWSEntryChecks_NormalizeMilli(t *testing.T) {
	for _, kind := range []string{"noteCheck", "settingCheck", "settingModify"} {
		for _, input := range []int64{1700000000, 1700000000000} {
			t.Run(kind+"/"+map[bool]string{true: "seconds", false: "millis"}[input < 1e11], func(t *testing.T) {
				n, s, v := new(svcmocks.MockNoteService), new(svcmocks.MockSettingService), new(svcmocks.MockVaultService)
				v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
				n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
				s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
				check := func(ct, mt int64) {
					require.Equal(t, int64(1700000000000), ct)
					require.Equal(t, int64(1700000000000), mt)
				}
				if kind == "noteCheck" {
					n.On("UpdateCheck", mock.Anything, int64(1), mock.Anything).Run(func(args mock.Arguments) { p := args.Get(2).(*dto.NoteUpdateCheckRequest); check(p.Ctime, p.Mtime) }).Return("", nil, nil).Once()
				} else {
					s.On("UpdateCheck", mock.Anything, int64(1), mock.Anything).Run(func(args mock.Arguments) { p := args.Get(2).(*dto.SettingUpdateCheckRequest); check(p.Ctime, p.Mtime) }).Return("", nil, nil).Once()
				}
				a := app.NewTestApp(&app.Services{NoteService: n, SettingService: s, VaultService: v})
				c := newWSPathTestClient(a)
				msg := wsMsg(kind, map[string]any{"vault": "v", "path": "a.md", "pathHash": "h", "ctime": input, "mtime": input})
				switch kind {
				case "noteCheck":
					NewNoteWSHandler(a).NoteModifyCheck(c, msg)
				case "settingCheck":
					NewSettingWSHandler(a).SettingModifyCheck(c, msg)
				case "settingModify":
					NewSettingWSHandler(a).SettingModify(c, msg)
				}
				n.AssertExpectations(t)
				s.AssertExpectations(t)
			})
		}
	}
}

func TestWSSettingEntries_PathValidation(t *testing.T) {
	for _, action := range []string{"modify", "check", "delete"} {
		for _, path := range []string{"../x", "normal/x"} {
			t.Run(action+"/"+path, func(t *testing.T) {
				s, v := new(svcmocks.MockSettingService), new(svcmocks.MockVaultService)
				s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
				v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil).Maybe()
				calls := 0
				method := "UpdateCheck"
				if action == "delete" {
					method = "Delete"
					s.On(method, mock.Anything, int64(1), mock.Anything).Run(func(mock.Arguments) { calls++ }).Return(&dto.SettingDTO{}, nil).Maybe()
				} else {
					s.On(method, mock.Anything, int64(1), mock.Anything).Run(func(mock.Arguments) { calls++ }).Return("", nil, nil).Maybe()
				}
				a := app.NewTestApp(&app.Services{SettingService: s, VaultService: v})
				c, messages := newSyncDeletePeer(t, a)
				msg := wsMsg(action, map[string]any{"vault": "v", "path": path, "pathHash": "h", "ctime": 1700000000, "mtime": 1700000000})
				h := NewSettingWSHandler(a)
				switch action {
				case "modify":
					h.SettingModify(c, msg)
				case "check":
					h.SettingModifyCheck(c, msg)
				case "delete":
					h.SettingDelete(c, msg)
				}
				expected := 1
				if path == "../x" {
					expected = 0
				}
				require.Equal(t, expected, calls)
				if expected == 0 {
					select {
					case raw := <-messages:
						var res map[string]any
						require.NoError(t, json.Unmarshal([]byte(raw), &res))
						require.Equal(t, float64(code.ErrorInvalidPath.Code()), res["code"])
					case <-time.After(time.Second):
						t.Fatal("未收到路径拒绝响应")
					}
				}
			})
		}
	}
}

func TestSyncNewEntries_PathValidationAndTimes(t *testing.T) {
	for _, kind := range []string{"file", "folder", "setting"} {
		t.Run(kind, func(t *testing.T) {
			f, d, s, v := new(svcmocks.MockFileService), new(svcmocks.MockFolderService), new(svcmocks.MockSettingService), new(svcmocks.MockVaultService)
			f.Test(t)
			d.Test(t)
			s.Test(t)
			v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
			f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
			d.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(d).Maybe()
			s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
			a := app.NewTestApp(&app.Services{FileService: f, FolderService: d, SettingService: s, VaultService: v})
			a.Config().App.TempPath = t.TempDir()
			c, messages := newSyncDeletePeer(t, a)
			ctx := t.Name()
			t.Cleanup(func() { syncDownloadDelete(ctx, kind) })
			endAction := FileSyncEnd
			switch kind {
			case "file":
				f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{}, nil)
				NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{Vault: "v", Context: ctx, Files: []dto.FileSyncCheckRequest{{Path: "../x", PathHash: "bad", Mtime: 1700000000}, {Path: "normal/x", PathHash: "good", Ctime: 1700000000, Mtime: 1700000001}, {Path: "normal/zero", PathHash: "zero"}}})
				require.Nil(t, c.Server.GetSessionByPathHash(c.User.ID, "bad"))
				session, ok := c.Server.GetSessionByPathHash(c.User.ID, "good").(*FileUploadBinaryChunkSession)
				require.True(t, ok)
				require.Equal(t, int64(1700000000000), session.Ctime)
				require.Equal(t, int64(1700000001000), session.Mtime)
				zero, ok := c.Server.GetSessionByPathHash(c.User.ID, "zero").(*FileUploadBinaryChunkSession)
				require.True(t, ok)
				require.Zero(t, zero.Ctime)
				require.Zero(t, zero.Mtime)
			case "folder":
				d.On("ListByUpdatedTimestamp", mock.Anything, int64(1), "v", int64(0)).Return([]*dto.FolderDTO{}, nil)
				d.On("IsStaleRevive", mock.Anything, int64(1), "v", "good", int64(1700000000000), "normal/x").Return(false, nil).Once()
				d.On("UpdateOrCreate", mock.Anything, int64(1), mock.MatchedBy(func(p *dto.FolderCreateRequest) bool { return p.Path == "normal/x" })).Return(&dto.FolderDTO{}, nil).Once()
				NewFolderWSHandler(a).doFolderSync(c, &dto.FolderSyncRequest{Vault: "v", Context: ctx, Folders: []dto.FolderSyncCheckRequest{{Path: "../x", PathHash: "bad", Mtime: 1700000000}, {Path: "normal/x", PathHash: "good", Mtime: 1700000000}}})
				endAction = FolderSyncEnd
			case "setting":
				s.On("Sync", mock.Anything, int64(1), mock.Anything).Return([]*dto.SettingDTO{}, nil)
				s.On("Get", mock.Anything, int64(1), mock.MatchedBy(func(p *dto.SettingGetRequest) bool { return p.PathHash == "missing-good" })).Return(nil, nil).Once()
				s.On("Get", mock.Anything, int64(1), mock.MatchedBy(func(p *dto.SettingGetRequest) bool { return p.PathHash == "delete-good" })).Return(&dto.SettingDTO{}, nil).Once()
				s.On("Delete", mock.Anything, int64(1), mock.MatchedBy(func(p *dto.SettingDeleteRequest) bool { return p.Path == "normal/deleted" })).Return(&dto.SettingDTO{}, nil).Once()
				NewSettingWSHandler(a).doSettingSync(c, &dto.SettingSyncRequest{Vault: "v", Context: ctx, LastTime: 1, MissingSettings: []dto.SettingSyncDelSetting{{Path: "../x", PathHash: "missing-bad"}, {Path: "normal/missing", PathHash: "missing-good"}}, Settings: []dto.SettingSyncCheckRequest{{Path: "../x", PathHash: "bad"}, {Path: "normal/x", PathHash: "good"}}, DelSettings: []dto.SettingSyncDelSetting{{Path: "../x", PathHash: "delete-bad"}, {Path: "normal/deleted", PathHash: "delete-good"}}})
				endAction = SettingSyncEnd
			}
			counts := syncDeleteMessage(t, messages, endAction)["data"].(map[string]any)
			if kind == "setting" {
				require.Equal(t, float64(1), counts["needUploadCount"])
			}
			f.AssertExpectations(t)
			d.AssertExpectations(t)
			s.AssertExpectations(t)
		})
	}
}

func TestWSSettingModify_NormalizesBeforeWrite(t *testing.T) {
	for _, input := range []int64{0, 1700000000, 1700000000000} {
		t.Run(strconv.FormatInt(input, 10), func(t *testing.T) {
			s, v := new(svcmocks.MockSettingService), new(svcmocks.MockVaultService)
			v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
			s.On("UpdateCheck", mock.Anything, int64(1), mock.Anything).Return("Create", nil, nil).Once()
			s.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, true).Run(func(args mock.Arguments) {
				p := args.Get(2).(*dto.SettingModifyOrCreateRequest)
				want := int64(1700000000000)
				if input == 0 {
					want = 0
				}
				require.Equal(t, want, p.Ctime)
				require.Equal(t, want, p.Mtime)
			}).Return(false, &dto.SettingDTO{}, nil).Once()
			a := app.NewTestApp(&app.Services{SettingService: s, VaultService: v})
			NewSettingWSHandler(a).SettingModify(newWSPathTestClient(a), wsMsg("SettingModify", map[string]any{"vault": "v", "path": "normal/x", "ctime": input, "mtime": input}))
			s.AssertExpectations(t)
		})
	}
}

func TestFileSyncExistingUpload_PathValidation(t *testing.T) {
	for _, path := range []string{"../x", "normal/x"} {
		t.Run(path, func(t *testing.T) {
			f, v := new(svcmocks.MockFileService), new(svcmocks.MockVaultService)
			v.On("GetOrCreate", mock.Anything, int64(1), "v").Return(nil, nil)
			f.On("ListByLastTime", mock.Anything, int64(1), mock.Anything).Return([]*dto.FileDTO{{Path: "normal/x", PathHash: "h", ContentHash: "old", Ctime: 1600000000000, Mtime: 1600000000000}}, nil)
			a := app.NewTestApp(&app.Services{FileService: f, VaultService: v})
			a.Config().App.TempPath = t.TempDir()
			c, messages := newSyncDeletePeer(t, a)
			ctx := t.Name()
			t.Cleanup(func() { syncDownloadDelete(ctx, "file") })
			NewFileWSHandler(a).doFileSync(c, &dto.FileSyncRequest{Vault: "v", Context: ctx, Files: []dto.FileSyncCheckRequest{{Path: path, PathHash: "h", ContentHash: "new", Mtime: 1700000000}}})
			counts := syncDeleteMessage(t, messages, FileSyncEnd)["data"].(map[string]any)
			session := c.Server.GetSessionByPathHash(c.User.ID, "h")
			if path == "../x" {
				require.Nil(t, session)
				require.Equal(t, float64(0), counts["needUploadCount"])
			} else {
				require.NotNil(t, session)
				uploaded := session.(*FileUploadBinaryChunkSession)
				require.Equal(t, int64(1600000000000), uploaded.Ctime)
				require.Equal(t, int64(1700000000000), uploaded.Mtime)
				require.Equal(t, float64(1), counts["needUploadCount"])
			}
		})
	}
}
