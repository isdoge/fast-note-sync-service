package dao

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type noteTagRepository struct {
	dao *Dao
}

// NewNoteTagRepository creates a NoteTagRepository backed by the per-user database.
// NewNoteTagRepository 创建基于用户库的 NoteTagRepository。
func NewNoteTagRepository(dao *Dao) domain.NoteTagRepository {
	return &noteTagRepository{dao: dao}
}

func (r *noteTagRepository) db(uid int64) (*gorm.DB, error) {
	d := r.dao.ResolveDB("user_" + strconv.FormatInt(uid, 10))
	if d == nil {
		return nil, gorm.ErrInvalidDB
	}
	if err := model.AutoMigrate(d, "NoteTag"); err != nil {
		return nil, err
	}
	return d, nil
}

func (r *noteTagRepository) ReplaceForNote(ctx context.Context, noteID int64, tags []string, vaultID, uid int64, source string) error {
	d, err := r.db(uid)
	if err != nil {
		return err
	}
	d = d.WithContext(ctx)
	tags = normalizeTags(tags)
	return d.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("note_id = ? AND uid = ?", noteID, uid).
			Delete(&model.NoteTag{}).Error; err != nil {
			return err
		}
		if len(tags) == 0 {
			return nil
		}
		rows := make([]*model.NoteTag, 0, len(tags))
		for _, tag := range tags {
			rows = append(rows, &model.NoteTag{
				NoteID:  noteID,
				Tag:     tag,
				VaultID: vaultID,
				UID:     uid,
				Source:  source,
			})
		}
		return tx.CreateInBatches(rows, 200).Error
	})
}

func (r *noteTagRepository) DeleteByNoteID(ctx context.Context, noteID, uid int64) error {
	d, err := r.db(uid)
	if err != nil {
		return err
	}
	return d.WithContext(ctx).Where("note_id = ? AND uid = ?", noteID, uid).
		Delete(&model.NoteTag{}).Error
}

func (r *noteTagRepository) DeleteByVaultID(ctx context.Context, vaultID, uid int64) error {
	d, err := r.db(uid)
	if err != nil {
		return err
	}
	return d.WithContext(ctx).Where("vault_id = ? AND uid = ?", vaultID, uid).
		Delete(&model.NoteTag{}).Error
}

func (r *noteTagRepository) ListByVault(ctx context.Context, vaultID, uid int64, prefix, scope string, limit int) ([]*domain.TagCount, error) {
	d, err := r.db(uid)
	if err != nil {
		return nil, err
	}
	type row struct {
		Tag   string
		Count int
	}
	var rows []row
	// 只统计存活笔记：经文件夹级联删除、库删除等未逐篇清理标签的路径删掉的笔记，
	// 其残留标签行在这里被过滤掉。
	// Count live notes only: tag rows left behind by paths that do not clean tags per note
	// (folder cascade delete, vault delete, ...) are filtered out here.
	q := d.WithContext(ctx).Model(&model.NoteTag{}).
		Select("note_tag.tag AS tag, COUNT(DISTINCT note_tag.note_id) AS count").
		Joins("JOIN note ON note.id = note_tag.note_id AND note.action <> ?", "delete").
		Where("note_tag.vault_id = ? AND note_tag.uid = ?", vaultID, uid)
	if p := strings.TrimSpace(prefix); p != "" {
		q = q.Where("LOWER(note_tag.tag) LIKE ? ESCAPE '!'", escapeLike(strings.ToLower(p))+"%")
	}
	// scope: source is recorded per note, not per tag. 'frontmatter' keeps notes whose tags
	// come from frontmatter (source frontmatter or both), 'inline' keeps notes with inline
	// tags (inline or both), 'all'/empty keeps every source. Notes with tags in both places
	// are counted under both scopes, so the two lists are not a partition of 'all'.
	// scope：来源按笔记记录而非按标签。frontmatter 保留有 frontmatter 标签的笔记（frontmatter 或 both），
	// inline 保留有内联标签的笔记（inline 或 both），all/空 看全部来源。两处都有标签的笔记
	// 会同时计入两个 scope，因此两者不是 all 的划分。
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "frontmatter":
		q = q.Where("note_tag.source IN ?", []string{TagSourceFrontmatter, TagSourceBoth})
	case "inline":
		q = q.Where("note_tag.source IN ?", []string{TagSourceInline, TagSourceBoth})
	}
	q = q.Group("note_tag.tag").Order("count DESC, tag ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*domain.TagCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, &domain.TagCount{Tag: r.Tag, Count: r.Count})
	}
	return out, nil
}

func (r *noteTagRepository) GetTagsByNoteID(ctx context.Context, noteID, uid int64) ([]string, error) {
	d, err := r.db(uid)
	if err != nil {
		return nil, err
	}
	var tags []string
	if err := d.WithContext(ctx).Model(&model.NoteTag{}).
		Where("note_id = ? AND uid = ?", noteID, uid).
		Order("tag ASC").Pluck("tag", &tags).Error; err != nil {
		return nil, err
	}
	return tags, nil
}

func (r *noteTagRepository) RebuildForVault(ctx context.Context, vaultID, uid int64) (int, error) {
	d, err := r.db(uid)
	if err != nil {
		return 0, err
	}
	var notes []*model.Note
	if err := d.WithContext(ctx).Select("id", "path", "content", "vault_id").
		Where("vault_id = ? AND action <> ?", vaultID, "delete").
		Find(&notes).Error; err != nil {
		return 0, err
	}
	if err := r.DeleteByVaultID(ctx, vaultID, uid); err != nil {
		return 0, err
	}
	written := 0
	for _, n := range notes {
		// 正文实际存于笔记目录的 content.txt（note.content 列仅为懒迁移前的旧数据），
		// 与 noteRepository.fillNoteContent 同一读取规则：文件优先，缺失时回退库内旧正文。
		// The body lives in the note folder's content.txt (the note.content column only holds
		// pre-lazy-migration data); same rule as noteRepository.fillNoteContent: file first,
		// fall back to the legacy DB column when the file is missing.
		content, exists, err := r.dao.LoadContentFromFile(r.dao.GetNoteFolderPath(uid, n.ID), "content.txt")
		if err != nil {
			r.dao.Logger().Warn("note tag rebuild: load note content failed, skipped",
				zap.Int64("uid", uid), zap.Int64("noteId", n.ID), zap.Error(err))
			continue
		}
		if !exists {
			content = n.Content
		}
		tags, source := ExtractTags(content)
		if len(tags) == 0 {
			continue
		}
		if err := r.ReplaceForNote(ctx, n.ID, tags, vaultID, uid, source); err != nil {
			return written, err
		}
		written += len(tags)
	}
	return written, nil
}

// escapeLike escapes LIKE wildcards with '!' (used with ESCAPE '!'): '!' itself, '%' and '_'.
// '!' is used instead of '\' because a backslash inside a MySQL string literal is itself an escape.
// A backslash in the input is then matched literally on SQLite, MySQL and PostgreSQL.
// escapeLike 以 '!' 为转义符（配合 ESCAPE '!'）转义 LIKE 通配符：'!'、'%'、'_'。
// 不用 '\' 是因为 MySQL 字符串字面量里反斜杠本身是转义符；输入中的反斜杠在三种库上都按字面匹配。
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

// normalizeTags reuses the shared tag grammar: trim '#', drop blanks, de-dupe
// case-insensitively (original casing preserved), then sort for stable output.
// normalizeTags 复用统一的标签规则：去掉 '#'、剔除空值、忽略大小写去重（保留原始大小写），再排序以稳定输出。
func normalizeTags(tags []string) []string {
	out := dedupeTags(tags)
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}
