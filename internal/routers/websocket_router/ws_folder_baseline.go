package websocket_router

import (
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	logpkg "github.com/haierkeys/fast-note-sync-service/pkg/logger"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"go.uber.org/zap"
)

// doFolderSyncBaseline runs the baseline-mode decision engine for folders. Folders
// have no content hash, so only BaseRevision decides; a delete intent applies only to
// a matching revision and a remote delete only removes empty folders (the service
// keeps its existing non-empty protection).
// doFolderSyncBaseline 执行目录的基线模式决策引擎。目录没有内容哈希，只按 BaseRevision 判断；
// 删除意图只作用于版本匹配的目录，远端删除只删空目录（服务层保留既有非空保护）。
func (h *FolderWSHandler) doFolderSyncBaseline(c *pkgapp.WebsocketClient, params *dto.FolderSyncRequest) {
	syncDownloadDropStale(connDone(c), "folder", params.Context)
	ctx := c.Context()
	uid := c.User.UID

	h.App.VaultService.GetOrCreate(ctx, uid, params.Vault)
	folderSvc := h.App.GetFolderService(c.ClientType(), c.ClientName(), c.ClientVersion())

	syncStartTime := timex.Now().UnixMilli()
	list, err := folderSvc.ListByUpdatedTimestamp(ctx, uid, params.Vault, params.LastTime)
	if err != nil {
		h.respondError(c, code.ErrorFolderListFailed, err, "websocket_router.folder.FolderSync.ListByUpdatedTimestamp")
		return
	}

	type clientState struct {
		pathHash     string
		baseRevision string
		deleted      bool
		missing      bool
	}
	clients := make(map[string]*clientState, len(params.Folders)+len(params.DelFolders)+len(params.MissingFolders))
	for _, f := range params.Folders {
		if !validWSPath(f.Path) {
			continue
		}
		clients[f.Path] = &clientState{pathHash: f.PathHash, baseRevision: f.BaseRevision}
	}
	for _, f := range params.DelFolders {
		if !validWSPath(f.Path) {
			continue
		}
		clients[f.Path] = &clientState{pathHash: f.PathHash, baseRevision: f.BaseRevision, deleted: true}
	}
	for _, f := range params.MissingFolders {
		if !validWSPath(f.Path) {
			continue
		}
		clients[f.Path] = &clientState{pathHash: f.PathHash, baseRevision: f.BaseRevision, missing: true}
	}

	serverByPath := make(map[string]*dto.FolderDTO, len(list))
	for _, f := range list {
		if prev, ok := serverByPath[f.Path]; !ok || f.UpdatedTimestamp > prev.UpdatedTimestamp {
			serverByPath[f.Path] = f
		}
	}

	// 窗口外的客户端路径必须补查当前元数据（含墓碑）：不在增量结果里绝不等于不存在。
	// Client paths outside the incremental window need a metadata lookup (tombstones
	// included): "not in the delta" never means "absent".
	missingPaths := make([]string, 0, len(clients))
	for path := range clients {
		if _, ok := serverByPath[path]; !ok && path != "" {
			missingPaths = append(missingPaths, path)
		}
	}
	if len(missingPaths) > 0 {
		meta, metaErr := folderSvc.GetPathsMetaBatch(ctx, uid, params.Vault, missingPaths)
		if metaErr != nil {
			// 无法确定服务端状态时不能继续：把客户端删除当作"服务端不存在"会吞掉删除
			// 意图，把活跃目录当成新目录。让本轮同步失败。
			// Without the server state we cannot continue: treating a client delete as
			// "server absent" swallows delete intents and treats live folders as new.
			h.respondError(c, code.ErrorFolderListFailed, metaErr, "websocket_router.folder.FolderSync.GetPathsMetaBatch")
			return
		}
		for path, f := range meta {
			if f != nil {
				serverByPath[path] = f
			}
		}
	}

	var messageQueue []dto.WSQueuedMessage
	var needModifyCount, needDeleteCount, needConflictCount int64
	handled := make(map[string]struct{}, len(serverByPath))
	hasWritePermission := pkgapp.VerifyPermissions(c.Scope, "ws", c.ClientType(), "note_w")

	enqueue := func(action WebSocketSendAction, data any) {
		messageQueue = append(messageQueue, dto.WSQueuedMessage{Context: params.Context, Action: action, Data: data})
	}

	for path, cs := range clients {
		if path == "" {
			continue
		}
		serverDTO, ok := serverByPath[path]
		server := baselineMeta{}
		if ok && serverDTO != nil {
			handled[path] = struct{}{}
			server = baselineMeta{
				Found:    true,
				Deleted:  serverDTO.Action == "delete",
				Revision: serverDTO.Revision,
				Path:     serverDTO.Path,
				PathHash: serverDTO.PathHash,
				Ctime:    serverDTO.Ctime,
				Mtime:    serverDTO.Mtime,
				LastTime: serverDTO.UpdatedTimestamp,
			}
		}

		decision := decideBaselineFolderEntry(cs.missing, cs.deleted, cs.baseRevision, server)
		switch decision.Action {
		case baselineActionNone:
			if decision.Confirmed && ok && serverDTO != nil {
				// 目录一致：下发确认，供客户端建立目录基线。
				// Folder agrees: confirm so the client can establish a folder baseline.
				enqueue(FolderSyncModify, dto.FolderSyncModifyMessage{
					Path:             server.Path,
					PathHash:         server.PathHash,
					Ctime:            server.Ctime,
					Mtime:            server.Mtime,
					UpdatedTimestamp: server.LastTime,
					Revision:         server.Revision,
				})
				needModifyCount++
			}
		case baselineActionUpload:
			if !hasWritePermission {
				break
			}
			if _, err := folderSvc.UpdateOrCreate(ctx, uid, &dto.FolderCreateRequest{
				Vault:        params.Vault,
				Path:         path,
				PathHash:     cs.pathHash,
				BaseRevision: cs.baseRevision,
				// 条件提交契约必须随请求下发：服务层据此在写临界区内再校验一次令牌。
				// The conditional-commit contract must travel with the request so the
				// service re-checks the token inside its write critical section.
				Baseline: baselineCommitFromFolder(c, cs.baseRevision, false),
			}); err != nil {
				if conflict, ok := isBaselineConflict(err); ok {
					enqueue(SyncConflict, baselineConflictMessage(
						dto.SyncConflictKindFolder, conflict.Reason, path, cs.pathHash, "", "", true, server, "",
					))
					needConflictCount++
				}
				h.logWarn(c, "websocket_router.folder.FolderSync.baseline.UpdateOrCreate", zap.String(logpkg.FieldPath, path), zap.Error(err))
				break
			}
		case baselineActionDelete:
			if !hasWritePermission {
				break
			}
			deleted, delErr := folderSvc.Delete(ctx, uid, &dto.FolderDeleteRequest{
				Vault:        params.Vault,
				Path:         path,
				PathHash:     server.PathHash,
				BaseRevision: cs.baseRevision,
				Baseline:     baselineCommitFromFolder(c, cs.baseRevision, false),
			})
			if delErr != nil {
				if conflict, ok := isBaselineConflict(delErr); ok {
					enqueue(SyncConflict, baselineConflictMessage(
						dto.SyncConflictKindFolder, conflict.Reason, path, cs.pathHash, "", "", true, server, "",
					))
					needConflictCount++
					break
				}
				h.logWarn(c, "websocket_router.folder.FolderSync.baseline.Delete", zap.String(logpkg.FieldPath, path), zap.Error(delErr))
				break
			}
			// 幂等命中（目录已不存在或已是墓碑）不广播删除：与笔记/附件一致。
			// An idempotent hit (missing or already tombstoned) is not broadcast,
			// matching note/file deletion.
			if deleted != nil && !deleted.Unchanged {
				c.BroadcastResponse(code.Success.WithData(dto.FolderSyncDeleteMessage{
					Path:             deleted.Path,
					PathHash:         deleted.PathHash,
					Ctime:            deleted.Ctime,
					Mtime:            deleted.Mtime,
					UpdatedTimestamp: deleted.UpdatedTimestamp,
					Revision:         deleted.Revision,
				}).WithVault(params.Vault).WithContext(params.Context), true, FolderSyncDelete)
			}
		case baselineActionApplyDelete:
			enqueue(FolderSyncDelete, dto.FolderSyncDeleteMessage{
				Path:             server.Path,
				PathHash:         server.PathHash,
				Ctime:            server.Ctime,
				Mtime:            server.Mtime,
				UpdatedTimestamp: server.LastTime,
				Revision:         server.Revision,
			})
			needDeleteCount++
		case baselineActionConfirmDelete:
			// both sides already deleted
		case baselineActionConflict:
			enqueue(SyncConflict, baselineConflictMessage(
				dto.SyncConflictKindFolder, decision.Reason, path, cs.pathHash, "", "", true, server, "",
			))
			needConflictCount++
		}
	}

	for path, serverDTO := range serverByPath {
		if _, done := handled[path]; done {
			continue
		}
		if serverDTO == nil {
			continue
		}
		if serverDTO.Action == "delete" {
			enqueue(FolderSyncDelete, dto.FolderSyncDeleteMessage{
				Path:             serverDTO.Path,
				PathHash:         serverDTO.PathHash,
				Ctime:            serverDTO.Ctime,
				Mtime:            serverDTO.Mtime,
				UpdatedTimestamp: serverDTO.UpdatedTimestamp,
				Revision:         serverDTO.Revision,
			})
			needDeleteCount++
			continue
		}
		enqueue(FolderSyncModify, dto.FolderSyncModifyMessage{
			Path:             serverDTO.Path,
			PathHash:         serverDTO.PathHash,
			Ctime:            serverDTO.Ctime,
			Mtime:            serverDTO.Mtime,
			UpdatedTimestamp: serverDTO.UpdatedTimestamp,
			Revision:         serverDTO.Revision,
		})
		needModifyCount++
	}

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
			TypeName:     "folder",
			Vault:        params.Vault,
			MessageQueue: messageQueue,
			PageSize:     pageSize,
			Window:       window,
		}
		entry.ConnDone = connDone(c)
		syncDownloadStore(params.Context, "folder", entry)
	}

	c.ToResponse(code.Success.WithData(dto.FolderSyncEndMessage{
		LastTime:          syncStartTime,
		NeedModifyCount:   needModifyCount,
		NeedDeleteCount:   needDeleteCount,
		NeedConflictCount: needConflictCount,
	}).WithVault(params.Vault).WithContext(params.Context), FolderSyncEnd)
}
