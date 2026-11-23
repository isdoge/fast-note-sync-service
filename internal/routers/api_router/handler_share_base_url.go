package api_router

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/app"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/internal/middleware"
	pkgapp "github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"go.uber.org/zap"
)

// ShareBaseURLTokenHeader is the alternative static-token header accepted by
// the share base URL admin endpoints, for external dynamic-DNS notifiers that
// cannot obtain an admin login token.
// ShareBaseURLTokenHeader 是分享基址管理接口接受的静态令牌请求头，供无法获取
// 管理员登录令牌的外部动态地址通知脚本使用。
const ShareBaseURLTokenHeader = middleware.ShareBaseURLTokenHeader

// ShareBaseURLHandler manages the runtime share base URL override.
// ShareBaseURLHandler 管理运行时分享基址覆盖值。
type ShareBaseURLHandler struct {
	*Handler
}

// NewShareBaseURLHandler creates ShareBaseURLHandler instance.
// NewShareBaseURLHandler 创建 ShareBaseURLHandler 实例。
func NewShareBaseURLHandler(a *app.App) *ShareBaseURLHandler {
	return &ShareBaseURLHandler{Handler: NewHandler(a)}
}

// authorize accepts either a request authenticated by the static token (handled
// in middleware) or an effective administrator user token. It writes the error
// response and returns false when neither applies.
// authorize 接受静态令牌认证（中间件已完成）或生效管理员用户令牌；两者都不满足
// 时写出错误响应并返回 false。
func (h *ShareBaseURLHandler) authorize(c *gin.Context) bool {
	if middleware.ShareBaseURLStaticAuthorized(c) {
		return true
	}
	return h.authorizeAdmin(c)
}

// authorizeAdmin requires the effective administrator. GET is admin-only per
// the endpoint contract, so the static notifier token can push a value but not
// read it back.
// authorizeAdmin 要求生效管理员。按接口约定 GET 仅限管理员：静态通知令牌可
// 推送值，但不能回读。
func (h *ShareBaseURLHandler) authorizeAdmin(c *gin.Context) bool {
	uid := pkgapp.GetUID(c)
	if uid != 0 && requireEffectiveAdmin(c.Request.Context(), h.App, uid) {
		return true
	}
	pkgapp.NewResponse(c).ToResponse(code.ErrorUserIsNotAdmin)
	return false
}

// Get returns the current effective share base URL and its source.
// @Summary Get effective share base URL
// @Description Return the currently effective share base URL and whether it comes from a runtime override or the config file. An empty baseUrl means there is no fixed share base URL right now, so links are built from ext-api-url or the request address.
// @Tags Admin
// @Security UserAuthToken
// @Produce json
// @Success 200 {object} pkgapp.Res{data=dto.ShareBaseURLResponse} "Success"
// @Failure 403 {object} pkgapp.Res "Insufficient privileges"
// @Router /api/admin/share-base-url [get]
func (h *ShareBaseURLHandler) Get(c *gin.Context) {
	if !h.authorizeAdmin(c) {
		return
	}
	response := pkgapp.NewResponse(c)
	value, source := h.App.ShareBaseURLValue()
	response.ToResponse(code.Success.WithData(dto.ShareBaseURLResponse{
		BaseURL: value,
		Source:  source,
	}))
}

// Update sets or clears the runtime share base URL override.
// @Summary Update runtime share base URL
// @Description Set a public http(s) base URL for generated share links (empty clears the override and falls back to the config). Intended for CGNAT / tunnel setups whose public address changes dynamically.
// @Tags Admin
// @Security UserAuthToken
// @Accept json
// @Produce json
// @Param params body dto.ShareBaseURLUpdateRequest true "Base URL"
// @Success 200 {object} pkgapp.Res{data=dto.ShareBaseURLResponse} "Success"
// @Failure 403 {object} pkgapp.Res "Insufficient privileges"
// @Router /api/admin/share-base-url [post]
func (h *ShareBaseURLHandler) Update(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	response := pkgapp.NewResponse(c)

	params := &dto.ShareBaseURLUpdateRequest{}
	// BindAndValid rejects bodies whose Content-Type the binder would drop, so
	// a missing/empty body is reported instead of silently clearing the value.
	// BindAndValid 拒绝会被 binder 丢弃的 body，缺失或空 body 会明确报错，
	// 而不是静默清空。
	valid, errs := pkgapp.BindAndValid(c, params)
	if !valid {
		response.ToResponse(code.ErrorInvalidParams.WithDetails(errs.ErrorsToString()).WithData(errs.MapsToString()))
		return
	}

	baseURL, err := normalizeShareBaseURL(params.BaseURL)
	if err != nil {
		response.ToResponse(code.ErrorInvalidParams.WithDetails(err.Error()))
		return
	}

	store := h.App.ShareBaseURLStore()
	if store == nil {
		h.App.Logger().Error("ShareBaseURLHandler.Update: store not initialized")
		response.ToResponse(code.ErrorServerInternal.WithDetails("share base url store unavailable"))
		return
	}
	if err := store.Set(baseURL); err != nil {
		// Never log the token or its value; only the operation error.
		// 绝不记录令牌或其值，只记录操作错误。
		h.App.Logger().Error("ShareBaseURLHandler.Update: persist failed", zap.Error(err))
		response.ToResponse(code.ErrorConfigSaveFailed.WithDetails(err.Error()))
		return
	}

	value, source := h.App.ShareBaseURLValue()
	h.App.Logger().Info("runtime share base url updated",
		zap.String("source", source),
		zap.Bool("cleared", baseURL == ""))
	response.ToResponse(code.Success.WithData(dto.ShareBaseURLResponse{
		BaseURL: value,
		Source:  source,
	}))
}

// normalizeShareBaseURL validates a share base URL: empty clears the override;
// otherwise it must be an absolute http/https URL without credentials, control
// characters, query string or fragment, and trailing slashes are trimmed.
// normalizeShareBaseURL 校验分享基址：空值表示清除覆盖；否则必须是绝对
// http/https URL，不含凭据、控制字符、查询串或片段，并去掉尾部斜杠。
func normalizeShareBaseURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}

	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errShareBaseURLInvalid("baseUrl contains control characters")
		}
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return "", errShareBaseURLInvalid("baseUrl is not a valid URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errShareBaseURLInvalid("baseUrl must use http or https")
	}
	if parsed.Host == "" {
		return "", errShareBaseURLInvalid("baseUrl must include a host")
	}
	if parsed.User != nil {
		return "", errShareBaseURLInvalid("baseUrl must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", errShareBaseURLInvalid("baseUrl must not contain a query string")
	}
	if parsed.Fragment != "" {
		return "", errShareBaseURLInvalid("baseUrl must not contain a fragment")
	}
	if strings.Contains(value, `\`) {
		return "", errShareBaseURLInvalid("baseUrl must not contain backslashes")
	}

	// Keep any subpath (e.g. a reverse proxy mounting the service under /fns),
	// only trimming trailing slashes so links never double up separators.
	// 保留子路径（如反向代理把服务挂在 /fns 下），只去掉尾部斜杠，避免链接
	// 出现重复分隔符。
	normalized := scheme + "://" + parsed.Host + parsed.EscapedPath()
	return strings.TrimRight(normalized, "/"), nil
}

// errShareBaseURLInvalid is a small helper to keep error text consistent.
// errShareBaseURLInvalid 统一错误文案的小工具。
func errShareBaseURLInvalid(msg string) error {
	return &shareBaseURLValidationError{msg: msg}
}

type shareBaseURLValidationError struct{ msg string }

func (e *shareBaseURLValidationError) Error() string { return e.msg }
