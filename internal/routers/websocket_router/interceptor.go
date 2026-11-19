package websocket_router

import (
	"strings"

	"github.com/haierkeys/fast-note-sync-service/internal/app"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/json"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewMessageInterceptor 创建 WebSocket 业务消息前置拦截器，依次执行认证、Vault 和 RBAC 检查。
// NewMessageInterceptor creates a WebSocket business message pre-handler interceptor
// that sequentially enforces auth, vault access, and RBAC checks.
func NewMessageInterceptor(appContainer *app.App) func(*pkgapp.WebsocketClient, *pkgapp.WebSocketMessage) bool {
	logger := appContainer.Logger()
	strictEmptyProtocol := appContainer.Config().Security.StrictEmptyProtocol
	return func(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage) bool {
		if !checkAuth(c, msg, logger) {
			return false
		}
		if !checkVaultAccess(c, msg, logger) {
			return false
		}
		if !checkRBAC(c, msg, logger, strictEmptyProtocol) {
			return false
		}
		return true
	}
}

// checkAuth 验证用户是否已完成身份认证。
// checkAuth verifies that the client has a valid authenticated user session.
func checkAuth(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, logger interface {
	Warn(string, ...zapcore.Field)
}) bool {
	if c.User == nil {
		logger.Warn("WS User not authenticated",
			zap.String("msgType", msg.Type),
			zap.String("traceId", c.TraceID))
		c.ToResponse(code.ErrorNotUserAuthToken)
		return false
	}
	return true
}

// vaultOptionalMessageTypes lists receive actions whose payloads are
// user-level and carry no vault field. For these, a vault-restricted token is
// still allowed to proceed (the message simply has nothing to check). Every
// other message type MUST carry a vault field when the token is restricted:
// omitting it no longer bypasses the restriction (B11).
// vaultOptionalMessageTypes 列出负载属于用户级、不带 vault 字件的接收动作。
// 对这些动作，受限令牌仍可继续（消息本身没有可校验的库）。其它消息类型在
// 令牌受限时必须携带 vault 字段，缺字段不再绕过限制（B11）。
var vaultOptionalMessageTypes = map[WebSocketReceiveAction]struct{}{
	// Connection lifecycle messages handled before business interceptors; kept
	// here for completeness so the guard stays explicit if routing changes.
	// 连接生命周期消息在业务拦截器之前处理；此处显式列出以便路由变化时
	// 守卫仍然明确。
	ClientReceiveInfo:    {},
	ClientReceiveAuth:    {},
	VaultFileMsgType:     {}, // binary frames: session-bound, vault checked at session creation
	SettingReceiveCheck:  {}, // per-item check payload is user-level (path only)
	SettingReceiveRePush: {}, // per-item repush payload is user-level (path only)
}

// checkVaultAccess 针对活跃 WebSocket 连接校验笔记库访问权限限制。
// checkVaultAccess validates that the requested vault is within the client's allowed scope.
func checkVaultAccess(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, logger interface {
	Warn(string, ...zapcore.Field)
}) bool {
	if c.Vaults == "" {
		return true
	}
	vault := resolveRequestVault(msg)
	if vault == "" {
		if _, optional := vaultOptionalMessageTypes[msg.Type]; optional {
			return true
		}
		// A restricted token must not reach vault-scoped handlers by omitting
		// the vault field; fail closed instead of passing through (B11).
		// 受限令牌不得靠省略 vault 字段进入库级处理；直接拒绝而不是放行（B11）。
		logger.Warn("WS OnMessage Vault Missing",
			zap.String("Type", msg.Type),
			zap.String("uid", c.User.ID))
		// 能提取到路径时附带 data.path（改名再带 oldPath），让插件释放该动作占用的
		// 并发名额；JSON 与 Protobuf 上行都能提取。
		// Attach data.path (plus oldPath on rename) when extractable so the plugin
		// can release the slot; both JSON and Protobuf uplink are supported.
		denied := code.ErrorAuthTokenScopeRestricted.WithDetails("Vault access restricted: missing vault")
		denied = attachErrorSlotData(denied, msg)
		c.ToResponse(denied, msg.Type+"Ack")
		return false
	}
	if !util.VerifyVaultAccess(c.Vaults, vault) {
		logger.Warn("WS OnMessage Vault Restricted",
			zap.String("Type", msg.Type),
			zap.String("uid", c.User.ID),
			zap.String("vault", vault))
		denied := code.ErrorAuthTokenScopeRestricted.WithDetails("Vault access restricted: " + vault)
		denied = attachErrorSlotData(denied, msg)
		c.ToResponse(denied, msg.Type+"Ack")
		return false
	}
	return true
}

// resolveRequestVault extracts the vault from an action-level payload regardless
// of the uplink wire format. Protobuf payloads are decoded field-wise; JSON text
// frames fall back to the "vault" key.
// resolveRequestVault 无论上行编码是 JSON 还是 Protobuf，都提取动作级载荷中的 vault。
// Protobuf 载荷按字段解码，JSON 文本帧则读取 "vault" 键。
func resolveRequestVault(msg *pkgapp.WebSocketMessage) string {
	if meta, ok := requestMeta(msg); ok {
		return meta.Vault
	}
	var vaultInfo struct {
		Vault string `json:"vault"`
	}
	_ = json.Unmarshal(msg.Data, &vaultInfo)
	return vaultInfo.Vault
}

// attachErrorSlotData adds the client-reported path (and oldPath for rename
// actions) to a denial response so the plugin can release the concurrency slot
// it reserved for the action and, for rename, roll the local rename back.
// attachErrorSlotData 给拒绝响应附带客户端上报的 path（改名动作再带 oldPath），
// 让插件释放该动作占用的并发名额，改名失败时还能回滚本地改名。
func attachErrorSlotData(denied *code.Code, msg *pkgapp.WebSocketMessage) *code.Code {
	path, oldPath := resolveErrorSlotPaths(msg)
	if path == "" && oldPath == "" {
		return denied
	}
	data := make(map[string]string, 2)
	if path != "" {
		data["path"] = path
	}
	if oldPath != "" {
		data["oldPath"] = oldPath
	}
	return denied.WithData(data)
}

// checkRBAC 将操作映射到 RBAC 权限功能点并校验客户端权限，权限不足时触发回滚。
// checkRBAC maps the message type to an RBAC function and verifies the client's scope.
// On denial it delegates to handlePermissionDenied for client-side rollback.
func checkRBAC(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, logger interface {
	Warn(string, ...zapcore.Field)
	Info(string, ...zapcore.Field)
}, strictEmptyProtocol bool) bool {
	function := resolveRBACFunction(msg.Type)
	if function == "" {
		return true // 无需权限检查的消息类型，直接放行 / message type requires no RBAC check
	}
	if pkgapp.VerifyPermissionsWithStrictEmptyProtocol(c.Scope, "ws", c.ClientType(), function, strictEmptyProtocol) {
		return true
	}
	logger.Warn("WS OnMessage Permission Denied",
		zap.String("Type", msg.Type),
		zap.String("uid", c.User.ID),
		zap.String("function", function))
	return handlePermissionDenied(c, msg, function, logger)
}

// resolveRBACFunction 将 WebSocket 消息类型映射到 RBAC 功能点字符串。
// resolveRBACFunction maps a WebSocket message type to its corresponding RBAC function key.
// Returns an empty string if no permission check is required for the given type.
func resolveRBACFunction(msgType string) string {
	switch msgType {
	case NoteReceiveSync, NoteReceiveCheck, NoteReceiveRePush, FolderReceiveSync:
		return "note_r"
	case NoteReceiveModify, NoteReceiveDelete, NoteReceiveRename, FolderReceiveModify, FolderReceiveDelete, FolderReceiveRename:
		return "note_w"
	case FileReceiveChunkDownload, FileReceiveRePush, FileReceiveSync:
		return "file_r"
	case FileReceiveUploadCheck, FileReceiveDelete, FileReceiveRename:
		return "file_w"
	case SettingReceiveSync, SettingReceiveCheck, SettingReceiveRePush:
		return "config_r"
	case SettingReceiveModify, SettingReceiveDelete, SettingReceiveClear:
		return "config_w"
	}
	return ""
}

// handlePermissionDenied 在权限拒绝后向客户端发送错误响应，并对写操作触发回滚同步。
// handlePermissionDenied sends an error response and, for write operations,
// triggers a compensating action (rename-back or re-push) to keep the client consistent.
// Always returns false to halt message processing.
//
// data 携带客户端上报的原样路径（JSON 上行可提取时）：插件在发出占名额动作前已按路径
// 占用并发名额，其通用出错分支需要 data.path 才能释放；回推 / 改名回滚补偿逻辑不变。
// The data carries the client-reported path as-is (extractable on JSON uplink only):
// the plugin reserved a concurrency slot by path before sending the action and its
// generic error branch needs data.path to release it; the re-push / rename-back
// compensation logic is unchanged.
func handlePermissionDenied(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, function string, logger interface {
	Info(string, ...zapcore.Field)
}) bool {
	resPath := resolveResourcePath(msg)
	denied := code.ErrorAuthTokenScopeRestricted.WithDetails("Permission denied: " + resPath)
	denied = attachErrorSlotData(denied, msg)
	c.ToResponse(denied, msg.Type+"Ack")

	if !strings.HasSuffix(function, "_w") {
		return false
	}

	// 写操作：优先尝试重命名回滚，失败则触发重推
	// Write operations: attempt rename-rollback first, fall back to re-push
	if strings.HasSuffix(msg.Type, "Rename") && rollbackRename(c, msg, function, logger) {
		return false
	}
	triggerRePush(c, msg, function, logger)
	return false
}

// resolveResourcePath 从消息数据中提取资源路径用于错误描述，优先取 path，其次取 name，最后回退到消息类型。
// resolveResourcePath extracts a human-readable resource identifier from message data,
// falling back to the message type when neither path nor name are present.
func resolveResourcePath(msg *pkgapp.WebSocketMessage) string {
	if meta, ok := requestMeta(msg); ok {
		if meta.Path != "" {
			return meta.Path
		}
		return msg.Type
	}
	var pathInfo struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(msg.Data, &pathInfo)
	if pathInfo.Path != "" {
		return pathInfo.Path
	}
	if pathInfo.Name != "" {
		return pathInfo.Name
	}
	return msg.Type
}

// resolveErrorSlotPaths extracts the path the plugin used to reserve its
// concurrency slot and, for rename actions, the old path needed for rollback.
// JSON uplink carries both in the payload; Protobuf uplink is decoded field-wise
// by action (see decodeProtobufRequestMeta).
// resolveErrorSlotPaths 提取插件占并发名额所用的路径；改名动作再取出回滚所需的 oldPath。
// JSON 上行直接读载荷字段；Protobuf 上行按动作字段编号解码（见 decodeProtobufRequestMeta）。
func resolveErrorSlotPaths(msg *pkgapp.WebSocketMessage) (path, oldPath string) {
	if meta, ok := requestMeta(msg); ok {
		return meta.Path, meta.OldPath
	}
	var pathInfo struct {
		Path    string `json:"path"`
		OldPath string `json:"oldPath"`
	}
	_ = json.Unmarshal(msg.Data, &pathInfo)
	return pathInfo.Path, pathInfo.OldPath
}

// rollbackRename 对重命名操作发送反向重命名消息，使客户端回退到原始路径。
// rollbackRename sends a compensating rename message so the client reverts to the original path.
// Returns true if the rollback message was successfully dispatched.
func rollbackRename(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, function string, logger interface {
	Info(string, ...zapcore.Field)
}) bool {
	var vault, newPath, newPathHash, oldPath, oldPathHash string
	if meta, ok := requestMeta(msg); ok {
		vault, newPath, newPathHash = meta.Vault, meta.Path, meta.PathHash
		oldPath, oldPathHash = meta.OldPath, meta.OldPathHash
	} else {
		var renameData map[string]interface{}
		if err := json.Unmarshal(msg.Data, &renameData); err != nil {
			return false
		}
		vault, _ = renameData["vault"].(string)
		newPath, _ = renameData["path"].(string)
		newPathHash, _ = renameData["pathHash"].(string)
		oldPath, _ = renameData["oldPath"].(string)
		oldPathHash, _ = renameData["oldPathHash"].(string)
	}

	if newPath == "" || oldPath == "" {
		return false
	}

	syncRenameAction := resolveSyncRenameAction(function, msg.Type)
	if syncRenameAction == "" {
		return false
	}

	rollbackData := map[string]interface{}{
		"path":        oldPath,
		"pathHash":    oldPathHash,
		"oldPath":     newPath,
		"oldPathHash": newPathHash,
	}
	c.ToResponse(code.Success.WithData(rollbackData).WithVault(vault), syncRenameAction)
	// 重命名回滚后无需再触发 RePush / No subsequent re-push needed after rename rollback
	return true
}

// resolveSyncRenameAction 根据 RBAC 功能点和消息类型确定用于回滚的同步重命名动作名称。
// resolveSyncRenameAction returns the compensating sync-rename action name for a given function and message type.
func resolveSyncRenameAction(function, msgType string) string {
	switch function {
	case "note_w":
		if strings.Contains(msgType, "Folder") {
			return FolderSyncRename
		}
		return NoteSyncRename
	case "file_w":
		return FileSyncRename
	}
	return ""
}

// triggerRePush 对写操作权限拒绝后触发对应的重推动作，保证客户端数据一致性。
// triggerRePush invokes the corresponding re-push handler to restore client-side consistency
// after a write operation is denied.
func triggerRePush(c *pkgapp.WebsocketClient, msg *pkgapp.WebSocketMessage, function string, logger interface {
	Info(string, ...zapcore.Field)
}) {
	rePushAction := resolveRePushAction(function)
	if rePushAction == "" {
		return
	}
	if h, ok := c.Server.GetHandler(rePushAction); ok {
		logger.Info("WS Trigger RePush on permission denied",
			zap.String("action", rePushAction),
			zap.String("uid", c.User.ID))
		h(c, msg)
	}
}

// resolveRePushAction 根据 RBAC 功能点返回对应的重推动作名称。
// resolveRePushAction returns the re-push action name associated with the given RBAC write function.
func resolveRePushAction(function string) string {
	switch function {
	case "note_w":
		return NoteReceiveRePush
	case "file_w":
		return FileReceiveRePush
	case "config_w":
		return SettingReceiveRePush
	}
	return ""
}
