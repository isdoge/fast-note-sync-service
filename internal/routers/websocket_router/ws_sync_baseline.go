package websocket_router

import (
	"context"
	"errors"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// baselineModeEnabled reports whether the negotiated baseline rules are in force.
// Only connections that completed ClientInfo negotiation with version >= 1 use the
// new decision tables; everything else keeps the legacy behavior untouched.
// baselineModeEnabled 判断本连接是否启用协商后的基线规则。只有完成 ClientInfo 协商且版本 >= 1
// 的连接走新决策表；其余连接（老插件、第三方 CLI）完全保持旧行为。
func baselineModeEnabled(c *pkgapp.WebsocketClient) bool {
	return c != nil && c.SyncBaselineVersion() >= 1
}

// baselineMeta is the server-side state of one path as seen by the decision tables.
// baselineMeta 是决策表看到的单个路径的服务端状态。
type baselineMeta struct {
	Found        bool   // A row exists (live or tombstone) // 存在行（活跃或墓碑）
	Deleted      bool   // The row is a tombstone (action=delete) // 该行是墓碑
	ContentHash  string // Active content hash (tombstone: last pre-delete hash) // 活跃内容哈希（墓碑为删除前最后哈希）
	Revision     string // Current revision token // 当前版本令牌
	Path         string // Canonical full path // 完整规范路径
	PathHash     string // Existing path hash // 现有路径哈希
	Ctime        int64  // Creation timestamp // 创建时间戳
	Mtime        int64  // Modification timestamp // 修改时间戳
	Size         int64  // Size // 大小
	LastTime     int64  // updated_timestamp // 记录更新时间戳
	ContentPurge bool   // Body already purged // 正文已清理
}

// baselineDecision is the outcome of one decision-table cell.
// baselineDecision 是决策表中一个格子的结论。
type baselineDecision struct {
	// Action is what the server should do for this path.
	// Action 是服务端对该路径应采取的动作。
	Action baselineAction
	// Reason is the conflict reason when Action == baselineActionConflict.
	// Reason 是 Action 为冲突时的冲突原因。
	Reason string
	// Confirmed is true when the two sides already agree (L == S) and only a
	// baseline confirmation message is needed.
	// Confirmed 表示双方内容已一致（L == S），只需补发基线确认。
	Confirmed bool
}

// baselineAction enumerates the server decisions of the design tables.
// baselineAction 枚举设计决策表中的服务端动作。
type baselineAction int

const (
	// baselineActionNone means no write and no message beyond a possible confirmation.
	// baselineActionNone 表示不写入、除必要确认外不发消息。
	baselineActionNone baselineAction = iota
	// baselineActionDownload means the server content should be pushed to the client.
	// baselineActionDownload 表示应把服务端内容下发给客户端。
	baselineActionDownload
	// baselineActionUpload means the client must upload (subject to CAS at commit).
	// baselineActionUpload 表示客户端需要上传（提交时仍做 CAS 校验）。
	baselineActionUpload
	// baselineActionDelete means the server should conditionally delete the live row.
	// baselineActionDelete 表示服务端应条件删除活跃行。
	baselineActionDelete
	// baselineActionConflict means the two sides diverge and must be reported.
	// baselineActionConflict 表示双方分叉，必须报冲突。
	baselineActionConflict
	// baselineActionApplyDelete means the server tombstone delete should be applied locally.
	// baselineActionApplyDelete 表示应把服务端墓碑删除应用到客户端。
	baselineActionApplyDelete
	// baselineActionConfirmDelete means both sides already deleted: acknowledge only.
	// baselineActionConfirmDelete 表示双方均已删除，只需确认。
	baselineActionConfirmDelete
)

// baselineClientState describes one client-reported entry (or deletion intent).
// baselineClientState 描述客户端上报的一条记录（或删除意图）。
type baselineClientState struct {
	// LocalHash is L, the client's current content hash ("" for deletion intents).
	// LocalHash 是 L，客户端当前内容哈希（删除意图为空）。
	LocalHash string
	// BaseHash is B, the trusted last-synced content hash ("" when missing).
	// BaseHash 是 B，可信的上次同步内容哈希（缺失时为空）。
	BaseHash string
	// HasBase is true when a trusted content baseline exists (B may legitimately be "0").
	// HasBase 表示存在可信内容基线（B 合法地可以是 "0"）。
	HasBase bool
	// BaseRevision is the server revision the client confirmed for that baseline.
	// BaseRevision 是客户端为该基线确认过的服务端版本令牌。
	BaseRevision string
	// Deleted is true when the entry is a delete intent (del* list).
	// Deleted 表示该条目是删除意图（del* 清单）。
	Deleted bool
	// Missing is true when the entry is a recovery request (missing* list): the
	// client has no local copy and asks the server to send its version. It is never
	// a delete intent and never a local modification (design §1.3).
	// Missing 表示该条目是恢复请求（missing* 清单）：客户端本地没有副本、请求服务端下发。
	// 它绝不是删除意图，也不是本地修改（设计 §1.3）。
	Missing bool
	// IsFile is true for note/file/setting (content) resources; folders have no content.
	// IsFile 表示笔记/附件/配置（内容资源）；目录没有内容。
	IsFile bool
	// IsConflictResolved is the user's explicit resolution mark (never skips the check).
	// IsConflictResolved 是用户明确的解决标记（绝不跳过校验）。
	IsConflictResolved bool
}

// baselineRevisionMatches reports whether the client's baseline revision names the
// server's current state. An empty BaseRevision is never a match: without a token the
// server must not run a destructive conditional delete.
// baselineRevisionMatches 判断客户端基线令牌是否等于服务端当前状态。空 BaseRevision 永远不算匹配：
// 没有令牌时服务端不得执行破坏性条件删除。
func baselineRevisionMatches(clientBaseRevision string, server baselineMeta) bool {
	return clientBaseRevision != "" && server.Revision != "" && clientBaseRevision == server.Revision
}

// decideBaselineActive applies design §3.2 (server row is live).
// decideBaselineActive 应用设计 §3.2（服务端为活跃行）。
func decideBaselineActive(client baselineClientState, server baselineMeta) baselineDecision {
	s := server.ContentHash

	// 恢复请求（missing*）：客户端本地没有副本、明确要求服务端下发，服务端活跃即下载。
	// A recovery request (missing*) means the client has no local copy and asks for the
	// server version, so a live row is a download (design §1.3: missing* is not a delete).
	if client.Missing {
		return baselineDecision{Action: baselineActionDownload}
	}

	// Priority 2: L == S — the two sides already agree, no conflict, no write.
	// 优先级 2：L == S——双方已一致，不制造冲突、不写入。
	if !client.Deleted && client.LocalHash == s {
		return baselineDecision{Action: baselineActionNone, Confirmed: true}
	}

	if client.Deleted {
		// 无文件，有基线，明确提交本地删除。
		// "No local file, has baseline, explicit local delete".
		if !client.HasBase {
			// 无文件，无基线，也无删除意图 → 下载（新设备）。
			// "No local file, no baseline, no delete intent" is handled by callers as download;
			// a delete intent without a baseline cannot be authorized.
			return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonNoBaseline}
		}
		if client.BaseHash == s && baselineRevisionMatches(client.BaseRevision, server) {
			// S == B 且 R 一致：条件删除服务端并确认。
			// S == B with a matching revision: conditional delete and confirm.
			return baselineDecision{Action: baselineActionDelete}
		}
		// S == B 但 R 不一致（删除后同内容重建），或 S != B（远端已修改）：保持活跃并冲突。
		// S == B with a stale revision (same-content rebuild), or S != B (remote changed):
		// keep the server row live and report a conflict.
		return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonLocalDeleted}
	}

	if !client.HasBase {
		// 有文件，无基线：内容不同 → 冲突（#5）。
		// "Has file, no baseline": differing content is a conflict (#5).
		return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonNoBaseline}
	}

	if client.LocalHash == client.BaseHash {
		// 有文件，L == B：本地没有任何未同步改动，远端变化可以安全接收（↓M）。
		// 不能因为 mtime 差异反向把本地旧内容上传覆盖远端。
		// Has file, L == B: nothing unsynced locally, so the remote change is safe to
		// accept (↓M). An mtime difference must never turn this into an upload that
		// overwrites the remote.
		return baselineDecision{Action: baselineActionDownload}
	}

	if client.BaseHash == s {
		// S == B，有文件，L != B：内容层面只有客户端改过。但设计表要求版本令牌也一致才
		// 允许上传——令牌不一致说明服务端其实经历过删除后重建等状态变化，此时按内容
		// 相同放行会把"旧对象"的修改写进"新对象"。
		// S == B with a file and L != B: only the client changed the content. The design
		// table still requires the revision token to match: a stale token means the server
		// went through a delete-then-recreate, and letting the write through would push an
		// edit of the old object onto the new one.
		if baselineRevisionMatches(client.BaseRevision, server) {
			return baselineDecision{Action: baselineActionUpload}
		}
		return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonRevisionChanged}
	}

	// S != B，有文件，L != B：客户端改过，服务端也改过 → 冲突（双侧修改，#4）。
	// S != B, has file, L != B: both sides changed — conflict (#4).
	return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonBothModified}
}

// decideBaselineTombstone applies design §3.3 (server row is a tombstone).
// decideBaselineTombstone 应用设计 §3.3（服务端为墓碑）。
func decideBaselineTombstone(client baselineClientState, server baselineMeta) baselineDecision {
	// 恢复请求遇到墓碑：服务端已删除，客户端本就没有副本，无需任何动作。
	// A recovery request meeting a tombstone: the server row is deleted and the client
	// has no copy, so nothing is needed.
	if client.Missing {
		return baselineDecision{Action: baselineActionConfirmDelete}
	}

	if client.Deleted {
		// 无文件，有基线或无基线，本地删除：幂等完成，消费删除意图。
		// "No local file, local delete": idempotent completion, consume the intent.
		return baselineDecision{Action: baselineActionConfirmDelete}
	}

	if !client.HasBase {
		// 有文件，无基线：既不能安全删除，也不能判断为有意重建 → 冲突（#1）。
		// "Has file, no baseline": neither a safe delete nor a provable rebuild — conflict (#1).
		return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonNoBaseline}
	}

	if client.LocalHash == client.BaseHash {
		// 有文件，L == B：本地没有未同步内容，应用远端删除。
		// "Has file, L == B": nothing unsynced locally — apply the remote delete.
		return baselineDecision{Action: baselineActionApplyDelete}
	}

	// 有文件，L != B：保留本地编辑，服务端保持删除，不自动复活（#1）。
	// "Has file, L != B": keep the local edit, keep the server deleted, never auto-revive (#1).
	return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonRemoteDeleted}
}

// decideBaselineMissing applies design §3.4 (no server row at all).
// decideBaselineMissing 应用设计 §3.4（服务端完全不存在该行）。
func decideBaselineMissing(client baselineClientState) baselineDecision {
	// 恢复请求而服务端也不存在：两侧都没有内容，无需动作、不得创建空内容。
	// A recovery request with no server row either: neither side has content, so no
	// action and no fabricated empty body.
	if client.Missing {
		return baselineDecision{Action: baselineActionConfirmDelete}
	}
	if client.Deleted {
		// 无文件，本地删除：确认删除已完成，不重试、不造空内容。
		// "No local file, local delete": the delete is already complete.
		return baselineDecision{Action: baselineActionConfirmDelete}
	}
	if !client.HasBase {
		// 有文件，无基线：正常新文件，允许"仍不存在"的条件创建。
		// "Has file, no baseline": a normal new file; allow conditional creation.
		return baselineDecision{Action: baselineActionUpload}
	}
	// 有文件，有基线：可能是旧墓碑被清理或服务端数据重置，保留本地、不自动上传（#3）。
	// "Has file, has baseline": an old tombstone may have been purged or the server reset;
	// keep the local copy and never auto-upload (#3).
	return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonServerMissing}
}

// decideBaselineFolder applies the folder-specific rules (§3.6): folders have no
// content hash, so only the revision token decides.
// decideBaselineFolder 应用目录专属规则（§3.6）：目录没有内容哈希，只按版本令牌判断。
func decideBaselineFolder(deletedIntent bool, baseRevision string, server baselineMeta) baselineDecision {
	return decideBaselineFolderEntry(false, deletedIntent, baseRevision, server)
}

// decideBaselineFolderEntry is decideBaselineFolder plus the missing* (recovery
// request) flag: a client without the folder asking for it is never a delete intent
// and never a conflict.
// decideBaselineFolderEntry 是带 missing*（恢复请求）标志的 decideBaselineFolder：
// 客户端没有该目录、请求下发时，既不是删除意图，也不产生冲突。
func decideBaselineFolderEntry(missing bool, deletedIntent bool, baseRevision string, server baselineMeta) baselineDecision {
	if server.Deleted {
		if missing || deletedIntent {
			return baselineDecision{Action: baselineActionConfirmDelete}
		}
		if baseRevision == "" {
			return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonNoBaseline}
		}
		// 服务端墓碑而客户端仍有目录：远端删除可以应用（目录无内容可丢）。
		// Server tombstone while the client still has the folder: apply the remote delete.
		return baselineDecision{Action: baselineActionApplyDelete}
	}
	if deletedIntent {
		if baseRevision != "" && baseRevision == server.Revision {
			return baselineDecision{Action: baselineActionDelete}
		}
		return baselineDecision{Action: baselineActionConflict, Reason: dto.SyncConflictReasonLocalDeleted}
	}
	return baselineDecision{Action: baselineActionNone, Confirmed: true}
}

// decideBaseline is the content-resource entry point: it dispatches to the active,
// tombstone or missing table based on the server state.
// decideBaseline 是内容资源入口：按服务端状态分派到活跃、墓碑或不存在决策表。
func decideBaseline(client baselineClientState, server baselineMeta) baselineDecision {
	if !server.Found {
		return decideBaselineMissing(client)
	}
	if server.Deleted {
		return decideBaselineTombstone(client, server)
	}
	return decideBaselineActive(client, server)
}

// baselineMetaFromNote builds the decision meta for a note/file/setting DTO.
// baselineMetaFromNote 用笔记/附件/配置 DTO 构造决策元数据。
func baselineMetaFromNote(found bool, deleted bool, contentHash, revision, path, pathHash string, ctime, mtime, size, lastTime int64) baselineMeta {
	return baselineMeta{
		Found:       found,
		Deleted:     deleted,
		ContentHash: contentHash,
		Revision:    revision,
		Path:        path,
		PathHash:    pathHash,
		Ctime:       ctime,
		Mtime:       mtime,
		Size:        size,
		LastTime:    lastTime,
	}
}

// baselineConflictMessage builds the unified conflict payload for one path.
// baseContent/baseMissing come from a verified history lookup only; callers that
// cannot prove the ancestor pass baseMissing=true and an empty baseContent.
// baselineConflictMessage 构造某路径的统一冲突载荷。baseContent/baseMissing 只来自已证实的历史
// 查询；无法证明共同祖先的调用方必须传 baseMissing=true 与空 baseContent。
func baselineConflictMessage(kind, reason string, path, pathHash, baseHash, baseContent string, baseMissing bool, server baselineMeta, serverContent string) dto.SyncConflictMessage {
	return dto.SyncConflictMessage{
		Kind:           kind,
		Path:           path,
		PathHash:       pathHash,
		Reason:         reason,
		ServerHash:     server.ContentHash,
		ServerRevision: server.Revision,
		ServerDeleted:  server.Deleted,
		BaseHash:       baseHash,
		BaseContent:    baseContent,
		BaseMissing:    baseMissing,
		ServerContent:  serverContent,
	}
}

// baselineCommitFromNote builds the conditional-commit contract of a note request.
// It is only attached for baseline-mode connections; nil keeps legacy behavior.
// baselineCommitFromNote 构造笔记请求的条件提交契约；只有基线模式连接才附加，nil 保持旧行为。
func baselineCommitFromNote(c *pkgapp.WebsocketClient, baseHash string, baseHashMissing bool, baseRevision string, isConflictResolved bool) *dto.BaselineCommit {
	if !baselineModeEnabled(c) {
		return nil
	}
	hash, missing := dto.NormalizeBaseline(baseHash, baseHashMissing)
	return &dto.BaselineCommit{
		HasBase:            !missing,
		BaseHash:           hash,
		BaseRevision:       baseRevision,
		IsConflictResolved: isConflictResolved,
	}
}

// baselineCommitFromFolder builds the folder contract (revision only).
// baselineCommitFromFolder 构造目录契约（只有版本令牌）。
func baselineCommitFromFolder(c *pkgapp.WebsocketClient, baseRevision string, isConflictResolved bool) *dto.BaselineCommit {
	if !baselineModeEnabled(c) {
		return nil
	}
	return &dto.BaselineCommit{BaseRevision: baseRevision, IsConflictResolved: isConflictResolved}
}

// resolveBaseContent looks up the real content of a client baseline in the note
// history. It only reports found when the history row for that exact hash exists;
// empty text with BaseHash "0" is a legitimate hit. Callers must set
// baseMissing=true whenever found is false and never fabricate an ancestor.
// resolveBaseContent 在笔记历史中查找客户端基线的真实正文。只有精确哈希的历史行存在时才算命中；
// 空正文配 BaseHash "0" 属于合法命中。found 为 false 时调用方必须设 baseMissing=true，
// 绝不伪造共同祖先。
func (h *WSHandler) resolveBaseContent(ctx context.Context, uid int64, noteID int64, baseHash string, baseMissing bool) (string, bool) {
	if baseMissing || baseHash == "" || noteID == 0 {
		return "", false
	}
	if h.App.NoteHistoryService == nil {
		return "", false
	}
	history, err := h.App.NoteHistoryService.GetByNoteIDAndHash(ctx, uid, noteID, baseHash)
	if err != nil || history == nil {
		return "", false
	}
	return history.Content, true
}

// isBaselineConflict reports whether err is a rejected conditional commit.
// isBaselineConflict 判断 err 是否为被拒绝的条件提交。
func isBaselineConflict(err error) (*dto.BaselineConflictError, bool) {
	var conflict *dto.BaselineConflictError
	if errors.As(err, &conflict) {
		return conflict, true
	}
	return nil, false
}

// isNotFound reports whether err is a GORM record-not-found.
// isNotFound 判断 err 是否为 GORM 记录不存在。
func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// respondBaselineConflict sends a rejected conditional commit as the unified
// SyncConflict payload with the existing 530 envelope. Only baseline-mode
// connections ever reach this; legacy connections never receive SyncConflict.
// respondBaselineConflict 用现有 530 信封把被拒绝的条件提交作为统一 SyncConflict 载荷回发。
// 只有基线模式连接会走到这里；旧连接永远不会收到 SyncConflict。
func (h *WSHandler) respondBaselineConflict(c *pkgapp.WebsocketClient, kind, path, pathHash, baseHash string, conflict *dto.BaselineConflictError, slotPath string) {
	if conflict == nil {
		conflict = &dto.BaselineConflictError{Reason: dto.SyncConflictReasonRevisionChanged}
	}
	server := baselineMeta{
		Found:       conflict.ServerActive || conflict.ServerDeleted,
		Deleted:     conflict.ServerDeleted,
		ContentHash: conflict.ServerHash,
		Revision:    conflict.ServerRevision,
		Path:        path,
		PathHash:    pathHash,
	}
	payload := baselineConflictMessage(kind, conflict.Reason, path, pathHash, baseHash, "", true, server, "")
	h.App.Logger().Info("baseline conditional commit rejected",
		zap.String("traceId", c.TraceID),
		zap.String("kind", kind),
		zap.String("path", path),
		zap.String("reason", conflict.Reason))
	c.ToResponse(code.ErrorSyncConflict.WithData(payload).WithVault("").WithContext("").WithPath(slotPath), string(SyncConflict))
}
