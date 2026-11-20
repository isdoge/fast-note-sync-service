package api_router

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	svcmocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRESTSettingWrites_PathValidation(t *testing.T) {
	for _, action := range []string{"modify", "delete", "renameNew", "renameOld"} {
		for _, path := range []string{"../x", "normal/x"} {
			t.Run(action+"/"+path, func(t *testing.T) {
				s := new(svcmocks.MockSettingService)
				called := 0
				run := func(mock.Arguments) { called++ }
				s.On("ModifyOrCreate", mock.Anything, int64(1), mock.Anything, false).Run(run).Return(false, &dto.SettingDTO{}, nil).Maybe()
				s.On("Delete", mock.Anything, int64(1), mock.Anything).Run(run).Return(&dto.SettingDTO{}, nil).Maybe()
				s.On("Rename", mock.Anything, int64(1), mock.Anything).Run(run).Return(&dto.SettingDTO{}, nil).Maybe()
				h := newTestSettingHandler(s)
				body := map[string]any{"vault": "v", "path": path, "newPath": "normal/new", "oldPath": "normal/old"}
				if action == "renameNew" {
					body["newPath"] = path
				}
				if action == "renameOld" {
					body["oldPath"] = path
				}
				b, err := json.Marshal(body)
				require.NoError(t, err)
				c, w := newSettingTestContext("POST", "/api/setting", string(b), 1)
				switch action {
				case "modify":
					h.CreateOrUpdate(c)
				case "delete":
					h.Delete(c)
				default:
					h.Rename(c)
				}
				if path == "../x" {
					assertResponseCode(t, w, code.ErrorInvalidPath.Code())
					require.Zero(t, called)
				} else {
					assertResponseCode(t, w, code.Success.Code())
					require.Equal(t, 1, called)
				}
			})
		}
	}
}

// 每个清洗路径且接收哈希的 REST 写入口都验证服务层实际收到的路径与哈希。
func TestRESTWrites_PathHashAfterCleaning(t *testing.T) {
	cases := []struct{ kind, action, method string }{
		{"note", "create", "ModifyOrCreate"}, {"note", "delete", "Delete"}, {"note", "restore", "Restore"},
		{"note", "frontmatter", "PatchFrontmatter"}, {"note", "append", "AppendContent"}, {"note", "prepend", "PrependContent"}, {"note", "replace", "ReplaceContent"}, {"note", "rename", "Rename"},
		{"file", "upload", "UpdateOrCreate"}, {"file", "delete", "Delete"}, {"file", "restore", "Restore"}, {"file", "rename", "Rename"}, {"file", "recycle", "RecycleClear"},
		{"folder", "create", "UpdateOrCreate"}, {"folder", "delete", "Delete"},
		{"setting", "create", "ModifyOrCreate"}, {"setting", "delete", "Delete"}, {"setting", "rename", "Rename"},
	}
	for _, tc := range cases {
		paths := []struct{ path, oldPath string }{{"/a.md", "/a.md"}, {"a.md", "a.md"}}
		if tc.action == "rename" {
			paths = append(paths, struct{ path, oldPath string }{"/a.md", "a.md"}, struct{ path, oldPath string }{"a.md", "/a.md"})
		}
		for _, pair := range paths {
			path, oldPath := pair.path, pair.oldPath
			t.Run(tc.kind+"/"+tc.action+"/new="+path+"/old="+oldPath, func(t *testing.T) {
				n, f, d, s := new(svcmocks.MockNoteService), new(svcmocks.MockFileService), new(svcmocks.MockFolderService), new(svcmocks.MockSettingService)
				n.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(n).Maybe()
				f.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(f).Maybe()
				d.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(d).Maybe()
				s.On("WithClient", mock.Anything, mock.Anything, mock.Anything).Return(s).Maybe()
				a := app.NewTestApp(&app.Services{NoteService: n, FileService: f, FolderService: d, SettingService: s})
				wss := pkgapp.NewWebsocketServer(pkgapp.WSConfig{}, a)
				hN, hF, hD, hS := NewNoteHandler(a, wss), NewFileHandler(a, wss), NewFolderHandler(a, wss), NewSettingHandler(a, wss)
				inputHash := "caller-hash"
				want := inputHash
				if path == "/a.md" {
					inputHash = util.EncodeHash32(path)
					want = util.EncodeHash32("a.md")
				}
				oldHash, wantOld := "caller-hash", "caller-hash"
				if oldPath == "/a.md" {
					oldHash = util.EncodeHash32(oldPath)
					wantOld = util.EncodeHash32("a.md")
				}
				called := false
				capture := func(args mock.Arguments) {
					called = true
					p := reflect.ValueOf(args.Get(2)).Elem()
					field, hash := "Path", "PathHash"
					if tc.kind == "setting" && tc.action == "rename" {
						field, hash = "NewPath", "NewPathHash"
					}
					require.Equal(t, "a.md", p.FieldByName(field).String())
					require.Equal(t, want, p.FieldByName(hash).String())
					if tc.action == "rename" {
						require.Equal(t, "a.md", p.FieldByName("OldPath").String())
						require.Equal(t, wantOld, p.FieldByName("OldPathHash").String())
					}
				}
				switch tc.kind {
				case "note":
					n.On("UpdateCheckWithNote", mock.Anything, int64(1), mock.Anything).Return("", nil, nil, nil).Maybe()
					getNote := &dto.NoteDTO{}
					if tc.action == "restore" {
						getNote.Action = "delete"
					}
					n.On("Get", mock.Anything, int64(1), mock.Anything).Return(getNote, nil).Maybe()
					var call *mock.Call
					// ModifyOrCreate 另带 mtimeCheck 与 skipConflictCheck。
					if tc.action == "create" {
						call = n.On(tc.method, mock.Anything, int64(1), mock.Anything, mock.Anything, mock.Anything)
						call.Return(false, &dto.NoteDTO{}, nil)
					} else {
						call = n.On(tc.method, mock.Anything, int64(1), mock.Anything)
					}
					if tc.action == "rename" {
						call.Return(&dto.NoteDTO{}, &dto.NoteDTO{}, nil)
					} else if tc.action == "replace" {
						call.Return(&dto.NoteReplaceResponse{}, nil)
					} else if tc.action != "create" {
						call.Return(&dto.NoteDTO{}, nil)
					}
					call.Run(capture).Once()
				case "file":
					var call *mock.Call
					if tc.action == "upload" {
						call = f.On(tc.method, mock.Anything, int64(1), mock.Anything, false).Return(false, &dto.FileDTO{}, nil)
					} else {
						call = f.On(tc.method, mock.Anything, int64(1), mock.Anything)
					}
					call.Run(capture).Once()
					if tc.action == "rename" {
						call.Return(&dto.FileDTO{}, &dto.FileDTO{}, nil)
					} else if tc.action == "recycle" {
						call.Return(nil)
					} else if tc.action != "upload" {
						call.Return(&dto.FileDTO{}, nil)
					}
				case "folder":
					d.On(tc.method, mock.Anything, int64(1), mock.Anything).Run(capture).Return(&dto.FolderDTO{}, nil).Once()
				case "setting":
					if tc.action == "create" {
						s.On(tc.method, mock.Anything, int64(1), mock.Anything, false).Run(capture).Return(false, &dto.SettingDTO{}, nil).Once()
					} else {
						s.On(tc.method, mock.Anything, int64(1), mock.Anything).Run(capture).Return(&dto.SettingDTO{}, nil).Once()
					}
				}
				body := map[string]any{"vault": "v", "path": path, "pathHash": inputHash, "oldPath": oldPath, "oldPathHash": oldHash, "newPath": path, "newPathHash": inputHash, "content": "x", "find": "x", "replace": "y", "updates": map[string]any{"tag": "x"}}
				b, err := json.Marshal(body)
				require.NoError(t, err)
				c, w := newNoteTestContext("POST", "/api/test", string(b), 1)
				if tc.action == "upload" {
					a.Config().App.TempPath = t.TempDir()
					var form bytes.Buffer
					writer := multipart.NewWriter(&form)
					for key, value := range map[string]string{"vault": "v", "path": path, "pathHash": inputHash} {
						require.NoError(t, writer.WriteField(key, value))
					}
					file, err := writer.CreateFormFile("file", "a.md")
					require.NoError(t, err)
					_, err = file.Write([]byte("content"))
					require.NoError(t, err)
					require.NoError(t, writer.Close())
					c.Request = httptest.NewRequest("POST", "/api/file", &form)
					c.Request.Header.Set("Content-Type", writer.FormDataContentType())
				}
				var call func(*gin.Context)
				switch tc.kind {
				case "note":
					call = map[string]func(*gin.Context){"create": hN.CreateOrUpdate, "delete": hN.Delete, "restore": hN.Restore, "frontmatter": hN.PatchFrontmatter, "append": hN.Append, "prepend": hN.Prepend, "replace": hN.Replace, "rename": hN.Rename}[tc.action]
				case "file":
					call = map[string]func(*gin.Context){"upload": hF.Upload, "delete": hF.Delete, "restore": hF.Restore, "rename": hF.Rename, "recycle": hF.RecycleClear}[tc.action]
				case "folder":
					call = map[string]func(*gin.Context){"create": hD.Create, "delete": hD.Delete}[tc.action]
				case "setting":
					call = map[string]func(*gin.Context){"create": hS.CreateOrUpdate, "delete": hS.Delete, "rename": hS.Rename}[tc.action]
				}
				call(c)
				assertResponseCode(t, w, code.Success.Code())
				require.True(t, called)
			})
		}
	}
}
