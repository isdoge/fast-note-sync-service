package api_router

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
)

// baselineConflictOf reports whether err is a rejected conditional commit.
// baselineConflictOf 判断 err 是否为被拒绝的条件提交。
func baselineConflictOf(err error) (*dto.BaselineConflictError, bool) {
	var conflict *dto.BaselineConflictError
	if errors.As(err, &conflict) {
		return conflict, true
	}
	return nil, false
}

// respondBaselineConflict writes a rejected REST conditional write as the existing
// 530 (ErrorSyncConflict) envelope with a structured conflict body. It performs no
// write and broadcasts nothing; callers must return immediately afterwards.
//
// The live note body is attached for note conflicts so a REST client can render the
// same conflict UI as the realtime path; attachment bytes are never inlined.
// respondBaselineConflict 把被拒绝的 REST 条件写按现有 530（ErrorSyncConflict）信封
// 返回，并附带结构化冲突体。它不写库、不广播；调用方返回后必须立即结束。
// 笔记冲突会附上活跃正文，使 REST 客户端能复用实时路径的冲突界面；附件从不内联二进制。
func (h *Handler) respondBaselineConflict(c *gin.Context, uid int64, kind, vault, path, pathHash, baseHash string, conflict *dto.BaselineConflictError) {
	response := pkgapp.NewResponse(c)
	if conflict == nil {
		conflict = &dto.BaselineConflictError{Reason: dto.SyncConflictReasonRevisionChanged}
	}
	payload := &dto.BaselineConflictResponse{
		Conflict:       true,
		Kind:           kind,
		Path:           path,
		PathHash:       pathHash,
		Reason:         conflict.Reason,
		ServerRevision: conflict.ServerRevision,
		ServerHash:     conflict.ServerHash,
		ServerDeleted:  conflict.ServerDeleted,
		BaseHash:       baseHash,
		// REST never resolves the proven common-ancestor body, so the real base
		// content is always reported unavailable rather than fabricated.
		// REST 不解析可证实的共同祖先正文，因此始终如实报告基线正文不可用，而不是伪造。
		BaseMissing: true,
	}
	if kind == dto.SyncConflictKindNote && !conflict.ServerDeleted && h.App.NoteService != nil {
		noteSvc := h.App.GetNoteService(h.getClientInfo(c))
		if note, err := noteSvc.Get(c.Request.Context(), uid, &dto.NoteGetRequest{
			Vault:    vault,
			Path:     path,
			PathHash: pathHash,
		}); err == nil && note != nil {
			payload.ServerContent = note.Content
		}
	}
	response.ToResponse(code.ErrorSyncConflict.WithData(payload).WithVault(vault))
}
