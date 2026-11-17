package service

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	domainmocks "github.com/haierkeys/fast-note-sync-service/internal/domain/mocks"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestBackupService_ExportArchivePreservesMtime(t *testing.T) {
	vaultRepo := new(domainmocks.MockVaultRepository)
	svc := newBackupSvc(new(domainmocks.MockBackupRepository), vaultRepo, &backupStorageStub{})
	mtime := time.Date(2020, 2, 4, 12, 30, 20, 0, time.UTC)
	attachment := filepath.Join(t.TempDir(), "source.txt")
	require.NoError(t, os.WriteFile(attachment, []byte("attachment"), 0600))
	vaultRepo.On("GetByID", mock.Anything, int64(2), int64(1)).Return(&domain.Vault{ID: 2}, nil)
	svc.noteRepo.(*domainmocks.MockNoteRepository).On("List", mock.Anything, int64(2), 1, 1000000, int64(1), "", false, "", false, "", "", []string(nil)).Return([]*domain.Note{
		{Path: "note.md", Content: "note", Mtime: mtime.UnixMilli()},
	}, nil)
	svc.fileRepo.(*domainmocks.MockFileRepository).On("List", mock.Anything, int64(2), 1, 1000000, int64(1), "", false, "", "").Return([]*domain.File{
		{Path: "assets/file.txt", SavePath: attachment, Mtime: mtime.UnixMilli()},
	}, nil)
	dest := t.TempDir()
	count, _, err := svc.exportArchiveFiles(context.Background(), 1, 2, dest, false, time.Time{})
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	for _, path := range []string{"note.md", "assets/file.txt"} {
		info, err := os.Stat(filepath.Join(dest, path))
		require.NoError(t, err)
		require.True(t, info.ModTime().Equal(mtime), "%s mtime: %v", path, info.ModTime())
	}
	zipPath := filepath.Join(t.TempDir(), "backup.zip")
	require.NoError(t, util.Zip(dest, zipPath))
	archive, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer archive.Close()
	for _, file := range archive.File {
		if !file.FileInfo().IsDir() {
			require.True(t, file.Modified.Equal(mtime), "%s archive mtime: %v", file.Name, file.Modified)
		}
	}
}
