package service

import (
	"errors"

	"github.com/haierkeys/fast-note-sync-service/internal/domain"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
)

// ErrBaselineConflict is the sentinel that callers match with errors.As to turn a
// rejected conditional commit into the unified SyncConflict payload.
// ErrBaselineConflict 是调用方用 errors.As 匹配的哨兵，把被拒绝的条件提交转成统一 SyncConflict 载荷。
var ErrBaselineConflict = errors.New("baseline conflict")

// checkBaselineCreate applies the "new item" branch of the conditional commit:
//
//   - no baseline at all (BaseRevision == "") and no resolution mark: only allowed
//     while the server still has neither a live row nor an unconfirmed conflict.
//     The caller has already established that no live row exists, so nothing more
//     is required here.
//   - server_missing resolution: allowed only while the row is still absent, which
//     the caller also established.
//   - tombstone resurrection: refused unless the user resolved the conflict and the
//     carried BaseRevision matches the tombstone's current revision (checked by the
//     caller, which owns the row).
//
// checkBaselineCreate 实现条件提交的"新建"分支：无基线且未标记解决时，仅当服务端既无活跃行
// 也无未确认冲突才允许（调用方已确认无活跃行）；server_missing 的"保留本地"选择同理。
//
// 携带基线令牌却落到"新建"分支，说明服务端该行已不存在（被清理或数据重置）。设计 §3.4
// 要求保留本地并报 server_missing，而不是 remote_deleted：后者会让插件误以为远端做过删除，
// 引导用户去"恢复"一个从未存在过的远端版本。
// A carried baseline revision landing on the "create" branch means the server row is
// gone (purged or reset). Design §3.4 reports server_missing and keeps the local copy;
// remote_deleted would make the plugin believe the remote deleted it.
func checkBaselineCreate(commit *dto.BaselineCommit) error {
	if commit == nil {
		return nil
	}
	if commit.BaseRevision == "" {
		return nil
	}
	if !commit.IsConflictResolved {
		return &dto.BaselineConflictError{Reason: dto.SyncConflictReasonServerMissing}
	}
	return nil
}

// newBaselineConflict builds a BaselineConflictError from the current server row.
// newBaselineConflict 用当前服务端行构造 BaselineConflictError。
func newBaselineConflict(reason string, current *domain.Note) error {
	return baselineConflictFromNote(reason, current)
}

// baselineConflictFromNote maps a note row onto the conflict error.
// baselineConflictFromNote 把笔记行映射为冲突错误。
func baselineConflictFromNote(reason string, note *domain.Note) error {
	conflict := &dto.BaselineConflictError{Reason: reason}
	if note != nil {
		conflict.ServerDeleted = note.Action == domain.NoteActionDelete
		conflict.ServerActive = !conflict.ServerDeleted
		conflict.ServerRevision = note.Revision
		conflict.ServerHash = note.ContentHash
	}
	return conflict
}

// baselineConflictFromFile maps a file row onto the conflict error.
// baselineConflictFromFile 把附件行映射为冲突错误。
func baselineConflictFromFile(reason string, file *domain.File) error {
	conflict := &dto.BaselineConflictError{Reason: reason}
	if file != nil {
		conflict.ServerDeleted = file.Action == domain.FileActionDelete
		conflict.ServerActive = !conflict.ServerDeleted
		conflict.ServerRevision = file.Revision
		conflict.ServerHash = file.ContentHash
	}
	return conflict
}

// baselineConflictFromSetting maps a setting row onto the conflict error.
// baselineConflictFromSetting 把配置行映射为冲突错误。
func baselineConflictFromSetting(reason string, setting *domain.Setting) error {
	conflict := &dto.BaselineConflictError{Reason: reason}
	if setting != nil {
		conflict.ServerDeleted = setting.Action == domain.SettingActionDelete
		conflict.ServerActive = !conflict.ServerDeleted
		conflict.ServerRevision = setting.Revision
		conflict.ServerHash = setting.ContentHash
	}
	return conflict
}

// baselineConflictFromFolder maps a folder row onto the conflict error.
// baselineConflictFromFolder 把目录行映射为冲突错误。
func baselineConflictFromFolder(reason string, folder *domain.Folder) error {
	conflict := &dto.BaselineConflictError{Reason: reason}
	if folder != nil {
		conflict.ServerDeleted = folder.Action == domain.FolderActionDelete
		conflict.ServerActive = !conflict.ServerDeleted
		conflict.ServerRevision = folder.Revision
	}
	return conflict
}

// checkBaselineUpdate applies the "has baseline update" branch: the caller must carry
// a revision token, and the row it writes must still carry that same token. Without a
// token the server cannot prove the client saw the current state, so the write is
// refused.
// checkBaselineUpdate 实现"有基线更新"分支：调用方必须携带版本令牌，且写入的行仍带同一令牌；
// 没有令牌就无法证明客户端见过当前状态，因此拒绝写入。
func checkBaselineUpdate(commit *dto.BaselineCommit, currentRevision string, currentHash string, conflictErr func(string) error) error {
	if commit == nil {
		return nil
	}
	if commit.BaseRevision == "" || commit.BaseRevision != currentRevision {
		return conflictErr(dto.BaselineRejectRevisionMoved)
	}
	// 内容资源还要求未声明解决时当前内容仍等于基线内容（避免旧基线覆盖重建）。
	// Content resources additionally require the current content to still equal the
	// baseline unless the user explicitly resolved the conflict.
	if !commit.IsConflictResolved && commit.HasBase && currentHash != commit.BaseHash {
		return conflictErr(dto.BaselineRejectBothModified)
	}
	return nil
}

// checkBaselineDelete applies the realtime delete branch: an already-deleted or
// missing row is an idempotent success (handled by callers), while a live row may
// only be soft-deleted when the client carries a matching revision (and, for content
// resources, a matching content hash).
//
// Every rejection of a client delete intent reports local_deleted, exactly like the
// batch delNotes/delFiles/delSettings/delFolders path: the client asked to delete a
// row that moved underneath it (remote edit, same-content rebuild, stale token), and
// the plugin must show one consistent "本地删除冲突" reason rather than mixing in
// both_modified / revision_changed.
// checkBaselineDelete 实现实时删除分支：已是墓碑或不存在时幂等成功（由调用方处理）；
// 活跃行只有在客户端携带匹配的版本令牌（内容资源还需匹配内容哈希）时才能软删除。
//
// 所有拒绝删除意图的原因统一为 local_deleted，与批量 delNotes/delFiles/delSettings/
// delFolders 完全一致：客户端要删的行在其脚下发生了变化（远端修改、同内容重建、令牌过期），
// 插件只需展示同一种"本地删除冲突"，不再混入 both_modified / revision_changed。
func checkBaselineDelete(commit *dto.BaselineCommit, currentRevision string, currentHash string, conflictErr func(string) error) error {
	if commit == nil {
		return nil
	}
	if commit.BaseRevision == "" || commit.BaseRevision != currentRevision {
		return conflictErr(dto.SyncConflictReasonLocalDeleted)
	}
	if currentHash != "" && (!commit.HasBase || currentHash != commit.BaseHash) {
		return conflictErr(dto.SyncConflictReasonLocalDeleted)
	}
	return nil
}
