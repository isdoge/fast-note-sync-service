package dao

import (
	"context"
	"fmt"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
)

// TestNoteRepository_ListByPathsMeta_ChunkedOver500 验证笔记批量元数据查询在超过
// 500 条路径时分块执行且结果完整：1201 条全部命中，同一路径多行保留
// updated_timestamp 最新的一条，不串库、且墓碑行一并返回。
func TestNoteRepository_ListByPathsMeta_ChunkedOver500(t *testing.T) {
	daoInst, cleanup := setupSyncWindowBatchEnv(t)
	defer cleanup()

	ctx := context.Background()
	const uid = int64(1)

	const total = 1201 // 覆盖 500+500+201 三块
	userDB := daoInst.ResolveDB("user_" + fmt.Sprint(uid))
	require.NoError(t, userDB.AutoMigrate(&model.Note{}))
	rows := make([]model.Note, 0, total+2)
	for i := 0; i < total; i++ {
		path := fmt.Sprintf("notes/note-%04d.md", i)
		action := "create"
		if i%17 == 0 {
			action = "delete" // 批量查询包含所有状态
		}
		rows = append(rows, model.Note{
			VaultID:          batchVaultID,
			Action:           action,
			Path:             path,
			PathHash:         util.EncodeHash32(path),
			ContentHash:      fmt.Sprintf("hash-%04d", i),
			UpdatedTimestamp: int64(10000 + i),
		})
	}
	// 同一路径的旧重复行：updated_timestamp 更小，不得胜出。
	rows = append(rows, model.Note{
		VaultID:          batchVaultID,
		Action:           "modify",
		Path:             "notes/note-0000.md",
		PathHash:         util.EncodeHash32("notes/note-0000.md"),
		ContentHash:      "stale-row",
		UpdatedTimestamp: 1,
	})
	// 另一 vault 的同路径行：不得串库。
	rows = append(rows, model.Note{
		VaultID:          batchVaultID + 1,
		Action:           "create",
		Path:             "notes/note-0000.md",
		PathHash:         util.EncodeHash32("notes/note-0000.md"),
		ContentHash:      "other-vault",
		UpdatedTimestamp: 99999999,
	})
	require.NoError(t, userDB.Create(&rows).Error)

	noteRepo := NewNoteRepository(daoInst)
	paths := make([]string, 0, total)
	for i := 0; i < total; i++ {
		paths = append(paths, fmt.Sprintf("notes/note-%04d.md", i))
	}
	got, err := noteRepo.ListByPathsMeta(ctx, paths, batchVaultID, uid)
	require.NoError(t, err)
	require.Len(t, got, total)
	for i := 0; i < total; i++ {
		path := fmt.Sprintf("notes/note-%04d.md", i)
		n, ok := got[path]
		require.True(t, ok, path)
		require.Equal(t, fmt.Sprintf("hash-%04d", i), n.ContentHash)
	}
	require.Equal(t, "hash-0000", got["notes/note-0000.md"].ContentHash)
	require.Equal(t, domain.NoteActionDelete, got["notes/note-0000.md"].Action)
}
