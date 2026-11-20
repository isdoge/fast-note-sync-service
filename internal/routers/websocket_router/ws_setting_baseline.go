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

// doSettingSyncBaseline runs the baseline-mode decision engine for settings. The
// legacy `cover` flag is ignored in this mode: it must never bypass the baseline
// protection (#217 double edit silently overwritten by mtime).
// doSettingSyncBaseline 执行配置的基线模式决策引擎。新模式忽略旧 `cover` 标志：
// 它绝不能绕过基线保护（#217 多设备双改被 mtime 静默覆盖）。
func (h *SettingWSHandler) doSettingSyncBaseline(c *pkgapp.WebsocketClient, params *dto.SettingSyncRequest) {
	syncDownloadDropStale(connDone(c), "setting", params.Context)
	ctx := c.Context()
	uid := c.User.UID

	h.App.VaultService.GetOrCreate(ctx, uid, params.Vault)
	settingSvc := h.App.GetSettingService(c.ClientType(), c.ClientName(), c.ClientVersion())

	syncStartTime := timex.Now().UnixMilli()
	list, err := settingSvc.Sync(ctx, uid, params)
	if err != nil {
		h.respondError(c, code.ErrorSettingListFailed, err, "websocket_router.setting.SettingSync.Sync")
		return
	}

	type clientState struct {
		pathHash string
		state    baselineClientState
	}
	clients := make(map[string]*clientState, len(params.Settings)+len(params.DelSettings)+len(params.MissingSettings))
	newState := func(pathHash, baseHash string, baseHashMissing bool, baseRevision, contentHash string) *clientState {
		hash, missing := dto.NormalizeBaseline(baseHash, baseHashMissing)
		return &clientState{pathHash: pathHash, state: baselineClientState{
			LocalHash:    contentHash,
			BaseHash:     hash,
			HasBase:      !missing,
			BaseRevision: baseRevision,
			IsFile:       true,
		}}
	}
	for _, s := range params.Settings {
		if !validWSPath(s.Path) {
			continue
		}
		clients[s.Path] = newState(s.PathHash, s.BaseHash, s.BaseHashMissing, s.BaseRevision, s.ContentHash)
	}
	for _, s := range params.DelSettings {
		if !validWSPath(s.Path) {
			continue
		}
		cs := newState(s.PathHash, s.BaseHash, s.BaseHashMissing, s.BaseRevision, "")
		cs.state.Deleted = true
		clients[s.Path] = cs
	}
	for _, s := range params.MissingSettings {
		if !validWSPath(s.Path) {
			continue
		}
		cs := newState(s.PathHash, s.BaseHash, s.BaseHashMissing, s.BaseRevision, "")
		cs.state.Missing = true
		clients[s.Path] = cs
	}

	serverByPathHash := make(map[string]*dto.SettingDTO, len(list))
	for _, s := range list {
		if prev, ok := serverByPathHash[s.PathHash]; !ok || s.UpdatedTimestamp > prev.UpdatedTimestamp {
			serverByPathHash[s.PathHash] = s
		}
	}

	// 未命中增量结果的客户端条目：按路径哈希批量查当前元数据（含墓碑）。
	// Client entries outside the delta: batch-query current metadata (tombstones included).
	missingHashes := make([]string, 0, len(clients))
	for _, cs := range clients {
		if _, ok := serverByPathHash[cs.pathHash]; !ok && cs.pathHash != "" {
			missingHashes = append(missingHashes, cs.pathHash)
		}
	}
	if len(missingHashes) > 0 {
		meta, metaErr := settingSvc.GetByPathHashesMetaBatch(ctx, uid, params.Vault, missingHashes)
		if metaErr != nil {
			h.respondError(c, code.ErrorSettingListFailed, metaErr, "websocket_router.setting.SettingSync.GetByPathHashesMetaBatch")
			return
		}
		for hash, s := range meta {
			if s != nil {
				serverByPathHash[hash] = s
			}
		}
	}

	var messageQueue []dto.WSQueuedMessage
	var needUploadCount, needModifyCount, needSyncMtimeCount, needDeleteCount, needConflictCount int64
	handled := make(map[string]struct{}, len(serverByPathHash))
	hasWritePermission := pkgapp.VerifyPermissions(c.Scope, "ws", c.ClientType(), "config_w")

	enqueue := func(action WebSocketSendAction, data any) {
		messageQueue = append(messageQueue, dto.WSQueuedMessage{Context: params.Context, Action: action, Data: data})
	}

	for path, cs := range clients {
		if path == "" {
			continue
		}
		serverDTO, ok := serverByPathHash[cs.pathHash]
		if ok && serverDTO != nil {
			handled[cs.pathHash] = struct{}{}
		}

		server := baselineMeta{}
		if ok && serverDTO != nil {
			server = baselineMetaFromNote(true, serverDTO.Action == "delete", serverDTO.ContentHash,
				serverDTO.Revision, serverDTO.Path, serverDTO.PathHash,
				serverDTO.Ctime, serverDTO.Mtime, 0, serverDTO.UpdatedTimestamp)
		}

		decision := decideBaseline(cs.state, server)
		switch decision.Action {
		case baselineActionNone:
			if decision.Confirmed {
				enqueue(SettingSyncMtime, dto.SettingSyncMtimeMessage{
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
			enqueue(SettingSyncModify, dto.SettingSyncModifyMessage{
				Vault:            params.Vault,
				Path:             server.Path,
				PathHash:         server.PathHash,
				Content:          serverDTO.Content,
				ContentHash:      server.ContentHash,
				Ctime:            server.Ctime,
				Mtime:            server.Mtime,
				UpdatedTimestamp: server.LastTime,
				Revision:         server.Revision,
			})
			needModifyCount++
		case baselineActionUpload:
			if !hasWritePermission {
				h.logWarn(c, "websocket_router.setting.SettingSync: permission denied for upload", zap.String(logger.FieldPath, path))
				break
			}
			enqueue(SettingSyncNeedUpload, dto.SettingSyncNeedUploadMessage{Path: path})
			needUploadCount++
		case baselineActionDelete:
			delParams := &dto.SettingDeleteRequest{
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
			deleted, delErr := settingSvc.Delete(ctx, uid, delParams)
			if delErr != nil {
				h.logWarn(c, "websocket_router.setting.SettingSync.baseline.Delete", zap.String(logger.FieldPath, path), zap.Error(delErr))
				break
			}
			if deleted != nil && !deleted.Unchanged {
				safego.Go(h.App.Logger(), func() {
					c.BroadcastResponse(code.Success.WithData(dto.SettingSyncDeleteMessage{
						Path:             deleted.Path,
						PathHash:         deleted.PathHash,
						Ctime:            deleted.Ctime,
						Mtime:            deleted.Mtime,
						UpdatedTimestamp: deleted.UpdatedTimestamp,
						Revision:         deleted.Revision,
						ContentHash:      deleted.ContentHash,
					}).WithVault(params.Vault), true, SettingSyncDelete)
				})
			}
		case baselineActionApplyDelete:
			enqueue(SettingSyncDelete, dto.SettingSyncDeleteMessage{
				Path:             server.Path,
				PathHash:         server.PathHash,
				Ctime:            server.Ctime,
				Mtime:            server.Mtime,
				UpdatedTimestamp: server.LastTime,
				Revision:         server.Revision,
				ContentHash:      server.ContentHash,
			})
			needDeleteCount++
		case baselineActionConfirmDelete:
			// both sides already deleted
		case baselineActionConflict:
			serverContent := ""
			if serverDTO != nil && serverDTO.Action != "delete" {
				serverContent = serverDTO.Content
			}
			enqueue(SyncConflict, baselineConflictMessage(
				dto.SyncConflictKindSetting, decision.Reason, path, server.PathHash,
				cs.state.BaseHash, "", true, server, serverContent,
			))
			needConflictCount++
		}
	}

	for pathHash, serverDTO := range serverByPathHash {
		if _, done := handled[pathHash]; done {
			continue
		}
		if serverDTO == nil {
			continue
		}
		if serverDTO.Action == "delete" {
			enqueue(SettingSyncDelete, dto.SettingSyncDeleteMessage{
				Path:             serverDTO.Path,
				PathHash:         serverDTO.PathHash,
				Ctime:            serverDTO.Ctime,
				Mtime:            serverDTO.Mtime,
				UpdatedTimestamp: serverDTO.UpdatedTimestamp,
				Revision:         serverDTO.Revision,
				ContentHash:      serverDTO.ContentHash,
			})
			needDeleteCount++
			continue
		}
		enqueue(SettingSyncModify, dto.SettingSyncModifyMessage{
			Vault:            params.Vault,
			Path:             serverDTO.Path,
			PathHash:         serverDTO.PathHash,
			Content:          serverDTO.Content,
			ContentHash:      serverDTO.ContentHash,
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
			TypeName:     "setting",
			Vault:        params.Vault,
			MessageQueue: messageQueue,
			PageSize:     pageSize,
			Window:       window,
		}
		entry.ConnDone = connDone(c)
		syncDownloadStore(params.Context, "setting", entry)
	}

	c.ToResponse(code.Success.WithData(dto.SettingSyncEndMessage{
		LastTime:           syncStartTime,
		NeedUploadCount:    needUploadCount,
		NeedModifyCount:    needModifyCount,
		NeedSyncMtimeCount: needSyncMtimeCount,
		NeedDeleteCount:    needDeleteCount,
		NeedConflictCount:  needConflictCount,
	}).WithVault(params.Vault).WithContext(params.Context), SettingSyncEnd)
}
