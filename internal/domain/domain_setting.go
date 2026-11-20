// Package domain 定义领域模型和接口
package domain

import (
	"context"
	"time"
)

// SettingAction 定义配置操作类型
type SettingAction string

const (
	SettingActionCreate SettingAction = "create"
	SettingActionModify SettingAction = "modify"
	SettingActionDelete SettingAction = "delete"
)

// Setting 配置领域模型
type Setting struct {
	ID               int64
	VaultID          int64
	Action           SettingAction
	Path             string
	PathHash         string
	Content          string
	ContentHash      string
	Size             int64
	Rename           int64
	ContentPurged    int64
	Ctime            int64
	Mtime            int64
	UpdatedTimestamp int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// IsDeleted 判断配置是否已删除
func (s *Setting) IsDeleted() bool {
	return s.Action == SettingActionDelete
}

// IsCreated 判断配置是否为新建
func (s *Setting) IsCreated() bool {
	return s.Action == SettingActionCreate
}

// IsModified 判断配置是否已修改
func (s *Setting) IsModified() bool {
	return s.Action == SettingActionModify
}

// SettingRepository 配置仓储接口
type SettingRepository interface {
	// GetByPathHash 根据路径哈希获取配置
	GetByPathHash(ctx context.Context, pathHash string, vaultID, uid int64) (*Setting, error)

	// ListByPathHash 根据路径哈希获取配置列表（处理重复记录）
	ListByPathHash(ctx context.Context, pathHash string, vaultID, uid int64) ([]*Setting, error)

	// ListByPathHashesMeta 以路径哈希为键批量获取元数据（含所有状态），同一哈希多行时
	// 保留 updated_timestamp 最新的一条；只查元数据列、不读正文文件，分块查询，
	// 用于增量同步窗口外条目的比对。
	// ListByPathHashesMeta batch-queries metadata keyed by path hash (all statuses
	// included); for duplicate rows of one hash the one with the latest
	// updated_timestamp wins. Only metadata columns are selected — content files are
	// not read. Chunked; used to diff out-of-window entries in incremental sync.
	ListByPathHashesMeta(ctx context.Context, pathHashes []string, vaultID, uid int64) (map[string]*Setting, error)

	// Create 创建配置
	Create(ctx context.Context, setting *Setting, uid int64) (*Setting, error)

	// Update 更新配置
	Update(ctx context.Context, setting *Setting, uid int64) (*Setting, error)

	// UpdateMtime 更新配置修改时间
	UpdateMtime(ctx context.Context, mtime int64, id, uid int64) error

	// UpdateActionMtime 更新配置类型并修改时间
	UpdateActionMtime(ctx context.Context, action SettingAction, mtime int64, id, uid int64) error

	// Delete 物理删除配置
	Delete(ctx context.Context, id, uid int64) error

	// PurgeContentByTime 释放早于给定时间的已删除配置正文，保留轻量墓碑行
	// （action=delete、content_purged=1、rename=2），且不刷新 updated_timestamp。
	// PurgeContentByTime releases stored bodies of expired tombstones while keeping a
	// lightweight metadata row (action=delete, content_purged=1, rename=2) and leaving
	// updated_timestamp untouched.
	PurgeContentByTime(ctx context.Context, timestamp, uid int64) error

	// PurgeContentByTimeAll 对所有用户执行 PurgeContentByTime
	PurgeContentByTimeAll(ctx context.Context, timestamp int64) error

	// List 分页获取配置列表
	List(ctx context.Context, vaultID int64, page, pageSize int, uid int64, keyword string) ([]*Setting, error)

	// ListCount 获取配置数量
	ListCount(ctx context.Context, vaultID, uid int64, keyword string) (int64, error)

	// ListByUpdatedTimestamp 根据更新时间戳获取配置列表
	ListByUpdatedTimestamp(ctx context.Context, timestamp, vaultID, uid int64) ([]*Setting, error)

	// DeleteByVaultID 物理删除该用户指定笔记本的所有配置（仅在整个仓库被销毁时使用）
	DeleteByVaultID(ctx context.Context, vaultID, uid int64) error

	// TombstoneByVaultID 将某仓库的存活配置转为删除墓碑（action=delete、rename=2、
	// content_purged=1、更新 updated_timestamp），使其它客户端增量同步时仍能收到删除，
	// 而不是像 DeleteByVaultID 那样直接物理删行、丢失全部删除证据。
	// TombstoneByVaultID converts live settings of a vault into deletion tombstones
	// (action=delete, rename=2, content_purged=1, refreshed updated_timestamp), so other
	// clients still receive the deletions through incremental sync instead of losing all
	// deletion evidence as with DeleteByVaultID.
	TombstoneByVaultID(ctx context.Context, vaultID, uid int64) error
}
