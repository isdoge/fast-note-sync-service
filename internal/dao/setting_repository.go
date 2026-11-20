// Package dao implements the data access layer
// Package dao 实现数据访问层
package dao

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/model"
	"github.com/haierkeys/fast-note-sync-service/internal/query"
	"github.com/haierkeys/fast-note-sync-service/pkg/logger"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// settingRepository implements domain.SettingRepository interface
// settingRepository 实现 domain.SettingRepository 接口
type settingRepository struct {
	dao             *Dao
	customPrefixKey string
}

// NewSettingRepository creates SettingRepository instance
// NewSettingRepository 创建 SettingRepository 实例
func NewSettingRepository(dao *Dao) domain.SettingRepository {
	return &settingRepository{dao: dao, customPrefixKey: "user_setting_"}
}

func (r *settingRepository) GetKey(uid int64) string {
	return r.customPrefixKey + strconv.FormatInt(uid, 10)
}

func init() {
	RegisterModel(ModelConfig{
		Name: "Setting",
		RepoFactory: func(d *Dao) daoDBCustomKey {
			return NewSettingRepository(d).(daoDBCustomKey)
		},
	})
}

// setting gets the setting query object
// setting 获取配置查询对象
func (r *settingRepository) setting(uid int64) *query.Query {
	key := r.GetKey(uid)
	return r.dao.QueryWithOnceInit(func(g *gorm.DB) error {
		return model.AutoMigrate(g, "Setting")
	}, key+"#setting", key)
}

// toDomain converts database model to domain model
// toDomain 将数据库模型转换为领域模型
func (r *settingRepository) toDomain(m *model.Setting, uid int64) (*domain.Setting, error) {
	if m == nil {
		return nil, nil
	}
	s := &domain.Setting{
		ID:               m.ID,
		VaultID:          m.VaultID,
		Action:           domain.SettingAction(m.Action),
		Path:             m.Path,
		PathHash:         m.PathHash,
		Content:          m.Content,
		ContentHash:      m.ContentHash,
		Size:             m.Size,
		Rename:           m.Rename,
		Ctime:            m.Ctime,
		Mtime:            m.Mtime,
		UpdatedTimestamp: m.UpdatedTimestamp,
		CreatedAt:        time.Time(m.CreatedAt),
		UpdatedAt:        time.Time(m.UpdatedAt),
	}
	if err := r.fillSettingContent(uid, s); err != nil {
		return nil, err
	}
	return s, nil
}

// fillSettingContent fills setting content
// fillSettingContent 填充配置内容
func (r *settingRepository) fillSettingContent(uid int64, s *domain.Setting) error {
	if s == nil {
		return nil
	}
	folder := r.dao.GetSettingFolderPath(uid, s.ID)

	content, exists, err := r.dao.LoadContentFromFile(folder, "content.txt")
	if err != nil {
		return err
	}

	if exists {
		s.Content = content
	} else if s.Content != "" {
		if err := r.dao.SaveContentToFile(folder, "content.txt", s.Content); err != nil {
			r.dao.Logger().Warn("lazy migration: SaveContentToFile failed for setting content",
				zap.Int64(logger.FieldUID, uid),
				zap.Int64("settingId", s.ID),
				zap.String(logger.FieldMethod, "settingRepository.fillSettingContent"),
				zap.Error(err),
			)
		}
	} else {
		return fmt.Errorf("setting content file not found: %w", os.ErrNotExist)
	}
	return nil
}

// GetByPathHash retrieves setting by path hash
// GetByPathHash 根据路径哈希获取配置
func (r *settingRepository) GetByPathHash(ctx context.Context, pathHash string, vaultID, uid int64) (*domain.Setting, error) {
	u := r.setting(uid).Setting
	m, err := u.WithContext(ctx).Where(
		u.VaultID.Eq(vaultID),
		u.PathHash.Eq(pathHash),
	).First()
	if err != nil {
		return nil, err
	}
	return r.toDomain(m, uid)
}

// ListByPathHash retrieves setting list by path hash (handles duplicate records)
// ListByPathHash 根据路径哈希获取配置列表（处理重复记录）
func (r *settingRepository) ListByPathHash(ctx context.Context, pathHash string, vaultID, uid int64) ([]*domain.Setting, error) {
	u := r.setting(uid).Setting
	mList, err := u.WithContext(ctx).Where(
		u.VaultID.Eq(vaultID),
		u.PathHash.Eq(pathHash),
	).Find()
	if err != nil {
		return nil, err
	}
	var results []*domain.Setting
	for _, m := range mList {
		s, err := r.toDomain(m, uid)
		if err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, nil
}

// ListByPathHashesMeta batch-queries setting metadata keyed by path hash (all statuses
// included). For duplicate rows of one hash the one with the latest updated_timestamp wins.
// Only metadata columns are selected — content files are not read (fillSettingContent is
// skipped). Chunked (500 per IN query) to stay below SQLite's parameter limit; used to
// diff out-of-window entries in incremental sync.
// ListByPathHashesMeta 以路径哈希为键批量获取配置元数据（含所有状态），同一哈希多行时
// 保留 updated_timestamp 最新的一条。只查元数据列、不读正文文件（跳过 fillSettingContent）。
// 每批 500 条分块查询以避开 SQLite 参数上限；用于增量同步窗口外条目的比对。
func (r *settingRepository) ListByPathHashesMeta(ctx context.Context, pathHashes []string, vaultID, uid int64) (map[string]*domain.Setting, error) {
	result := make(map[string]*domain.Setting, len(pathHashes))
	if len(pathHashes) == 0 {
		return result, nil
	}

	u := r.setting(uid).Setting
	const chunkSize = 500
	for start := 0; start < len(pathHashes); start += chunkSize {
		end := start + chunkSize
		if end > len(pathHashes) {
			end = len(pathHashes)
		}
		ms, err := u.WithContext(ctx).Where(
			u.VaultID.Eq(vaultID),
			u.PathHash.In(pathHashes[start:end]...),
		).Order(u.UpdatedTimestamp.Desc()).Find()
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			// 同一哈希的历史重复行保留 UpdatedTimestamp 最新的一条。
			// Keep the latest metadata row for each hash.
			if _, ok := result[m.PathHash]; !ok {
				result[m.PathHash] = &domain.Setting{
					ID:               m.ID,
					VaultID:          m.VaultID,
					Action:           domain.SettingAction(m.Action),
					Path:             m.Path,
					PathHash:         m.PathHash,
					ContentHash:      m.ContentHash,
					Size:             m.Size,
					Rename:           m.Rename,
					Ctime:            m.Ctime,
					Mtime:            m.Mtime,
					UpdatedTimestamp: m.UpdatedTimestamp,
					CreatedAt:        time.Time(m.CreatedAt),
					UpdatedAt:        time.Time(m.UpdatedAt),
				}
			}
		}
	}
	return result, nil
}

// Create creates a setting
// Create 创建配置
func (r *settingRepository) Create(ctx context.Context, setting *domain.Setting, uid int64) (*domain.Setting, error) {
	var result *domain.Setting
	var createErr error

	err := r.dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		u := r.setting(uid).Setting
		m := &model.Setting{
			VaultID:          setting.VaultID,
			Action:           string(setting.Action),
			Path:             setting.Path,
			PathHash:         setting.PathHash,
			ContentHash:      setting.ContentHash,
			Size:             setting.Size,
			Rename:           setting.Rename,
			Ctime:            setting.Ctime,
			Mtime:            setting.Mtime,
			UpdatedTimestamp: timex.Now().UnixMilli(),
			CreatedAt:        timex.Now(),
			UpdatedAt:        timex.Now(),
		}

		content := setting.Content
		m.Content = "" // Do not store content in database // 不在数据库存储内容

		createErr = u.WithContext(ctx).Create(m)
		if createErr != nil {
			return createErr
		}

		// 保存到文件存储
		folderPath := r.dao.GetSettingFolderPath(uid, m.ID)
		if err := r.dao.SaveContentToFile(folderPath, "content.txt", content); err != nil {
			return err
		}

		sRes, err := r.toDomain(m, uid)
		if err != nil {
			return err
		}
		result = sRes

		result.Content = content
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// Update updates the setting
// Update 更新配置
func (r *settingRepository) Update(ctx context.Context, setting *domain.Setting, uid int64) (*domain.Setting, error) {
	var result *domain.Setting
	var updateErr error

	err := r.dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		u := r.setting(uid).Setting
		m := &model.Setting{
			ID:               setting.ID,
			VaultID:          setting.VaultID,
			Action:           string(setting.Action),
			Path:             setting.Path,
			PathHash:         setting.PathHash,
			ContentHash:      setting.ContentHash,
			Size:             setting.Size,
			Rename:           setting.Rename,
			Ctime:            setting.Ctime,
			Mtime:            setting.Mtime,
			UpdatedTimestamp: timex.Now().UnixMilli(),
			UpdatedAt:        timex.Now(),
		}

		content := setting.Content
		m.Content = "" // Do not update content in database // 不在数据库更新内容

		updateErr = u.WithContext(ctx).Where(u.ID.Eq(setting.ID)).Save(m)
		if updateErr != nil {
			return updateErr
		}

		// 保存到文件存储
		folderPath := r.dao.GetSettingFolderPath(uid, setting.ID)
		if err := r.dao.SaveContentToFile(folderPath, "content.txt", content); err != nil {
			return err
		}

		sRes, err := r.toDomain(m, uid)
		if err != nil {
			return err
		}
		result = sRes

		result.Content = content
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateMtime updates the setting modification time
// UpdateMtime 更新配置修改时间
func (r *settingRepository) UpdateMtime(ctx context.Context, mtime int64, id, uid int64) error {
	return r.dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		u := r.setting(uid).Setting
		_, err := u.WithContext(ctx).Where(u.ID.Eq(id)).UpdateSimple(
			u.Mtime.Value(mtime),
			u.UpdatedTimestamp.Value(timex.Now().UnixMilli()),
			u.UpdatedAt.Value(timex.Now()),
		)
		return err
	})
}

// UpdateActionMtime updates setting type and modification time
// UpdateActionMtime 更新配置类型并修改时间
func (r *settingRepository) UpdateActionMtime(ctx context.Context, action domain.SettingAction, mtime int64, id, uid int64) error {
	return r.dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		u := r.setting(uid).Setting

		_, err := u.WithContext(ctx).Where(
			u.ID.Eq(id),
		).UpdateSimple(
			u.Action.Value(string(action)),
			u.Mtime.Value(mtime),
			u.UpdatedTimestamp.Value(timex.Now().UnixMilli()),
			u.UpdatedAt.Value(timex.Now()),
		)
		return err
	})
}

// Delete physically deletes the setting
// Delete 物理删除配置
func (r *settingRepository) Delete(ctx context.Context, id, uid int64) error {
	return r.dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		u := r.setting(uid).Setting
		_, err := u.WithContext(ctx).Where(u.ID.Eq(id)).Delete()
		if err != nil {
			return err
		}

		// Delete physical files
		// 删除物理文件
		folder := r.dao.GetSettingFolderPath(uid, id)
		_ = r.dao.RemoveContentFolder(folder)

		return nil
	})
}

// DeletePhysicalByTime physically deletes settings marked for deletion based on time
// DeletePhysicalByTime 根据时间物理删除已标记删除的配置
func (r *settingRepository) DeletePhysicalByTime(ctx context.Context, timestamp, uid int64) error {
	return r.dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		u := r.setting(uid).Setting

		// Find records to be physically deleted, clean up files
		// 查找待物理删除的记录，清理文件
		mList, err := u.WithContext(ctx).Where(
			u.Action.Eq("delete"),
			u.UpdatedTimestamp.Lt(timestamp),
		).Find()

		if err == nil {
			for _, m := range mList {
				folder := r.dao.GetSettingFolderPath(uid, m.ID)
				_ = r.dao.RemoveContentFolder(folder)
			}
		}

		_, err = u.WithContext(ctx).Where(
			u.Action.Eq("delete"),
			u.UpdatedTimestamp.Lt(timestamp),
		).Delete()

		return err
	})
}

// DeletePhysicalByTimeAll physically deletes settings marked for deletion for all users based on time
// DeletePhysicalByTimeAll 根据时间物理删除所有用户的已标记删除的配置
func (r *settingRepository) DeletePhysicalByTimeAll(ctx context.Context, timestamp int64) error {
	uids, err := r.dao.GetAllUserUIDs()
	if err != nil {
		return err
	}

	for _, uid := range uids {
		if err := r.DeletePhysicalByTime(ctx, timestamp, uid); err != nil {
			continue
		}
	}
	return nil
}

// List gets the setting list with pagination
// List 分页获取配置列表
func (r *settingRepository) List(ctx context.Context, vaultID int64, page, pageSize int, uid int64, keyword string) ([]*domain.Setting, error) {
	u := r.setting(uid).Setting
	db := u.WithContext(ctx).Where(u.VaultID.Eq(vaultID), u.Action.Neq("delete"))
	if keyword != "" {
		db = db.Where(u.Path.Like("%" + keyword + "%"))
	}

	offset := (page - 1) * pageSize
	mList, err := db.Order(u.UpdatedAt.Desc()).Offset(offset).Limit(pageSize).Find()
	if err != nil {
		return nil, err
	}

	var results []*domain.Setting
	for _, m := range mList {
		s, err := r.toDomain(m, uid)
		if err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, nil
}

// ListCount gets the setting count
// ListCount 获取配置数量
func (r *settingRepository) ListCount(ctx context.Context, vaultID, uid int64, keyword string) (int64, error) {
	u := r.setting(uid).Setting
	db := u.WithContext(ctx).Where(u.VaultID.Eq(vaultID), u.Action.Neq("delete"))
	if keyword != "" {
		db = db.Where(u.Path.Like("%" + keyword + "%"))
	}
	return db.Count()
}

// ListByUpdatedTimestamp gets the setting list based on updated timestamp
// ListByUpdatedTimestamp 根据更新时间戳获取配置列表
func (r *settingRepository) ListByUpdatedTimestamp(ctx context.Context, timestamp, vaultID, uid int64) ([]*domain.Setting, error) {
	u := r.setting(uid).Setting
	mList, err := u.WithContext(ctx).Where(
		u.VaultID.Eq(vaultID),
		u.UpdatedTimestamp.Gt(timestamp),
	).Order(u.UpdatedTimestamp.Desc()).Find()

	if err != nil {
		return nil, err
	}

	var results []*domain.Setting
	for _, m := range mList {
		s, err := r.toDomain(m, uid)
		if err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, nil
}

// DeleteByVaultID physically deletes all settings of the specified notebook for this user
// DeleteByVaultID 物理删除该用户指定笔记本的所有配置
func (r *settingRepository) DeleteByVaultID(ctx context.Context, vaultID, uid int64) error {
	return r.dao.ExecuteWrite(ctx, uid, r, func(db *gorm.DB) error {
		u := r.setting(uid).Setting

		// 1. Find all record IDs under the Vault to clean up files
		// 1. 查找该 Vault 下的所有记录 ID，以便清理文件
		mList, err := u.WithContext(ctx).Where(u.VaultID.Eq(vaultID)).Select(u.ID).Find()
		if err == nil {
			for _, m := range mList {
				folder := r.dao.GetSettingFolderPath(uid, m.ID)
				_ = r.dao.RemoveContentFolder(folder)
			}
		}

		// 2. Physically delete database records
		// 2. 物理删除数据库记录
		_, err = u.WithContext(ctx).Where(u.VaultID.Eq(vaultID)).Delete()
		return err
	})
}

// Ensure settingRepository implements domain.SettingRepository interface
// 确保 settingRepository 实现了 domain.SettingRepository 接口
var _ domain.SettingRepository = (*settingRepository)(nil)
