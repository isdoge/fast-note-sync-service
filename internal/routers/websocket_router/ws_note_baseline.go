package websocket_router

import (
	"context"

	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/logger"
	"github.com/haierkeys/fast-note-sync-service/pkg/safego"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"go.uber.org/zap"
)

// doNoteSyncBaseline runs the baseline-mode decision engine for notes. It never
// mutates the legacy path: only connections that negotiated syncBaselineVersion >= 1
// reach it, and every decision follows design §3.2/§3.3/§3.4.
// doNoteSyncBaseline 执行笔记的基线模式决策引擎。它绝不改动旧路径：只有协商到
// syncBaselineVersion >= 1 的连接才会走到这里，每个决策遵循设计 §3.2/§3.3/§3.4。
func (h *NoteWSHandler) doNoteSyncBaseline(c *pkgapp.WebsocketClient, params *dto.NoteSyncRequest) {
	syncDownloadDropStale(connDone(c), "note", params.Context)
	ctx := c.Context()
	uid := c.User.UID

	noteSvc := h.App.GetNoteService(c.ClientType(), c.ClientName(), c.ClientVersion())

	pkgapp.NoteModifyLog(c.TraceID, uid, "NoteSync", "", params.Vault)
	h.App.VaultService.GetOrCreate(ctx, uid, params.Vault)

	// Record the cursor before querying so writes during this round are not lost.
	// 查询前记录游标，避免本轮处理期间的写入在下次增量时被永久遗漏。
	syncStartTime := timex.Now().UnixMilli()

	list, err := noteSvc.ListByLastTime(ctx, uid, params)
	if err != nil {
		h.respondError(c, code.ErrorNoteListFailed, err, "websocket_router.note.NoteSync.ListByLastTime")
		return
	}

	// 1. Index client-reported states by path.
	// 1. 按路径索引客户端上报的状态。
	type clientState struct {
		pathHash string
		missing  bool
		deleted  bool
		state    baselineClientState
	}
	clients := make(map[string]*clientState, len(params.Notes)+len(params.DelNotes)+len(params.MissingNotes))
	noteItem := func(pathHash, baseHash string, baseHashMissing bool, baseRevision, contentHash string) *clientState {
		hash, missing := dto.NormalizeBaseline(baseHash, baseHashMissing)
		return &clientState{pathHash: pathHash, state: baselineClientState{
			LocalHash:    contentHash,
			BaseHash:     hash,
			HasBase:      !missing,
			BaseRevision: baseRevision,
			IsFile:       true,
		}}
	}
	for _, n := range params.Notes {
		cs := noteItem(n.PathHash, n.BaseHash, n.BaseHashMissing, n.BaseRevision, n.ContentHash)
		clients[n.Path] = cs
	}
	for _, n := range params.DelNotes {
		cs := noteItem(n.PathHash, n.BaseHash, n.BaseHashMissing, n.BaseRevision, "")
		cs.deleted = true
		cs.state.Deleted = true
		clients[n.Path] = cs
	}
	for _, n := range params.MissingNotes {
		cs := noteItem(n.PathHash, n.BaseHash, n.BaseHashMissing, n.BaseRevision, "")
		cs.missing = true
		cs.state.Missing = true
		clients[n.Path] = cs
	}

	// 2. Index the server rows inside the incremental window.
	// 2. 索引增量窗口内的服务端行。
	serverByPath := make(map[string]*dto.NoteDTO, len(list))
	for _, n := range list {
		if prev, ok := serverByPath[n.Path]; !ok || n.UpdatedTimestamp > prev.UpdatedTimestamp {
			serverByPath[n.Path] = n
		}
	}

	// 3. Batch-query the current metadata (tombstones included) for every client path
	// missing from the incremental result: "not in the delta" never means "absent".
	// 3. 对未进入增量结果的客户端路径批量查询当前元数据（含墓碑）：不在增量结果里绝不等于不存在。
	missingPaths := make([]string, 0, len(clients))
	for path := range clients {
		if _, ok := serverByPath[path]; !ok && path != "" {
			missingPaths = append(missingPaths, path)
		}
	}
	if len(missingPaths) > 0 {
		meta, metaErr := noteSvc.GetPathsMetaBatch(ctx, uid, params.Vault, missingPaths)
		if metaErr != nil {
			// 无法确定服务端状态时不能继续：把客户端删除当作"服务端不存在"会静默吞掉
			// 删除意图，并把活跃笔记当成新文件。让本轮同步失败。
			// Without the server state we cannot continue: treating a client delete as
			// "server absent" would swallow delete intents and treat live notes as new.
			h.respondError(c, code.ErrorNoteListFailed, metaErr, "websocket_router.note.NoteSync.GetPathsMetaBatch")
			return
		}
		for path, n := range meta {
			if n != nil {
				serverByPath[path] = n
			}
		}
	}

	var messageQueue []dto.WSQueuedMessage
	var needUploadCount, needModifyCount, needSyncMtimeCount, needDeleteCount, needConflictCount int64

	// handled tracks paths already consumed so the server list loop cannot send a
	// second, conflicting message for the same path.
	// handled 记录已处理的路径，避免服务端列表循环对同一路径再发一条冲突消息。
	handled := make(map[string]struct{}, len(serverByPath))

	enqueue := func(action WebSocketSendAction, data any) {
		messageQueue = append(messageQueue, dto.WSQueuedMessage{Context: params.Context, Action: action, Data: data})
	}

	// 4. Apply the decision tables to every client-reported path.
	// 4. 对每个客户端上报路径应用决策表。
	for path, cs := range clients {
		if path == "" {
			continue
		}
		handled[path] = struct{}{}

		serverDTO, ok := serverByPath[path]
		server := baselineMeta{}
		if ok && serverDTO != nil {
			server = baselineMetaFromNote(true, serverDTO.Action == "delete", serverDTO.ContentHash,
				serverDTO.Revision, serverDTO.Path, serverDTO.PathHash,
				serverDTO.Ctime, serverDTO.Mtime, serverDTO.Size, serverDTO.UpdatedTimestamp)
			server.ContentPurge = serverDTO.Action == "delete" && serverDTO.ContentHash == "" && serverDTO.Revision != ""
		}

		decision := decideBaseline(cs.state, server)
		switch decision.Action {
		case baselineActionNone:
			if decision.Confirmed {
				// 内容一致：下发带 ContentHash + Revision 的确认，供客户端建立基线。
				// Content agrees: confirm with ContentHash + Revision so the client can
				// establish a baseline.
				enqueue(NoteSyncMtime, dto.NoteSyncMtimeMessage{
					Path:             server.Path,
					Ctime:            server.Ctime,
					Mtime:            server.Mtime,
					UpdatedTimestamp: server.LastTime,
					ContentHash:      server.ContentHash,
					Revision:         server.Revision,
				})
				needSyncMtimeCount++
			}
		case baselineActionDownload:
			enqueue(NoteSyncModify, dto.NoteSyncModifyMessage{
				Path:             server.Path,
				PathHash:         server.PathHash,
				ContentHash:      server.ContentHash,
				Ctime:            server.Ctime,
				Mtime:            server.Mtime,
				UpdatedTimestamp: server.LastTime,
				Revision:         server.Revision,
			})
			needModifyCount++
			messageQueue[len(messageQueue)-1].NoteID = serverDTO.ID
		case baselineActionUpload:
			enqueue(NoteSyncNeedPush, dto.NoteSyncNeedPushMessage{Path: path, PathHash: cs.pathHash})
			needUploadCount++
		case baselineActionDelete:
			// 条件删除：服务端活跃、S == B 且令牌匹配。
			// Conditional delete: server live, S == B and the revision matches.
			delParams := &dto.NoteDeleteRequest{
				Vault:    params.Vault,
				Path:     path,
				PathHash: server.PathHash,
				BaseHash: cs.state.BaseHash,
				Baseline: &dto.BaselineCommit{
					HasBase:      cs.state.HasBase,
					BaseHash:     cs.state.BaseHash,
					BaseRevision: cs.state.BaseRevision,
				},
			}
			deleted, delErr := noteSvc.Delete(ctx, uid, delParams)
			if delErr != nil {
				h.logWarn(c, "websocket_router.note.NoteSync.baseline.Delete", zap.String(logger.FieldPath, path), zap.Error(delErr))
				break
			}
			if deleted != nil && !deleted.Unchanged {
				safego.Go(h.App.Logger(), func() {
					c.BroadcastResponse(code.Success.WithData(dto.NoteSyncDeleteMessage{
						Path:             deleted.Path,
						PathHash:         deleted.PathHash,
						Ctime:            deleted.Ctime,
						Mtime:            deleted.Mtime,
						Size:             deleted.Size,
						UpdatedTimestamp: deleted.UpdatedTimestamp,
						Revision:         deleted.Revision,
						ContentHash:      deleted.ContentHash,
					}).WithVault(params.Vault), true, NoteSyncDelete)
				})
			}
		case baselineActionApplyDelete:
			enqueue(NoteSyncDelete, dto.NoteSyncDeleteMessage{
				Path:             server.Path,
				PathHash:         server.PathHash,
				Ctime:            server.Ctime,
				Mtime:            server.Mtime,
				Size:             server.Size,
				UpdatedTimestamp: server.LastTime,
				Revision:         server.Revision,
				ContentHash:      server.ContentHash,
			})
			needDeleteCount++
		case baselineActionConfirmDelete:
			// 双方均已删除：无需任何消息。
			// Both sides deleted: no message needed.
		case baselineActionConflict:
			h.enqueueNoteConflict(ctx, c, params, uid, noteSvc, path, cs.state, server, serverDTO, decision.Reason, enqueue)
			needConflictCount++
		}
	}

	// 5. Server rows nobody reported: a live row is a download, a tombstone is a
	// delete notification. Never a conflict (the client simply does not have it).
	// 5. 客户端未上报的服务端行：活跃行下发，墓碑下发删除通知；不产生冲突。
	for path, serverDTO := range serverByPath {
		if _, done := handled[path]; done {
			continue
		}
		if serverDTO == nil {
			continue
		}
		if serverDTO.Action == "delete" {
			enqueue(NoteSyncDelete, dto.NoteSyncDeleteMessage{
				Path:             serverDTO.Path,
				PathHash:         serverDTO.PathHash,
				Ctime:            serverDTO.Ctime,
				Mtime:            serverDTO.Mtime,
				Size:             serverDTO.Size,
				UpdatedTimestamp: serverDTO.UpdatedTimestamp,
				Revision:         serverDTO.Revision,
				ContentHash:      serverDTO.ContentHash,
			})
			needDeleteCount++
			continue
		}
		enqueue(NoteSyncModify, dto.NoteSyncModifyMessage{
			Path:             serverDTO.Path,
			PathHash:         serverDTO.PathHash,
			ContentHash:      serverDTO.ContentHash,
			Ctime:            serverDTO.Ctime,
			Mtime:            serverDTO.Mtime,
			UpdatedTimestamp: serverDTO.UpdatedTimestamp,
			Revision:         serverDTO.Revision,
		})
		messageQueue[len(messageQueue)-1].NoteID = serverDTO.ID
		needModifyCount++
	}

	c.IsFirstSync = true

	if len(messageQueue) > 0 {
		pageSize := h.App.Config().App.SyncDownChunkNum
		if pageSize <= 0 {
			pageSize = 50
		}
		window := 0
		if c.ProtoVersion >= 2 {
			window = h.App.Config().App.PipelineWindowDownClamped()
		}
		entry := &syncDownloadEntry{
			Context:      params.Context,
			TypeName:     "note",
			Vault:        params.Vault,
			MessageQueue: messageQueue,
			PageSize:     pageSize,
			Window:       window,
			FillContent: func(ctx context.Context, noteID int64) (string, error) {
				n, err := noteSvc.GetByID(ctx, uid, noteID)
				if err != nil {
					return "", err
				}
				return n.Content, nil
			},
		}
		entry.ConnDone = connDone(c)
		syncDownloadStore(params.Context, "note", entry)
	}

	c.ToResponse(code.Success.WithData(dto.NoteSyncEndMessage{
		LastTime:           syncStartTime,
		NeedUploadCount:    needUploadCount,
		NeedModifyCount:    needModifyCount,
		NeedSyncMtimeCount: needSyncMtimeCount,
		NeedDeleteCount:    needDeleteCount,
		NeedConflictCount:  needConflictCount,
	}).WithVault(params.Vault).WithContext(params.Context), NoteSyncEnd)
}

// enqueueNoteConflict builds and queues the unified conflict payload, resolving the
// real base content from history and the remote content for notes.
// enqueueNoteConflict 构造并排入统一冲突载荷，从历史解析真实基线正文，并带上远端正文。
func (h *NoteWSHandler) enqueueNoteConflict(
	ctx context.Context,
	c *pkgapp.WebsocketClient,
	params *dto.NoteSyncRequest,
	uid int64,
	noteSvc interface {
		GetByID(ctx context.Context, uid, id int64) (*dto.NoteDTO, error)
	},
	path string,
	client baselineClientState,
	server baselineMeta,
	serverDTO *dto.NoteDTO,
	reason string,
	enqueue func(WebSocketSendAction, any),
) {
	// 服务端不存在（server_missing）时没有 note 行，历史也就无从查起：只能标记
	// baseMissing，绝不伪造共同祖先。
	// A missing server row (server_missing) leaves no note row to look history up
	// from: report baseMissing and never fabricate an ancestor.
	noteID := int64(0)
	if serverDTO != nil {
		noteID = serverDTO.ID
	}
	baseContent, found := h.resolveBaseContent(ctx, uid, noteID, client.BaseHash, !client.HasBase)
	baseMissing := !found
	serverContent := ""
	if noteID != 0 && serverDTO.Action != "delete" {
		if n, err := noteSvc.GetByID(ctx, uid, noteID); err == nil && n != nil {
			serverContent = n.Content
		}
	}
	enqueue(SyncConflict, baselineConflictMessage(
		dto.SyncConflictKindNote, reason, path, server.PathHash,
		client.BaseHash, baseContent, baseMissing, server, serverContent,
	))
}
