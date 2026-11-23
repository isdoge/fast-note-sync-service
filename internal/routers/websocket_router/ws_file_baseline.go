package websocket_router

import (
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/logger"
	"github.com/haierkeys/fast-note-sync-service/pkg/safego"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"go.uber.org/zap"
)

// doFileSyncBaseline runs the baseline-mode decision engine for attachments.
// Attachment bytes are never inlined into conflict payloads: the remote version is
// fetched through the existing download flow, which binds to serverRevision.
// doFileSyncBaseline 执行附件的基线模式决策引擎。冲突载荷绝不内联附件二进制：
// 远端版本通过现有下载流程取得，并绑定 serverRevision。
func (h *FileWSHandler) doFileSyncBaseline(c *pkgapp.WebsocketClient, params *dto.FileSyncRequest) {
	syncDownloadDropStale(connDone(c), "file", params.Context)
	ctx := c.Context()
	uid := c.User.UID

	h.App.VaultService.GetOrCreate(ctx, uid, params.Vault)
	fileService := h.App.GetFileService(c.ClientType(), c.ClientName(), c.ClientVersion())

	syncStartTime := timex.Now().UnixMilli()
	list, err := fileService.ListByLastTime(ctx, uid, params)
	if err != nil {
		h.respondError(c, code.ErrorFileListFailed, err, "websocket_router.file.FileSync.ListByLastTime")
		return
	}

	type clientState struct {
		pathHash string
		state    baselineClientState
		// size/ctime/mtime are the client-reported attributes of the local version.
		// They must drive the upload session: using the server row's values makes a
		// new attachment (server absent → 0 bytes) or a larger replacement finish
		// after the first chunk and persist a truncated size.
		// size/ctime/mtime 是客户端上报的本地版本属性，必须用于创建上传会话；
		// 若沿用服务端旧行（新建时不存在 → 0 字节），会话分片数会错，首个分片即判定
		// 完成，落库大小错误且内容被截断。
		size, ctime, mtime int64
	}
	clients := make(map[string]*clientState, len(params.Files)+len(params.DelFiles)+len(params.MissingFiles))
	newState := func(pathHash, baseHash string, baseHashMissing bool, baseRevision, contentHash string, size, ctime, mtime int64) *clientState {
		hash, missing := dto.NormalizeBaseline(baseHash, baseHashMissing)
		return &clientState{pathHash: pathHash, size: size, ctime: ctime, mtime: mtime, state: baselineClientState{
			LocalHash:    contentHash,
			BaseHash:     hash,
			HasBase:      !missing,
			BaseRevision: baseRevision,
			IsFile:       true,
		}}
	}
	for _, f := range params.Files {
		clients[f.Path] = newState(f.PathHash, f.BaseHash, f.BaseHashMissing, f.BaseRevision, f.ContentHash,
			f.Size, timex.NormalizeMilli(f.Ctime), timex.NormalizeMilli(f.Mtime))
	}
	for _, f := range params.DelFiles {
		cs := newState(f.PathHash, f.BaseHash, f.BaseHashMissing, f.BaseRevision, "", 0, 0, 0)
		cs.state.Deleted = true
		clients[f.Path] = cs
	}
	for _, f := range params.MissingFiles {
		cs := newState(f.PathHash, f.BaseHash, f.BaseHashMissing, f.BaseRevision, "", 0, 0, 0)
		cs.state.Missing = true
		clients[f.Path] = cs
	}

	serverByPath := make(map[string]*dto.FileDTO, len(list))
	for _, f := range list {
		if prev, ok := serverByPath[f.Path]; !ok || f.UpdatedTimestamp > prev.UpdatedTimestamp {
			serverByPath[f.Path] = f
		}
	}

	missingPaths := make([]string, 0, len(clients))
	for path := range clients {
		if _, ok := serverByPath[path]; !ok && path != "" {
			missingPaths = append(missingPaths, path)
		}
	}
	if len(missingPaths) > 0 {
		meta, metaErr := fileService.GetPathsMetaBatch(ctx, uid, params.Vault, missingPaths)
		if metaErr != nil {
			h.respondError(c, code.ErrorFileListFailed, metaErr, "websocket_router.file.FileSync.GetPathsMetaBatch")
			return
		}
		for path, f := range meta {
			if f != nil {
				serverByPath[path] = f
			}
		}
	}

	var messageQueue []dto.WSQueuedMessage
	var needUploadCount, needModifyCount, needSyncMtimeCount, needDeleteCount, needConflictCount int64
	handled := make(map[string]struct{}, len(serverByPath))
	hasWritePermission := pkgapp.VerifyPermissions(c.Scope, "ws", c.ClientType(), "file_w")

	enqueue := func(action WebSocketSendAction, data any) {
		messageQueue = append(messageQueue, dto.WSQueuedMessage{Context: params.Context, Action: action, Data: data})
	}

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
		}

		decision := decideBaseline(cs.state, server)
		switch decision.Action {
		case baselineActionNone:
			if decision.Confirmed {
				enqueue(FileSyncMtime, dto.FileSyncMtimeMessage{
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
			enqueue(FileSyncUpdate, dto.FileSyncModifyMessage{
				Path:             server.Path,
				PathHash:         server.PathHash,
				ContentHash:      server.ContentHash,
				Size:             server.Size,
				Ctime:            server.Ctime,
				Mtime:            server.Mtime,
				UpdatedTimestamp: server.LastTime,
				Revision:         server.Revision,
			})
			needModifyCount++
		case baselineActionUpload:
			// 服务端没有该路径时，系统垃圾文件不再发起上传会话；老客户端每轮上报
			// 本地垃圾文件，会话只会被写入入口拒收。服务端已有垃圾行保持原有决策。
			if !server.Found && h.writeRejectsSystemJunk(path) {
				break
			}
			if !hasWritePermission {
				h.logWarn(c, "websocket_router.file.FileSync: permission denied for upload", zap.String(logger.FieldPath, path))
				break
			}
			// 上传会话必须按客户端上报的版本属性建立；服务端行可能不存在（Size=0）
			// 或仍是旧版本大小，拿它算分片数会提前判完成并截断内容。
			// Build the session from the client-reported attributes: the server row may
			// be absent (Size=0) or still describe the old version, and its size would
			// finish the upload early and truncate the content.
			session, ferr := h.handleFileUploadSessionCreateWithBaseline(c, params.Vault, path, cs.pathHash, cs.state.LocalHash, cs.size, cs.ctime, cs.mtime, params.Context, cs.state)
			if ferr != nil {
				h.logError(c, "websocket_router.file.FileSync.handleFileUploadSession", ferr)
				break
			}
			enqueue(FileUpload, dto.FileSyncUploadMessage{
				Path:      session.Path,
				PathHash:  session.PathHash,
				SessionID: session.ID,
				ChunkSize: session.ChunkSize,
			})
			needUploadCount++
		case baselineActionDelete:
			delParams := &dto.FileDeleteRequest{
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
			deleted, delErr := fileService.Delete(ctx, uid, delParams)
			if delErr != nil {
				h.logWarn(c, "websocket_router.file.FileSync.baseline.Delete", zap.String(logger.FieldPath, path), zap.Error(delErr))
				break
			}
			if deleted != nil && !deleted.Unchanged {
				safego.Go(h.App.Logger(), func() {
					c.BroadcastResponse(code.Success.WithData(dto.FileSyncDeleteMessage{
						Path:             deleted.Path,
						PathHash:         deleted.PathHash,
						Ctime:            deleted.Ctime,
						Mtime:            deleted.Mtime,
						Size:             deleted.Size,
						UpdatedTimestamp: deleted.UpdatedTimestamp,
						Revision:         deleted.Revision,
						ContentHash:      deleted.ContentHash,
					}).WithVault(params.Vault), true, FileSyncDelete)
				})
			}
		case baselineActionApplyDelete:
			enqueue(FileSyncDelete, dto.FileSyncDeleteMessage{
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
			// both sides already deleted
		case baselineActionConflict:
			enqueue(SyncConflict, baselineConflictMessage(
				dto.SyncConflictKindFile, decision.Reason, path, server.PathHash,
				cs.state.BaseHash, "", true, server, "",
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
			enqueue(FileSyncDelete, dto.FileSyncDeleteMessage{
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
		enqueue(FileSyncUpdate, dto.FileSyncModifyMessage{
			Path:             serverDTO.Path,
			PathHash:         serverDTO.PathHash,
			ContentHash:      serverDTO.ContentHash,
			Size:             serverDTO.Size,
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
			TypeName:     "file",
			Vault:        params.Vault,
			MessageQueue: messageQueue,
			PageSize:     pageSize,
			Window:       window,
		}
		entry.ConnDone = connDone(c)
		syncDownloadStore(params.Context, "file", entry)
	}

	c.ToResponse(code.Success.WithData(dto.FileSyncEndMessage{
		LastTime:           syncStartTime,
		NeedUploadCount:    needUploadCount,
		NeedModifyCount:    needModifyCount,
		NeedSyncMtimeCount: needSyncMtimeCount,
		NeedDeleteCount:    needDeleteCount,
		NeedConflictCount:  needConflictCount,
	}).WithVault(params.Vault).WithContext(params.Context), FileSyncEnd)
}

// handleFileUploadSessionCreateWithBaseline binds the upload session to the client's
// baseline so the final chunk commit can re-check the server revision.
// handleFileUploadSessionCreateWithBaseline 把上传会话绑定到客户端基线，供最终分片提交时
// 重新校验服务端版本。
func (h *FileWSHandler) handleFileUploadSessionCreateWithBaseline(c *pkgapp.WebsocketClient, vault, path, pathHash, contentHash string, size, ctime, mtime int64, context string, state baselineClientState) (*FileUploadBinaryChunkSession, error) {
	session, err := h.handleFileUploadSessionCreate(c, vault, path, pathHash, contentHash, size, ctime, mtime, context)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	session.SyncBaselineVersion = c.SyncBaselineVersion()
	session.BaseHash = state.BaseHash
	session.BaseHashMissing = !state.HasBase
	session.BaseRevision = state.BaseRevision
	session.IsConflictResolved = state.IsConflictResolved
	session.mu.Unlock()
	return session, nil
}
