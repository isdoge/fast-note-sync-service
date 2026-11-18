package dao

import (
	"context"
	"testing"

	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/stretchr/testify/require"
)

// TestNoteTagRepository_ListByVault_EscapesPrefixAndSkipsDeleted 覆盖 #509 修复：
// 前缀中的 % _ \ 按字面匹配；已删除笔记残留的标签行不计入。
func TestNoteTagRepository_ListByVault_EscapesPrefixAndSkipsDeleted(t *testing.T) {
	daoInst, _, _, cleanup := setupFTSTestEnv(t, false)
	defer cleanup()

	ctx := context.Background()
	uid, vaultID := int64(7), int64(3)
	userDb := daoInst.ResolveDB("user_7")
	require.NoError(t, userDb.AutoMigrate(&model.Note{}))
	for _, n := range []model.Note{
		{ID: 1, VaultID: vaultID, Path: "a.md", PathHash: "ha", Action: "create"},
		{ID: 2, VaultID: vaultID, Path: "b.md", PathHash: "hb", Action: "modify"},
		{ID: 3, VaultID: vaultID, Path: "c.md", PathHash: "hc", Action: "delete"},
	} {
		n := n
		require.NoError(t, userDb.Create(&n).Error)
	}

	repo := NewNoteTagRepository(daoInst)
	require.NoError(t, repo.ReplaceForNote(ctx, 1, []string{"100%", "a_b", `x\y`, "work"}, vaultID, uid, TagSourceInline))
	require.NoError(t, repo.ReplaceForNote(ctx, 2, []string{"100x", "axb", "xzy", "work"}, vaultID, uid, TagSourceInline))
	// 已删除笔记（如文件夹级联删除）残留的标签行
	require.NoError(t, repo.ReplaceForNote(ctx, 3, []string{"work", "stale"}, vaultID, uid, TagSourceInline))

	names := func(prefix string) map[string]int {
		got, err := repo.ListByVault(ctx, vaultID, uid, prefix, "all", 0)
		require.NoError(t, err)
		out := map[string]int{}
		for _, tc := range got {
			out[tc.Tag] = tc.Count
		}
		return out
	}

	require.Equal(t, map[string]int{"100%": 1}, names("100%"), "Bug: 前缀中的 %% 必须按字面匹配")
	require.Equal(t, map[string]int{"a_b": 1}, names("a_"), "Bug: 前缀中的 _ 必须按字面匹配")
	require.Equal(t, map[string]int{`x\y`: 1}, names(`x\`), `Bug: 前缀中的 \ 必须按字面匹配`)

	all := names("")
	require.Equal(t, 2, all["work"], "Bug: 已删除笔记的标签行不得计入引用次数")
	_, hasStale := all["stale"]
	require.False(t, hasStale, "Bug: 只出现在已删除笔记上的标签不得列出")
}

// TestNoteTagRepository_ListByVault_ScopeIncludesBothSource 覆盖 #509 修复：
// frontmatter 与内联都有标签的笔记（source=both）在 frontmatter / inline 两个 scope 下都要计入。
func TestNoteTagRepository_ListByVault_ScopeIncludesBothSource(t *testing.T) {
	daoInst, _, _, cleanup := setupFTSTestEnv(t, false)
	defer cleanup()

	ctx := context.Background()
	uid, vaultID := int64(7), int64(3)
	userDb := daoInst.ResolveDB("user_7")
	require.NoError(t, userDb.AutoMigrate(&model.Note{}))
	for _, n := range []model.Note{
		{ID: 1, VaultID: vaultID, Path: "fm.md", PathHash: "h1", Action: "create"},
		{ID: 2, VaultID: vaultID, Path: "in.md", PathHash: "h2", Action: "create"},
		{ID: 3, VaultID: vaultID, Path: "both.md", PathHash: "h3", Action: "create"},
	} {
		n := n
		require.NoError(t, userDb.Create(&n).Error)
	}

	repo := NewNoteTagRepository(daoInst)
	require.NoError(t, repo.ReplaceForNote(ctx, 1, []string{"work"}, vaultID, uid, TagSourceFrontmatter))
	require.NoError(t, repo.ReplaceForNote(ctx, 2, []string{"work"}, vaultID, uid, TagSourceInline))
	require.NoError(t, repo.ReplaceForNote(ctx, 3, []string{"work"}, vaultID, uid, TagSourceBoth))

	count := func(scope string) int {
		got, err := repo.ListByVault(ctx, vaultID, uid, "", scope, 0)
		require.NoError(t, err)
		require.Len(t, got, 1)
		return got[0].Count
	}
	require.Equal(t, 3, count("all"))
	require.Equal(t, 2, count("frontmatter"), "Bug: source=both 的笔记在 frontmatter scope 下被排除")
	require.Equal(t, 2, count("inline"), "Bug: source=both 的笔记在 inline scope 下被排除")
}
