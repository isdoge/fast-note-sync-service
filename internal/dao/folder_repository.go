package dao

import (
	"context"
	"strconv"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/internal/query"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"gorm.io/gorm"
)

type folderRepository struct {
	Dao             *Dao
	customPrefixKey string
}

func NewFolderRepository(d *Dao) domain.FolderRepository {
	return &folderRepository{Dao: d, customPrefixKey: "user_folder_"} // Modified initialization
}

func (r *folderRepository) GetKey(uid int64) string {
	return r.customPrefixKey + strconv.FormatInt(uid, 10)
}

func init() {
	RegisterModel(ModelConfig{
		Name: "Folder",
		RepoFactory: func(d *Dao) daoDBCustomKey {
			return NewFolderRepository(d).(daoDBCustomKey)
		},
	})
}

// folder returns the query with auto-migration
func (r *folderRepository) folder(uid int64) *query.Query {
	return r.Dao.QueryWithOnceInit(func(g *gorm.DB) error {
		return model.AutoMigrate(g, "Folder")
	}, r.GetKey(uid)+"#folder", r.GetKey(uid))
}

func (r *folderRepository) GetByID(ctx context.Context, id, uid int64) (*domain.Folder, error) {
	f := r.folder(uid).Folder
	m, err := f.WithContext(ctx).Where(f.ID.Eq(id)).First()
	if err != nil {
		return nil, err
	}
	return r.modelToDomain(m), nil
}

func (r *folderRepository) GetByPathHash(ctx context.Context, pathHash string, vaultID, uid int64) (*domain.Folder, error) {
	f := r.folder(uid).Folder
	if err := requireUnambiguousPath(f.WithContext(ctx).Where(f.VaultID.Eq(vaultID), f.PathHash.Eq(pathHash)).UnderlyingDB()); err != nil {
		return nil, err
	}
	m, err := f.WithContext(ctx).Where(f.VaultID.Eq(vaultID), f.PathHash.Eq(pathHash)).First()
	if err != nil {
		return nil, err
	}
	return r.modelToDomain(m), nil
}

func (r *folderRepository) GetAllByPathHash(ctx context.Context, pathHash string, vaultID, uid int64) ([]*domain.Folder, error) {
	f := r.folder(uid).Folder
	ms, err := f.WithContext(ctx).Where(f.VaultID.Eq(vaultID), f.PathHash.Eq(pathHash), f.Action.Neq("delete")).Find()
	if err != nil {
		return nil, err
	}
	var res []*domain.Folder
	for _, m := range ms {
		res = append(res, r.modelToDomain(m))
	}
	return res, nil
}

func (r *folderRepository) GetByFID(ctx context.Context, fid int64, vaultID, uid int64) ([]*domain.Folder, error) {
	var ms []*model.Folder
	f := r.folder(uid).Folder
	ms, err := f.WithContext(ctx).Where(f.VaultID.Eq(vaultID), f.FID.Eq(fid), f.Action.Neq("delete")).Find()
	if err != nil {
		return nil, err
	}
	var res []*domain.Folder
	for _, m := range ms {
		res = append(res, r.modelToDomain(m))
	}
	return res, nil
}

func (r *folderRepository) Create(ctx context.Context, folder *domain.Folder, uid int64) (*domain.Folder, error) {
	var result *domain.Folder
	err := r.Dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		m := r.domainToModel(folder)
		m.CreatedAt = timex.Now()
		m.UpdatedAt = timex.Now()
		m.UpdatedTimestamp = timex.Now().UnixMilli()
		// 新建目录是新的服务端状态，必须生成新令牌。
		// A new folder is a new server state and must mint a token.
		m.Revision = util.NewRevisionToken()
		f := r.folder(uid).Folder
		err := f.WithContext(ctx).Create(m)
		if err != nil {
			return err
		}
		result = r.modelToDomain(m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *folderRepository) Update(ctx context.Context, folder *domain.Folder, uid int64) (*domain.Folder, error) {
	var result *domain.Folder
	err := r.Dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		m := r.domainToModel(folder)
		m.UpdatedAt = timex.Now()
		m.UpdatedTimestamp = timex.Now().UnixMilli()
		// 目录删除、恢复、重命名一律换新令牌。
		// Folder delete, restore and rename always mint a new token.
		m.Revision = util.NewRevisionToken()
		f := r.folder(uid).Folder
		// 显式 Select 全部可写列：目录移回根目录时 FID=0、Level=0，GORM 的 Updates(struct)
		// 会跳过零值，导致父级与层级停留在旧值。目录是完整状态覆盖，必须逐列写入。
		// Select every writable column explicitly: moving a folder back to the root
		// (FID=0, Level=0) would otherwise be skipped as zero values by Updates(struct),
		// leaving the old parent and level behind.
		_, err := f.WithContext(ctx).Where(f.ID.Eq(m.ID)).Select(
			f.ID,
			f.VaultID,
			f.Action,
			f.Path,
			f.PathHash,
			f.Level,
			f.FID,
			f.Ctime,
			f.Mtime,
			f.UpdatedTimestamp,
			f.UpdatedAt,
			f.Revision,
		).Updates(m)
		if err != nil {
			return err
		}
		result = r.modelToDomain(m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *folderRepository) Delete(ctx context.Context, id, uid int64) error {
	return r.Dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		f := r.folder(uid).Folder
		_, err := f.WithContext(ctx).Where(f.ID.Eq(id)).Delete()
		return err
	})
}

func (r *folderRepository) ListByUpdatedTimestamp(ctx context.Context, timestamp, vaultID, uid int64) ([]*domain.Folder, error) {
	var ms []*model.Folder
	f := r.folder(uid).Folder
	ms, err := f.WithContext(ctx).Where(f.VaultID.Eq(vaultID), f.UpdatedTimestamp.Gt(timestamp)).Find()
	if err != nil {
		return nil, err
	}
	var res []*domain.Folder
	for _, m := range ms {
		res = append(res, r.modelToDomain(m))
	}
	return res, nil
}

func (r *folderRepository) ListByPathPrefix(ctx context.Context, pathPrefix string, vaultID, uid int64) ([]*domain.Folder, error) {
	var ms []*model.Folder
	f := r.folder(uid).Folder
	// Use LIKE 'prefix/%' to find all subdirectories
	// 使用 LIKE 'prefix/%' 来查找所有子目录
	pattern := pathPrefix + "/%"
	ms, err := f.WithContext(ctx).Where(f.VaultID.Eq(vaultID), f.Path.Like(pattern), f.Action.Neq("delete")).Find()
	if err != nil {
		return nil, err
	}
	var res []*domain.Folder
	for _, m := range ms {
		if !isPathWithinPrefix(m.Path, pathPrefix) {
			continue
		}
		res = append(res, r.modelToDomain(m))
	}
	return res, nil
}

// ListByPathsMeta 按完整路径批量获取目录元数据（含墓碑，不区分状态）。
// 用于增量同步窗口外的路径补查："不在增量结果里"绝不等于"不存在"。
// ListByPathsMeta batch-loads folder metadata (tombstones included) for exact paths,
// so a path outside the incremental window is never mistaken for a missing one.
func (r *folderRepository) ListByPathsMeta(ctx context.Context, paths []string, vaultID, uid int64) (map[string]*domain.Folder, error) {
	result := make(map[string]*domain.Folder, len(paths))
	if len(paths) == 0 {
		return result, nil
	}

	u := r.folder(uid).Folder
	const chunkSize = 500
	for start := 0; start < len(paths); start += chunkSize {
		end := start + chunkSize
		if end > len(paths) {
			end = len(paths)
		}
		ms, err := u.WithContext(ctx).Where(
			u.VaultID.Eq(vaultID),
			u.Path.In(paths[start:end]...),
		).Find()
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			f := r.modelToDomain(m)
			if existing, ok := result[f.Path]; !ok || f.UpdatedTimestamp > existing.UpdatedTimestamp {
				result[f.Path] = f
			}
		}
	}
	return result, nil
}

func (r *folderRepository) modelToDomain(m *model.Folder) *domain.Folder {
	if m == nil {
		return nil
	}
	return &domain.Folder{
		ID:               m.ID,
		VaultID:          m.VaultID,
		Action:           domain.FolderAction(m.Action),
		Path:             m.Path,
		PathHash:         m.PathHash,
		Level:            m.Level,
		FID:              m.FID,
		Ctime:            m.Ctime,
		Mtime:            m.Mtime,
		UpdatedTimestamp: m.UpdatedTimestamp,
		Revision:         m.Revision,
		CreatedAt:        time.Time(m.CreatedAt),
		UpdatedAt:        time.Time(m.UpdatedAt),
	}
}

func (r *folderRepository) domainToModel(d *domain.Folder) *model.Folder {
	if d == nil {
		return nil
	}
	return &model.Folder{
		ID:               d.ID,
		VaultID:          d.VaultID,
		Action:           string(d.Action),
		Path:             d.Path,
		PathHash:         d.PathHash,
		Level:            d.Level,
		FID:              d.FID,
		Ctime:            d.Ctime,
		Mtime:            d.Mtime,
		UpdatedTimestamp: d.UpdatedTimestamp,
		Revision:         d.Revision,
		CreatedAt:        timex.Time(d.CreatedAt),
		UpdatedAt:        timex.Time(d.UpdatedAt),
	}
}
func (r *folderRepository) List(ctx context.Context, vaultID int64, uid int64) ([]*domain.Folder, error) {
	u := r.folder(uid).Folder
	ms, err := u.WithContext(ctx).Where(u.VaultID.Eq(vaultID)).Find()
	if err != nil {
		return nil, err
	}
	var res []*domain.Folder
	for _, m := range ms {
		res = append(res, r.modelToDomain(m))
	}
	return res, nil
}

func (r *folderRepository) ListAll(ctx context.Context, uid int64) ([]*domain.Folder, error) {
	u := r.folder(uid).Folder
	ms, err := u.WithContext(ctx).Find()
	if err != nil {
		return nil, err
	}
	var res []*domain.Folder
	for _, m := range ms {
		res = append(res, r.modelToDomain(m))
	}
	return res, nil
}

// DeleteByVaultID removes all folder records for a specific vault
// DeleteByVaultID 删除指定仓库的所有文件夹记录
func (r *folderRepository) DeleteByVaultID(ctx context.Context, vaultID, uid int64) error {
	return r.Dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		f := r.folder(uid).Folder
		_, err := f.WithContext(ctx).Where(f.VaultID.Eq(vaultID)).Delete()
		return err
	})
}

// Ensure folderRepository implements domain.FolderRepository interface
// 确保 folderRepository 实现了 domain.FolderRepository 接口
var _ domain.FolderRepository = (*folderRepository)(nil)

// GetByPathHashAndPath restricts the hash lookup to the requested path.
// GetByPathHashAndPath 将哈希查询限定到请求路径，保留原有状态与墓碑语义。
func (r *folderRepository) GetByPathHashAndPath(ctx context.Context, pathHash, path string, vaultID, uid int64) (*domain.Folder, error) {
	if path == "" {
		return r.GetByPathHash(ctx, pathHash, vaultID, uid)
	}
	f := r.folder(uid).Folder
	m, err := f.WithContext(ctx).Where(f.VaultID.Eq(vaultID), f.PathHash.Eq(pathHash), f.Path.Eq(path)).First()
	if err != nil {
		return nil, err
	}
	return r.modelToDomain(m), nil
}

// GetAllByPathHashAndPath restricts the hash lookup to the requested path.
// GetAllByPathHashAndPath 将哈希查询限定到请求路径，保留原有状态与墓碑语义。
func (r *folderRepository) GetAllByPathHashAndPath(ctx context.Context, pathHash, path string, vaultID, uid int64) ([]*domain.Folder, error) {
	f := r.folder(uid).Folder
	ms, err := f.WithContext(ctx).Where(f.VaultID.Eq(vaultID), f.PathHash.Eq(pathHash), f.Path.Eq(path), f.Action.Neq("delete")).Find()
	if err != nil {
		return nil, err
	}
	var res []*domain.Folder
	for _, m := range ms {
		res = append(res, r.modelToDomain(m))
	}
	return res, nil
}

// UpdateIfRevision applies a folder update only when the row still carries
// expectedRevision. It returns false when another device changed the row.
// UpdateIfRevision 仅在行仍携带 expectedRevision 时应用目录更新；行已被改动时返回 false。
func (r *folderRepository) UpdateIfRevision(ctx context.Context, folder *domain.Folder, expectedRevision string, uid int64) (bool, error) {
	updated := false
	err := r.Dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		m := r.domainToModel(folder)
		m.UpdatedAt = timex.Now()
		m.UpdatedTimestamp = timex.Now().UnixMilli()
		m.Revision = util.NewRevisionToken()
		f := r.folder(uid).Folder
		// 同 Update：显式 Select 避免 FID/Level 的零值被跳过。
		// Same as Update: explicit Select so FID/Level zero values are written.
		res, err := f.WithContext(ctx).Where(
			f.ID.Eq(m.ID),
			f.Revision.Eq(expectedRevision),
		).Select(
			f.ID,
			f.VaultID,
			f.Action,
			f.Path,
			f.PathHash,
			f.Level,
			f.FID,
			f.Ctime,
			f.Mtime,
			f.UpdatedTimestamp,
			f.UpdatedAt,
			f.Revision,
		).Updates(m)
		if err != nil {
			return err
		}
		if res.RowsAffected == 0 {
			return nil
		}
		updated = true
		folder.Revision = m.Revision
		folder.UpdatedTimestamp = m.UpdatedTimestamp
		return nil
	})
	if err != nil {
		return false, err
	}
	return updated, nil
}
