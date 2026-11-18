package dao

import (
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"gorm.io/gorm"
)

// requireUnambiguousPath guards legacy hash-only lookups before selecting a row.
// requireUnambiguousPath 在旧的纯哈希查询取行前拒绝跨路径歧义。
func requireUnambiguousPath(db *gorm.DB) error {
	var paths []string
	if err := db.Session(&gorm.Session{}).Distinct("path").Limit(2).Pluck("path", &paths).Error; err != nil {
		return err
	}
	if len(paths) > 1 {
		return domain.ErrAmbiguousPathHash
	}
	return nil
}
