package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/service"
	servicemocks "github.com/haierkeys/fast-note-sync-service/internal/service/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 真实仓储验证碰撞路径的生命周期；辅助服务不访问磁盘，也不启动监听端口。
func collisionServices(t *testing.T) (service.NoteService, service.FileService, service.FolderService, domain.NoteRepository, domain.FileRepository, domain.FolderRepository) {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(t.TempDir()))
	t.Cleanup(func() { require.NoError(t, os.Chdir(wd)) })
	require.NoError(t, os.MkdirAll("storage/database", 0755))
	db, err := gorm.Open(sqlite.Open(filepath.Join("storage/database", "main.db")), &gorm.Config{})
	require.NoError(t, err)
	cfg := &config.DatabaseConfig{Type: "sqlite", Path: "storage/database/main.db", EnableWriteQueue: util.Ptr(false)}
	d := dao.New(db, context.Background(), dao.WithConfig(cfg), dao.WithUserDatabaseConfig(cfg), dao.WithLogger(zap.NewNop()), dao.WithBleveManager(dao.NewBleveManager(util.Ptr(false), util.Ptr(false), zap.NewNop())))
	nr, fr, dr := dao.NewNoteRepository(d), dao.NewFileRepository(d), dao.NewFolderRepository(d)
	v := new(servicemocks.MockVaultService)
	v.On("MustGetID", mock.Anything, int64(1), "v").Return(int64(1), nil)
	f := new(servicemocks.MockFolderService)
	f.On("SyncResourceFID", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	f.On("EnsurePathFID", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(int64(0), nil)
	f.On("CleanupEmptyAncestors", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	share := new(domainmocks.MockUserShareRepository)
	share.On("UpdateStatusByRes", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	migrated := make(chan struct{})
	share.On("MigrateResID", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Run(func(mock.Arguments) { close(migrated) })
	if strings.Contains(t.Name(), "NoteLifecycle/") && strings.HasSuffix(t.Name(), "rename") {
		t.Cleanup(func() {
			select {
			case <-migrated:
			case <-time.After(time.Second):
				t.Error("rename migration did not finish")
			}
		})
	}
	return service.NewNoteService(nil, nr, nil, nil, fr, share, nil, v, f, nil, nil, nil, nil), service.NewFileService(nil, fr, nr, v, f, nil, nil, nil, nil), service.NewFolderService(dr, nr, fr, v, nil, nil, nil, nil), nr, fr, dr
}

func TestPathCollisionNoteLifecycle(t *testing.T) {
	for _, prefix := range []string{"", "x/"} {
		for _, op := range []string{"create", "modify", "delete", "rename", "restore"} {
			t.Run(prefix+op, func(t *testing.T) {
				n, _, _, repo, _, _ := collisionServices(t)
				ctx := context.Background()
				a, b := prefix+"Aa.md", prefix+"BB.md"
				h := util.EncodeHash32(a)
				require.Equal(t, h, util.EncodeHash32(b))
				first, err := repo.Create(ctx, &domain.Note{VaultID: 1, Path: a, PathHash: h, Content: "A", ContentHash: "A", Action: domain.NoteActionCreate}, 1)
				require.NoError(t, err)
				if op != "create" {
					_, err = repo.Create(ctx, &domain.Note{VaultID: 1, Path: b, PathHash: h, Content: "B", ContentHash: "B", Action: domain.NoteActionCreate}, 1)
					require.NoError(t, err)
				}
				switch op {
				case "create", "modify":
					_, got, e := n.ModifyOrCreate(ctx, 1, &dto.NoteModifyOrCreateRequest{Vault: "v", Path: b, PathHash: h, Content: "B2", ContentHash: "B2"}, false)
					require.NoError(t, e)
					require.NotEqual(t, first.ID, got.ID)
				case "delete":
					_, err = n.Delete(ctx, 1, &dto.NoteDeleteRequest{Vault: "v", Path: b, PathHash: h})
					require.NoError(t, err)
				case "rename":
					_, got, e := n.Rename(ctx, 1, &dto.NoteRenameRequest{Vault: "v", OldPath: b, OldPathHash: h, Path: prefix + "renamed.md"})
					require.NoError(t, e)
					require.Equal(t, "B", got.Content)
				case "restore":
					rows, e := repo.ListByPathHash(ctx, h, 1, 1)
					require.NoError(t, e)
					for _, r := range rows {
						r.Action = domain.NoteActionDelete
						require.NoError(t, repo.UpdateDelete(ctx, r, 1))
					}
					got, e := n.Restore(ctx, 1, &dto.NoteRestoreRequest{Vault: "v", Path: b, PathHash: h})
					require.NoError(t, e)
					require.Equal(t, b, got.Path)
				}
				untouched, err := repo.GetByID(ctx, first.ID, 1)
				require.NoError(t, err)
				require.Equal(t, a, untouched.Path)
				require.Equal(t, "A", untouched.Content)
				expected := domain.NoteActionCreate
				if op == "restore" {
					expected = domain.NoteActionDelete
				}
				require.Equal(t, expected, untouched.Action)
			})
		}
	}
}

func TestPathCollisionFileLifecycle(t *testing.T) {
	for _, prefix := range []string{"", "x/"} {
		for _, op := range []string{"create", "modify", "delete", "rename", "restore"} {
			t.Run(prefix+op, func(t *testing.T) {
				_, s, _, _, r, _ := collisionServices(t)
				ctx := context.Background()
				a, b := prefix+"Aa.md", prefix+"BB.md"
				h := util.EncodeHash32(a)
				require.Equal(t, h, util.EncodeHash32(b))
				// 附件查询优先最新活跃行，先插入待操作的 A，再插入必须保持不变的 B。
				if op != "create" {
					_, err := r.Create(ctx, &domain.File{VaultID: 1, Path: a, PathHash: h, ContentHash: "A", Action: domain.FileActionCreate}, 1)
					require.NoError(t, err)
				}
				peer, err := r.Create(ctx, &domain.File{VaultID: 1, Path: b, PathHash: h, ContentHash: "B", Action: domain.FileActionCreate}, 1)
				require.NoError(t, err)
				rows, e := r.ListByPathHash(ctx, h, 1, 1)
				require.NoError(t, e)
				for _, row := range rows {
					require.NoError(t, os.MkdirAll(filepath.Dir(row.SavePath), 0755))
					require.NoError(t, os.WriteFile(row.SavePath, []byte(row.ContentHash), 0644))
				}
				switch op {
				case "create", "modify":
					_, got, e := s.UpdateOrCreate(ctx, 1, &dto.FileUpdateRequest{Vault: "v", Path: a, PathHash: h, ContentHash: "A2"}, false)
					require.NoError(t, e)
					require.NotEqual(t, peer.ID, got.ID)
				case "delete":
					_, err = s.Delete(ctx, 1, &dto.FileDeleteRequest{Vault: "v", Path: a, PathHash: h})
					require.NoError(t, err)
				case "rename":
					_, got, e := s.Rename(ctx, 1, &dto.FileRenameRequest{Vault: "v", OldPath: a, OldPathHash: h, Path: prefix + "renamed.md"})
					require.NoError(t, e)
					require.Equal(t, "A", got.ContentHash)
				case "restore":
					rows, e := r.ListByPathHash(ctx, h, 1, 1)
					require.NoError(t, e)
					for _, row := range rows {
						row.Action = domain.FileActionDelete
						_, e = r.Update(ctx, row, 1)
						require.NoError(t, e)
					}
					got, e := s.Restore(ctx, 1, &dto.FileRestoreRequest{Vault: "v", Path: a, PathHash: h})
					require.NoError(t, e)
					require.Equal(t, a, got.Path)
				}
				got, err := r.GetByID(ctx, peer.ID, 1)
				require.NoError(t, err)
				require.Equal(t, b, got.Path)
				require.Equal(t, "B", got.ContentHash)
				expected := domain.FileActionCreate
				if op == "restore" {
					expected = domain.FileActionDelete
				}
				require.Equal(t, expected, got.Action)
			})
		}
	}
}

func TestPathCollisionFolderLifecycle(t *testing.T) {
	for _, prefix := range []string{"", "x/"} {
		for _, op := range []string{"create", "rename", "delete", "deleteTree", "cleanup"} {
			t.Run(prefix+op, func(t *testing.T) {
				_, _, s, _, _, r := collisionServices(t)
				ctx := context.Background()
				a, b := prefix+"Aa.md", prefix+"BB.md"
				h := util.EncodeHash32(a)
				require.Equal(t, h, util.EncodeHash32(b))
				peer, err := r.Create(ctx, &domain.Folder{VaultID: 1, Path: a, PathHash: h, Action: domain.FolderActionCreate}, 1)
				require.NoError(t, err)
				if op != "create" {
					_, err = r.Create(ctx, &domain.Folder{VaultID: 1, Path: b, PathHash: h, Action: domain.FolderActionCreate}, 1)
					require.NoError(t, err)
				}
				switch op {
				case "create":
					got, e := s.UpdateOrCreate(ctx, 1, &dto.FolderCreateRequest{Vault: "v", Path: b, PathHash: h})
					require.NoError(t, e)
					require.NotEqual(t, peer.ID, got.ID)
				case "rename":
					old, _, e := s.Rename(ctx, 1, &dto.FolderRenameRequest{Vault: "v", OldPath: b, OldPathHash: h, Path: prefix + "renamed"})
					require.NoError(t, e)
					require.Equal(t, b, old.Path)
				case "delete":
					_, err = s.Delete(ctx, 1, &dto.FolderDeleteRequest{Vault: "v", Path: b, PathHash: h})
					require.NoError(t, err)
				case "deleteTree":
					_, err = s.DeleteTree(ctx, 1, &dto.FolderDeleteRequest{Vault: "v", Path: b, PathHash: h})
					require.NoError(t, err)
				case "cleanup":
					require.NoError(t, s.CleanDuplicateFolders(ctx, 1, 1))
				}
				got, err := r.GetByID(ctx, peer.ID, 1)
				require.NoError(t, err)
				require.Equal(t, a, got.Path)
				require.Equal(t, domain.FolderActionCreate, got.Action)
			})
		}
	}
}

func TestPathCollisionHashOnly(t *testing.T) {
	for _, kind := range []string{"note", "file", "folder"} {
		t.Run(kind, func(t *testing.T) {
			_, _, _, nr, fr, dr := collisionServices(t)
			ctx := context.Background()
			h := util.EncodeHash32("Aa.md")
			for _, p := range []string{"Aa.md", "BB.md"} {
				switch kind {
				case "note":
					_, err := nr.Create(ctx, &domain.Note{VaultID: 1, Path: p, PathHash: h, Content: p, Action: domain.NoteActionCreate}, 1)
					require.NoError(t, err)
				case "file":
					_, err := fr.Create(ctx, &domain.File{VaultID: 1, Path: p, PathHash: h, Action: domain.FileActionCreate}, 1)
					require.NoError(t, err)
				case "folder":
					_, err := dr.Create(ctx, &domain.Folder{VaultID: 1, Path: p, PathHash: h, Action: domain.FolderActionCreate}, 1)
					require.NoError(t, err)
				}
			}
			var err error
			switch kind {
			case "note":
				_, err = nr.GetByPathHash(ctx, h, 1, 1)
			case "file":
				_, err = fr.GetByPathHash(ctx, h, 1, 1)
			case "folder":
				_, err = dr.GetByPathHash(ctx, h, 1, 1)
			}
			require.Error(t, err, "只有哈希时不得任取碰撞路径")
		})
	}
}

func TestPathCollisionRecycleClear(t *testing.T) {
	for _, kind := range []string{"note", "file"} {
		for _, hashOnly := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/path", true: "/hashOnly"}[hashOnly], func(t *testing.T) {
				_, _, _, nr, fr, _ := collisionServices(t)
				ctx := context.Background()
				h := util.EncodeHash32("Aa.md")
				for _, p := range []string{"Aa.md", "BB.md"} {
					if kind == "note" {
						_, err := nr.Create(ctx, &domain.Note{VaultID: 1, Path: p, PathHash: h, Content: p, Action: domain.NoteActionDelete}, 1)
						require.NoError(t, err)
					} else {
						_, err := fr.Create(ctx, &domain.File{VaultID: 1, Path: p, PathHash: h, Action: domain.FileActionDelete}, 1)
						require.NoError(t, err)
					}
				}
				path := "BB.md"
				if hashOnly {
					path = ""
				}
				var err error
				if kind == "note" {
					err = nr.RecycleClear(ctx, path, h, 1, 1)
				} else {
					err = fr.RecycleClear(ctx, path, h, 1, 1)
				}
				if hashOnly {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				if kind == "note" {
					rows, e := nr.ListByPathHash(ctx, h, 1, 1)
					require.NoError(t, e)
					for _, r := range rows {
						want := int64(0)
						if !hashOnly && r.Path == "BB.md" {
							want = 2
						}
						require.Equal(t, want, r.Rename)
					}
				} else {
					rows, e := fr.ListByPathHash(ctx, h, 1, 1)
					require.NoError(t, e)
					for _, r := range rows {
						want := int64(0)
						if !hashOnly && r.Path == "BB.md" {
							want = 2
						}
						require.Equal(t, want, r.Rename)
					}
				}
			})
		}
	}
}

func TestPathCollisionSyncLists(t *testing.T) {
	n, f, d, nr, fr, dr := collisionServices(t)
	ctx := context.Background()
	h := util.EncodeHash32("Aa.md")
	for _, p := range []string{"Aa.md", "BB.md"} {
		_, err := nr.Create(ctx, &domain.Note{VaultID: 1, Path: p, PathHash: h, Content: p, Action: domain.NoteActionCreate}, 1)
		require.NoError(t, err)
		_, err = fr.Create(ctx, &domain.File{VaultID: 1, Path: p, PathHash: h, Action: domain.FileActionCreate}, 1)
		require.NoError(t, err)
		_, err = dr.Create(ctx, &domain.Folder{VaultID: 1, Path: p, PathHash: h, Action: domain.FolderActionCreate}, 1)
		require.NoError(t, err)
	}
	notes, err := n.ListByLastTime(ctx, 1, &dto.NoteSyncRequest{Vault: "v"})
	require.NoError(t, err)
	require.Len(t, notes, 2)
	files, err := f.ListByLastTime(ctx, 1, &dto.FileSyncRequest{Vault: "v"})
	require.NoError(t, err)
	require.Len(t, files, 2)
	folders, err := d.ListByUpdatedTimestamp(ctx, 1, "v", 0)
	require.NoError(t, err)
	require.Len(t, folders, 2)
}
