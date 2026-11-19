package websocket_router

import (
	"time"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/convert"
	"github.com/haierkeys/fast-note-sync-service/pkg/logger"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"go.uber.org/zap"
)

// SettingWSHandler WebSocket setting handler
// SettingWSHandler WebSocket 配置处理器
// Uses App Container to inject dependencies
// 使用 App Container 注入依赖
type SettingWSHandler struct {
	*WSHandler
}

// NewSettingWSHandler creates SettingWSHandler instance
// NewSettingWSHandler 创建 SettingWSHandler 实例
func NewSettingWSHandler(a *app.App) *SettingWSHandler {
	return &SettingWSHandler{
		WSHandler: NewWSHandler(a),
	}
}

// SettingModify handles setting modification messages
// SettingModify 处理配置修改消息
func (h *SettingWSHandler) SettingModify(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SettingModifyOrCreateRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.setting.SettingModify.BindAndValid", msg)
		return
	}

	// 只校验、不改写客户端路径，与笔记/附件入口保持一致。
	if !validWSPath(params.Path) {
		c.ToResponse(code.ErrorInvalidPath.WithDetails("invalid setting path"))
		return
	}

	params.Ctime = timex.NormalizeMilli(params.Ctime)
	params.Mtime = timex.NormalizeMilli(params.Mtime)
	// mtime 缺省（<=0）时用服务端当前毫秒时间兜底：写 0 会让后续窗口内比较
	// s.Mtime >= cSetting.Mtime 恒为假，服务端新改的配置被误判成"客户端更新"。
	// ctime 不兜底，保持客户端上报值。
	// Fall back to the server's current millisecond time when mtime is missing
	// (<=0): storing 0 makes the in-window comparison s.Mtime >= cSetting.Mtime
	// always false, misjudging freshly server-updated settings as "client newer".
	// ctime keeps the client-reported value.
	if params.Mtime <= 0 {
		params.Mtime = timex.Now().UnixMilli()
	}

	ctx := c.Context()

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "SettingModify", params.Path, params.Vault)

	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	settingSvc := h.App.GetSettingService(c.ClientType(), c.ClientName(), c.ClientVersion())

	checkParams := convert.StructAssign(params, &dto.SettingUpdateCheckRequest{}).(*dto.SettingUpdateCheckRequest)
	updateMode, settingCheck, err := settingSvc.UpdateCheck(ctx, c.User.UID, checkParams)
	if err != nil {
		h.respondErrorWithPath(c, code.ErrorSettingModifyOrCreateFailed, err, params.Path, msg.Type, "websocket_router.setting.SettingModify.UpdateCheck")
		return
	}

	switch updateMode {
	case "UpdateContent", "Create":
		_, setting, err := settingSvc.ModifyOrCreate(ctx, c.User.UID, params, true)
		if err != nil {
			h.respondErrorWithPath(c, code.ErrorSettingModifyOrCreateFailed, err, params.Path, msg.Type, "websocket_router.setting.SettingModify.ModifyOrCreate")
			return
		}

		c.ToResponse(code.Success.WithData(dto.SettingModifyAckMessage{
			LastTime: setting.UpdatedTimestamp,
			Path:     setting.Path,
			PathHash: setting.PathHash,
		}).WithVault(params.Vault).WithContext(params.Context), string(SettingModifyAck))
		c.BroadcastResponse(code.Success.WithData(
			dto.SettingSyncModifyMessage{
				Vault:            params.Vault,
				Path:             setting.Path,
				PathHash:         setting.PathHash,
				Content:          setting.Content,
				ContentHash:      setting.ContentHash,
				Ctime:            setting.Ctime,
				Mtime:            setting.Mtime,
				UpdatedTimestamp: setting.UpdatedTimestamp,
			},
		).WithVault(params.Vault), true, SettingSyncModify)
		return

	case "UpdateMtime":
		c.ToResponse(code.Success.WithData(dto.SettingModifyAckMessage{
			LastTime: settingCheck.UpdatedTimestamp,
			Path:     settingCheck.Path,
			PathHash: settingCheck.PathHash,
		}).WithVault(params.Vault).WithContext(params.Context), string(SettingModifyAck))
		return
	default:
		c.ToResponse(code.SuccessNoUpdate.WithData(dto.SettingModifyAckMessage{
			LastTime: params.Mtime,
			Path:     params.Path,
			PathHash: params.PathHash,
		}).WithVault(params.Vault).WithContext(params.Context), string(SettingModifyAck))
		return
	}
}

// SettingModifyCheck checks the necessity of setting modification
// SettingModifyCheck 检查配置修改必要性
func (h *SettingWSHandler) SettingModifyCheck(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SettingUpdateCheckRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.setting.SettingModifyCheck.BindAndValid", msg)
		return
	}

	// 只校验、不改写客户端路径，与笔记/附件入口保持一致。
	if !validWSPath(params.Path) {
		c.ToResponse(code.ErrorInvalidPath.WithDetails("invalid setting path"))
		return
	}

	params.Ctime = timex.NormalizeMilli(params.Ctime)
	params.Mtime = timex.NormalizeMilli(params.Mtime)

	ctx := c.Context()

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "SettingModifyCheck", params.Path, params.Vault)

	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	settingSvc := h.App.GetSettingService(c.ClientType(), c.ClientName(), c.ClientVersion())

	updateMode, settingCheck, err := settingSvc.UpdateCheck(ctx, c.User.UID, params)
	if err != nil {
		h.respondError(c, code.ErrorSettingUpdateCheckFailed, err, "websocket_router.setting.SettingModifyCheck.UpdateCheck")
		return
	}

	switch updateMode {
	case "UpdateContent", "Create":
		c.ToResponse(code.Success.WithData(
			dto.SettingSyncNeedUploadMessage{
				Path: settingCheck.Path,
			},
		), SettingSyncNeedUpload)
		return
	case "UpdateMtime":
		c.ToResponse(code.Success.WithData(
			dto.SettingSyncMtimeMessage{
				Path:             settingCheck.Path,
				Ctime:            settingCheck.Ctime,
				Mtime:            settingCheck.Mtime,
				UpdatedTimestamp: settingCheck.UpdatedTimestamp,
			},
		), SettingSyncMtime)
		return
	default:
		c.ToResponse(code.SuccessNoUpdate.WithVault(params.Vault))
		return
	}
}

// SettingDelete handles setting deletion messages
// SettingDelete 处理配置删除消息
func (h *SettingWSHandler) SettingDelete(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SettingDeleteRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.setting.SettingDelete.BindAndValid", msg)
		return
	}

	// 只校验、不改写客户端路径，与笔记/附件入口保持一致。
	if !validWSPath(params.Path) {
		c.ToResponse(code.ErrorInvalidPath.WithDetails("invalid setting path"))
		return
	}

	ctx := c.Context()

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "SettingDelete", params.Path, params.Vault)

	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	settingSvc := h.App.GetSettingService(c.ClientType(), c.ClientName(), c.ClientVersion())

	setting, err := settingSvc.Delete(ctx, c.User.UID, params)
	if err != nil {
		h.respondErrorWithPath(c, code.ErrorSettingDeleteFailed, err, params.Path, msg.Type, "websocket_router.setting.SettingDelete.Delete")
		return
	}

	c.ToResponse(code.Success.WithData(dto.SettingDeleteAckMessage{
		LastTime: setting.UpdatedTimestamp,
		Path:     setting.Path,
		PathHash: setting.PathHash,
	}).WithVault(params.Vault).WithContext(params.Context), string(SettingDeleteAck))
	c.BroadcastResponse(code.Success.WithData(
		dto.SettingSyncDeleteMessage{
			Path:             setting.Path,
			PathHash:         setting.PathHash,
			Ctime:            setting.Ctime,
			Mtime:            setting.Mtime,
			UpdatedTimestamp: setting.UpdatedTimestamp,
		},
	).WithVault(params.Vault), true, SettingSyncDelete)
}

// SettingSync handles setting synchronization messages
// SettingSync 处理配置同步消息
func (h *SettingWSHandler) SettingSync(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SettingSyncRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.setting.SettingSync.BindAndValid", msg)
		return
	}

	// 分批协议快速路径：totalBatches <= 1 时直接执行，无需缓存归集
	// Fast path: totalBatches <= 1 means single batch, skip cache accumulation
	if params.TotalBatches > 1 {
		entry, created := syncBatchGetOrCreate(params.Context, "setting", params.TotalBatches)
		if created {
			// 观测用：若此 context+type 早已集齐并被清理，这里会是迟到重传重建的孤儿 entry
			// （见同步流水线设计 §3.3 第 2 点），5min TTL 会自动回收，不做额外防护
			// Observability: if this context+type had already been collected and cleaned up,
			// this is a late-retransmit rebuild of an orphan entry (design §3.3 point 2);
			// the 5-minute TTL reclaims it automatically, no extra protection needed
			h.App.Logger().Debug("websocket_router.setting.SettingSync: created new batch cache entry",
				zap.String(logger.FieldTraceID, c.TraceID),
				zap.String("context", params.Context),
				zap.Int("batchIndex", params.BatchIndex),
				zap.Int("totalBatches", params.TotalBatches))
		}

		entry.mu.Lock()
		// 重复 BatchIndex（客户端因未收到 ack 而重传）时跳过 append/计数，只重发 ack
		// Duplicate BatchIndex (client retransmitted after missing the ack): skip append/count, just resend the ack
		if !entry.markBatchReceived(params.BatchIndex) {
			for _, s := range params.Settings {
				entry.Items = append(entry.Items, s)
			}
			entry.ReceivedCount++
			for _, ds := range params.DelSettings {
				entry.DelItems = append(entry.DelItems, ds)
			}
			for _, ms := range params.MissingSettings {
				entry.MissingItems = append(entry.MissingItems, ms)
			}
			entry.UpdatedAt = time.Now()
		}
		received := entry.ReceivedCount
		total := entry.TotalBatches
		entry.mu.Unlock()

		// 无条件先回 BatchAck（含集齐的最后一批）：旧客户端把多出的最后一批 ack 静默丢弃
		// （无监听者的 emit，见设计 §2.1 事实4），新客户端窗口协议靠它滑动
		// Unconditionally send BatchAck first (including the batch that completes collection):
		// old clients silently drop the extra ack for the last batch (emit with no listener,
		// design §2.1 fact 4); new clients rely on it to slide the window
		c.ToResponse(code.Success.WithData(map[string]interface{}{
			"context":    params.Context,
			"batchIndex": params.BatchIndex,
		}).WithVault(params.Vault).WithContext(params.Context), SettingSyncBatchAck)

		if received < total {
			// 未集齐：等待其余批次
			// Not all batches received yet: wait for the rest
			return
		}

		// 全部批次到达：从缓存中提取数据，清理缓存，执行差量比对
		// All batches received: extract from cache, delete cache, run differential sync
		syncBatchDelete(params.Context, "setting")
		allSettings := make([]dto.SettingSyncCheckRequest, 0, len(entry.Items))
		for _, item := range entry.Items {
			allSettings = append(allSettings, item.(dto.SettingSyncCheckRequest))
		}
		params.Settings = allSettings

		allDelSettings := make([]dto.SettingSyncDelSetting, 0, len(entry.DelItems))
		for _, item := range entry.DelItems {
			allDelSettings = append(allDelSettings, item.(dto.SettingSyncDelSetting))
		}
		params.DelSettings = allDelSettings

		allMissingSettings := make([]dto.SettingSyncDelSetting, 0, len(entry.MissingItems))
		for _, item := range entry.MissingItems {
			allMissingSettings = append(allMissingSettings, item.(dto.SettingSyncDelSetting))
		}
		params.MissingSettings = allMissingSettings
	}

	h.doSettingSync(c, params)
}

func (h *SettingWSHandler) doSettingSync(c *pkgapp.WebsocketClient, params *dto.SettingSyncRequest) {
	// 新一轮同步取代本连接该类型的旧轮：清理旧轮残留的下行分页条目
	syncDownloadDropStale(connDone(c), "setting", params.Context)
	ctx := c.Context()

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "SettingSync", "", params.Vault)

	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	settingSvc := h.App.GetSettingService(c.ClientType(), c.ClientName(), c.ClientVersion())

	// Record sync start time before querying to avoid missing writes that occur during query processing.
	// 查询前记录同步开始时间，防止查询处理期间的写入被遗漏（经典增量同步快照时间戳方案）。
	syncStartTime := timex.Now().UnixMilli()

	list, err := settingSvc.Sync(ctx, c.User.UID, params)
	if err != nil {
		h.respondError(c, code.ErrorSettingListFailed, err, "websocket_router.setting.SettingSync.Sync")
		return
	}

	cSettings := make(map[string]dto.SettingSyncCheckRequest)
	cSettingsKeys := make(map[string]struct{})
	for _, s := range params.Settings {
		if !validWSPath(s.Path) {
			h.App.Logger().Warn("websocket_router.setting.SettingSync: invalid setting path, skip entry",
				zap.String(logger.FieldTraceID, c.TraceID),
				zap.String(logger.FieldPath, s.Path))
			continue
		}
		s.Mtime = timex.NormalizeMilli(s.Mtime)
		cSettings[s.PathHash] = s
		cSettingsKeys[s.PathHash] = struct{}{}
	}

	// Create message queue for collecting all messages to be sent
	// 创建消息队列，用于收集所有待发送的消息
	// Check and create vault, internally uses SF to merge concurrent requests, avoiding duplicate creation issues
	// 检查并创建仓库，内部使用SF合并并发请求, 避免重复创建问题
	var messageQueue []dto.WSQueuedMessage

	var lastTime int64
	var needUploadCount int64
	var needModifyCount int64
	var needSyncMtimeCount int64
	var needDeleteCount int64

	var cDelSettingsKeys map[string]struct{} = make(map[string]struct{}, 0)

	// Handle settings deleted by client
	// 处理客户端删除的配置
	if len(params.DelSettings) > 0 {
		hasWritePermission := pkgapp.VerifyPermissions(c.Scope, "ws", c.ClientType(), "config_w")

		for _, delSetting := range params.DelSettings {
			if !validWSPath(delSetting.Path) {
				h.App.Logger().Warn("websocket_router.setting.SettingSync: invalid setting path, skip entry",
					zap.String(logger.FieldTraceID, c.TraceID),
					zap.String(logger.FieldPath, delSetting.Path))
				continue
			}

			// Check if setting exists before deleting
			getCheckParams := &dto.SettingGetRequest{
				Vault:    params.Vault,
				PathHash: delSetting.PathHash,
			}
			checkSetting, err := settingSvc.Get(ctx, c.User.UID, getCheckParams)

			if err == nil && checkSetting != nil && checkSetting.Action != "delete" {
				if !hasWritePermission {
					h.App.Logger().Warn("websocket_router.setting.SettingSync: permission denied for deletion",
						zap.String(logger.FieldTraceID, c.TraceID),
						zap.Int64(logger.FieldUID, c.User.UID),
						zap.String(logger.FieldPath, delSetting.Path))
					continue
				}

				delParams := &dto.SettingDeleteRequest{
					Vault:    params.Vault,
					Path:     delSetting.Path,
					PathHash: delSetting.PathHash,
				}
				setting, err := settingSvc.Delete(ctx, c.User.UID, delParams)
				if err != nil {
					h.App.Logger().Error("websocket_router.setting.SettingSync.SettingService.Delete",
						zap.String(logger.FieldTraceID, c.TraceID),
						zap.Int64(logger.FieldUID, c.User.UID),
						zap.String(logger.FieldPath, delSetting.Path),
						zap.Error(err))
					continue
				}

				// 记录客户端已主动删除的 PathHash,避免重复下发
				cDelSettingsKeys[delSetting.PathHash] = struct{}{}
				// Broadcast deletion to other clients
				// 将删除消息广播给其他客户端
				c.BroadcastResponse(code.Success.WithData(
					dto.SettingSyncDeleteMessage{
						Path: setting.Path,
					},
				).WithVault(params.Vault), true, SettingSyncDelete)
			} else {
				h.App.Logger().Debug("websocket_router.setting.SettingSync.SettingService.Get check failed (not found or already deleted), broadcasting delete anyway",
					zap.String(logger.FieldTraceID, c.TraceID),
					zap.String("pathHash", delSetting.PathHash))

				// Record PathHash
				// 记录 PathHash
				cDelSettingsKeys[delSetting.PathHash] = struct{}{}

				// Broadcast deletion with available info (Path)
				// 使用现有信息(Path)广播删除
				c.BroadcastResponse(code.Success.WithData(
					dto.SettingSyncDeleteMessage{
						Path: delSetting.Path,
					},
				).WithVault(params.Vault), true, SettingSyncDelete)
			}

		}
	}

	// Handle settings missing on client (only for incremental sync)
	// 处理客户端缺失的配置（仅限增量同步）
	if params.LastTime > 0 && len(params.MissingSettings) > 0 {
		for _, missingSetting := range params.MissingSettings {
			if !validWSPath(missingSetting.Path) {
				h.App.Logger().Warn("websocket_router.setting.SettingSync: invalid setting path, skip entry",
					zap.String(logger.FieldTraceID, c.TraceID),
					zap.String(logger.FieldPath, missingSetting.Path))
				continue
			}
			getParams := &dto.SettingGetRequest{
				Vault:    params.Vault,
				PathHash: missingSetting.PathHash,
			}
			setting, err := settingSvc.Get(ctx, c.User.UID, getParams)
			if err != nil {
				h.App.Logger().Warn("websocket_router.setting.SettingSync.SettingService.Get",
					zap.String(logger.FieldTraceID, c.TraceID),
					zap.String("pathHash", missingSetting.PathHash),
					zap.Error(err))
				continue
			}

			if setting != nil && setting.Action != "delete" {

				messageQueue = append(messageQueue, dto.WSQueuedMessage{
						Context: params.Context,
					Action: SettingSyncModify,
					Data: dto.SettingSyncModifyMessage{
						Vault:            params.Vault,
						Path:             setting.Path,
						PathHash:         setting.PathHash,
						Content:          setting.Content,
						ContentHash:      setting.ContentHash,
						Ctime:            setting.Ctime,
						Mtime:            setting.Mtime,
						UpdatedTimestamp: setting.UpdatedTimestamp,
					},
				})
				needModifyCount++
				// 加入排除索引
				cDelSettingsKeys[setting.PathHash] = struct{}{}
			}
		}
	}

	for _, s := range list {
		// 如果该配置是客户端刚才通过参数告知删除的,则跳过下发
		if _, ok := cDelSettingsKeys[s.PathHash]; ok {
			continue
		}

		if s.Action == "delete" {
			// Server already deleted, notify client to delete (regardless of whether client has it)
			// 服务端已经删除，通知客户端删除（不再检查客户端是否存在）
			if _, ok := cSettings[s.PathHash]; ok {
				delete(cSettingsKeys, s.PathHash)
			}
			// 将消息添加到队列
			messageQueue = append(messageQueue, dto.WSQueuedMessage{
						Context: params.Context,
				Action: SettingSyncDelete,
				Data: dto.SettingSyncDeleteMessage{
					Path:             s.Path,
					PathHash:         s.PathHash,
					Ctime:            s.Ctime,
					Mtime:            s.Mtime,
					UpdatedTimestamp: s.UpdatedTimestamp,
				},
			})
			needDeleteCount++
		} else {
			if cSetting, ok := cSettings[s.PathHash]; ok {
				delete(cSettingsKeys, s.PathHash)
				if s.ContentHash == cSetting.ContentHash && s.Mtime == cSetting.Mtime {
					continue
				}
				// 强制覆盖连接端
				if params.Cover {
					// 将消息添加到队列而非立即发送
					messageQueue = append(messageQueue, dto.WSQueuedMessage{
						Context: params.Context,
						Action: SettingSyncModify,
						Data: dto.SettingSyncModifyMessage{
							Vault:            params.Vault,
							Path:             s.Path,
							PathHash:         s.PathHash,
							Content:          s.Content,
							ContentHash:      s.ContentHash,
							Ctime:            s.Ctime,
							Mtime:            s.Mtime,
							UpdatedTimestamp: s.UpdatedTimestamp,
						},
					})
					needModifyCount++
					continue
				}
				// 链接端和服务端， 文件内容相同
				if s.ContentHash != cSetting.ContentHash {
					if s.Mtime >= cSetting.Mtime {
						// Server file mtime is greater than client file mtime, notify client to update
						// 将消息添加到队列而非立即发送
						// 服务端文件 mtime 大于链接端文件 mtime，则通知连接端更新
						// 将消息添加到队列而非立即发送
						messageQueue = append(messageQueue, dto.WSQueuedMessage{
						Context: params.Context,
							Action: SettingSyncModify,
							Data: dto.SettingSyncModifyMessage{
								Vault:            params.Vault,
								Path:             s.Path,
								PathHash:         s.PathHash,
								Content:          s.Content,
								ContentHash:      s.ContentHash,
								Ctime:            s.Ctime,
								Mtime:            s.Mtime,
								UpdatedTimestamp: s.UpdatedTimestamp,
							},
						})
						needModifyCount++
					} else {
						// Server file mtime is less than client file mtime, notify client to update
						// 将消息添加到队列而非立即发送
						// 服务端文件 mtime 小于链接端文件 mtime，则通知连接端更新
						// 将消息添加到队列而非立即发送
						messageQueue = append(messageQueue, dto.WSQueuedMessage{
						Context: params.Context,
							Action: SettingSyncNeedUpload,
							Data: dto.SettingSyncNeedUploadMessage{
								Path: s.Path,
							},
						})
						needUploadCount++
					}
				} else {
					// Client and server have same content, but different mtime
					// 将消息添加到队列而非立即发送
					// 链接端和服务端， 文件内容相同，文件 mtime 时间不同
					// 将消息添加到队列而非立即发送
					messageQueue = append(messageQueue, dto.WSQueuedMessage{
						Context: params.Context,
						Action: SettingSyncMtime,
						Data: dto.SettingSyncMtimeMessage{
							Path:             s.Path,
							Ctime:            s.Ctime,
							Mtime:            s.Mtime,
							UpdatedTimestamp: s.UpdatedTimestamp,
						},
					})
					needSyncMtimeCount++
				}
			} else {
				// 将消息添加到队列而非立即发送
				messageQueue = append(messageQueue, dto.WSQueuedMessage{
						Context: params.Context,
					Action: SettingSyncModify,
					Data: dto.SettingSyncModifyMessage{
						Vault:            params.Vault,
						Path:             s.Path,
						PathHash:         s.PathHash,
						Content:          s.Content,
						ContentHash:      s.ContentHash,
						Ctime:            s.Ctime,
						Mtime:            s.Mtime,
						UpdatedTimestamp: s.UpdatedTimestamp,
					},
				})
				needModifyCount++
			}
		}
	}

	// Use syncStartTime (recorded before query) as lastTime to prevent writes that occurred
	// during query processing from being permanently missed on the next incremental sync.
	// 使用查询前记录的 syncStartTime 作为 lastTime，防止查询处理期间的写入在下次增量同步时被永久遗漏。
	lastTime = syncStartTime
	hasWritePermission := pkgapp.VerifyPermissions(c.Scope, "ws", c.ClientType(), "config_w")

	// 增量同步（lastTime > 0）时，窗口外条目先按路径哈希批量查服务端当前行：
	// 最新一行是存活行且内容哈希、修改时间都与客户端上报一致时静默跳过，
	// 不发上传指令、不打无权限警告。其余情形（服务端没有、墓碑、哈希或修改时间不同）
	// 保持原有判定不变。
	// In incremental sync (lastTime > 0), batch-fetch the server's current rows for
	// out-of-window entries first: when the latest row is alive and both content hash
	// and mtime match the client report, skip silently — no upload directive and no
	// permission-denied warning. All other cases keep the existing verdict.
	var serverSettings map[string]*dto.SettingDTO
	if params.LastTime > 0 && len(cSettingsKeys) > 0 {
		remainingHashes := make([]string, 0, len(cSettingsKeys))
		for pathHash := range cSettingsKeys {
			remainingHashes = append(remainingHashes, pathHash)
		}
		metaMap, metaErr := settingSvc.GetByPathHashesMetaBatch(ctx, c.User.UID, params.Vault, remainingHashes)
		if metaErr != nil {
			// 查询失败退回原有行为：当作服务端没有，照旧要求上传
			// On query failure fall back to the old behavior: treat as missing and request upload
			h.App.Logger().Warn("websocket_router.setting.SettingSync.GetByPathHashesMetaBatch",
				zap.String(logger.FieldTraceID, c.TraceID),
				zap.Int64(logger.FieldUID, c.User.UID),
				zap.Error(metaErr))
		} else {
			serverSettings = metaMap
		}
	}

	for pathHash := range cSettingsKeys {
		s := cSettings[pathHash]
		// 配置清单本来就按路径哈希索引（与窗口内 delete(cSettingsKeys, s.PathHash) 的
		// 匹配键一致）；客户端带了路径时再核对路径相等，避免哈希碰撞误判。
		// The setting inventory is keyed by path hash (same key as the in-window
		// delete(cSettingsKeys, s.PathHash)); when the client supplied a path, also
		// require it to match so hash collisions cannot substitute.
		if sSetting, ok := serverSettings[pathHash]; ok && sSetting != nil &&
			sSetting.Action != "delete" && sSetting.ContentHash == s.ContentHash && sSetting.Mtime == s.Mtime &&
			(s.Path == "" || sSetting.Path == s.Path) {
			continue
		}
		// Add message to queue instead of sending immediately
		// 将消息添加到队列而非立即发送
		if hasWritePermission {
			messageQueue = append(messageQueue, dto.WSQueuedMessage{
						Context: params.Context,
				Action: SettingSyncNeedUpload,
				Data:   dto.SettingSyncNeedUploadMessage{Path: s.Path},
			})
			needUploadCount++
		} else {
			h.App.Logger().Warn("websocket_router.setting.SettingSync: permission denied for upload",
				zap.String(logger.FieldTraceID, c.TraceID),
				zap.Int64(logger.FieldUID, c.User.UID),
				zap.String(logger.FieldPath, s.Path))
		}
	}

	// 先登记分页队列，再发结束消息：客户端收到结束消息会立刻回首个分页确认，
	// 开启并行处理时该确认可能抢在登记之前到达，被当作找不到队列丢弃，本轮下载就此卡住。
	// Register the paged queue before sending the End message: the client acks the first page
	// as soon as End arrives, and with parallel dispatch that ack could land before the queue
	// was stored, be dropped as "entry not found" and stall the whole download round.
	if len(messageQueue) > 0 {
		pageSize := h.App.Config().App.SyncDownChunkNum
		if pageSize <= 0 {
			pageSize = 50 // 默认值防呆
		}
		// 窗口协商：仅 pv>=2 连接启用下行窗口，旧连接固定 0（stop-and-wait，见设计 §4.2/§4.4）
		// Window negotiation: only pv>=2 connections get the download window enabled, old
		// connections stay at 0 (stop-and-wait, see design §4.2/§4.4)
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
		// 默认不自动发送，等待客户端拉取
	}

	// Send SettingSyncEnd message
	// 发送 SettingSyncEnd 消息
	c.ToResponse(code.Success.WithData(
		dto.SettingSyncEndMessage{
			LastTime:           lastTime,
			NeedUploadCount:    needUploadCount,
			NeedModifyCount:    needModifyCount,
			NeedSyncMtimeCount: needSyncMtimeCount,
			NeedDeleteCount:    needDeleteCount,
		},
	).WithVault(params.Vault).WithContext(params.Context), SettingSyncEnd)
}

// SettingSyncPageAck handles WebSocket messages for client page ACK
// SettingSyncPageAck 处理客户端发来的分页下载 ACK 消息
func (h *SettingWSHandler) SettingSyncPageAck(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SyncPageAckRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.setting.SettingSyncPageAck.BindAndValid", msg)
		return
	}

	entry, ok := syncDownloadGet(params.Context, "setting")
	if !ok {
		h.App.Logger().Warn("SettingSyncPageAck: sync download entry not found",
			zap.String(logger.FieldTraceID, c.TraceID),
			zap.String("context", params.Context))
		return
	}

	handlePageAck(c, entry, params.PageIndex, "setting", h.App.Logger(), c.TraceID)
}


// SettingClear handles clear all settings messages
// SettingClear 处理清理所有配置消息
func (h *SettingWSHandler) SettingClear(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SettingClearRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.setting.SettingClear.BindAndValid", msg)
		return
	}

	ctx := c.Context()

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "SettingClear", "", params.Vault)

	err := h.App.GetSettingService(c.ClientType(), c.ClientName(), c.ClientVersion()).ClearByVault(ctx, c.User.UID, params.Vault)
	if err != nil {
		h.respondError(c, code.ErrorSettingDeleteFailed, err, "websocket_router.setting.SettingClear.ClearByVault")
		return
	}

	// Broadcast clearing to other clients with vault info
	// 将清除消息广播给其他客户端，带上笔记本信息
	c.BroadcastResponse(code.Success.WithData(nil).WithVault(params.Vault), false, SettingSyncClear)
}

// SettingRePush handles setting missing pull request
// SettingRePush 处理配置缺失请求拉取
func (h *SettingWSHandler) SettingRePush(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SettingGetRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.setting.SettingRePush.BindAndValid", msg)
		return
	}

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "SettingRePush", params.Path, params.Vault)

	ctx := c.Context()
	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	setting, err := h.App.GetSettingService(c.ClientType(), c.ClientName(), c.ClientVersion()).Get(ctx, c.User.UID, params)
	if err != nil {
		h.App.Logger().Debug("websocket_router.setting.SettingRePush.Get: record not found or error, proceeding to send delete",
			zap.String(logger.FieldTraceID, c.TraceID),
			zap.Error(err))
	}

	if setting != nil && setting.Action != "delete" {
		c.ToResponse(code.Success.WithData(
			dto.SettingSyncModifyMessage{
				Vault:            params.Vault,
				Path:             setting.Path,
				PathHash:         setting.PathHash,
				Content:          setting.Content,
				ContentHash:      setting.ContentHash,
				Ctime:            setting.Ctime,
				Mtime:            setting.Mtime,
				UpdatedTimestamp: setting.UpdatedTimestamp,
			},
		).WithVault(params.Vault), SettingSyncModify)
	} else {
		// If setting not found, send delete message to client to clean up local unauthorized creation
		// 如果未找到配置，则向客户端发送删除消息，以清理本地未授权的创建
		c.ToResponse(code.Success.WithData(
			dto.SettingSyncDeleteMessage{
				Path: params.Path,
			},
		).WithVault(params.Vault), string(SettingSyncDelete))
	}
}
