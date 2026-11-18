package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
)

func bindBackslashRequest(t *testing.T, req any) {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(data))
	c.Request.Header.Set("Content-Type", "application/json")
	ok, errs := pkgapp.BindAndValid(c, req)
	require.True(t, ok, "%v", errs)
}

func TestBackslashNoteLifecycle(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "backslash first"
		if reverse {
			name = "slash first"
		}
		t.Run(name, func(t *testing.T) {
			svc, _, _, repo, _, _ := collisionServices(t)
			ctx := context.Background()
			first, other := `testdir\note.md`, "testdir/note.md"
			if reverse {
				first, other = other, first
			}
			create := &dto.NoteModifyOrCreateRequest{Vault: "v", Path: first, PathHash: util.EncodeHash32(first), Content: "old", ContentHash: "old", Mtime: 1700000000000}
			bindBackslashRequest(t, create)
			_, created, err := svc.ModifyOrCreate(ctx, 1, create, false)
			require.NoError(t, err)
			require.Equal(t, "testdir/note.md", created.Path)
			require.Equal(t, util.EncodeHash32(created.Path), created.PathHash)
			get := &dto.NoteGetRequest{Vault: "v", Path: other, PathHash: util.EncodeHash32(other)}
			bindBackslashRequest(t, get)
			got, err := svc.Get(ctx, 1, get)
			require.NoError(t, err)
			require.Equal(t, created.ID, got.ID)
			modify := &dto.NoteModifyOrCreateRequest{Vault: "v", Path: other, PathHash: util.EncodeHash32(other), Content: "new", ContentHash: "new", Mtime: 1700000001000}
			bindBackslashRequest(t, modify)
			_, modified, err := svc.ModifyOrCreate(ctx, 1, modify, false)
			require.NoError(t, err)
			require.Equal(t, created.ID, modified.ID)
			require.Equal(t, "new", modified.Content)
			del := &dto.NoteDeleteRequest{Vault: "v", Path: first, PathHash: util.EncodeHash32(first)}
			bindBackslashRequest(t, del)
			deleted, err := svc.Delete(ctx, 1, del)
			require.NoError(t, err)
			require.Equal(t, created.ID, deleted.ID)
			_, err = svc.Get(ctx, 1, get)
			require.Error(t, err)
			// Both spellings resolve to this same tombstone, without a second legacy row.
			rows, err := repo.ListByPathHash(ctx, created.PathHash, 1, 1)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, domain.NoteActionDelete, rows[0].Action)
		})
	}
}

func TestBackslashResourceRenameDelete(t *testing.T) {
	for _, kind := range []string{"note", "file", "folder"} {
		for _, which := range []string{"old", "new", "both"} {
			t.Run(kind+"/"+which, func(t *testing.T) {
				n, f, d, _, _, _ := collisionServices(t)
				ctx := context.Background()
				old, newPath := "a/old.md", "b/new.md"
				if which != "new" {
					old = strings.ReplaceAll(old, "/", `\`)
				}
				if which != "old" {
					newPath = strings.ReplaceAll(newPath, "/", `\`)
				}
				switch kind {
				case "note":
					create := &dto.NoteModifyOrCreateRequest{Vault: "v", Path: `a\old.md`, PathHash: "legacy", Content: "body", ContentHash: "body"}
					bindBackslashRequest(t, create)
					_, _, err := n.ModifyOrCreate(ctx, 1, create, false)
					require.NoError(t, err)
					req := &dto.NoteRenameRequest{Vault: "v", OldPath: old, OldPathHash: util.EncodeHash32(old), Path: newPath, PathHash: util.EncodeHash32(newPath)}
					bindBackslashRequest(t, req)
					_, got, err := n.Rename(ctx, 1, req)
					require.NoError(t, err)
					require.Equal(t, "b/new.md", got.Path)
					require.Equal(t, util.EncodeHash32(got.Path), got.PathHash)
					del := &dto.NoteDeleteRequest{Vault: "v", Path: `b\new.md`, PathHash: "legacy"}
					bindBackslashRequest(t, del)
					_, err = n.Delete(ctx, 1, del)
					require.NoError(t, err)
				case "file":
					create := &dto.FileUpdateRequest{Vault: "v", Path: `a\old.md`, PathHash: "legacy", ContentHash: "body"}
					bindBackslashRequest(t, create)
					_, created, err := f.UpdateOrCreate(ctx, 1, create, false)
					require.NoError(t, err)
					require.Equal(t, "a/old.md", created.Path)
					require.NoError(t, os.MkdirAll(filepath.Dir(created.SavePath), 0755))
					require.NoError(t, os.WriteFile(created.SavePath, []byte("body"), 0644))
					req := &dto.FileRenameRequest{Vault: "v", OldPath: old, OldPathHash: util.EncodeHash32(old), Path: newPath, PathHash: util.EncodeHash32(newPath)}
					bindBackslashRequest(t, req)
					_, got, err := f.Rename(ctx, 1, req)
					require.NoError(t, err)
					require.Equal(t, "b/new.md", got.Path)
					require.Equal(t, util.EncodeHash32(got.Path), got.PathHash)
					del := &dto.FileDeleteRequest{Vault: "v", Path: `b\new.md`, PathHash: "legacy"}
					bindBackslashRequest(t, del)
					_, err = f.Delete(ctx, 1, del)
					require.NoError(t, err)
				case "folder":
					create := &dto.FolderCreateRequest{Vault: "v", Path: `a\old.md`, PathHash: "legacy"}
					bindBackslashRequest(t, create)
					created, err := d.UpdateOrCreate(ctx, 1, create)
					require.NoError(t, err)
					require.Equal(t, "a/old.md", created.Path)
					req := &dto.FolderRenameRequest{Vault: "v", OldPath: old, OldPathHash: util.EncodeHash32(old), Path: newPath, PathHash: util.EncodeHash32(newPath)}
					bindBackslashRequest(t, req)
					_, got, err := d.Rename(ctx, 1, req)
					require.NoError(t, err)
					require.Equal(t, "b/new.md", got.Path)
					require.Equal(t, util.EncodeHash32(got.Path), got.PathHash)
					del := &dto.FolderDeleteRequest{Vault: "v", Path: `b\new.md`, PathHash: "legacy"}
					bindBackslashRequest(t, del)
					_, err = d.Delete(ctx, 1, del)
					require.NoError(t, err)
				}
			})
		}
	}
}
