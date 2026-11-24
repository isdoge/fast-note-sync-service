package api_router

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/middleware"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
)

// UserCapabilityHandler reports the effective capabilities of the token that
// authenticated the request. Mounted on the plain auth group, so both WebGUI
// login tokens and restricted manual tokens can query their own scope without
// any extra function point.
// UserCapabilityHandler 报告本次请求所用令牌的有效能力。挂在普通鉴权组，
// WebGUI 登录令牌与受限手动令牌都能查询自己的 scope，无需额外功能点。
type UserCapabilityHandler struct {
	*Handler
}

// NewUserCapabilityHandler creates UserCapabilityHandler instance.
// NewUserCapabilityHandler 创建 UserCapabilityHandler 实例。
func NewUserCapabilityHandler(a *app.App) *UserCapabilityHandler {
	return &UserCapabilityHandler{Handler: NewHandler(a)}
}

// TokenCapabilities returns the effective permissions of the current token.
// @Summary Get token capabilities
// @Description Report the effective protocol, vault allowlist, read/write permissions and read-only flag of the authenticated token. Never returns secrets or the raw token.
// @Tags User
// @Security UserAuthToken
// @Produce json
// @Success 200 {object} pkgapp.Res{data=dto.TokenCapabilitiesResponse} "Success"
// @Router /api/user/token-capabilities [get]
func (h *UserCapabilityHandler) TokenCapabilities(c *gin.Context) {
	response := pkgapp.NewResponse(c)
	uid := pkgapp.GetUID(c)
	if uid == 0 {
		response.ToResponse(code.ErrorInvalidUserAuthToken)
		return
	}

	scope := c.GetString("scope")
	vaultsRaw := c.GetString("vaults")
	// The entry middleware records the client type it validated; fall back to
	// the request header for requests authenticated outside it.
	clientType, _ := c.Get("auth_rest_client")
	client := ""
	if v, ok := clientType.(string); ok {
		client = v
	}
	if client == "" {
		client = c.GetHeader("x-client")
	}

	strict := false
	if v, ok := c.Get("auth_strict_empty_protocol"); ok {
		strict, _ = v.(bool)
	}

	protocol := scopeProtocol(scope)
	// Report the union of REST and WebSocket capabilities: a sync token
	// normally carries both (p:rest,ws) and clients care about both channels.
	// A protocol-restricted token simply gets false on the channel it cannot
	// use, so the report never overstates access.
	// 报告 REST 与 WebSocket 能力的并集：同步令牌通常同时含两者
	//（p:rest,ws），客户端也关心两条通道；仅限单一协议的令牌在另一通道上
	// 自然是 false，报告不会高估权限。
	perms := unionCapabilities(
		tokenCapabilityFunctions(scope, "rest", client, strict),
		tokenCapabilityFunctions(scope, "ws", client, strict),
	)

	vaults := util.SplitVaultAllowlist(vaultsRaw)
	restricted := len(vaults) > 0 || !isFullPermission(scope, "rest", client, strict)

	tokenID := ""
	if id, ok := middleware.AuthTokenID(c); ok && id > 0 {
		tokenID = strconv.FormatInt(id, 10)
	}

	response.ToResponse(code.Success.WithData(dto.TokenCapabilitiesResponse{
		UID:         uid,
		IsAdmin:     requireEffectiveAdmin(c.Request.Context(), h.App, uid),
		TokenID:     tokenID,
		Protocol:    protocol,
		Restricted:  restricted,
		Vaults:      vaults,
		Permissions: perms,
		ReadOnly:    !perms.NoteWrite && !perms.FileWrite,
	}))
}

// tokenCapabilityFunctions computes the effective read/write permissions using
// the exact same VerifyPermissions logic the middleware applies to real
// requests, so the report can never diverge from enforcement.
// tokenCapabilityFunctions 用与真实请求完全一致的 VerifyPermissions 逻辑计算
// 有效读写权限，保证报告结果与实际鉴权不会背离。
func tokenCapabilityFunctions(scope, protocol, client string, strict bool) dto.TokenCapabilityFunctions {
	allow := func(function string) bool {
		return pkgapp.VerifyPermissionsWithStrictEmptyProtocol(scope, protocol, client, function, strict)
	}
	return dto.TokenCapabilityFunctions{
		NoteRead:     allow("note_r"),
		NoteWrite:    allow("note_w"),
		FileRead:     allow("file_r"),
		FileWrite:    allow("file_w"),
		SettingRead:  allow("config_r"),
		SettingWrite: allow("config_w"),
		VaultRead:    allow("vault_r"),
		VaultWrite:   allow("vault_w"),
	}
}

// unionCapabilities ORs two permission sets field by field.
// unionCapabilities 按字段对两组权限做逻辑或。
func unionCapabilities(a, b dto.TokenCapabilityFunctions) dto.TokenCapabilityFunctions {
	return dto.TokenCapabilityFunctions{
		NoteRead:     a.NoteRead || b.NoteRead,
		NoteWrite:    a.NoteWrite || b.NoteWrite,
		FileRead:     a.FileRead || b.FileRead,
		FileWrite:    a.FileWrite || b.FileWrite,
		SettingRead:  a.SettingRead || b.SettingRead,
		SettingWrite: a.SettingWrite || b.SettingWrite,
		VaultRead:    a.VaultRead || b.VaultRead,
		VaultWrite:   a.VaultWrite || b.VaultWrite,
	}
}

// isFullPermission reports whether the token is unrestricted in practice: it
// admits both read and write for every resource dimension. A token whose scope
// is empty (legacy wildcard) also counts as full unless strict mode is on.
// isFullPermission 报告令牌是否实际不受限：各资源维度的读写都被允许。scope 为空
// （旧版通配）在非严格模式下同样视为全权限。
func isFullPermission(scope, protocol, client string, strict bool) bool {
	for _, function := range []string{"note_r", "note_w", "file_r", "file_w", "config_r", "config_w", "vault_r", "vault_w"} {
		if !pkgapp.VerifyPermissionsWithStrictEmptyProtocol(scope, protocol, client, function, strict) {
			return false
		}
	}
	return true
}

// scopeProtocol extracts the protocol dimension (p:) from a scope string. An
// empty value is returned when the scope has no protocol dimension.
// scopeProtocol 提取 scope 中的协议维度（p:）；无协议维度时返回空字符串。
func scopeProtocol(scope string) string {
	for _, part := range strings.Split(strings.TrimSpace(scope), " ") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "p:") {
			return strings.TrimPrefix(part, "p:")
		}
	}
	return ""
}
