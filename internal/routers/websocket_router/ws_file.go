package websocket_router

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/logger"
	"github.com/haierkeys/fast-note-sync-service/pkg/safego"
	"github.com/haierkeys/fast-note-sync-service/pkg/timex"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// FileWSHandler WebSocket file handler
// FileWSHandler WebSocket 文件处理器
// Uses App Container to inject dependencies
// 使用 App Container 注入依赖
type FileWSHandler struct {
	*WSHandler
}

// NewFileWSHandler creates FileWSHandler instance
// NewFileWSHandler 创建 FileWSHandler 实例
func NewFileWSHandler(a *app.App) *FileWSHandler {
	return &FileWSHandler{
		WSHandler: NewWSHandler(a),
	}
}

type FileUploadBinaryChunkSession struct {
	ID             string              // Session ID // 会话 ID
	Vault          string              // Vault Name // 仓库名称
	Path           string              // File Path // 文件路径
	PathHash       string              // File Path Hash // 文件路径哈希值
	ContentHash    string              // File Content Hash // 文件内容哈希值
	Ctime          int64               // Creation time // 创建时间
	Mtime          int64               // Modification time // 修改时间
	Size           int64               // File size // 文件大小
	TotalChunks    int64               // Total chunks // 总分块数
	UploadedChunks int64               // Uploaded chunks // 已上传分块数
	UploadedBytes  int64               // Uploaded bytes // 已上传字节数
	ChunkSize      int64               // Chunk size // 分块大小
	SavePath       string              // Temp save path // 临时保存路径
	FileHandle     *os.File            // File handle // 文件句柄
	mu             sync.Mutex          // Mutex to protect concurrent operations // 互斥锁，保护并发操作
	CreatedAt      time.Time           // Created time // 创建时间
	CancelFunc     context.CancelFunc  // Cancel function for timeout control // 取消函数，用于超时控制
	uploadedChunks map[uint32]struct{} // Record of uploaded chunk indices for idempotency // 已上传分块索引记录，用于幂等
	isCompleted    bool                // Whether upload is completely finished // 上传是否已彻底完成
	Context        string              // Context // 上下文

	// 基线模式上传会话绑定：分片全部写完、最终替换活跃附件之前必须再做一次 CAS 校验，
	// 防止上传期间远端附件被另一设备更新或删除后仍被旧上传覆盖。
	// Baseline upload-session binding: the final commit re-runs the CAS check after all
	// chunks are written, so an upload cannot overwrite a remote attachment that another
	// device changed or deleted while the chunks were in flight.
	SyncBaselineVersion uint32 // Negotiated baseline version at session start // 会话开始时的基线协议版本
	BaseHash            string // Trusted content baseline // 可信内容基线
	BaseHashMissing     bool   // No trusted content baseline // 没有可信内容基线
	BaseRevision        string // Server revision of the baseline // 基线的服务端版本令牌
	IsConflictResolved  bool   // User resolved a conflict // 用户已解决冲突
}

func (s *FileUploadBinaryChunkSession) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Cancel timeout timer
	// 取消超时定时器
	if s.CancelFunc != nil {
		s.CancelFunc()
		s.CancelFunc = nil
	}

	if s.FileHandle != nil {
		if err := s.FileHandle.Close(); err != nil {
			zap.L().Warn("cleanup: failed to close file handle",
				zap.String(logger.FieldSessionID, s.ID),
				zap.String(logger.FieldPath, s.Path),
				zap.String(logger.FieldMethod, "FileUploadBinaryChunkSession.Cleanup"),
				zap.Error(err),
			)
		}
		s.FileHandle = nil
	}
	// Check if SavePath exists before attempting to remove it
	if s.SavePath != "" {
		if _, err := os.Stat(s.SavePath); err == nil {
			if err := os.Remove(s.SavePath); err != nil {
				zap.L().Warn("cleanup: failed to remove temp file",
					zap.String(logger.FieldSessionID, s.ID),
					zap.String(logger.FieldPath, s.SavePath),
					zap.String(logger.FieldMethod, "FileUploadBinaryChunkSession.Cleanup"),
					zap.Error(err),
				)
			}
		}
		s.SavePath = ""
	}
}

// GetPathHash returns the path hash of the session
// GetPathHash 返回会话的路径哈希值
func (s *FileUploadBinaryChunkSession) GetPathHash() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PathHash
}

// GetPathIdentity returns the vault and full path used to distinguish collisions.
// GetPathIdentity 返回库名和完整路径，用于区分碰撞路径。
func (s *FileUploadBinaryChunkSession) GetPathIdentity() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Vault, s.Path
}

func (s *FileUploadBinaryChunkSession) GetCreatedAt() time.Time {
	return s.CreatedAt
}

// FileDownloadChunkSession defines the session state for file chunk download
// Used to track progress and file info for large file chunk downloads
// FileDownloadChunkSession 定义文件分块下载的会话状态。
// 用于跟踪大文件分块下载的进度和文件信息。
type FileDownloadChunkSession struct {
	SessionID   string // Session ID // 会话 ID
	Path        string // File path (for logging) // 文件路径(用于日志)
	Size        int64  // File size // 文件大小
	TotalChunks int64  // Total chunks // 总分块数
	ChunkSize   int64  // Chunk size // 分块大小
	SavePath    string // File actual save path // 文件实际保存路径
	ContentHash string // Content hash // 内容哈希
}

// FileUploadCheck checks file upload request, initializes upload session or confirms no upload needed
// FileUploadCheck 检查文件上传请求，初始化上传会话或确认无需上传。
func (h *FileWSHandler) FileUploadCheck(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.FileUpdateCheckRequest{}

	// Bind and validate parameters
	// 绑定并验证参数
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.file.FileUploadCheck.BindAndValid", msg)
		return
	}

	slotPath := clientSlotPath(c, msg, params.Path)
	// Required parameter validation
	// 必填参数校验
	if params.PathHash == "" {
		c.ToResponse(code.ErrorInvalidParams.WithDetails("pathHash is required").WithData(errPathData(slotPath)))
		return
	}
	if params.ContentHash == "" {
		c.ToResponse(code.ErrorInvalidParams.WithDetails("contentHash is required").WithData(errPathData(slotPath)))
		return
	}
	if params.Mtime == 0 {
		c.ToResponse(code.ErrorInvalidParams.WithDetails("mtime is required").WithData(errPathData(slotPath)))
		return
	}
	if params.Ctime == 0 {
		c.ToResponse(code.ErrorInvalidParams.WithDetails("ctime is required").WithData(errPathData(slotPath)))
		return
	}
	// 部分第三方客户端传秒级时间戳，归一化为毫秒（#461）
	params.Ctime = timex.NormalizeMilli(params.Ctime)
	params.Mtime = timex.NormalizeMilli(params.Mtime)

	// 只校验、不改写路径（#342 S-8）：同步协议要求按客户端上报的原样路径落库
	if !validWSPath(params.Path) {
		c.ToResponse(code.ErrorInvalidPath.WithDetails("invalid file path").WithData(errPathData(slotPath)))
		return
	}
	if h.writeRejectsSystemJunk(params.Path) {
		h.respondSystemJunkPath(c, slotPath)
		return
	}

	ctx := c.Context()

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "FileUploadCheck", params.Path, params.Vault)

	// Check and create vault, internally uses SF to merge concurrent requests, avoiding duplicate creation issues
	// 检查并创建仓库，内部使用SF合并并发请求, 避免重复创建问题
	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	// 检查文件更新状态
	fileService := h.App.GetFileService(c.ClientType(), c.ClientName(), c.ClientVersion())
	params.Baseline = baselineCommitFromNote(c, params.BaseHash, params.BaseHashMissing, params.BaseRevision, params.IsConflictResolved)
	updateMode, fileSvc, err := fileService.UploadCheck(ctx, c.User.UID, params)

	if err != nil {
		if conflict, ok := isBaselineConflict(err); ok {
			h.respondBaselineConflict(c, dto.SyncConflictKindFile, params.Vault, params.Path, params.PathHash, params.BaseHash, conflict, slotPath)
			return
		}
		h.respondErrorWithPath(c, code.ErrorFileUploadCheckFailed, err, slotPath, msg.Type, "websocket_router.file.FileUploadCheck.UploadCheck")
		return
	}

	// UpdateContent 或 Create 模式，需要客户端上传文件
	switch updateMode {
	case "UpdateContent", "Create":

		state := baselineClientState{
			LocalHash:          params.ContentHash,
			BaseHash:           params.BaseHash,
			HasBase:            params.Baseline != nil && params.Baseline.HasBase,
			BaseRevision:       params.BaseRevision,
			IsConflictResolved: params.IsConflictResolved,
			IsFile:             true,
		}
		if params.Baseline != nil {
			state.BaseHash = params.Baseline.BaseHash
		}
		session, err := h.handleFileUploadSessionCreateWithBaseline(c, params.Vault, params.Path, params.PathHash, params.ContentHash, params.Size, params.Ctime, params.Mtime, params.Context, state)
		if err != nil {
			h.respondErrorWithPath(c, code.ErrorFileUploadCheckFailed, err, slotPath, msg.Type, "websocket_router.file.FileUploadCheck.handleFileUploadSessionCreate")
			return
		}

		c.ToResponse(code.Success.WithData(
			dto.FileSyncUploadMessage{
				Path:      session.Path,
				PathHash:  session.PathHash,
				SessionID: session.ID,
				ChunkSize: session.ChunkSize,
			},
		).WithVault(params.Vault).WithContext(params.Context), FileUpload)
		return

	case "UpdateMtime":
		// 当用户 mtime 小于服务端 mtime 时，通知用户更新mtime
		c.ToResponse(code.Success.WithData(
			dto.FileSyncMtimeMessage{
				Path:             fileSvc.Path,
				Ctime:            fileSvc.Ctime,
				Mtime:            fileSvc.Mtime,
				UpdatedTimestamp: fileSvc.UpdatedTimestamp,
				ContentHash:      fileSvc.ContentHash,
				Revision:         fileSvc.Revision,
			},
		).WithContext(params.Context), FileSyncMtime)
		return
	default:
		// 无需更新也需回 FileUploadAck：默认配置 is-return-sussess=false 下 SuccessNoUpdate 什么都不回，
		// 官方插件发检查前占用的并发名额（等 FileUpload/FileSyncMtime/FileUploadAck 才释放）会被成批占住，
		// 被分页上传抢先传完的附件其监听检查全部无回包，整轮同步不收尾（笔记入口 NoteModify 无更新时回 NoteModifyAck 是同样的处理）。
		// LastTime 故意留 0：本次检查未改动服务端内容，不应推进客户端同步游标；
		// 2.4.0 插件收到非零 lastTime 会无条件取较大值推进游标（服务端#101 漏拉成因），留 0 对已知客户端最安全。
		// No-update also needs a FileUploadAck: with the default is-return-sussess=false config,
		// SuccessNoUpdate sends nothing, so the concurrency slot the official plugin reserved before
		// the check (released only by FileUpload/FileSyncMtime/FileUploadAck) gets held in bulk when
		// paged uploads finish first and the watcher checks get no reply at all, stalling the round.
		// LastTime is deliberately 0: this check changed nothing server-side, so it must not advance
		// the client's sync cursor; 2.4.0 plugins unconditionally take max(lastTime) on non-zero values
		// (the server#101 missed-pull cause), so 0 is safest for all known clients.
		if fileSvc != nil {
			c.ToResponse(code.Success.WithData(dto.FileUploadAckMessage{
				Path:        params.Path,
				PathHash:    params.PathHash,
				ContentHash: fileSvc.ContentHash,
				Revision:    fileSvc.Revision,
			}).WithVault(params.Vault).WithContext(params.Context), string(FileUploadAck))
		} else {
			// 无需更新
			c.ToResponse(code.SuccessNoUpdate.WithVault(params.Vault).WithContext(params.Context))
		}
	}
}

// FileUploadChunkBinary handles binary data for file chunk upload.
// FileUploadChunkBinary 处理文件分块上传的二进制数据。
func (h *FileWSHandler) FileUploadChunkBinary(c *pkgapp.WebsocketClient, data []byte) {
	if c.User == nil {
		c.ToResponse(code.ErrorNotUserAuthToken)
		return
	}
	// Check if context is cancelled (connection might be closed)
	// 检查 context 是否已取消（连接可能已关闭）
	select {
	case <-c.Context().Done():
		h.logInfo(c, "FileUploadChunkBinary: context cancelled, skipping chunk processing")
		return
	default:
	}

	// Verify permissions (file_w scope required for file chunk upload)
	// 校验权限 (文件分块上传需要拥有 file_w 权限范围)
	if !pkgapp.VerifyPermissions(c.Scope, "ws", c.ClientType(), "file_w") {
		h.logWarn(c, "FileUploadChunkBinary: permission denied for file_w")
		c.ToResponse(code.ErrorAuthTokenScopeRestricted.WithDetails("Permission denied: file_w"))
		return
	}

	if len(data) < 40 {
		h.logError(c, "websocket_router.file.FileUploadChunkBinary", fmt.Errorf("invalid data length: %d", len(data)))
		return
	}

	// Parse session ID and chunk index
	// 解析会话 ID 和分块索引
	sessionID := string(data[:36])
	chunkIndex := binary.BigEndian.Uint32(data[36:40])
	chunkData := data[40:]

	// Get session from global server (supports cross-connection)
	// 从全局服务器获取会话 (支持跨连接)
	binarySession := c.Server.GetSession(c.User.ID, sessionID)

	if binarySession == nil {
		h.logError(c, "websocket_router.file.FileUploadChunkBinary", fmt.Errorf("session not found: %s", sessionID))
		c.ToResponse(code.ErrorFileUploadSessionNotFound.WithData(map[string]string{
			"sessionID": sessionID,
		}))
		return
	}

	session := binarySession.(*FileUploadBinaryChunkSession)
	if session == nil {
		h.logError(c, "websocket_router.file.FileUploadChunkBinary", fmt.Errorf("session is nil: %s", sessionID))
		c.ToResponse(code.ErrorFileUploadSessionNotFound.WithData(map[string]string{
			"sessionID": sessionID,
		}))
		return
	}

	session.mu.Lock()
	// 1. Check if completely finished (Idempotency for late chunks)
	// 1. 检查是否已彻底完成 (针对延迟到达分片的幂等性)
	if session.isCompleted {
		session.mu.Unlock()
		c.ToResponse(code.Success)
		return
	}

	// 2. Check if chunk index has been uploaded (Idempotency for duplicate chunks)
	// 2. 检查分块索引是否已上传 (针对重复分片的幂等性)
	if _, ok := session.uploadedChunks[chunkIndex]; ok {
		session.mu.Unlock()
		c.ToResponse(code.Success)
		return
	}

	// Calculate write offset and write data
	// 计算写入偏移量并写入数据
	offset := int64(chunkIndex) * int64(session.ChunkSize)

	if session.FileHandle == nil {
		// Lazy initialize file handle on first chunk
		// 在收到第一个分片时延迟初始化文件句柄
		f, err := os.Create(session.SavePath)
		if err != nil {
			session.mu.Unlock()
			h.handleFileUploadSessionCleanup(c, sessionID)
			h.respondErrorWithData(c, code.ErrorFileUploadFailed, err, map[string]string{"sessionID": sessionID}, "websocket_router.file.FileUploadChunkBinary.Create")
			return
		}
		session.FileHandle = f
	}
	fileHandle := session.FileHandle
	session.mu.Unlock()

	if _, err := fileHandle.WriteAt(chunkData, offset); err != nil {
		// Cleanup session on fatal error
		// 致命错误时清理会话
		h.handleFileUploadSessionCleanup(c, sessionID)
		h.respondErrorWithData(c, code.ErrorFileUploadFailed, err, map[string]string{"sessionID": sessionID}, "websocket_router.file.FileUploadChunkBinary.WriteAt")
		return
	}

	// Update uploaded count and bytes
	// 更新已上传计数 and 字节数
	session.mu.Lock()
	session.uploadedChunks[chunkIndex] = struct{}{} // 记录已上传的分块索引
	session.UploadedChunks++
	session.UploadedBytes += int64(len(chunkData))
	uploadedBytes := session.UploadedBytes
	uploadedChunks := session.UploadedChunks
	totalChunks := session.TotalChunks
	session.mu.Unlock()

	// Check if all data has been uploaded (judged by bytes or total chunks received)
	// 检查是否所有数据都已上传 (根据字节数或收到的总分块数判断)
	if uploadedBytes >= session.Size || uploadedChunks >= totalChunks {

		h.logInfo(c, "FileUploadComplete: upload finished",
			zap.String("sessionID", sessionID),
			zap.String("path", session.Path),
			zap.Int64("uploadedBytes", uploadedBytes),
			zap.Int64("totalSize", session.Size),
			zap.Int64("uploadedChunks", uploadedChunks),
			zap.Int64("totalChunks", totalChunks))

		// NOTE: Session removal is delayed until UploadComplete is successful
		// 注意：Session 的移除延迟到 UploadComplete 成功之后

		// Cancel timeout timer and snapshot the file handle under the session lock:
		// a concurrent session reuse/cleanup writes both fields under the same lock.
		// 在会话锁内取消超时定时器并取出文件句柄：并发的会话复用 / 清理会在同一把锁下
		// 改写这两个字段，锁外直接读会形成数据竞态。
		session.mu.Lock()
		cancelTimer := session.CancelFunc
		fileHandle := session.FileHandle
		session.mu.Unlock()
		if cancelTimer != nil {
			cancelTimer()
		}

		// Close temp file handle
		// 关闭临时文件句柄
		if fileHandle == nil {
			h.handleFileUploadSessionCleanup(c, sessionID)
			h.respondErrorWithData(c, code.ErrorFileUploadFailed, fmt.Errorf("upload session file handle is nil"), map[string]string{"sessionID": sessionID}, "websocket_router.file.FileUploadChunkBinary.Close")
			return
		}
		if err := fileHandle.Close(); err != nil {
			// 超时定时器已在上方停掉；此刻若不销毁会话，它会变成僵尸：客户端重试时
			// 拿到"全部分片已上传"的响应却再也不会触发 UploadComplete。立即移除会话
			// 并删除临时文件，让客户端重试时重建干净的会话。
			// The timeout timer was stopped above; keeping the session would leave a
			// zombie whose retry sees every chunk as uploaded and never reaches
			// UploadComplete. Remove it and delete the temp file so a retry rebuilds
			// a clean session.
			h.handleFileUploadSessionCleanup(c, sessionID)
			h.respondErrorWithData(c, code.ErrorFileUploadFailed, err, map[string]string{"sessionID": sessionID}, "websocket_router.file.FileUploadChunkBinary.Close")
			return
		}
		// Avoid double closing during cleanup; only clear the handle we closed.
		// 仅清空本次关闭的句柄，避免清理流程重复关闭。
		session.mu.Lock()
		if session.FileHandle == fileHandle {
			session.FileHandle = nil
		}
		session.mu.Unlock()

		ctx := c.Context()

		// Check and create vault, internally uses SF to merge concurrent requests, avoiding duplicate creation issues
		// 检查并创建仓库，内部使用SF合并并发请求, 避免重复创建问题
		h.App.VaultService.GetOrCreate(ctx, c.User.UID, session.Vault)

		session.mu.Lock()
		svcParams := &dto.FileUpdateRequest{
			Vault:       session.Vault,
			Path:        session.Path,
			PathHash:    session.PathHash,
			ContentHash: session.ContentHash,
			SavePath:    session.SavePath, // 将临时文件的完整路径作为 SavePath 传入
			Size:        session.Size,
			Ctime:       session.Ctime,
			Mtime:       session.Mtime,
		}
		// 附件上传会话绑定基线：最终提交时在服务层临界区内再次做 CAS 校验，
		// 若期间远端附件已被另一设备更新或删除，则不替换活跃附件并返回冲突。
		// The upload session carries the client's baseline: the final commit re-runs the
		// CAS check inside the service critical section, so an attachment changed or
		// deleted by another device meanwhile is never overwritten.
		if session.SyncBaselineVersion >= 1 {
			svcParams.Baseline = &dto.BaselineCommit{
				HasBase:            !session.BaseHashMissing,
				BaseHash:           session.BaseHash,
				BaseRevision:       session.BaseRevision,
				IsConflictResolved: session.IsConflictResolved,
			}
		}
		// 收尾响应与广播用的字段一并在锁内取快照：并发复用的请求会在会话锁内
		// 改写 Context / Ctime / Mtime，锁外读取会形成数据竞态。
		// Snapshot the fields used by the completion replies and broadcast under the
		// same lock: a concurrent reuse request rewrites Context / Ctime / Mtime
		// under the session lock, so reading them outside would race.
		ackPath, ackPathHash := session.Path, session.PathHash
		ackVault, ackContext := session.Vault, session.Context
		ackCtime, ackMtime, ackSize := session.Ctime, session.Mtime, session.Size
		ackSessionID := session.ID
		session.mu.Unlock()

		// Update or create file record (DAO layer will automatically move temp file from SavePath to f_{id} folder)
		// 更新或创建 file 记录 (DAO 层会自动将 SavePath 里的临时文件移动到 f_{id} 文件夹)
		fileService := h.App.GetFileService(c.ClientType(), c.ClientName(), c.ClientVersion())
		_, fileSvc, err := fileService.UploadComplete(ctx, c.User.UID, svcParams)

		if err != nil {
			if conflict, ok := isBaselineConflict(err); ok {
				// 分片已全部写完但远端已被抢先改动：不替换活跃附件，回冲突并清理会话与临时文件。
				// All chunks are in but the remote changed first: do not replace the active
				// attachment; report the conflict and reclaim the session and temp file.
				h.respondBaselineConflict(c, dto.SyncConflictKindFile, ackVault, ackPath, ackPathHash, session.BaseHash, conflict, ackPath)
				c.Server.RemoveSession(c.User.ID, ackSessionID)
				session.Cleanup()
				return
			}
			// 陈旧重报被墓碑闸门拒绝：回 ACK 清理客户端待传状态、补发删除指令清理
			// 本地残留；不落库、不广播，并立即回收上传会话与临时文件。
			// 闸门只拒绝内容与墓碑一致的原样重报，补发删除不会丢失未同步的修改。
			if errors.Is(err, code.ErrorStaleReviveRejected) {
				c.ToResponse(code.Success.WithData(dto.FileUploadAckMessage{
					Path:     ackPath,
					PathHash: ackPathHash,
				}).WithVault(ackVault).WithContext(ackContext), string(FileUploadAck))
				c.ToResponse(code.Success.WithData(dto.FileSyncDeleteMessage{
					Path:     ackPath,
					PathHash: ackPathHash,
					Ctime:    ackCtime,
					Mtime:    ackMtime,
					Size:     ackSize,
				}).WithVault(ackVault).WithContext(ackContext), string(FileSyncDelete))
				c.Server.RemoveSession(c.User.ID, ackSessionID)
				session.Cleanup()
				return
			}
			// 同 Close 失败：会话已停止超时回收且分片已齐，不销毁就会卡死后续重传。
			// 先移除会话并删除临时文件，再回带 sessionID 和 path 的错误响应，让客户端
			// 清理活跃状态、释放并发槽位并在重试时重建干净会话。
			// Same as a Close failure: the timeout timer is stopped and all chunks are
			// in, so leaving the session would deadlock retries. Remove it and delete
			// the temp file first, then reply with sessionID and path so the client can
			// clean up, release the slot and rebuild a clean session on retry.
			h.handleFileUploadSessionCleanup(c, sessionID)
			h.respondErrorWithData(c, code.ErrorFileModifyOrCreateFailed, err, map[string]string{
				"sessionID": sessionID,
				"path":      ackPath,
			}, "websocket_router.file.FileUploadChunkBinary.UploadComplete")
			return
		}

		// Always send FileUploadAck to client, even if fileSvc is nil (defensive edge case)
		// 始终发送 FileUploadAck 给客户端，即使 fileSvc 为 nil（防御性边界情况）
		// Without this ack, the client will never clear its pending upload state and will retry indefinitely
		// 缺少此 ack，客户端永远无法清除其待上传状态，将无限重试
		var ackLastTime int64
		if fileSvc != nil {
			ackLastTime = fileSvc.UpdatedTimestamp
		}

		// Reply to sender with ack carrying server timestamp, so client can update lastFileSyncTime
		// 回复发送方携带服务端时间戳的 ack，让客户端可以更新 lastFileSyncTime
		ackContentHash, ackRevision := "", ""
		if fileSvc != nil {
			ackContentHash, ackRevision = fileSvc.ContentHash, fileSvc.Revision
		}
		c.ToResponse(code.Success.WithData(dto.FileUploadAckMessage{
			LastTime:    ackLastTime,
			Path:        ackPath,
			PathHash:    ackPathHash,
			ContentHash: ackContentHash,
			Revision:    ackRevision,
		}).WithVault(ackVault).WithContext(ackContext), string(FileUploadAck))

		// Mark as completed and remove from global server
		// 标记为已完成并从全局服务器移除
		session.mu.Lock()
		session.isCompleted = true
		session.mu.Unlock()
		c.Server.RemoveSession(c.User.ID, ackSessionID)
		// 回收会话资源：正常落库路径的临时文件已被 DAO rename 搬走（此处 Stat
		// 不存在、跳过），而"内容与 mtime 完全一致直接返回"和"只更新 mtime"两条
		// 幂等短路路径不搬文件——超时定时器又已在上文取消，没人会再清理，必须在
		// 这里删掉，否则临时文件在 temp 目录永久残留。Cleanup 幂等（SavePath 置
		// 空后为空操作），文件不存在时静默跳过、删除失败只记 Warn 不影响 ack。
		// Reclaim session resources: on the normal persistence path the temp file
		// was already renamed away by the DAO (Stat misses and we skip), while the
		// two short-circuit paths — "content and mtime fully identical, return
		// directly" and "mtime-only update" — never move it, and the timeout timer
		// was already cancelled above, so nothing else would clean it up. Cleanup is
		// idempotent (no-op once SavePath is cleared), skips missing files silently
		// and only logs a Warn on removal failure, never affecting the ack.
		session.Cleanup()

		// Broadcast file update message (only when fileSvc is available)
		// 广播文件更新消息（仅在 fileSvc 可用时）
		if fileSvc != nil {
			c.BroadcastResponse(code.Success.WithData(
				dto.FileSyncModifyMessage{
					Path:             fileSvc.Path,
					PathHash:         fileSvc.PathHash,
					ContentHash:      fileSvc.ContentHash,
					Size:             fileSvc.Size,
					Ctime:            fileSvc.Ctime,
					Mtime:            fileSvc.Mtime,
					UpdatedTimestamp: fileSvc.UpdatedTimestamp,
					Revision:         fileSvc.Revision,
				},
			).WithVault(session.Vault), true, FileSyncUpdate)
		} else {
			h.logInfo(c, "FileUploadChunkBinary: fileSvc is nil, ack sent but skipping broadcast", zap.String("path", session.Path))
		}
	}
}

// FileDelete handles file deletion request.
// FileDelete 处理文件删除请求。
func (h *FileWSHandler) FileDelete(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.FileDeleteRequest{}

	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.file.FileDelete.BindAndValid", msg)
		return
	}

	slotPath := clientSlotPath(c, msg, params.Path)
	// 只校验、不改写路径（#342 S-8）：不去前导 "/"，仅按段拒绝越界与 NUL
	if !validWSPath(params.Path) {
		c.ToResponse(code.ErrorInvalidPath.WithDetails("invalid file path").WithData(errPathData(slotPath)))
		return
	}

	ctx := c.Context()

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "FileDelete", params.Path, params.Vault)

	// 获取或创建仓库
	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	// Execute deletion logic
	// 执行删除逻辑
	fileService := h.App.GetFileService(c.ClientType(), c.ClientName(), c.ClientVersion())
	params.Baseline = baselineCommitFromNote(c, params.BaseHash, params.BaseHashMissing, params.BaseRevision, false)
	fileSvc, err := fileService.Delete(ctx, c.User.UID, params)

	if err != nil {
		if conflict, ok := isBaselineConflict(err); ok {
			h.respondBaselineConflict(c, dto.SyncConflictKindFile, params.Vault, params.Path, params.PathHash, params.BaseHash, conflict, slotPath)
			return
		}
		h.respondErrorWithPath(c, code.ErrorFileDeleteFailed, err, slotPath, msg.Type, "websocket_router.file.FileDelete.Delete")
		return
	}

	c.ToResponse(code.Success.WithData(dto.FileDeleteAckMessage{
		LastTime: fileSvc.UpdatedTimestamp,
		Path:     fileSvc.Path,
		PathHash: fileSvc.PathHash,
		Revision: fileSvc.Revision,
	}).WithVault(params.Vault).WithContext(params.Context), string(FileDeleteAck))

	// 记录不存在或已删除（幂等重复删除）：只回 Ack，不广播，避免向其他设备重复下发删除
	if fileSvc.Unchanged {
		return
	}

	// Broadcast file deletion message
	// 广播文件删除消息
	c.BroadcastResponse(code.Success.WithData(
		dto.FileSyncDeleteMessage{
			Path:             fileSvc.Path,
			PathHash:         fileSvc.PathHash,
			Ctime:            fileSvc.Ctime,
			Mtime:            fileSvc.Mtime,
			Size:             fileSvc.Size,
			UpdatedTimestamp: fileSvc.UpdatedTimestamp,
			Revision:         fileSvc.Revision,
			ContentHash:      fileSvc.ContentHash,
		},
	).WithVault(params.Vault), true, FileSyncDelete)
}

// FileRename handles file rename request.
// FileRename 处理文件重命名请求。
func (h *FileWSHandler) FileRename(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.FileRenameRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.file.FileRename.BindAndValid", msg)
		return
	}

	uid := c.User.UID
	uidStr := strconv.FormatInt(uid, 10)

	slotPath := clientSlotPath(c, msg, params.Path)
	// 只校验、不改写新旧两个路径（#342 S-8）
	if !validWSPath(params.Path) || !validWSPath(params.OldPath) {
		c.ToResponse(code.ErrorInvalidPath.WithDetails("invalid file path").WithData(errPathData(slotPath)))
		return
	}
	// 改名只看新路径：旧路径是垃圾、新路径正常的放行，便于清理历史数据。
	if h.writeRejectsSystemJunk(params.Path) {
		h.respondSystemJunkPath(c, slotPath)
		return
	}

	// Check if there is an active upload session for the old path hash
	// 检查旧路径哈希是否存在活跃的上传会话
	activeSession := c.Server.GetSessionByPathHash(uidStr, params.OldPathHash, params.Vault, params.OldPath)
	if activeSession != nil {
		if sess, ok := activeSession.(*FileUploadBinaryChunkSession); ok {
			sess.mu.Lock()
			sess.Path = params.Path
			sess.PathHash = params.PathHash
			sess.mu.Unlock()
		}
	}

	// 基线模式统一走「创建目标 + 条件删除源」，原生 Rename 会绕过版本令牌。
	// Baseline mode uses create-target + conditional-delete-source; a native rename
	// would bypass the revision token.
	if baselineModeEnabled(c) {
		h.respondErrorWithPath(c, code.ErrorFileRenameFailed,
			code.ErrorFileRenameFailed.WithDetails("rename is not supported in baseline mode; create the target and delete the source"),
			slotPath, msg.Type, "websocket_router.file.FileRename.BaselineMode")
		return
	}

	fileService := h.App.GetFileService(c.ClientType(), c.ClientName(), c.ClientVersion())
	oldFile, newFile, err := fileService.Rename(c.Context(), uid, params)
	if err != nil {
		// If DB record not found but we have an active session redirecting the upload, bypass the DB error.
		// 如果数据库找不到记录，但是我们在内存中重定向了活跃上传会话，则忽略数据库错误，伪造成功响应且不进行广播。
		if errors.Is(err, code.ErrorFileNotFound) && activeSession != nil {
			now := timex.Now().UnixMilli()
			c.ToResponse(code.Success.WithData(dto.FileRenameAckMessage{
				LastTime: now,
				Path:     params.Path,
				PathHash: params.PathHash,
			}).WithVault(params.Vault).WithContext(params.Context), string(FileRenameAck))
			return
		}
		h.respondErrorWithPath(c, code.ErrorFileRenameFailed, err, slotPath, msg.Type, "websocket_router.file.FileRename.Rename")
		return
	}

	// Reply to sender with ack carrying server timestamp, so client can update lastFileSyncTime
	// 回复发送方携带服务端时间戳的 ack，让客户端可以更新 lastFileSyncTime
	c.ToResponse(code.Success.WithData(dto.FileRenameAckMessage{
		LastTime: newFile.UpdatedTimestamp,
		Path:     newFile.Path,
		PathHash: newFile.PathHash,
	}).WithVault(params.Vault).WithContext(params.Context), string(FileRenameAck))

	c.BroadcastResponse(code.Success.WithData(
		dto.FileSyncRenameMessage{
			Path:             newFile.Path,
			PathHash:         newFile.PathHash,
			ContentHash:      newFile.ContentHash,
			Ctime:            newFile.Ctime,
			Mtime:            newFile.Mtime,
			Size:             newFile.Size,
			OldPath:          oldFile.Path,
			OldPathHash:      oldFile.PathHash,
			UpdatedTimestamp: newFile.UpdatedTimestamp,
		},
	).WithVault(params.Vault), true, FileSyncRename)
}

// FileChunkDownload handles file chunk download request.
// Client requests file download via this interface, server creates download session and starts sending chunks.
// FileChunkDownload 处理文件分片下载请求。
// 客户端通过此接口请求下载文件,服务端创建下载会话并开始发送分片。
func (h *FileWSHandler) FileChunkDownload(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.FileGetRequest{}

	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.file.FileChunkDownload.BindAndValid", msg)
		return
	}

	slotPath := clientSlotPath(c, msg, params.Path)
	ctx := c.Context()

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "FileChunkDownload", params.Path, params.Vault)

	// 获取或创建仓库
	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	// Get file info
	// 获取文件信息
	fileService := h.App.GetFileService(c.ClientType(), c.ClientName(), c.ClientVersion())
	fileSvc, err := fileService.Get(ctx, c.User.UID, params)

	if err != nil {
		h.respondErrorWithPath(c, code.ErrorFileGetFailed, err, slotPath, msg.Type, "websocket_router.file.FileChunkDownload.Get")
		return
	}

	// Check if file exists on disk
	// 检查文件是否存在于磁盘
	if _, err := os.Stat(fileSvc.SavePath); os.IsNotExist(err) {
		h.respondErrorWithPath(c, code.ErrorFileGetFailed, fmt.Errorf("file not found on disk: %s (path: %s, pathHash: %s)", fileSvc.SavePath, fileSvc.Path, fileSvc.PathHash), slotPath, msg.Type, "websocket_router.file.FileChunkDownload.Stat")
		return
	}

	// Create download session
	// 创建下载会话
	sessionID := uuid.New().String()
	chunkSize := getChunkSizeFromConfig(h.App.Config()) // 从注入的配置获取

	// Calculate total chunks
	// 计算总分块数
	totalChunks := util.Ceil(fileSvc.Size, chunkSize)

	// Initialize download session
	// 初始化下载会话
	session := &FileDownloadChunkSession{
		SessionID:   sessionID,
		Path:        fileSvc.Path,
		Size:        fileSvc.Size,
		TotalChunks: totalChunks,
		ChunkSize:   chunkSize,
		SavePath:    fileSvc.SavePath,
		ContentHash: fileSvc.ContentHash,
	}

	// Send download ready message
	// 发送下载准备消息
	c.ToResponse(code.Success.WithData(
		dto.FileSyncDownloadMessage{
			Path:        fileSvc.Path,
			ContentHash: session.ContentHash,
			Ctime:       fileSvc.Ctime,
			Mtime:       fileSvc.Mtime,
			SessionID:   session.SessionID,
			ChunkSize:   session.ChunkSize,
			TotalChunks: session.TotalChunks,
			Size:        session.Size,
			// 带上该附件当前的 revision：插件下载完成后据此记录“已确认基线”，
			// 否则后续改名/修改会被当成无基线冲突。
			// Carry the attachment's current revision so the plugin can record a
			// confirmed baseline after the download; without it every later
			// rename/edit is rejected as a no-baseline conflict.
			Revision: fileSvc.Revision,
		},
	).WithVault(params.Vault).WithContext(params.Context), FileSyncChunkDownload)

	// Start chunk sending, pass timeout and logger
	// 启动分片发送,传入超时时间和 logger
	safego.Go(h.App.Logger(), func() { h.handleFileChunkDownloadSendChunks(c, session) })
}

// FileSync batch checks if user files need update.
// Compares file list between client and server, deciding which files need upload, update or delete.
// FileSync 批量检测用户文件是否需要更新。
// 对比客户端和服务端的文件列表，决定哪些文件需要上传、更新或删除。
func (h *FileWSHandler) FileSync(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.FileSyncRequest{}

	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.file.FileSync.BindAndValid", msg)
		return
	}

	// 分批协议快速路径：totalBatches <= 1 时直接执行，无需缓存归集
	// Fast path: totalBatches <= 1 means single batch, skip cache accumulation
	if params.TotalBatches > 1 {
		entry, created := syncBatchGetOrCreate(params.Context, "file", params.TotalBatches)
		if created {
			// 观测用：若此 context+type 早已集齐并被清理，这里会是迟到重传重建的孤儿 entry
			// （见同步流水线设计 §3.3 第 2 点），5min TTL 会自动回收，不做额外防护
			// Observability: if this context+type had already been collected and cleaned up,
			// this is a late-retransmit rebuild of an orphan entry (design §3.3 point 2);
			// the 5-minute TTL reclaims it automatically, no extra protection needed
			h.App.Logger().Debug("websocket_router.file.FileSync: created new batch cache entry",
				zap.String(logger.FieldTraceID, c.TraceID),
				zap.String("context", params.Context),
				zap.Int("batchIndex", params.BatchIndex),
				zap.Int("totalBatches", params.TotalBatches))
		}

		entry.mu.Lock()
		// 重复 BatchIndex（客户端因未收到 ack 而重传）时跳过 append/计数，只重发 ack
		// Duplicate BatchIndex (client retransmitted after missing the ack): skip append/count, just resend the ack
		if !entry.markBatchReceived(params.BatchIndex) {
			for _, f := range params.Files {
				entry.Items = append(entry.Items, f)
			}
			entry.ReceivedCount++
			for _, df := range params.DelFiles {
				entry.DelItems = append(entry.DelItems, df)
			}
			for _, mf := range params.MissingFiles {
				entry.MissingItems = append(entry.MissingItems, mf)
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
		}).WithVault(params.Vault).WithContext(params.Context), FileSyncBatchAck)

		if received < total {
			// 未集齐：等待其余批次
			// Not all batches received yet: wait for the rest
			return
		}

		// 全部批次到达：从缓存中提取数据，清理缓存，执行差量比对
		// All batches received: extract from cache, delete cache, run differential sync
		syncBatchDelete(params.Context, "file")
		allFiles := make([]dto.FileSyncCheckRequest, 0, len(entry.Items))
		for _, item := range entry.Items {
			allFiles = append(allFiles, item.(dto.FileSyncCheckRequest))
		}
		params.Files = allFiles

		allDelFiles := make([]dto.FileSyncDelFile, 0, len(entry.DelItems))
		for _, item := range entry.DelItems {
			allDelFiles = append(allDelFiles, item.(dto.FileSyncDelFile))
		}
		params.DelFiles = allDelFiles

		allMissingFiles := make([]dto.FileSyncDelFile, 0, len(entry.MissingItems))
		for _, item := range entry.MissingItems {
			allMissingFiles = append(allMissingFiles, item.(dto.FileSyncDelFile))
		}
		params.MissingFiles = allMissingFiles
	}

	if baselineModeEnabled(c) {
		h.doFileSyncBaseline(c, params)
		return
	}

	h.doFileSync(c, params)
}

func (h *FileWSHandler) doFileSync(c *pkgapp.WebsocketClient, params *dto.FileSyncRequest) {
	// 新一轮同步取代本连接该类型的旧轮：清理旧轮残留的下行分页条目
	syncDownloadDropStale(connDone(c), "file", params.Context)
	ctx := c.Context()

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "FileSync", "", params.Vault)

	// 获取或创建仓库
	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	// Get list of changed files after last sync
	// 获取最后一次同步后的变更文件列表
	fileService := h.App.GetFileService(c.ClientType(), c.ClientName(), c.ClientVersion())

	// Record sync start time before querying to avoid missing writes that occur during query processing.
	// 查询前记录同步开始时间，防止查询处理期间的写入被遗漏（经典增量同步快照时间戳方案）。
	syncStartTime := timex.Now().UnixMilli()

	list, err := fileService.ListByLastTime(ctx, c.User.UID, params)

	if err != nil {
		h.respondError(c, code.ErrorFileListFailed, err, "websocket_router.file.FileSync.ListByLastTime")
		return
	}

	// Build client file index
	// 构建客户端文件索引
	cFiles := newSyncInventory[dto.FileSyncCheckRequest]()
	for _, item := range params.Files {
		item.Ctime = timex.NormalizeMilli(item.Ctime)
		item.Mtime = timex.NormalizeMilli(item.Mtime)
		cFiles.add(item.Path, item.PathHash, item)
	}

	// Create message queue for collecting all messages to be sent
	// 创建消息队列，用于收集所有待发送的消息
	var messageQueue []dto.WSQueuedMessage

	var lastTime int64
	var needUploadCount int64
	var needModifyCount int64
	var needSyncMtimeCount int64
	var needDeleteCount int64

	var cDelFilesKeys map[string]struct{} = make(map[string]struct{}, 0)

	// Handle files deleted by client
	// 处理客户端删除的文件
	if len(params.DelFiles) > 0 {
		hasWritePermission := pkgapp.VerifyPermissions(c.Scope, "ws", c.ClientType(), "file_w")

		for _, delFile := range params.DelFiles {
			// Check if file exists before deleting
			// 删除前检查文件是否存在
			getParams := &dto.FileGetRequest{
				Vault:    params.Vault,
				PathHash: delFile.PathHash,
			}
			checkFile, err := fileService.Get(ctx, c.User.UID, getParams)

			// If file exists, execute delete
			// 如果文件存在，执行删除
			if err == nil && checkFile != nil && checkFile.Action != "delete" {
				if !hasWritePermission {
					h.App.Logger().Warn("websocket_router.file.FileSync: permission denied for deletion",
						zap.String(logger.FieldTraceID, c.TraceID),
						zap.Int64(logger.FieldUID, c.User.UID),
						zap.String(logger.FieldPath, delFile.Path))
					continue
				}

				delParams := &dto.FileDeleteRequest{
					Vault:    params.Vault,
					Path:     delFile.Path,
					PathHash: delFile.PathHash,
				}
				fileSvc, err := fileService.Delete(ctx, c.User.UID, delParams)
				if err != nil {
					h.App.Logger().Error("websocket_router.file.FileSync.FileService.Delete",
						zap.String(logger.FieldTraceID, c.TraceID),
						zap.Int64(logger.FieldUID, c.User.UID),
						zap.String(logger.FieldPath, delFile.Path),
						zap.Error(err))
					continue
				}

				// Record paths deleted by client to avoid duplicate sending
				// 记录客户端已主动删除的路径，避免重复下发
				cDelFilesKeys[delFile.Path] = struct{}{}

				// 与单条删除入口一致：幂等删除只保留排除记录，不重复广播。
				if fileSvc.Unchanged {
					continue
				}

				// Broadcast deletion to other clients
				// 将删除消息广播给其他客户端
				// 异步 fire-and-forget：DB 删除已在上面同步完成，广播只用于通知其他设备，
				// 不应让循环等待最慢设备的 wg.Wait()，否则 N 条删除会叠加 N 次广播等待
				// Async fire-and-forget: the DB delete above already completed synchronously;
				// broadcasting only notifies other devices and must not make the loop wait on
				// the slowest device's wg.Wait(), or N deletes would stack N broadcast waits
				safego.Go(h.App.Logger(), func() {
					c.BroadcastResponse(code.Success.WithData(
						dto.FileSyncDeleteMessage{
							Path:             fileSvc.Path,
							PathHash:         fileSvc.PathHash,
							Ctime:            fileSvc.Ctime,
							Mtime:            fileSvc.Mtime,
							Size:             fileSvc.Size,
							UpdatedTimestamp: fileSvc.UpdatedTimestamp,
						},
					).WithVault(params.Vault), true, FileSyncDelete)
				})

			} else {
				// 不存在或已删除：仍排除客户端主动删除的路径，但不向其他端广播。
				cDelFilesKeys[delFile.Path] = struct{}{}
			}
		}
	}

	// Handle files missing on client (only for incremental sync)
	// 处理客户端缺失的文件（仅限增量同步）
	if params.LastTime > 0 && len(params.MissingFiles) > 0 {
		for _, missingFile := range params.MissingFiles {
			getParams := &dto.FileGetRequest{
				Vault:    params.Vault,
				PathHash: missingFile.PathHash,
			}
			fileSvc, err := fileService.Get(ctx, c.User.UID, getParams)
			if err != nil {
				if strings.Contains(err.Error(), "record not found") {
					h.App.Logger().Debug("websocket_router.file.FileSync.FileService.Get: missing file not found on server",
						zap.String(logger.FieldTraceID, c.TraceID),
						zap.String("pathHash", missingFile.PathHash))
				} else {
					h.App.Logger().Warn("websocket_router.file.FileSync.FileService.Get",
						zap.String(logger.FieldTraceID, c.TraceID),
						zap.String("pathHash", missingFile.PathHash),
						zap.Error(err))
				}
				continue
			}

			if fileSvc != nil && fileSvc.Action != "delete" {

				messageQueue = append(messageQueue, dto.WSQueuedMessage{
					Context: params.Context,
					Action:  FileSyncUpdate,
					Data: dto.FileSyncModifyMessage{
						Path:             fileSvc.Path,
						PathHash:         fileSvc.PathHash,
						ContentHash:      fileSvc.ContentHash,
						Size:             fileSvc.Size,
						Ctime:            fileSvc.Ctime,
						Mtime:            fileSvc.Mtime,
						UpdatedTimestamp: fileSvc.UpdatedTimestamp,
					},
				})
				needModifyCount++
				// 加入排除索引
				cDelFilesKeys[fileSvc.Path] = struct{}{}
			}
		}
	}

	// Iterate over server file list for processing
	// 遍历服务端文件列表进行处理
	for _, file := range list {
		// 如果该文件是客户端刚才通过参数告知删除的，则跳过下发
		if _, ok := cDelFilesKeys[file.Path]; ok {
			continue
		}

		// lastTime is set after the loop via timex.Now(), do not update here
		// lastTime 在循环后统一由 timex.Now() 赋值，此处不更新

		if file.Action == "delete" {
			// Server already deleted, notify client to delete (regardless of whether client has it)
			// 服务端已删除，通知客户端删除（不再检查客户端是否存在）
			cFiles.take(file.Path, file.PathHash)
			// 将消息添加到队列
			messageQueue = append(messageQueue, dto.WSQueuedMessage{
				Context: params.Context,
				Action:  FileSyncDelete,
				Data: dto.FileSyncDeleteMessage{
					Path:             file.Path,
					PathHash:         file.PathHash,
					Ctime:            file.Ctime,
					Mtime:            file.Mtime,
					Size:             file.Size,
					UpdatedTimestamp: file.UpdatedTimestamp,
				},
			})
			needDeleteCount++

		} else {

			if cFile, ok := cFiles.take(file.Path, file.PathHash); ok {
				// Client has this file
				// 客户端存在该文件

				if file.ContentHash == cFile.ContentHash && file.Mtime == cFile.Mtime {
					// Content and time match, no action
					// 内容与时间一致，无操作
					continue
				} else if file.ContentHash != cFile.ContentHash {
					// Content inconsistent
					// 内容不一致
					if file.Mtime > cFile.Mtime {
						// 服务端修改时间比客户端新, 通知客户端下载更新文件
						fileMessage := &dto.FileSyncModifyMessage{
							Path:             file.Path,
							PathHash:         file.PathHash,
							ContentHash:      file.ContentHash,
							Size:             file.Size,
							Ctime:            file.Ctime,
							Mtime:            file.Mtime,
							UpdatedTimestamp: file.UpdatedTimestamp,
						}
						// 将消息添加到队列而非立即发送
						messageQueue = append(messageQueue, dto.WSQueuedMessage{
							Context: params.Context,
							Action:  FileSyncUpdate,
							Data:    fileMessage,
						})
						needModifyCount++
					} else {
						// 服务端修改时间比客户端旧, 通知客户端上传文件
						if pkgapp.VerifyPermissions(c.Scope, "ws", c.ClientType(), "file_w") {
							if !validWSPath(cFile.Path) {
								h.App.Logger().Warn("websocket_router.file.FileSync: invalid file path, skip upload",
									zap.String(logger.FieldTraceID, c.TraceID),
									zap.String(logger.FieldPath, cFile.Path))
								continue
							}
							session, ferr := h.handleFileUploadSessionCreate(c, params.Vault, cFile.Path, cFile.PathHash, cFile.ContentHash, cFile.Size, file.Ctime, cFile.Mtime, params.Context)
							if ferr != nil {
								h.logError(c, "websocket_router.file.FileSync handleFileUploadSession err", ferr)
								continue
							}
							// 将消息添加到队列而非立即发送
							messageQueue = append(messageQueue, dto.WSQueuedMessage{
								Context: params.Context,
								Action:  FileUpload,
								Data: dto.FileSyncUploadMessage{
									Path:      session.Path,
									PathHash:  session.PathHash,
									SessionID: session.ID,
									ChunkSize: session.ChunkSize,
								},
							})
							needUploadCount++
						} else {
							h.App.Logger().Warn("websocket_router.file.FileSync: permission denied for upload",
								zap.String(logger.FieldTraceID, c.TraceID),
								zap.Int64(logger.FieldUID, c.User.UID),
								zap.String(logger.FieldPath, cFile.Path))
						}
					}
				} else {
					// Content matches, but modification time differs, notify client to update file modification time
					// 内容一致, 但修改时间不一致, 通知客户端更新文件修改时间
					// Add message to queue instead of sending immediately
					// 将消息添加到队列而非立即发送
					messageQueue = append(messageQueue, dto.WSQueuedMessage{
						Context: params.Context,
						Action:  FileSyncMtime,
						Data: dto.FileSyncMtimeMessage{
							Path:             file.Path,
							Ctime:            file.Ctime,
							Mtime:            file.Mtime,
							UpdatedTimestamp: file.UpdatedTimestamp,
						},
					})
					needSyncMtimeCount++
				}
			} else {
				// File client doesn't have, notify client to download file
				// 客户端没有的文件, 通知客户端下载文件
				// 将消息添加到队列而非立即发送
				messageQueue = append(messageQueue, dto.WSQueuedMessage{
					Context: params.Context,
					Action:  FileSyncUpdate,
					Data: dto.FileSyncModifyMessage{
						Path:             file.Path,
						PathHash:         file.PathHash,
						ContentHash:      file.ContentHash,
						Size:             file.Size,
						Ctime:            file.Ctime,
						Mtime:            file.Mtime,
						UpdatedTimestamp: file.UpdatedTimestamp,
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
	// Handle files that exist on client but not synced on server (request client upload)
	// 处理客户端存在但服务端未同步的文件（请求客户端上传）
	if remaining := cFiles.remaining(); len(remaining) > 0 {
		// 增量同步（lastTime > 0）时，窗口外条目先按路径批量查服务端当前行：
		// 最新一行是存活行且内容哈希、修改时间都与客户端上报一致时静默跳过，
		// 不建上传会话、不打无权限警告。其余情形（服务端没有、墓碑、哈希或修改时间
		// 不同）保持原有"要求上传"的判定不变。
		// In incremental sync (lastTime > 0), batch-fetch the server's current rows for
		// out-of-window entries first: when the latest row is alive and both content hash
		// and mtime match the client report, skip silently — no upload session and no
		// permission-denied warning. All other cases keep the existing verdict.
		var serverFiles map[string]*dto.FileDTO
		if params.LastTime > 0 {
			paths := make([]string, 0, len(remaining))
			for _, file := range remaining {
				if file.Path != "" {
					paths = append(paths, file.Path)
				}
			}
			metaMap, metaErr := fileService.GetPathsMetaBatch(ctx, c.User.UID, params.Vault, paths)
			if metaErr != nil {
				// 查询失败退回原有行为：当作服务端没有，照旧要求上传
				// On query failure fall back to the old behavior: treat as missing and request upload
				h.App.Logger().Warn("websocket_router.file.FileSync.GetPathsMetaBatch",
					zap.String(logger.FieldTraceID, c.TraceID),
					zap.Int64(logger.FieldUID, c.User.UID),
					zap.Error(metaErr))
			} else {
				serverFiles = metaMap
			}
		}

		hasWritePermission := pkgapp.VerifyPermissions(c.Scope, "ws", c.ClientType(), "file_w")
		for _, file := range remaining {
			// 客户端带了路径时按路径核对（与窗口内 cFiles.take 的路径优先语义一致，
			// 路径哈希碰撞的两条不同路径不会互相顶替）；老客户端没带路径时不做比对。
			// When the client supplied a path, match by path (same path-first
			// semantics as cFiles.take in-window; colliding paths never substitute for
			// each other); legacy clients without a path are not compared.
			if file.Path != "" {
				if sFile, ok := serverFiles[file.Path]; ok && sFile != nil &&
					sFile.Action != "delete" && sFile.ContentHash == file.ContentHash && sFile.Mtime == file.Mtime {
					continue
				}
			}

			// 服务端没有存活行时，系统垃圾文件不再要求客户端上传（老客户端每轮都会
			// 上报本地垃圾文件，发起会话只会被写入入口拒收）。服务端已有的垃圾行仍走
			// 原有判定，历史数据照常下发与删除。
			// When the server has no live row, do not request an upload for a system junk
			// file; existing server junk rows keep the original verdict.
			if file.Path != "" && h.writeRejectsSystemJunk(file.Path) {
				if sFile, ok := serverFiles[file.Path]; !ok || sFile == nil || sFile.Action == "delete" {
					continue
				}
			}

			// Create upload session and return FileUpload message
			// 创建上传会话并返回 FileUpload 消息
			if hasWritePermission {
				if !validWSPath(file.Path) {
					h.App.Logger().Warn("websocket_router.file.FileSync: invalid file path, skip upload",
						zap.String(logger.FieldTraceID, c.TraceID),
						zap.String(logger.FieldPath, file.Path))
					continue
				}
				session, ferr := h.handleFileUploadSessionCreate(c, params.Vault, file.Path, file.PathHash, file.ContentHash, file.Size, file.Ctime, file.Mtime, params.Context)
				if ferr != nil {
					h.logError(c, "websocket_router.file.FileSync handleFileUploadSession err", ferr)
					continue
				}
				// Add message to queue instead of sending immediately
				messageQueue = append(messageQueue, dto.WSQueuedMessage{
					Context: params.Context,
					Action:  FileUpload,
					Data: dto.FileSyncUploadMessage{
						Path:      session.Path,
						PathHash:  session.PathHash,
						SessionID: session.ID,
						ChunkSize: session.ChunkSize,
					},
				})
				needUploadCount++
			} else {
				h.App.Logger().Warn("websocket_router.file.FileSync: permission denied for missing file upload",
					zap.String(logger.FieldTraceID, c.TraceID),
					zap.Int64(logger.FieldUID, c.User.UID),
					zap.String(logger.FieldPath, file.Path))
			}
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
			TypeName:     "file",
			Vault:        params.Vault,
			MessageQueue: messageQueue,
			PageSize:     pageSize,
			Window:       window,
		}
		entry.ConnDone = connDone(c)
		syncDownloadStore(params.Context, "file", entry)
		// 默认不自动发送，等待客户端拉取
	}

	// Send FileSyncEnd message
	// 发送 FileSyncEnd 消息
	c.ToResponse(code.Success.WithData(
		dto.FileSyncEndMessage{
			LastTime:           lastTime,
			NeedUploadCount:    needUploadCount,
			NeedModifyCount:    needModifyCount,
			NeedSyncMtimeCount: needSyncMtimeCount,
			NeedDeleteCount:    needDeleteCount,
		},
	).WithVault(params.Vault).WithContext(params.Context), FileSyncEnd)
}

// FileSyncPageAck handles WebSocket messages for client page ACK
// FileSyncPageAck 处理客户端发来的分页下载 ACK 消息
func (h *FileWSHandler) FileSyncPageAck(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.SyncPageAckRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.file.FileSyncPageAck.BindAndValid", msg)
		return
	}

	entry, ok := syncDownloadGet(params.Context, "file")
	if !ok {
		h.App.Logger().Warn("FileSyncPageAck: sync download entry not found",
			zap.String(logger.FieldTraceID, c.TraceID),
			zap.String("context", params.Context))
		return
	}

	handlePageAck(c, entry, params.PageIndex, "file", h.App.Logger(), c.TraceID)
}

// cleanupSession cleans up discarded upload sessions due to completion or timeout.
// cleanupSession 清理因为完成或超时而废弃的上传会话。
func (h *FileWSHandler) handleFileUploadSessionCleanup(c *pkgapp.WebsocketClient, sessionID string) {
	binarySession := c.Server.GetSession(c.User.ID, sessionID)
	if binarySession == nil {
		return
	}
	c.Server.RemoveSession(c.User.ID, sessionID)

	session := binarySession.(pkgapp.SessionCleaner)
	session.Cleanup()

	h.logInfo(c, "cleanupSession: session cleaned up",
		zap.String("sessionID", sessionID))
}

func (h *FileWSHandler) handleFileUploadSessionTimeout(c *pkgapp.WebsocketClient, sessionID string, timeout time.Duration) context.CancelFunc {
	if timeout <= 0 {
		return nil
	}

	// Use context.Background() to ensure timeout timer is not affected by connection loss
	// This keeps the session valid during network fluctuation and reconnection
	// 使用 context.Background() 确保超时定时器不受连接断开影响
	// 这样在网络波动重连期间，会话依然有效
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case <-timer.C:
			// Timeout triggered, clean up session
			// 超时触发，清理会话
			h.logWarn(c, "startSessionTimeout: session timeout, cleaning up",
				zap.String("sessionID", sessionID),
				zap.Duration("timeout", timeout))

			// Execute cleanup on timeout
			// 超时执行清理
			h.handleFileUploadSessionCleanup(c, sessionID)
		case <-ctx.Done():
			// Cancel timer
			// 取消定时器
			return
		}
	}()

	return cancel
}

// handleFileUploadSession initializes a file upload session and returns upload message.
// handleFileUploadSession 初始化一个文件上传会话并返回上传消息.
func (h *FileWSHandler) handleFileUploadSessionCreate(c *pkgapp.WebsocketClient, vault, path, pathHash, contentHash string, size, ctime, mtime int64, context string) (*FileUploadBinaryChunkSession, error) {
	// 写入口统一拒收系统垃圾文件；同步清单已在发起上传前跳过垃圾路径，
	// 这里兜住“服务端已有历史垃圾条目、客户端内容更新”的写入。
	if h.writeRejectsSystemJunk(path) {
		return nil, code.ErrorInvalidPath.WithDetails("system junk file is not synced")
	}
	// Reuse a session only for the same vault and full path with matching attributes,
	// and only while it is still accepting chunks. Shared fields are read/written
	// under session.mu because a parallel chunk write holds the same lock.
	// 仅复用同一库、同一完整路径且属性一致、且仍在接收分片的会话。共享字段在
	// session.mu 下读写，因为并发分片写入持有同一把锁。
	if existingSession := c.Server.GetSessionByPathHash(c.User.ID, pathHash, vault, path); existingSession != nil {
		if session, ok := existingSession.(*FileUploadBinaryChunkSession); ok {
			session.mu.Lock()
			// 最后一片已写完（UploadedChunks 已到 TotalChunks，或已标记完成）的会话正在
			// 收尾：复用会让客户端以为还要重传、拿到全部已传分片后空等 FileUploadAck。
			// A session whose last chunk was already written is finishing: reusing it
			// would leave the client waiting for an ack that never comes.
			finished := session.isCompleted || (session.TotalChunks > 0 && session.UploadedChunks >= session.TotalChunks)
			reusable := !finished && session.ContentHash == contentHash && session.Vault == vault && session.Size == size
			if reusable {
				// Context 无条件覆盖（含清空）：上一轮被取消/重连淘汰后，新请求不带
				// context 时必须清掉旧值，否则完成 ack 会带着已作废的 context 被插件丢弃。
				// Always overwrite Context, including clearing it: after the previous
				// round was retired by cancel/reconnect, a context-free request must
				// clear the stale value or the completion ack is discarded by the plugin.
				session.Context = context
				// 保留会话期间文件属性可能变化（仅 mtime 变动时会走到这里），刷新落库用的时间戳。
				// File attributes may have changed while the session lived (an mtime-only
				// change lands here); refresh the timestamps used when persisting.
				session.Ctime = ctime
				session.Mtime = mtime
				session.mu.Unlock()
				h.App.Logger().Info("FileUploadSessionCreate: reusing existing active session for path hash",
					zap.String("traceId", c.TraceID),
					zap.Int64("uid", c.User.UID),
					zap.String("sessionID", session.ID),
					zap.String("path", path),
				)
				return session, nil
			}
			session.mu.Unlock()
		}
	}

	// Clean up any existing stale sessions for the same vault and full path to prevent resource leak and duplication
	h.App.Logger().Info("FileUploadSessionCreate: cleaning existing stale sessions for path hash",
		zap.String("traceId", c.TraceID),
		zap.Int64("uid", c.User.UID),
		zap.String("pathHash", pathHash),
		zap.String("path", path),
	)
	c.Server.CleanSessionsByPathHash(c.User.ID, pathHash, vault, path)

	sessionID := uuid.New().String()
	h.App.Logger().Info("FileUploadSessionCreate: creating upload session",
		zap.String(logger.FieldTraceID, c.TraceID),
		zap.Int64(logger.FieldUID, c.User.UID),
		zap.String("sessionID", sessionID),
		zap.String("path", path),
		zap.String("pathHash", pathHash),
		zap.Int64("size", size),
	)

	cfg := h.App.Config()
	tempDir := cfg.App.TempPath
	if tempDir == "" {
		tempDir = "storage/temp"
	}

	// Create temp directory
	// 创建临时目录
	if err := os.MkdirAll(tempDir, 0754); err != nil {
		h.logError(c, "websocket_router.file.handleFileUploadSession.MkdirAll", err)
		return nil, err
	}

	var tempPath string

	for i := 0; i < 10; i++ {
		tryFileName := uuid.New().String()
		tryPath := filepath.Join(tempDir, tryFileName)

		if _, err := os.Stat(tryPath); os.IsNotExist(err) {
			tempPath = tryPath
			break
		}
	}

	// Initialize chunk upload session
	// 初始化分块上传会话
	session := &FileUploadBinaryChunkSession{
		ID:             sessionID,
		Vault:          vault,
		Path:           path,
		PathHash:       pathHash,
		ContentHash:    contentHash,
		Size:           size,
		Ctime:          ctime,
		Mtime:          mtime,
		ChunkSize:      getChunkSizeFromConfig(cfg), // 从注入的配置获取
		SavePath:       tempPath,
		FileHandle:     nil, // Lazy creation in FileUploadChunkBinary // 在 FileUploadChunkBinary 中延迟创建
		CreatedAt:      time.Now(),
		uploadedChunks: make(map[uint32]struct{}),
		Context:        context,
	}
	// Adjust chunk size based on file size
	// 根据文件大小调整分块大小
	session.TotalChunks = util.Ceil(session.Size, session.ChunkSize)

	// Configure timeout
	// 配置超时时间
	var timeout time.Duration
	if cfg.App.UploadSessionTimeout != "" && cfg.App.UploadSessionTimeout != "0" {
		var err error
		timeout, err = util.ParseDuration(cfg.App.UploadSessionTimeout)
		if err != nil {
			h.logWarn(c, "handleFileUploadSession: invalid upload-session-timeout config, using default 20m",
				zap.String("config", cfg.App.UploadSessionTimeout),
				zap.Error(err))
			timeout = 20 * time.Minute
		}
	} else {
		timeout = 20 * time.Minute
	}

	// Start timeout cleanup task
	// 启动超时清理任务
	session.CancelFunc = h.handleFileUploadSessionTimeout(c, sessionID, timeout)

	// Register to global server
	// 注册到全局服务器
	c.Server.SetSession(c.User.ID, session.ID, session)

	return session, nil
}

// handleFileChunkDownloadSendChunks executes file chunk sending.
// Runs in independent goroutine, reads file and sends binary chunks via WebSocket.
// handleFileChunkDownloadSendChunks 执行文件分片发送。
// 在独立的 goroutine 中运行,读取文件并通过 WebSocket 发送二进制分片。
func (h *FileWSHandler) handleFileChunkDownloadSendChunks(c *pkgapp.WebsocketClient, session *FileDownloadChunkSession) {
	logger := h.App.Logger()
	// Get timeout from config, default 1 hour
	// 从配置获取超时时间，默认 1 小时
	timeout := 1 * time.Hour
	cfg := h.App.Config()
	if cfg.App.DownloadSessionTimeout != "" && cfg.App.DownloadSessionTimeout != "0" {
		if t, err := util.ParseDuration(cfg.App.DownloadSessionTimeout); err == nil {
			timeout = t
		} else {
			LogWarnWithLogger(logger, c, "sendFileChunks: invalid download-session-timeout config, using default 1h",
				zap.String("config", cfg.App.DownloadSessionTimeout),
				zap.Error(err))
		}
	}

	// Create timeout context, based on WebSocket connection context
	// 创建超时上下文，基于 WebSocket 连接的 context
	ctx, cancel := context.WithTimeout(c.Context(), timeout)
	defer cancel()

	// Open file
	// 打开文件
	file, err := os.Open(session.SavePath)
	if err != nil {
		LogErrorWithLogger(logger, c, "sendFileChunks: failed to open file", err)
		c.ToResponse(code.ErrorFileGetFailed.WithDetails("failed to open file"))
		return
	}
	defer file.Close()

	LogInfoWithLogger(logger, c, "sendFileChunks: starting file download",
		zap.String("sessionID", session.SessionID),
		zap.String("path", session.Path),
		zap.Int64("size", session.Size),
		zap.Int64("totalChunks", session.TotalChunks))

	// Loop to send chunks
	// 循环发送分片
	for chunkIndex := int64(0); chunkIndex < session.TotalChunks; chunkIndex++ {
		// Check timeout
		// 检查超时
		select {
		case <-ctx.Done():
			LogWarnWithLogger(logger, c, "sendFileChunks: download timeout",
				zap.String("sessionID", session.SessionID),
				zap.Int64("sentChunks", chunkIndex),
				zap.Int64("totalChunks", session.TotalChunks))
			return
		default:
		}

		// Calculate current chunk size
		// 计算当前分片的大小
		chunkStart := chunkIndex * session.ChunkSize
		chunkEnd := chunkStart + session.ChunkSize
		if chunkEnd > session.Size {
			chunkEnd = session.Size
		}
		currentChunkSize := chunkEnd - chunkStart

		// 读取分片数据
		chunkData := make([]byte, currentChunkSize)
		n, err := file.ReadAt(chunkData, chunkStart)
		if err != nil && err.Error() != "EOF" {
			LogErrorWithLogger(logger, c, "sendFileChunks: failed to read chunk", err)
			c.ToResponse(code.ErrorFileGetFailed.WithDetails("failed to read file chunk"))
			return
		}

		// 构造二进制消息
		// 格式: [36 bytes session_id][4 bytes chunk_index][chunk_data]
		headerSize := 40
		packet := make([]byte, headerSize+n)

		// 1. Session ID (36 bytes)
		copy(packet[0:36], []byte(session.SessionID))

		// 2. Chunk Index (4 bytes, Big Endian)
		binary.BigEndian.PutUint32(packet[36:40], uint32(chunkIndex))

		// 3. Chunk Data
		copy(packet[40:], chunkData[:n])

		// 发送二进制消息
		err = c.SendBinary(VaultFileMsgType, packet)
		if err != nil {
			LogErrorWithLogger(logger, c, "sendFileChunks: failed to send chunk", err)
			return
		}

		// 每发送 100 个分片记录一次日志
		if (chunkIndex+1)%100 == 0 || chunkIndex == session.TotalChunks-1 {
			LogInfoWithLogger(logger, c, "sendFileChunks: progress",
				zap.String("sessionID", session.SessionID),
				zap.Int64("sent", chunkIndex+1),
				zap.Int64("total", session.TotalChunks))
		}
	}

	LogInfoWithLogger(logger, c, "sendFileChunks: download completed",
		zap.String("sessionID", session.SessionID),
		zap.String("path", session.Path),
		zap.Int64("totalChunks", session.TotalChunks))
}

func (h *FileWSHandler) FileRePush(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) {
	params := &dto.FileGetRequest{}
	valid, errs := c.BindAndValidWithAction(msg.Type, msg.Data, params)
	if !valid {
		h.respondErrorWithData(c, code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()), errs, errs.MapsToString(), "websocket_router.file.FileRePush.BindAndValid", msg)
		return
	}

	pkgapp.NoteModifyLog(c.TraceID, c.User.UID, "FileRePush", params.Path, params.Vault)

	ctx := c.Context()
	// 获取或创建仓库
	h.App.VaultService.GetOrCreate(ctx, c.User.UID, params.Vault)

	fileSvc, err := h.App.GetFileService(c.ClientType(), c.ClientName(), c.ClientVersion()).Get(ctx, c.User.UID, params)
	if err != nil {
		// 只有"确实不存在"才下发删除；哈希歧义、数据库错误等必须回错误，否则客户端
		// 会删掉服务端明明存活的附件。
		// Only a genuine "not found" may turn into a delete; ambiguous hashes and
		// database errors must return an error instead of deleting a live file locally.
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			h.respondErrorWithPath(c, code.ErrorFileGetFailed, err, params.Path, msg.Type, "websocket_router.file.FileRePush.Get")
			return
		}
		h.App.Logger().Debug("websocket_router.file.FileRePush.Get: file not found, proceeding to send delete",
			zap.String(logger.FieldTraceID, c.TraceID),
			zap.Error(err))
	}

	if fileSvc != nil && fileSvc.Action != "delete" {
		c.ToResponse(code.Success.WithData(
			dto.FileSyncModifyMessage{
				Path:             fileSvc.Path,
				PathHash:         fileSvc.PathHash,
				ContentHash:      fileSvc.ContentHash,
				Size:             fileSvc.Size,
				Ctime:            fileSvc.Ctime,
				Mtime:            fileSvc.Mtime,
				UpdatedTimestamp: fileSvc.UpdatedTimestamp,
			},
		).WithVault(params.Vault), FileSyncUpdate)
	} else {
		// If file not found, send delete message to client to clean up local unauthorized creation
		// 如果未找到文件，则向客户端发送删除消息，以清理本地未授权的创建
		c.ToResponse(code.Success.WithData(
			dto.FileSyncDeleteMessage{
				Path:     params.Path,
				PathHash: params.PathHash,
			},
		).WithVault(params.Vault), string(FileSyncDelete))
	}
}

// getChunkSizeFromConfig 从注入的配置获取分片大小, 默认为 512KB
func getChunkSizeFromConfig(cfg *app.AppConfig) int64 {
	return util.ParseSize(cfg.App.FileChunkSize, 1024*512)
}
