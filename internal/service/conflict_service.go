// Package service implements the business logic layer
// Package service 实现业务逻辑层
package service

import (
	"context"
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/dao"
	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/logger"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"go.uber.org/zap"
)

// ConflictService defines the conflict service interface
// ConflictService 定义冲突文件服务接口
type ConflictService interface {
	// CreateConflictFile creates a conflict file
	// CreateConflictFile 创建冲突文件
	// When merging fails, save the client content as a conflict file
	// 当合并失败时，将客户端内容保存为冲突文件
	CreateConflictFile(ctx context.Context, uid int64, params *dto.ConflictFileRequest) (*dto.ConflictFileResponse, error)
}

// conflictService implements ConflictService interface
// conflictService 实现 ConflictService 接口
type conflictService struct {
	noteRepo     domain.NoteRepository
	noteTagRepo  domain.NoteTagRepository
	vaultService VaultService
	logger       *zap.Logger
	clientName   string
}

// NewConflictService creates a ConflictService instance
// NewConflictService 创建 ConflictService 实例
func NewConflictService(noteRepo domain.NoteRepository, noteTagRepo domain.NoteTagRepository, vaultSvc VaultService, logger *zap.Logger) ConflictService {
	return &conflictService{
		noteRepo:     noteRepo,
		noteTagRepo:  noteTagRepo,
		vaultService: vaultSvc,
		logger:       logger,
		clientName:   "conflict-service",
	}
}

// CreateConflictFile creates a conflict file
// CreateConflictFile 创建冲突文件
func (s *conflictService) CreateConflictFile(ctx context.Context, uid int64, params *dto.ConflictFileRequest) (*dto.ConflictFileResponse, error) {
	// Get VaultID
	// 获取 VaultID
	vaultID, err := s.vaultService.MustGetID(ctx, uid, params.Vault)
	if err != nil {
		return nil, err
	}

	// Generate conflict file path
	// 生成冲突文件路径
	conflictPath := s.generateConflictPath(params.OriginalPath)

	// Generate conflict path hash
	// 生成冲突路径哈希
	conflictPathHash := util.EncodeHash32(conflictPath)

	// Create conflict file note
	// 创建冲突文件笔记
	conflictNote := &domain.Note{
		VaultID:     vaultID,
		Path:        conflictPath,
		PathHash:    conflictPathHash,
		Content:     params.ClientContent,
		ContentHash: params.ClientContentHash,
		ClientName:  s.clientName,
		Size:        int64(len(params.ClientContent)),
		Mtime:       params.Mtime,
		Ctime:       params.Ctime,
		Action:      domain.NoteActionCreate,
	}

	// Save conflict file
	// 保存冲突文件
	created, err := s.noteRepo.Create(ctx, conflictNote, uid)
	if err != nil {
		s.logger.Error("failed to create conflict file",
			zap.String("originalPath", params.OriginalPath),
			zap.String("conflictPath", conflictPath),
			zap.Int64(logger.FieldUID, uid),
			zap.Error(err))
		return nil, code.ErrorDBQuery.WithDetails(err.Error())
	}

	// 冲突副本与普通新建笔记一样写入标签索引；失败只记日志，不影响冲突副本本身
	// Index the conflict copy's tags like any newly created note; failures are logged only
	if s.noteTagRepo != nil {
		tags, source := dao.ExtractTags(params.ClientContent)
		if err := s.noteTagRepo.ReplaceForNote(ctx, created.ID, tags, vaultID, uid, source); err != nil {
			s.logger.Warn("conflict file tag index failed",
				zap.String("conflictPath", conflictPath),
				zap.Int64(logger.FieldUID, uid),
				zap.Int64("noteId", created.ID),
				zap.Error(err))
		}
	}

	s.logger.Info("conflict file created",
		zap.String("originalPath", params.OriginalPath),
		zap.String("conflictPath", conflictPath),
		zap.Int64(logger.FieldUID, uid),
		zap.Int64("noteId", created.ID))

	return &dto.ConflictFileResponse{
		ConflictPath: conflictPath,
		Message:      "合并失败，已保存冲突版本",
		NoteID:       created.ID,
	}, nil
}

// generateConflictPath generates conflict file path
// Format: {baseName}.conflict.{timestamp}{ext}
// Example: notes/test.md -> notes/test.conflict.20060102150405.md
// generateConflictPath 生成冲突文件路径
// 格式: {baseName}.conflict.{timestamp}{ext}
// 例如: notes/test.md -> notes/test.conflict.20060102150405.md
func (s *conflictService) generateConflictPath(originalPath string) string {
	return util.ConflictPath(originalPath, time.Now())
}

// 确保 conflictService 实现了 ConflictService 接口
var _ ConflictService = (*conflictService)(nil)
